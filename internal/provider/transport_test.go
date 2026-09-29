package provider

import (
	"net/http"
	"testing"
	"time"
)

// The clients used to build their transport from scratch, which silently
// dropped everything the default does. Each of these was a real consequence,
// and none of them reported itself: no proxy meant HTTPS_PROXY was ignored, no
// handshake timeout meant a connection that never came up hung the turn, and no
// idle timeout meant connections were held for the life of the process.
func TestTransportKeepsWhatTheDefaultDoes(t *testing.T) {
	got := Transport(90 * time.Second)

	if got.Proxy == nil {
		t.Error("no Proxy, so HTTPS_PROXY is ignored and nothing says so")
	}
	if got.TLSHandshakeTimeout == 0 {
		t.Error("no handshake timeout, so a connection that never comes up hangs the turn")
	}
	if got.IdleConnTimeout == 0 {
		t.Error("no idle timeout, so idle connections are never closed")
	}
	if got.ResponseHeaderTimeout != 90*time.Second {
		t.Errorf("the one thing asked for did not take: %s", got.ResponseHeaderTimeout)
	}

	// Cloned, not shared: a vendor setting its own timeout must not change
	// every other client's.
	if got == http.DefaultTransport {
		t.Error("the default transport itself was handed out and modified")
	}
	if other := Transport(time.Second); other.ResponseHeaderTimeout == got.ResponseHeaderTimeout {
		t.Error("two clients are sharing one transport")
	}
}
