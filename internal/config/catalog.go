// models.dev is a public catalog of what every vendor sells — context windows,
// output ceilings and list prices, one JSON document, kept current by people
// whose job it is. The table in models.go is the same figures read off pricing
// pages by hand, which is how two of them came to be wrong in the direction
// that costs a turn, and why two more could not be filled at all: Z.ai's glm-5
// line and gpt-5 cannot be probed from an account with no credit left.
//
// It fills the numbers in; it does not replace the table. The table is what
// answers when the network does not, and it stays the only thing that knows a
// model is Retired — models.dev lists Gemini 2.5 with figures, and Gemini
// answers 404 for it.
package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A var so the test can point it somewhere else. Nothing else writes it.
var catalogURL = "https://models.dev/api.json"

// catalogTTL is a day: list prices move on an announcement, not on the hour,
// and the document is four megabytes. catalogTimeout is generous for the same
// reason — this runs behind "loading models...", where waiting is expected and
// failing quietly is the fallback.
const (
	catalogTTL     = 24 * time.Hour
	catalogTimeout = 20 * time.Second
)

// catalog is the reduced index: "provider/model", lowercased, to the four
// figures uhai asks about. Reduced before it is stored, because the document
// carries 213 providers and uhai speaks to six.
var catalog struct {
	sync.Mutex
	index map[string]modelInfo
}

// LoadCatalog reads the stored figures into memory, and is called once where
// the agent is built. Explicit rather than on first use, for two reasons that
// are really one: lookup runs while a picker row is drawn and while a turn is
// priced, and neither wants a file read — nor to answer differently depending
// on whether a file happens to be there. A process that never loads it runs on
// the table in models.go, which is what a table is for, and a test gets the
// table unless it asks for anything else.
//
// Stale figures are still the right figures for every model whose price did
// not move, so the age is not checked here. RefreshCatalog decides when to go
// and look again.
func LoadCatalog() {
	index, _ := readCache[map[string]modelInfo]("models-dev", catalogTTL)
	catalog.Lock()
	catalog.index = index
	catalog.Unlock()
}

// catalogInfo answers from memory and never goes out.
func catalogInfo(providerName, model string) (modelInfo, bool) {
	catalog.Lock()
	defer catalog.Unlock()
	info, ok := catalog.index[catalogKey(providerName, model)]
	return info, ok
}

// catalogKey is the index key. There was a translation table here for the one
// provider whose models.dev id differed from ours — gemini, which models.dev
// files under google — and it went with the provider. Every name uhai ships
// now matches; a future one that does not will want the table back.
func catalogKey(providerName, model string) string {
	return providerName + "/" + strings.ToLower(model)
}

// RefreshCatalog fetches models.dev unless what is on disk is still fresh. It
// blocks on the network, so the model picker calls it from the goroutine it
// already loads models in, and drops what it returns: being offline is routine
// and the table underneath is sound.
func RefreshCatalog() error {
	if cached, fresh := readCache[map[string]modelInfo]("models-dev", catalogTTL); fresh && len(cached) > 0 {
		return nil
	}
	index, err := fetchCatalog()
	if err != nil {
		return err
	}
	if len(index) == 0 {
		// An empty answer parses but says nothing. Storing it would mean a day
		// of believing the vendor sells nothing.
		return fmt.Errorf("models.dev returned no models for any known provider")
	}
	catalog.Lock()
	catalog.index = index
	catalog.Unlock()
	return writeCache("models-dev", index)
}

// catalogDoc is the shape of api.json, cut down to the fields uhai has a use
// for. Everything else in it — release dates, modalities, knowledge cutoffs —
// is dropped at the decoder rather than stored and ignored.
type catalogDoc map[string]struct {
	Models map[string]struct {
		// A pointer because absent and false mean different things: most
		// entries simply do not say, and NoTools must not be set for those.
		ToolCall *bool `json:"tool_call"`
		Limit    struct {
			Context int `json:"context"`
			Output  int `json:"output"`
		} `json:"limit"`
		Cost struct {
			Input     float64 `json:"input"`
			Output    float64 `json:"output"`
			CacheRead float64 `json:"cache_read"`
		} `json:"cost"`
	} `json:"models"`
}

func fetchCatalog() (map[string]modelInfo, error) {
	client := &http.Client{Timeout: catalogTimeout}
	resp, err := client.Get(catalogURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models.dev: %s", resp.Status)
	}

	var doc catalogDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("models.dev: %w", err)
	}

	wanted := make(map[string]bool, len(providers))
	for _, name := range Providers() {
		wanted[catalogKey(name, "")] = true
	}

	index := make(map[string]modelInfo)
	for name, p := range doc {
		if !wanted[name+"/"] {
			continue
		}
		for id, m := range p.Models {
			// A model with no context window has nothing to say that the
			// table does not say better.
			if m.Limit.Context == 0 {
				continue
			}
			index[name+"/"+strings.ToLower(id)] = modelInfo{
				Context:      m.Limit.Context,
				MaxOutput:    m.Limit.Output,
				InputUSD:     m.Cost.Input,
				OutputUSD:    m.Cost.Output,
				CacheReadUSD: m.Cost.CacheRead,
				NoTools:      m.ToolCall != nil && !*m.ToolCall,
			}
		}
	}
	return index, nil
}
