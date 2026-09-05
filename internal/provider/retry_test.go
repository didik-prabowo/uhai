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
		if got := worthRetrying(reply(status, "")); got != want {
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

// reply is a response with a body, which worthRetrying reads for a 429.
func reply(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// The bodies are the ones the providers actually send.
func TestARateLimitAboutMoneyIsNotRetried(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"1113","message":"Insufficient balance or no resource package. Please recharge."}}`,
		`{"error":{"code":"insufficient_quota","message":"You exceeded your current quota, please check your plan and billing details."}}`,
	} {
		if worthRetrying(reply(429, body)) {
			t.Errorf("waiting will not pay the bill, but this was retried:\n%s", body)
		}
	}

	// The ordinary kind still is, including the wording that says quota and
	// means pace.
	for _, body := range []string{
		`{"error":{"code":"1302","message":"Rate limit reached for requests"}}`,
		`{"error":{"message":"Quota exceeded for quota metric 'Generate requests per minute'"}}`,
		``,
	} {
		if !worthRetrying(reply(429, body)) {
			t.Errorf("a plain rate limit should be waited out:\n%s", body)
		}
	}
}

// Reading the body to decide must not consume it: the caller still renders the
// provider's own words, which is the only place the reason appears.
func TestReadingA429LeavesTheBodyIntact(t *testing.T) {
	const body = `{"error":{"message":"Insufficient balance. Please recharge."}}`
	resp := reply(429, body)
	worthRetrying(resp)

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("body unreadable after the peek: %v", err)
	}
	if string(got) != body {
		t.Errorf("body after the peek =\n%s\nwant\n%s", got, body)
	}
}

// The loop, not just the policy: an empty wallet is one attempt and no wait,
// where it used to be three attempts and fifteen seconds.
func TestPostDoesNotWaitOnAnEmptyWallet(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"code":"1113","message":"Insufficient balance or no resource package. Please recharge."}}`)
	}))
	defer server.Close()

	start := time.Now()
	resp, err := Post(context.Background(), server.Client(), func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, server.URL, strings.NewReader("{}"))
	})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()

	if hits != 1 {
		t.Errorf("asked %d times for money that is not there, want 1", hits)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("waited %s before giving up, want none of it", waited)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "recharge") {
		t.Errorf("the reason has to survive to be shown, got %q", body)
	}
}

// The wording an exhausted OpenAI account actually sends, which the first
// markers all missed: it says neither "quota" nor "recharge".
func TestOpenAIRunningOutOfCreditsIsNotRetried(t *testing.T) {
	const body = `{"error":{"message":"You have no credits remaining. Add credits to continue using the API at https://platform.openai.com/settings/organization/billing/.","type":"insufficient_quota"}}`
	if worthRetrying(reply(429, body)) {
		t.Error("an account with no credits will not grow any in ten seconds")
	}
	// And without the type field, which is how it arrives from some paths.
	plain := `{"error":{"message":"You have no credits remaining. Add credits to continue."}}`
	if worthRetrying(reply(429, plain)) {
		t.Error("the message alone has to be enough")
	}
}

// Gemini words a daily quota and a per-minute one identically, so the sentence
// is useless and the quotaId decides. Both bodies below are what the API
// actually sent; only the violation differs.
func TestGeminiDailyQuotaWaitsButAMinuteQuotaDoesNot(t *testing.T) {
	const said = `"message":"You exceeded your current quota, please check your plan and billing details."`

	daily := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED",` + said +
		`,"details":[{"violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]}]}}`
	if worthRetrying(reply(429, daily)) {
		t.Error("a quota counted per day will not reset in ten seconds")
	}

	minute := `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED",` + said +
		`,"details":[{"violations":[{"quotaId":"GenerateRequestsPerMinutePerProjectPerModel-FreeTier"}]}]}}`
	if !worthRetrying(reply(429, minute)) {
		t.Error("a quota counted per minute is exactly what backoff is for")
	}
}
