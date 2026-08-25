// Package collect gathers current cumulative download/pull counts from the
// sources ghdl tracks: GitHub release assets, Docker Hub repositories, and
// GHCR container packages. Each collector returns a flat slice of
// db.Observation; the caller stamps them with a timestamp and persists.
package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/cenkalti/backoff/v5"

	"github.com/kradalby/ghdl/db"
)

const userAgent = "ghdl (+https://github.com/kradalby/ghdl)"

// httpTimeout bounds a single request end to end. http.DefaultClient has no
// timeout, so a server that accepts the connection and then stalls would hang
// the collector forever: the ticker in collectLoop has capacity 1, so ticks
// coalesce and collection never recovers without a restart.
const httpTimeout = 30 * time.Second

// newHTTPClient builds the client every collector uses.
func newHTTPClient() *http.Client {
	return &http.Client{Timeout: httpTimeout}
}

// errNotANumber marks text that was expected to hold a count but does not.
var errNotANumber = errors.New("not a number")

// A Collector gathers current cumulative counts from one source.
type Collector interface {
	Source() string
	Collect(ctx context.Context) ([]db.Observation, error)
}

// splitRepo splits "owner/name" into its parts.
func splitRepo(s string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(s, "/")
	return owner, name, ok && owner != "" && name != ""
}

// parseNum parses a human-formatted integer such as "1,418,338" or "0".
//
// It is strict about trailing junk on purpose. GitHub abbreviates large counts
// in some places ("1.42M"), and a lenient digit scan would silently turn that
// into 1 — a plausible-looking count that corrupts the series for good. Refusing
// to parse instead surfaces as a scrape error, which is what the fixture-backed
// parser tests are there to catch.
func parseNum(s string) (int64, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0, fmt.Errorf("parse count %q: %w", s, errNotANumber)
	}

	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse count %q: %w", s, errNotANumber)
	}

	return n, nil
}

// rateLimited reports whether a response is a rate-limit rejection. GitHub
// answers 429 for the primary limit and 403 for the secondary one, the latter
// identifiable by Retry-After or an exhausted x-ratelimit-remaining. Both must
// be retried: the versions walk fires up to maxVersionPages requests per
// package back to back, so throttling is the *expected* steady-state failure,
// and treating it as permanent loses the source for the whole interval.
func rateLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}

	if resp.StatusCode != http.StatusForbidden {
		return false
	}

	return resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-Ratelimit-Remaining") == "0"
}

// retryAfter turns a rate-limit response into an error carrying the server's
// requested delay, which backoff honours verbatim (and resets its own backoff
// for). Falls back to a plain error, i.e. ordinary exponential backoff, when
// the header is absent or unparseable.
func retryAfter(resp *http.Response, url string) error {
	err := fmt.Errorf("GET %s: %s", url, resp.Status)

	v := resp.Header.Get("Retry-After")
	if v == "" {
		return err
	}

	var d time.Duration

	switch secs, aerr := strconv.Atoi(v); {
	case aerr == nil && secs >= 0:
		d = time.Duration(secs) * time.Second
	default:
		t, terr := http.ParseTime(v)
		if terr != nil {
			return err
		}

		d = max(time.Until(t), 0)
	}

	return fmt.Errorf("%w (%w)", err, &backoff.RetryAfterError{Duration: d})
}

// classify maps a response status onto the retry policy shared by getJSON and
// getHTML: 5xx and rate limits are transient, every other non-200 is permanent.
func classify(resp *http.Response, url string) error {
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	case resp.StatusCode >= 500:
		return fmt.Errorf("GET %s: %s", url, resp.Status) // transient — retry
	case rateLimited(resp):
		return retryAfter(resp, url)
	default:
		return backoff.Permanent(fmt.Errorf("GET %s: %s", url, resp.Status))
	}
}

func newRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return req, nil
}

// getJSON fetches and decodes a JSON document, retrying on transient failures.
// Docker Hub's repository API and ghcr.io's token endpoint are both rate
// limited, so this needs the same retry policy as the HTML scrape.
func getJSON(ctx context.Context, hc *http.Client, url string, v any) error {
	_, err := backoff.Retry(ctx, func() (struct{}, error) {
		req, err := newRequest(ctx, url)
		if err != nil {
			return struct{}{}, backoff.Permanent(err)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := hc.Do(req)
		if err != nil {
			return struct{}{}, err // network error — retry
		}
		defer resp.Body.Close()

		if cerr := classify(resp, url); cerr != nil {
			return struct{}{}, cerr
		}

		return struct{}{}, json.NewDecoder(resp.Body).Decode(v)
	}, backoff.WithBackOff(backoff.NewExponentialBackOff()), backoff.WithMaxTries(8))

	return err
}

// getHTML fetches and parses an HTML page, retrying on transient failures.
// GitHub's package pages 502 intermittently, and pagination multiplies the
// number of requests, so a single blip must not fail the whole collection. 5xx,
// rate limits and network errors are retried; other 4xx are permanent.
func getHTML(ctx context.Context, hc *http.Client, url string) (*goquery.Document, error) {
	return backoff.Retry(ctx, func() (*goquery.Document, error) {
		req, err := newRequest(ctx, url)
		if err != nil {
			return nil, backoff.Permanent(err)
		}

		resp, err := hc.Do(req)
		if err != nil {
			return nil, err // network error — retry
		}
		defer resp.Body.Close()

		if cerr := classify(resp, url); cerr != nil {
			return nil, cerr
		}

		return goquery.NewDocumentFromReader(resp.Body)
	}, backoff.WithBackOff(backoff.NewExponentialBackOff()), backoff.WithMaxTries(8))
}

// docFromReader is a small seam so the HTML parsers can be unit-tested against
// saved fixtures without a network fetch.
func docFromReader(r io.Reader) (*goquery.Document, error) {
	return goquery.NewDocumentFromReader(r)
}
