package config

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// A cut-down api.json: one model per case the overlay has to get right.
const catalogFixture = `{
  "openai": {"models": {
    "gpt-5": {"tool_call": true, "limit": {"context": 400000, "output": 128000},
              "cost": {"input": 1.25, "output": 10, "cache_read": 0.125}},
    "text-embedding-3": {"tool_call": false, "limit": {"context": 8192}},
    "gpt-nolimit":      {"limit": {"context": 0, "output": 0}}
  }},
  "someone-else": {"models": {"whatever": {"limit": {"context": 999}}}}
}`

// isolateCatalog is isolate plus the bit only these tests need: the index is
// process-wide, so a test that leaves one loaded decides what the next sees.
func isolateCatalog(t *testing.T) {
	t.Helper()
	isolate(t)
	reset := func() {
		catalog.Lock()
		catalog.index = nil
		catalog.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func serveCatalog(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := catalogURL
	catalogURL = srv.URL
	t.Cleanup(func() { catalogURL = old })
}

func TestRefreshCatalogReducesToKnownProviders(t *testing.T) {
	isolateCatalog(t)
	serveCatalog(t, catalogFixture)

	if err := RefreshCatalog(); err != nil {
		t.Fatalf("RefreshCatalog: %v", err)
	}

	if _, ok := catalogInfo("openai", "gpt-5"); !ok {
		t.Error("gpt-5 not indexed")
	}
	// A provider uhai does not speak to is dropped before it is stored.
	if _, ok := catalogInfo("someone-else", "whatever"); ok {
		t.Error("indexed a provider uhai has no endpoint for")
	}
	// So is a model that reports no context window: the table says more.
	if _, ok := catalogInfo("openai", "gpt-nolimit"); ok {
		t.Error("indexed a model with no context window")
	}
}

func TestCatalogOverlaysTableButKeepsRetired(t *testing.T) {
	isolateCatalog(t)
	serveCatalog(t, catalogFixture)
	if err := RefreshCatalog(); err != nil {
		t.Fatalf("RefreshCatalog: %v", err)
	}

	// gpt-5 has no entry of its own in models.go and would fall to the
	// defaults, since no family prefix matches it either.
	if got := ContextWindow("openai/gpt-5"); got != 400_000 {
		t.Errorf("context = %d, want the catalog's 400000", got)
	}
	if !KnownModel("openai/gpt-5") {
		t.Error("a catalog hit should count as known, not as a family fallback")
	}

	// models.dev has no notion of a model the vendor still lists and no longer
	// serves, so the table has to keep saying so — and it still does for a
	// gateway pointed at one, which is the only way to reach Gemini now.
	if Recommendable("gw/gemini-2.5-flash") {
		t.Error("the table stopped marking a retired family")
	}

	// tool_call: false is the one flag the catalog may add.
	if SupportsTools("openai/text-embedding-3") {
		t.Error("tool_call false did not reach NoTools")
	}

	// An unqualified name never consults the catalog: there is no provider to
	// key on, and the table is the whole answer.
	if got := ContextWindow("gpt-5"); got != defaultContext {
		t.Errorf("bare name = %d, want the table's default %d", got, defaultContext)
	}
}

func TestCatalogMaxOutputFallsBackToTable(t *testing.T) {
	isolateCatalog(t)
	// Context but no output ceiling, which is how five OpenAI entries arrive.
	serveCatalog(t, `{"anthropic": {"models": {
	  "claude-haiku-4-5": {"limit": {"context": 200000}, "cost": {"input": 1, "output": 5}}
	}}}`)
	if err := RefreshCatalog(); err != nil {
		t.Fatalf("RefreshCatalog: %v", err)
	}
	if got := MaxOutput("anthropic/claude-haiku-4-5"); got != 64_000 {
		t.Errorf("max output = %d, want the table's 64000", got)
	}
}

func TestRefreshCatalogRejectsAnEmptyAnswer(t *testing.T) {
	isolateCatalog(t)
	serveCatalog(t, `{"someone-else": {"models": {"x": {"limit": {"context": 1}}}}}`)
	if err := RefreshCatalog(); err == nil {
		t.Error("stored a catalog with nothing in it for a whole day")
	}
}

func TestCachedModelsServesStaleWhenTheProviderFails(t *testing.T) {
	isolateCatalog(t)

	live := &fakeLister{models: []string{"gpt-4o-mini"}}
	got, err := CachedModels("openai", live)
	if err != nil || len(got) != 1 {
		t.Fatalf("first call = %v, %v", got, err)
	}
	if live.calls != 1 {
		t.Fatalf("calls = %d, want 1", live.calls)
	}

	// Fresh: the provider is not asked again.
	if _, err := CachedModels("openai", live); err != nil || live.calls != 1 {
		t.Errorf("fresh cache still went out: calls = %d", live.calls)
	}

	// Stale and failing: the old list beats an empty picker.
	expire(t, "models-openai", []string{"gpt-4o-mini"})
	broken := &fakeLister{err: errors.New("dial tcp: no route to host")}
	got, err = CachedModels("openai", broken)
	if err != nil {
		t.Fatalf("stale fallback returned an error: %v", err)
	}
	if len(got) != 1 || got[0] != "gpt-4o-mini" {
		t.Errorf("stale fallback = %v, want the cached list", got)
	}
	if broken.calls != 1 {
		t.Errorf("calls = %d, want the stale entry to be retried once", broken.calls)
	}
}

type fakeLister struct {
	models []string
	err    error
	calls  int
}

func (f *fakeLister) Models() ([]string, error) {
	f.calls++
	return f.models, f.err
}

// expire writes a cache file stamped two days ago, which is past every TTL
// here. Written by hand because nothing in the package backdates a stamp.
func expire(t *testing.T, name string, v []string) {
	t.Helper()
	path, err := InDir("cache", name+".json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cacheEnvelope[[]string]{At: time.Now().Add(-48 * time.Hour), V: v})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The six hours are a courtesy until a gateway gains a provider, and then they
// are a wall. /model refresh takes the lists down and leaves models.dev alone:
// that one is a public catalog nobody edits locally, and four megabytes is the
// wrong answer to "I added a model to my proxy".
func TestForgetModelListsSparesTheCatalog(t *testing.T) {
	isolateCatalog(t)

	if err := writeCache("models-9router", []string{"cc/claude-opus-5"}); err != nil {
		t.Fatal(err)
	}
	if err := writeCache("models-dev", map[string]modelInfo{"openai/gpt-5": {Context: 400_000}}); err != nil {
		t.Fatal(err)
	}

	if err := ForgetModelLists(); err != nil {
		t.Fatal(err)
	}
	if got, _ := readCache[[]string]("models-9router", modelListTTL); len(got) != 0 {
		t.Errorf("the provider list survived: %v", got)
	}
	if got, _ := readCache[map[string]modelInfo]("models-dev", catalogTTL); len(got) != 1 {
		t.Errorf("models.dev was thrown away too: %v", got)
	}
}

// Nothing cached is nothing to forget, not an error to report.
func TestForgetModelListsOnAColdCache(t *testing.T) {
	isolateCatalog(t)
	if err := ForgetModelLists(); err != nil {
		t.Errorf("an empty cache reported %v", err)
	}
}
