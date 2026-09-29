// What the rest of uhai asks for, and the servers kept warm to answer it.
package lsp

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// Symbol is one declaration the server knows about.
type Symbol struct {
	Name      string
	Container string // the package or type it belongs to, when the server says
	Kind      string
	Location
}

// Location is a place in the project. Line and Column are 1-based, which is
// what a person and every editor mean by them; the protocol counts from zero
// and that conversion happens here and nowhere else.
type Location struct {
	Path   string
	Line   int
	Column int
}

func (l Location) String() string { return fmt.Sprintf("%s:%d:%d", l.Path, l.Line, l.Column) }

// wireLocation is a Location as it travels.
type wireLocation struct {
	URI   string `json:"uri"`
	Range struct {
		Start struct {
			Line      int `json:"line"`
			Character int `json:"character"`
		} `json:"start"`
	} `json:"range"`
}

func (w wireLocation) location() Location {
	return Location{Path: fromURI(w.URI), Line: w.Range.Start.Line + 1, Column: w.Range.Start.Character + 1}
}

// running is every server uhai has started, keyed by project and language.
//
// Keyed by project because one uhai serves several: the daemon holds a
// workspace per project and a server started for one must never answer a
// question about another — it would answer confidently, from the wrong tree.
// Kept rather than started per call because starting one costs an index of the
// whole module, which is tens of seconds on a large repository and would make
// the tool slower than the grep it replaces.
//
// used is when each was last asked something, which is the other half of
// keeping them: see idleAfter.
var running struct {
	sync.Mutex
	byKey map[string]*client
	used  map[string]time.Time
}

// idleAfter is how long a server nobody has asked anything is kept.
//
// Kept warm is the whole point, so this is deliberately long: a server costs
// an index of the module to start, and a timeout short enough to notice would
// pay for that index again on the next question. What it bounds is the other
// end. gopls is hundreds of megabytes, there is one per project and language,
// and until this existed none of them stopped until the process did — which in
// a daemon means for as long as any terminal stays attached, since a terminal
// attached is what keeps the daemon alive. A machine with four checkouts open
// held four indexes of four repositories, in a process nobody was watching.
//
// Half an hour is the daemon's own idle timeout, for the same reason it is:
// long enough to survive lunch.
const idleAfter = 30 * time.Minute

// sweepEvery is how often the servers are looked at. A minute is far below the
// timeout, so asking costs nothing and the answer is never much out of date.
const sweepEvery = time.Minute

// sweeping starts the reaper on the first server started, rather than in an
// init: a uhai that never asks a symbol question should not have a goroutine
// waking every minute to look at a map that stays empty.
var sweeping sync.Once

func reapIdle() {
	for range time.Tick(sweepEvery) {
		reapOnce(idleAfter)
	}
}

// reapOnce stops every server nobody has asked anything for longer than after,
// and returns how many went — a count, because a test cannot watch a process
// disappear and the map is the only thing it can hold on to.
func reapOnce(after time.Duration) int {
	running.Lock()
	var done []*client
	for key, c := range running.byKey {
		if time.Since(running.used[key]) < after {
			continue
		}
		done = append(done, c)
		delete(running.byKey, key)
		delete(running.used, key)
	}
	running.Unlock()

	// Outside the lock. close waits up to two seconds for a server that does
	// not take the hint, and a question arriving in that window has a server
	// of its own to start rather than a reaping to wait for.
	for _, c := range done {
		c.close()
	}
	return len(done)
}

// Symbols finds declarations by name across the project.
//
// It is the entry point rather than a position because a position is what the
// model does not have: it knows a name. Asking it for line and column means
// asking it to grep first and then count characters, which is the step it gets
// wrong. Every position uhai sends back to a server came from a server, so
// nothing here ever computes one — which is also why none of the protocol's
// UTF-16 column arithmetic appears in this package.
//
// ok is false when no server can answer for this project at all: none
// installed, or none of the languages uhai knows. That is a normal answer, not
// a failure — the model is told to use grep and the turn goes on.
func Symbols(ctx context.Context, root, name string) (found []Symbol, ok bool, err error) {
	for _, s := range forProject(root) {
		c, cerr := serverFor(ctx, root, s)
		if cerr != nil {
			continue // not installed, or would not start; another language may still answer
		}
		ok = true

		var raw []struct {
			Name          string       `json:"name"`
			ContainerName string       `json:"containerName"`
			Kind          int          `json:"kind"`
			Location      wireLocation `json:"location"`
		}
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		qerr := c.call(cctx, "workspace/symbol", map[string]any{"query": name}, &raw)
		cancel()
		if qerr != nil {
			err = qerr
			continue
		}
		for _, r := range raw {
			// The protocol's query is a fuzzy one — gopls answers "Symbols"
			// to "Symbol" — and the model asked about a name it read in the
			// source. A near miss offered as the answer is worse than none.
			if r.Name != name {
				continue
			}
			found = append(found, Symbol{
				Name: r.Name, Container: r.ContainerName,
				Kind: kindName(r.Kind), Location: r.Location.location(),
			})
		}
	}
	return found, ok, err
}

