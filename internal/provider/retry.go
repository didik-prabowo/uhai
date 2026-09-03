package provider

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Attempts is how many times one request is tried. Rate limits are routine on
// free tiers, and a 502 from a proxy is usually gone a second later.
const Attempts = 3

// maxBackoff caps the wait, so a server sending "Retry-After: 3600" cannot
// park the CLI for an hour.
const maxBackoff = 10 * time.Second

// Post sends a request, retrying the failures that tend to pass on their own.
// The request is built again for each attempt rather than reused, since its
// body has already been read by then; that is what build is for.
//
// It lives here because every vendor client needs the same behaviour, and two
// copies of a backoff loop is one copy too many.
func Post(ctx context.Context, hc *http.Client, build func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}

		resp, err := hc.Do(req)
		if err != nil || attempt == Attempts || !worthRetrying(resp.StatusCode) {
			return resp, err
		}

		wait := backoff(resp, attempt)
		resp.Body.Close()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// worthRetrying covers being rate limited and the server having a bad moment.
// Anything else — a bad key, a wrong model name — will fail the same way twice.
func worthRetrying(status int) bool { return status == http.StatusTooManyRequests || status >= 500 }

// backoff honours Retry-After when the server sends one, and otherwise waits
// 1s, then 2s.
func backoff(resp *http.Response, attempt int) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
			return min(time.Duration(secs)*time.Second, maxBackoff)
		}
	}
	return min(time.Duration(1<<(attempt-1))*time.Second, maxBackoff)
}
