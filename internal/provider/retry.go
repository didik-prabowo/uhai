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
// 429 is the one status that has to be read rather than counted, because two
// different failures wear it: going too fast, which waiting fixes, and having
// no money, which waiting does not.
func worthRetrying(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return !outOfMoney(resp)
	}
	return resp.StatusCode >= 500
}

// outOfMoney reports whether a 429 is about the bill instead of the pace.
// Z.ai answers an empty wallet with 429 and "Insufficient balance or no
// resource package. Please recharge."; OpenAI says either insufficient_quota
// or "You have no credits remaining", which is the one an exhausted account
// actually sends and which the first four markers here all missed. Neither improves in ten seconds, so uhai used to
// spend fifteen of them failing three times identically.
//
// Matched on the words that mean money rather than on a vendor's error code,
// which every vendor numbers differently. Bare "quota" is deliberately not
// among them: it is how several providers word an ordinary rate limit.
func outOfMoney(resp *http.Response) bool {
	body := peek(resp)
	for _, marker := range []string{
		"insufficient balance",
		"insufficient_quota",
		"no credits",
		"recharge",
		"billing details",
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
