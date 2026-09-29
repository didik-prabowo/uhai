package provider

import (
	"net/http"
	"time"
)

// Transport is what every vendor client talks through: Go's own default, with
// a header timeout added.
//
// Cloned from the default rather than built from scratch, which is what the
// clients did. A bare &http.Transport{} carries none of the default's
// behaviour, and three of those omissions matter:
//
// It has no Proxy, so HTTPS_PROXY and HTTP_PROXY were ignored without a word.
// What that leaves is a connection that does not work and nothing anywhere
// saying why — the shape of failure this repository treats as the worst one,
// and the machine it happens on is the one behind a corporate proxy, where
// every other tool works.
//
// It has no TLSHandshakeTimeout, and headerTimeout does not cover the gap:
// ResponseHeaderTimeout starts once the request has been written, so a
// handshake that never completes is not waiting for a header. An interactive
// turn has no deadline of its own, so that hangs until the user gives up.
//
// Its IdleConnTimeout is zero, which means never: idle connections were kept
// for the life of the process rather than the default's ninety seconds.
//
// The timeout is a parameter because it is a vendor's decision — it bounds the
// wait for the first byte and nothing else, since a streamed answer
// legitimately takes minutes and a whole-request timeout would cut it off.
func Transport(headerTimeout time.Duration) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = headerTimeout
	return t
}