// At answers what: "definition", "references" or "implementations", for a
// symbol Symbols already found. The position travels back exactly as it came.
func At(ctx context.Context, root, what string, s Symbol) ([]Location, error) {
	lang, known := forFile(s.Path)
	if !known {
		return nil, fmt.Errorf("no language server for %s", s.Path)
	}
	c, err := serverFor(ctx, root, lang)
	if err != nil {
		return nil, err
	}

	params := map[string]any{
		"textDocument": map[string]any{"uri": fileURI(s.Path)},
		"position":     map[string]any{"line": s.Line - 1, "character": s.Column - 1},
	}
	method := ""
	switch what {
	case "definition":
		method = "textDocument/definition"
	case "references":
		method = "textDocument/references"
		// Without the declaration: the model asked who uses this, and it
		// already has the declaration in its hand.
		params["context"] = map[string]any{"includeDeclaration": false}
	case "implementations":
		method = "textDocument/implementation"
	default:
		return nil, fmt.Errorf("unknown question %q", what)
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	// A server may answer one location or a list of them, and the protocol
	// allows both for definition. Try the list, fall back to the single.
	var many []wireLocation
	if err := c.call(ctx, method, params, &many); err == nil && len(many) > 0 {
		return locations(many), nil
	}
	var one wireLocation
	if err := c.call(ctx, method, params, &one); err != nil {
		return nil, err
	}
	if one.URI == "" {
		return nil, nil
	}
	return locations([]wireLocation{one}), nil
}

func locations(ws []wireLocation) []Location {
	out := make([]Location, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.location())
	}
	return out
}

// serverFor is the running server for one project and language, started if
// this is the first question it has been asked.
func serverFor(ctx context.Context, root string, s server) (*client, error) {
	// PATH first, and cheaply: a server nobody installed is the common case on
	// any machine, and it should cost a lookup rather than a failed spawn and
	// a handshake timeout.
	if _, err := exec.LookPath(s.argv[0]); err != nil {
		return nil, fmt.Errorf("%s is not installed", s.argv[0])
	}

	key := root + "\x00" + s.lang
	running.Lock()
	defer running.Unlock()
	if running.byKey == nil {
		running.byKey, running.used = map[string]*client{}, map[string]time.Time{}
	}
	// Before the early return as well as after it, so a server answering
	// questions all afternoon is not reaped for never having been started
	// twice.
	running.used[key] = time.Now()
	sweeping.Do(func() { go reapIdle() })

	if c, ok := running.byKey[key]; ok {
		return c, nil
	}
	// Under the lock on purpose. Starting one costs an index of the module,
	// and two turns asking at once would otherwise start two of them and keep
	// whichever finished last — paying twice and leaking the loser.
	c, err := start(ctx, root, s.argv)
	if err != nil {
		delete(running.used, key) // no server, so nothing for the reaper to hold
		return nil, err
	}
	running.byKey[key] = c
	return c, nil
}

// Close stops every server. For a test, and for a process that means to exit
// tidily; a server also exits on its own when the pipe to it closes, which is
// what happens when uhai dies without getting this far.
func Close() {
	running.Lock()
	defer running.Unlock()
	for key, c := range running.byKey {
		c.close()
		delete(running.byKey, key)
		delete(running.used, key)
	}
}

// kindName is the protocol's SymbolKind as a word. Only the kinds a person
// would ask about are named; the rest keep their number rather than being
// guessed at.
func kindName(kind int) string {
	names := map[int]string{
		5: "class", 6: "method", 7: "property", 8: "field", 9: "constructor",
		10: "enum", 11: "interface", 12: "function", 13: "variable", 14: "constant",
		23: "struct", 26: "type parameter",
	}
	if name, ok := names[kind]; ok {
		return name
	}
	return fmt.Sprintf("kind %d", kind)
}
