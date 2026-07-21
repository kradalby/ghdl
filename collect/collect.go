// Package collect gathers current cumulative download/pull counts from the
// sources ghdl tracks: GitHub release assets, Docker Hub repositories, and
// GHCR container packages. Each collector returns a flat slice of
// db.Observation; the caller stamps them with a timestamp and persists.
package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/cenkalti/backoff/v5"

	"github.com/kradalby/ghdl/db"
)

const userAgent = "ghdl (+https://github.com/kradalby/ghdl)"

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
func parseNum(s string) int64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int64(r-'0')
	}
	return n
}

func newRequest(ctx context.Context, url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return req, nil
}

func getJSON(ctx context.Context, hc *http.Client, url string, v any) error {
	req, err := newRequest(ctx, url)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// getHTML fetches and parses an HTML page, retrying on transient failures.
// GitHub's package pages 502 intermittently, and pagination multiplies the
// number of requests, so a single blip must not fail the whole collection. 5xx
// and network errors are retried; 4xx are permanent.
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

		switch {
		case resp.StatusCode >= 500:
			return nil, fmt.Errorf("GET %s: %s", url, resp.Status) // transient — retry
		case resp.StatusCode != http.StatusOK:
			return nil, backoff.Permanent(fmt.Errorf("GET %s: %s", url, resp.Status))
		}
		return goquery.NewDocumentFromReader(resp.Body)
	}, backoff.WithBackOff(backoff.NewExponentialBackOff()), backoff.WithMaxTries(8))
}

// docFromReader is a small seam so the HTML parsers can be unit-tested against
// saved fixtures without a network fetch.
func docFromReader(r io.Reader) (*goquery.Document, error) {
	return goquery.NewDocumentFromReader(r)
}
