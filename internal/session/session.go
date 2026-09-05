// Package session is the conversation and where it is kept. This file is the
// conversation: Session is the data and nothing else, so that changing where
// it lives cannot change what it is. store.go is the contract for keeping one,
// files.go the implementation uhai ships.
//
// It lives apart from config because the two are opposites. Settings, keys and
// permissions are input a person writes to change what uhai does; a session is
// output uhai produces by running. Nothing in config ever needed a Session —
// it only shared the directory.
package session

import (
	"crypto/rand"
	"encoding/base32"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/didik-prabowo/uhai/internal/provider"
)

// A session is one conversation, written to ~/.uhai/sessions after every turn
// so a closed terminal — or a crash — does not take the work with it.
//
// ponytail: whole history rewritten each turn, one file per session. Fine for
// conversations that fit in a model's context; revisit if they ever do not.

// Session is what gets saved and what -resume brings back.
type Session struct {
	// ID names the file and is what -resume takes. It carries no meaning on
	// purpose: a name that encodes when or where a thing was made is a fact
	// duplicated in two places, and the copy in the name is the one that goes
	// stale. When and what are fields, and the listing prints them.
	ID       string             `json:"id"`
	Started  time.Time          `json:"started"`
	Updated  time.Time          `json:"updated"` // moves every turn, so a listing shows what was worked on last
	PID      int                `json:"pid"`     // the process that held it, for telling two live sessions apart
	Model    string             `json:"model"`
	Messages []provider.Message `json:"messages"`

	// Spend is what the conversation has cost, kept per model rather than as
	// one total. Pricing a stored total later at whatever model happens to be
	// loaded is a confident wrong figure the moment /model is used — the
	// tokens are the fact, the dollars are worked out per entry and added up.
	Spend []Spend `json:"spend,omitempty"`
}

// Spend is one model's share of a conversation.
type Spend struct {
	Model string         `json:"model"`
	Usage provider.Usage `json:"usage"`
}

// idBytes is how much randomness an id carries. Eight bytes is 64 bits: a
// directory of conversations will not collide, and base32 turns it into 13
// characters of which the first four are already enough to name one.
const idBytes = 8

// New starts a conversation with an id of its own, minted here rather than at
// save time so the id is the same string before and after the first turn — the
// prompt prints it on the way out, and it has to be the one on disk.
func New() Session {
	return Session{ID: NewID(), Started: time.Now(), PID: os.Getpid()}
}

// NewID is opaque and says nothing, which is the point. It is lower case and
// has no padding so it survives being read aloud, typed by hand, and pasted
// into a shell without quoting. Exported for stores, which live in their own
// packages and have to name a Session that reached them without an id.
func NewID() string {
	var b [idBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice; if it ever does, a clashing
		// id would overwrite a conversation, so fall back to something that
		// cannot repeat within a process rather than to the empty string.
		return strconv.FormatInt(time.Now().UnixNano(), 32)
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// Prompt is the first thing that was asked, for telling one session from
// another in a listing.
func (s Session) Prompt() string {
	for _, m := range s.Messages {
		if m.Role != provider.RoleUser {
			continue
		}
		for _, block := range m.Content {
			if block.Type == provider.BlockText && strings.TrimSpace(block.Text) != "" {
				return strings.TrimSpace(strings.SplitN(block.Text, "\n", 2)[0])
			}
		}
	}
	return ""
}
