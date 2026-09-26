// Searching the web. The other half of fetch_url, which could only open an
// address somebody already had — so a model asked to look something up
// invented one, and seven cookpad ids in a row were 404s that each cost a
// turn to discover.
//
// One backend, no interface. Every keyless source was tried and none of them
// answer: DuckDuckGo does not reply to this machine at all and Mojeek returns
// a captcha, so a key is the price of the feature. Brave is the boring
// choice — one GET, one header, JSON back, and a free tier that does not ask
// for a card. A second backend can be added the day somebody has a key for a
// different one.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// braveEndpoint is a var so a test can point it at a server it controls.
// Nothing else writes to it.
var braveEndpoint = "https://api.search.brave.com/res/v1/web/search"

const (
	// searchDefault is how many results come back unasked. Five is about what
	// fits in a glance and leaves room for the descriptions, which are the
	// part that decides which one is worth a fetch_url.
	searchDefault = 5
	searchMax     = 20
)

// searchTool is the search_web tool: what the model is told about it, and the
// thing that runs.
type searchTool struct{}

func (searchTool) Name() string { return NameSearch }

// NeedsConfirm is true for the same reason fetch_url's is, one step earlier:
// the query itself leaves the machine. What somebody is searching for is
// often more telling than the page they end up reading, and the model writes
// the query out of whatever is in the conversation.
func (searchTool) NeedsConfirm() bool { return true }

func (searchTool) Description() string {
	return "Search the web and return titles, addresses and short descriptions. " +
		"Use it when the answer depends on something outside this machine and you do not have a URL for it — " +
		"then open the most promising result with fetch_url, which is where the actual content comes from. " +
		"This returns descriptions, not pages."
}
func (searchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {"type": "string", "description": "What to search for, as you would type it into a search box"},
				"count": {"type": "integer", "description": "How many results, 1 to 20, default 5"}
			},
			"required": ["query"]
		}`)
}

func (searchTool) Run(ctx context.Context, _ string, input json.RawMessage) (string, bool) {
	var args struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}
	if strings.TrimSpace(args.Query) == "" {
		return "the query must not be empty", true
	}
	switch {
	case args.Count <= 0:
		args.Count = searchDefault
	case args.Count > searchMax:
		args.Count = searchMax
	}

	key := SearchKey()
	if key == "" {
		// An answer rather than an error, the way a missing language server
		// is: most machines are in this state and discovering it must not
		// cost the turn. It says not to retry because the model otherwise
		// tries the same call with a shorter query.
		return "there is no search key on this machine, so the web cannot be searched. " +
			"Do not try again this turn — tell the user to get a free key at " +
			"https://brave.com/search/api/ and add it with a \"brave\" entry in ~/.uhai/auth.json, " +
			"or set BRAVE_API_KEY. If they have given you an address, fetch_url still works.", false
	}

	endpoint := braveEndpoint + "?" + url.Values{
		"q":     {args.Query},
		"count": {fmt.Sprint(args.Count)},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err.Error(), true
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", key)

	// fetchClient rather than a second one: same timeout, and its redirect
	// rule already refuses to be walked off the public internet.
	resp, err := fetchClient.Do(req)
	if err != nil {
		return fmt.Sprintf("could not reach the search service: %v", err), true
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBytes))
	if err != nil {
		return fmt.Sprintf("could not read the answer: %v", err), true
	}
	if resp.StatusCode != http.StatusOK {
		// The service's own sentence, kept: 401 and 429 mean different things
		// to do about them, and only the body says which this is.
		return fmt.Sprintf("the search service answered %s: %s", resp.Status, truncate(string(body))), true
	}

	var answer struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return fmt.Sprintf("could not read the search results: %v", err), true
	}
	if len(answer.Web.Results) == 0 {
		return "nothing found for " + args.Query, false
	}

	var out strings.Builder
	for i, r := range answer.Web.Results {
		// The descriptions carry Brave's own <strong> around the matched
		// words, stripped with what fetch_url strips pages with.
		fmt.Fprintf(&out, "%d. %s\n   %s\n   %s\n", i+1, textFromHTML(r.Title), r.URL, textFromHTML(r.Description))
	}
	return strings.TrimRight(out.String(), "\n"), false
}

// SearchKey answers with the key the search service wants, "" when there is
// none. The default reads the environment and nothing else.
//
// It is a variable because credentials live in internal/config, and config
// imports this package — permission.go resolves rules against the tool names
// in names.go — so the arrow cannot also point the other way. config replaces
// this at startup with one that reads auth.json too, which leaves this
// package knowing nothing about where a credential is kept, the same way it
// knows nothing about which provider is in use.
//
// Not config's UHAI_API_KEY wildcard, whichever way it is reached: that fills
// in every provider's key field, which is right for a model provider somebody
// is pointing at a gateway and would be a leak here — the key paying for the
// conversation sent to a search engine that never asked for one.
var SearchKey = func() string { return strings.TrimSpace(os.Getenv(SearchKeyEnv)) }

// SearchKeyEnv is the variable that overrides the stored key, named here
// because config reads it too and two spellings of it is the split names.go
// exists to prevent.
const SearchKeyEnv = "BRAVE_API_KEY"
