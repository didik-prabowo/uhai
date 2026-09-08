package lsp

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// The one test that talks to a real language server, and the only one that can
// say the protocol was spoken correctly rather than agreed with by a fake.
//
// Skipped unless gopls is installed, which is the same rule the tool itself
// follows: a machine without it is not a failure. It is slow — gopls indexes
// this module before it answers anything — so it is worth the minute it takes
// and worth being the only one of its kind.
func TestAgainstRealGopls(t *testing.T) {
	if os.Getenv("UHAI_LSP_TEST") == "" {
		t.Skip("set UHAI_LSP_TEST=1 to run this against a real gopls (slow: it indexes the module)")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root = strings.TrimSuffix(root, "/internal/lsp")
	t.Cleanup(Close)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	found, ok, err := Symbols(ctx, root, "UseProviderOn")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skip("gopls is not installed")
	}
	if len(found) != 1 {
		t.Fatalf("want the one declaration, got %+v", found)
	}
	if !strings.HasSuffix(found[0].Path, "internal/cli/commands.go") || found[0].Kind != "function" {
		t.Fatalf("got %+v", found[0])
	}

	// What grep cannot do: the callers, and not the word.
	refs, err := At(ctx, root, "references", found[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) == 0 {
		t.Fatal("a function this project calls in several places has no references")
	}
	for _, r := range refs {
		if r.Line < 1 || r.Column < 1 {
			t.Fatalf("positions leave here 1-based, got %+v", r)
		}
	}

	// Asked twice, started once: the second question must not pay for another
	// index of the module.
	start := time.Now()
	if _, _, err := Symbols(ctx, root, "UseProviderOn"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("the second question took %s — the server is not being kept warm", took)
	}
}
