package provider

import (
	"bytes"
	"context"
	"io"
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
		if err != nil || attempt == Attempts || !worthRetrying(resp) {
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
//
// 429 is the one status that has to be read rather than counted, because
// several different failures wear it: going too fast, which waiting fixes, and
// an empty wallet or a daily quota, which it does not.
func worthRetrying(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return !waitingWontHelp(resp)
	}
	return resp.StatusCode >= 500
}

// waitingWontHelp reports whether a 429 will still be a 429 in ten seconds.
// Two kinds qualify, and only one of them is about money.
//
// An empty wallet is the plain case: Z.ai says "Insufficient balance ...
// Please recharge", OpenAI answers insufficient_quota with "You have no
// credits remaining". Matched on the words rather than a vendor's error code,
// which every vendor numbers differently.
//
// A quota counted per day is the other. Gemini's free tier allows twenty
// requests a day and says so with "You exceeded your current quota, please
// check your plan and billing details" — word for word what it sends when a
// per-minute quota trips, which waiting does fix. The sentence cannot tell
// them apart; the quotaId in error.details can, so that is what is read.
// "billing details" used to be a marker here and had to go: it caught the
// recoverable one too, and losing a turn to a rate limit that would have
// cleared is worse than waiting out one that would not.
func waitingWontHelp(resp *http.Response) bool {
	body := peek(resp)

	// GenerateRequestsPerDayPerProjectPerModel-FreeTier, lowercased.
	if strings.Contains(body, "perday") {
		return true
	}
	for _, marker := range []string{
		"insufficient balance",
		"insufficient_quota",
		"no credits",
		"recharge",
	} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

// peek reads the start of an error body and puts it back, since whatever the
// caller does with the response still needs it. Error bodies are a sentence;
// the limit is there so a provider answering 429 with a stream cannot be read
// into memory in full.
func peek(resp *http.Response) string {
	const limit = 4 << 10
	head, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil && len(head) == 0 {
		return ""
	}
	resp.Body = readCloser{io.MultiReader(bytes.NewReader(head), resp.Body), resp.Body}
	return strings.ToLower(string(head))
}

// readCloser puts a body back together: read from the front again, close the
// socket underneath.
type readCloser struct {
	io.Reader
	io.Closer
}

// backoff honours Retry-After when the server sends one. Without it, a server
// having a bad moment is given a second and then two; a rate limit is given
// five and then ten, because asking a full queue again a second later is how
// it stays full — the free tiers that answer 429 mean it.
func backoff(resp *http.Response, attempt int) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
			return min(time.Duration(secs)*time.Second, maxBackoff)
		}
	}
	base := time.Second
	if resp.StatusCode == http.StatusTooManyRequests {
		base = 5 * time.Second
	}
	return min(base<<(attempt-1), maxBackoff)
}
