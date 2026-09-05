// Fetching a page. The one tool here that leaves the machine, which is why
// most of this file is about where it refuses to go rather than about HTTP.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// fetchTimeout bounds the whole request. A page that has not answered in
// fifteen seconds is not going to be worth the turn it is holding up.
const fetchTimeout = 15 * time.Second

// fetchMaxBytes is read before giving up. Execute truncates the text to 8,000
// characters anyway; this is the cap on what is pulled over the wire to get
// there, since markup is most of a page and none of it survives.
const fetchMaxBytes = 2 << 20 // 2 MiB

// fetchClient refuses to follow a redirect off the public internet. Checking
// the first address and then following wherever it leads is the shape the
// check has to have to be worth anything: an open redirect on a public host
// otherwise walks straight to the metadata endpoint.
var fetchClient = &http.Client{
	Timeout: fetchTimeout,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return publicHost(req.URL)
	},
}

func fetchURL(ctx context.Context, input json.RawMessage) (string, bool) {
	var args struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}

	target, err := url.Parse(strings.TrimSpace(args.URL))
	if err != nil {
		return fmt.Sprintf("could not read that URL: %v", err), true
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		// file:// would make this a second read_file with none of its rules,
		// and the rest are not things a page is served over.
		return fmt.Sprintf("only http and https can be fetched, not %q", target.Scheme), true
	}
	if err := publicHost(target); err != nil {
		return err.Error(), true
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err.Error(), true
	}
	req.Header.Set("User-Agent", "uhai")
	req.Header.Set("Accept", "text/html,text/plain,application/json;q=0.9,*/*;q=0.1")

	resp, err := fetchClient.Do(req)
	if err != nil {
		return fmt.Sprintf("could not fetch: %v", err), true
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBytes))
	if err != nil {
		return fmt.Sprintf("could not read the page: %v", err), true
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The status is the answer here: a 404 is a fact about the URL, and
		// the body is usually an error page nobody needs.
		return fmt.Sprintf("HTTP %d from %s", resp.StatusCode, target), true
	}

	text := string(body)
	if strings.Contains(resp.Header.Get("Content-Type"), "html") {
		text = textFromHTML(text)
	}
	if strings.TrimSpace(text) == "" {
		return "the page came back empty", true
	}
	return text, false
}

// privateBlocks are the addresses a page is never legitimately served from and
// that a machine can reach without leaving its network: loopback, the private
// ranges, and the link-local block that holds every cloud's metadata endpoint.
//
// This is the whole reason fetch_url is not simply an http.Get. A model asked
// to "check what this service returns" will try 169.254.169.254 as readily as
// anything else, and that address hands out credentials.
var privateBlocks = func() []*net.IPNet {
	var out []*net.IPNet
	for _, cidr := range []string{
		"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
		"169.254.0.0/16", "0.0.0.0/8", "100.64.0.0/10",
		"::1/128", "fc00::/7", "fe80::/10",
	} {
		if _, block, err := net.ParseCIDR(cidr); err == nil {
			out = append(out, block)
		}
	}
	return out
}()

// publicHost resolves the host and refuses if any address it answers to is one
// nobody serves a public page from. Every address is checked, not the first:
// a name that answers with one public address and one private one is the
// interesting case, not an accident.
func publicHost(u *url.URL) error {
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("that URL names no host")
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("refusing to fetch %s: that is this machine", host)
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("could not resolve %s: %v", host, err)
	}
	for _, ip := range ips {
		for _, block := range privateBlocks {
			if block.Contains(ip) {
				return fmt.Errorf("refusing to fetch %s: %s is on a private network", host, ip)
			}
		}
	}
	return nil
}

var (
	// No backreference: Go's regexp is RE2 and has none, so the closing tag is
	// spelled out again rather than matched against the opening one. Mismatched
	// nesting would strip a little more than it should, which for a stripper is
	// the harmless direction.
	dropped  = regexp.MustCompile(`(?is)<(?:script|style|noscript|svg)\b[^>]*>.*?</(?:script|style|noscript|svg)>`)
	tags     = regexp.MustCompile(`(?s)<[^>]*>`)
	blanks   = regexp.MustCompile(`\n{3,}`)
	trailing = regexp.MustCompile(`[ \t]+\n`)
)

// textFromHTML is a stripper, not a parser. Markup is most of a page and none
// of it is worth a token, so scripts and styles go whole, tags go, and
// entities come back as the characters they stand for.
//
// ponytail: regexes over HTML, which is famously not a language they can
// parse. It is enough for prose and documentation, which is what gets fetched;
// reach for a real parser when something needs the structure rather than the
// words.
func textFromHTML(page string) string {
	page = dropped.ReplaceAllString(page, "\n")
	page = tags.ReplaceAllString(page, "")
	page = html.UnescapeString(page)
	page = trailing.ReplaceAllString(page, "\n")
	return strings.TrimSpace(blanks.ReplaceAllString(page, "\n\n"))
}

// fetchURLTool is how the model is told about fetch_url.
var fetchURLTool = tool{
	name:    "fetch_url",
	confirm: true,
	run:     fetchURL,
	description: "Fetch a web page or document over http(s) and return it as text. " +
		"Use it to read documentation, a changelog, or an API reference the answer depends on. " +
		"Markup is stripped; only addresses on the public internet can be reached.",
	schema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"url": {"type": "string", "description": "The http or https URL to fetch"}
			},
			"required": ["url"]
		}`),
}
