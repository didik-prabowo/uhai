package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The retrying is shared by every vendor client, so it is checked once here
// rather than in each of them.
func TestRetryPolicy(t *testing.T) {
	for status, want := range map[int]bool{429: true, 500: true, 503: true, 200: false, 400: false, 401: false} {
		if got := worthRetrying(status); got != want {
			t.Errorf("worthRetrying(%d) = %v, want %v", status, got, want)
		}
	}

	// A server that says how long to wait is believed, up to the cap.
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", "3")
	if got := backoff(resp, 1); got != 3*time.Second {
		t.Errorf("Retry-After should be honoured, waited %s", got)
	}
	resp.Header.Set("Retry-After", "3600")
	if got := backoff(resp, 1); got != maxBackoff {
		t.Errorf("a huge Retry-After must be capped, waited %s", got)
	}

	// Without the header it doubles each attempt.
	plain := &http.Response{Header: http.Header{}}
	if got := backoff(plain, 1); got != time.Second {
		t.Errorf("first backoff = %s, want 1s", got)
	}
	if got := backoff(plain, 3); got != 4*time.Second {
		t.Errorf("third backoff = %s, want 4s", got)
	}

	// A rate limit waits longer than a server hiccup: asking a queue that is
	// already full again one second later is how it stays full.
	limited := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
	if got := backoff(limited, 1); got != 5*time.Second {
		t.Errorf("first backoff after 429 = %s, want 5s", got)
	}
	if got := backoff(limited, 2); got != maxBackoff {
		t.Errorf("second backoff after 429 = %s, want the %s cap", got, maxBackoff)
	}
}

// The loop itself, not just the policy: a rate limit that clears is retried
// until it works, the attempts run out rather than going round forever, and a
// cancelled context ends the wait instead of sleeping through it.
func TestPostRetriesUntilItWorks(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.Header().Set("Retry-After", "1") // keeps the test quick and the wait real
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		io.WriteString(w, "ok")
	}))
	defer server.Close()

	build := func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, server.URL, strings.NewReader("body"))
	}
	resp, err := Post(context.Background(), server.Client(), build)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || hits != 3 {
		t.Fatalf("status %d after %d attempts", resp.StatusCode, hits)
	}

	// A server that never recovers is given exactly Attempts tries, and the
	// last response is handed back rather than an invented error.
	hits = 0
	always := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer always.Close()

	resp, err = Post(context.Background(), always.Client(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, always.URL, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hits != Attempts || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("%d attempts, last status %d", hits, resp.StatusCode)
	}

	// Cancelling ends the wait between attempts: esc must not have to sit
	// through a backoff.
	hits = 0
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := Post(ctx, always.Client(), func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, always.URL, nil)
	}); err == nil {
		t.Fatal("a cancelled context must end the retrying")
	}
}
