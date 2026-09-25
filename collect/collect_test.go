package collect

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v7"
	"github.com/stretchr/testify/require"
)

func TestClassify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		header    http.Header
		wantErr   bool
		permanent bool
		// wait is the Retry-After delay the error must carry; nil for none.
		wait *time.Duration
	}{
		{name: "ok", status: http.StatusOK},
		{name: "server error retried", status: http.StatusBadGateway, wantErr: true},
		{name: "not found permanent", status: http.StatusNotFound, wantErr: true, permanent: true},
		{name: "plain forbidden permanent", status: http.StatusForbidden, wantErr: true, permanent: true},
		{
			name:    "secondary limit without delay",
			status:  http.StatusForbidden,
			header:  http.Header{"X-Ratelimit-Remaining": {"0"}},
			wantErr: true,
		},
		{
			name:    "retry-after seconds",
			status:  http.StatusTooManyRequests,
			header:  http.Header{"Retry-After": {"120"}},
			wantErr: true,
			wait:    new(2 * time.Minute),
		},
		{
			name:    "retry-after date in the past",
			status:  http.StatusForbidden,
			header:  http.Header{"Retry-After": {"Mon, 02 Jan 2006 15:04:05 GMT"}},
			wantErr: true,
			wait:    new(time.Duration(0)),
		},
		{
			name:    "unparseable retry-after",
			status:  http.StatusTooManyRequests,
			header:  http.Header{"Retry-After": {"soon"}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp := &http.Response{StatusCode: tt.status, Status: http.StatusText(tt.status), Header: tt.header}
			err := classify(resp, "https://example.com")

			if !tt.wantErr {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			require.Equal(t, tt.permanent, errors.Is(err, backoff.ErrPermanent))

			var ra *backoff.RetryAfterError
			if tt.wait == nil {
				require.NotErrorAs(t, err, &ra)
				return
			}

			require.ErrorAs(t, err, &ra)
			require.Equal(t, *tt.wait, ra.Duration)
		})
	}
}

// runAll retries each Collect with a backoff.Retry of its own, which honours
// the server's delay only if a rate limit that outlasts the fetcher's retries
// still reaches it as a RetryAfterError.
func TestRateLimitOutlastingRetriesKeepsDelay(t *testing.T) {
	t.Parallel()

	fetchers := map[string]func(context.Context, *http.Client, string) error{
		"getJSON": func(ctx context.Context, hc *http.Client, url string) error {
			var v struct{}
			return getJSON(ctx, hc, url, &v)
		},
		"getHTML": func(ctx context.Context, hc *http.Client, url string) error {
			_, err := getHTML(ctx, hc, url)
			return err
		},
	}

	for name, fetch := range fetchers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var hits atomic.Int32

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				// No wait between the fetcher's own tries keeps the test fast;
				// only the last response's delay has to reach the caller.
				delay := "0"
				if hits.Add(1) == maxTries {
					delay = "3600"
				}

				w.Header().Set("Retry-After", delay)
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			t.Cleanup(srv.Close)

			err := fetch(t.Context(), srv.Client(), srv.URL)

			var ra *backoff.RetryAfterError
			require.ErrorAs(t, err, &ra)
			require.Equal(t, time.Hour, ra.Duration)
			require.ErrorIs(t, err, backoff.ErrExhausted)
			require.EqualValues(t, maxTries, hits.Load())
		})
	}
}

func TestPermanentFailureNotRetried(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	_, err := getHTML(t.Context(), srv.Client(), srv.URL)
	require.ErrorIs(t, err, backoff.ErrPermanent)

	var ra *backoff.RetryAfterError
	require.NotErrorAs(t, err, &ra)
	require.EqualValues(t, 1, hits.Load())
}
