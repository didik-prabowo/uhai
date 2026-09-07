// Things fetched over the network that keep: a provider's own model list, and
// models.dev's figures. Both ask the same two questions — what is on disk, and
// is it recent enough to believe — so both ask them here.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/didik-prabowo/uhai/internal/provider"
)

// cacheEnvelope stamps a value with when it was fetched. The time lives in the
// file rather than in its mtime because a ~/.uhai carried to another machine
// should carry the age of the data, not the age of the copy.
type cacheEnvelope[T any] struct {
	At time.Time `json:"at"`
	V  T         `json:"v"`
}

// readCache loads ~/.uhai/cache/<name>.json. A stale value comes back all the
// same, with fresh false, because six hours old beats an empty model picker on
// a train — the caller decides which it wants. A missing or corrupt file is a
// miss rather than an error: nothing here is worth failing a session over.
func readCache[T any](name string, ttl time.Duration) (v T, fresh bool) {
	path, err := InDir("cache", name+".json")
	if err != nil {
		return v, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return v, false
	}
	var env cacheEnvelope[T]
	if err := json.Unmarshal(data, &env); err != nil {
		return v, false
	}
	return env.V, time.Since(env.At) < ttl
}

func writeCache[T any](name string, v T) error {
	path, err := InDir("cache", name+".json")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(cacheEnvelope[T]{At: time.Now(), V: v})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ForgetModelLists drops every cached model list, so the next look goes out.
// The TTL answers "has this drifted on its own"; this answers "I just changed
// it" — a gateway gains a provider and the six hours that were a courtesy
// become a wall. Only the lists go: models.dev is a public catalog nobody
// edits locally, and re-fetching four megabytes to find a model the vendor
// added to a *gateway* would be answering the wrong question.
func ForgetModelLists() error {
	dir, err := InDir("cache")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // nothing cached is nothing to forget
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "models-") && name != "models-dev.json" {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// modelListTTL is how long a provider's own list is believed. Six hours, which
// is zot's figure and the right order: a new model is announced rather than
// discovered, and being half a day behind costs at most one typo note.
const modelListTTL = 6 * time.Hour

// CachedModels is ModelLister.Models with a memory. The picker asks every
// connected provider in turn, one request each, so opening /model cost four
// round trips every time and showed nothing at all with the network down.
//
// A failed or empty fetch falls back to whatever is on disk, however old: a
// stale list is a working picker, and an empty one is not. Only a non-empty
// answer is stored, so a provider having a bad minute cannot erase the list.
func CachedModels(name string, lister provider.ModelLister) ([]string, error) {
	cached, fresh := readCache[[]string]("models-"+name, modelListTTL)
	if fresh && len(cached) > 0 {
		return cached, nil
	}
	models, err := lister.Models()
	if err != nil || len(models) == 0 {
		if len(cached) > 0 {
			return cached, nil
		}
		return models, err
	}
	_ = writeCache("models-"+name, models)
	return models, nil
}
