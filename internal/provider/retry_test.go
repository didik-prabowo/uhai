package provider

import (
	"net/http"
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
}
