// What a tool may do without being asked, in the shape Claude Code uses:
// three lists of rules, each rule a tool and optionally what it may act on.
//
//	"permissions": {
//	  "allow": ["Bash(go test:*)", "Read(*)"],
//	  "ask":   ["Bash(git push:*)"],
//	  "deny":  ["Write", "Bash(rm:*)", "Read(./.env)"]
//	}
//
// Deny wins over ask, ask over allow, and anything nobody named keeps its
// default: the tools that escape this process ask, the ones that only look do
// not.
package config

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/didik-prabowo/uhai/internal/tools"
)

// The three answers, as named in settings.json.
const (
	PermAllow = "allow"
	PermAsk   = "ask"
	PermDeny  = "deny"
)

// Permissions is the settings block: rules, grouped by the answer they give.
type Permissions struct {
	Allow []string `json:"allow,omitempty"`
	Ask   []string `json:"ask,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// aliases are the friendly spellings Claude Code uses, on top of each tool's
// own name — which always works, and is not listed here. Listing both would be
// the same string written twice, and the copy that goes stale is the one that
// silently stops matching a rule somebody wrote months ago.
var aliases = map[string]string{
	"bash":  tools.NameBash,
	"read":  tools.NameRead,
	"write": tools.NameWrite,
	"edit":  tools.NameEdit,
	"fetch": tools.NameFetch,
	"task":  tools.NameSpawnTask,
}

// toolName resolves what a rule calls a tool to what the code calls it. A tool
// answers to its own name without anyone maintaining a list, so adding a tool
// makes it nameable in a rule by itself — the previous version was a
// hand-written map, and spawn_task was missing from it for months.
func toolName(spelling string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(spelling))
	if s == "*" {
		return "*", true
	}
	if real, ok := aliases[s]; ok {
		return real, true
	}
	// The agent's tool: not in Definitions, but a rule has to be able to name
	// it, or "deny": ["spawn_task"] parses as nothing and protects nothing.
	if s == tools.NameSpawnTask {
		return s, true
	}
	for _, spec := range tools.Definitions() {
		if spec.Name == s {
			return s, true
		}
	}
	return "", false
}

// rule is one line of one of those lists: a tool, and what it may act on —
// a command for Bash, a path for the file tools, empty for the whole tool.
type rule struct {
	tool string
	spec string
}

func parseRule(text string) (rule, bool) {
	text = strings.TrimSpace(text)

	name, spec := text, ""
	if open := strings.Index(text, "("); open >= 0 && strings.HasSuffix(text, ")") {
		name, spec = text[:open], text[open+1:len(text)-1]
	}

	tool, ok := toolName(name)
	if !ok {
		return rule{}, false // a tool nobody has is not a rule, it is a typo
	}
	return rule{tool: tool, spec: strings.TrimSpace(spec)}, true
}

// matches reports whether this rule covers a call. subject is the command for
// Bash and the path for the file tools; a rule with no specifier covers the
// whole tool whatever the subject.
func (r rule) matches(tool, subject string) bool {
	if r.tool != tool && r.tool != "*" {
		return false
	}
	if r.spec == "" {
		return true
	}
	if tool == tools.NameBash {
		return matchCommand(r.spec, subject)
	}
	return matchPath(r.spec, subject)
}

// Subjects is everything one call acts on: the command for a shell call, and
// every path for a file tool — because edit_file takes a list, and a rule about
// one of those paths has to be answered even when the call carries twenty.
//
// It is here rather than in the front end because two places need the same
// answer — the confirmation, and the check that runs before a tool is offered —
// and two copies of it would drift in exactly the way that leaves one of them
// ignoring a rule somebody wrote.
//
// Always at least one element, "" when there is nothing to narrow by, so a
// caller cannot accidentally skip the loop and allow everything.
func Subjects(tool, input string) []string {
	var args struct {
		Command string `json:"command"`
		Path    string `json:"path"`
		Edits   []struct {
			Path string `json:"path"`
		} `json:"edits"`
	}
	json.Unmarshal([]byte(input), &args)

	if tool == tools.NameBash {
		return []string{args.Command}
	}

	var paths []string
	if args.Path != "" {
		paths = append(paths, args.Path)
	}
	for _, e := range args.Edits {
		if e.Path != "" {
			paths = append(paths, e.Path)
		}
	}
	if len(paths) == 0 {
		return []string{""}
	}
	return paths
}

// PermissionFor answers a whole call, which is not the same as answering one
// path. A batch is only as permissive as its least permissive part, the way a
// chained shell line is — and for the same reason: a deny that can be escaped by
// putting the denied path second in a list is not a deny.
//
// The folding lives here and nowhere else. Every time one decision was copied
// into two places today, the second copy was the one that was wrong.
//
// root is the project whose rules are being asked for, "" for the process's
// own directory. It is not optional and not defaulted: this is the one thing
// in config consulted while a turn is running, and in the daemon a turn runs
// for a project that is not the one the process was started in.
func PermissionFor(root, tool, input string) string {
	answer := PermAllow
	for _, subject := range Subjects(tool, input) {
		switch Permission(root, tool, subject) {
		case PermDeny:
			return PermDeny
		case PermAsk:
			answer = PermAsk
		}
	}
	return answer
}

// Permission answers one call: the tool// Permission answers one call: the tool, and what it wants to act on — the
// command for run_bash, the path for the file tools, "" when there is nothing
// to narrow by.
//
// Deny is checked first and wins outright, then ask, then allow. A rule with a
// specifier is more specific than a bare tool name, so "deny Bash(rm:*)" and
// "allow Bash" together mean everything but rm.
func Permission(root, tool, subject string) string {
	s, err := LoadSettingsIn(root)
	if err != nil {
		return defaultPermission(tool, subject)
	}

	// A chained command is only as safe as its least safe part.
	if tool == tools.NameBash {
		return commandPermission(s, subject)
	}

	switch {
	case bestMatch(s.Permissions.Deny, tool, subject) >= 0:
		return PermDeny
	case bestMatch(s.Permissions.Ask, tool, subject) >= 0:
		return PermAsk
	case bestMatch(s.Permissions.Allow, tool, subject) >= 0:
		return PermAllow
	}
	return defaultPermission(tool, subject)
}

// commandPermission decides a whole command line, which may be several
// commands. Every part has to be allowed for the line to be allowed, any part
// denied denies the line, and a command that builds itself at runtime is never
// waved through — the rules can only judge what they can read.
func commandPermission(s Settings, command string) string {
	parts := splitCommand(command)
	if len(parts) == 0 {
		return defaultPermission(tools.NameBash, command)
	}

	answer := PermAllow
	for _, part := range parts {
		switch rule := onePart(s, part); rule {
		case PermDeny:
			return PermDeny
		case PermAsk:
			answer = PermAsk
		}
	}
	if answer == PermAllow && buildsItself(command) {
		return PermAsk
	}
	return answer
}

func onePart(s Settings, command string) string {
	switch {
	case bestMatch(s.Permissions.Deny, tools.NameBash, command) >= 0:
		return PermDeny
	case bestMatch(s.Permissions.Ask, tools.NameBash, command) >= 0:
		return PermAsk
	case bestMatch(s.Permissions.Allow, tools.NameBash, command) >= 0:
		return PermAllow
	}
	return defaultPermission(tools.NameBash, command)
}

// bestMatch returns the length of the longest matching rule, -1 for none. The
// length is what makes "Bash(git push:*)" beat "Bash(git:*)" whichever order
// they were written in.
func bestMatch(rules []string, tool, subject string) int {
	best := -1
	for _, text := range rules {
		r, ok := parseRule(text)
		if !ok || !r.matches(tool, subject) {
			continue
		}
		if len(r.spec) > best {
			best = len(r.spec)
		}
	}
	return best
}

// ToolDenied reports whether a tool is refused outright, specifier or not.
// Such a tool is never offered to the model at all.
func ToolDenied(root, tool string) bool {
	s, err := LoadSettingsIn(root)
	if err != nil {
		return false
	}
	for _, text := range s.Permissions.Deny {
		if r, ok := parseRule(text); ok && r.spec == "" && (r.tool == tool || r.tool == "*") {
			return true
		}
	}
	return false
}

// defaultPermission is what applies when no rule mentions a tool: the ones that
// escape this process ask, the rest do not — except for reading a file that is
// a credential by convention, which is refused.
//
// That exception is the only place a default looks at the subject, and it is
// there because reading is the one capability with no confirmation in front of
// it. A tool result goes four places — the model reads it, the history keeps
// it, the next request sends it to the provider, and the session file writes it
// to disk in plaintext — so `read_file .env` put a credential in all four
// without anybody being asked anything. See the security issue for the half of
// that problem this does not solve.
//
// Refused rather than asked: a question about reading a file needs a
// confirmation for a tool that has none, and building one is a change to every
// front end. A denial is one answer the model can read and act on, and anyone
// who means it writes `"allow": ["Read(./.env)"]`, which is checked before this
// and wins.
func defaultPermission(tool, subject string) string {
	if tool == tools.NameRead && tools.SecretPath(subject) {
		return PermDeny
	}
	if tools.NeedsConfirm(tool) {
		return PermAsk
	}
	return PermAllow
}

// splitCommand cuts a line into the commands it is made of, at the operators
// that separate them — and only outside quotes. Inside them an operator is
// text: `echo "a && b"` is one command, and a regexp split it into two parts
// neither of which was anything that would run, so the answer became the least
// permissive of two fictions.
//
// A lone `&` is not a separator. `2>&1` is the everyday case, and cutting there
// left `go build 2>` and `1`, the second of which no rule names, so a line that
// was allowed turned into a question.
//
// A backslash escapes the next byte, outside quotes as well as inside double
// ones, and that branch is not cosmetic: without it `echo \" && rm -rf /` looked
// like one quoted command to this function and two commands to bash — which is
// the direction that matters, because thinking we are inside a quote is how a
// separator goes unseen and the second half of a line rides in on the first
// half's rule.
//
// Where this parse still differs from bash's, it differs by closing a quote
// early, which splits into more parts rather than fewer — and more parts can
// only make the answer stricter.
func splitCommand(command string) []string {
	var parts []string
	var b strings.Builder
	var quote byte

	cut := func() {
		parts = append(parts, b.String())
		b.Reset()
	}

	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case c == '\\' && i+1 < len(command) && quote != '\'':
			// Escaped, so whatever follows is a character and not syntax.
			// Single quotes are the exception: a shell has no escape inside them.
			b.WriteByte(c)
			i++
			b.WriteByte(command[i])

		case quote != 0:
			if c == quote {
				quote = 0
			}
			b.WriteByte(c)

		case c == '\'' || c == '"':
			quote = c
			b.WriteByte(c)

		case c == ';' || c == '\n' || c == '|':
			cut() // and `||` as well: the empty part between falls out below

		case c == '&':
			switch {
			case i+1 < len(command) && command[i+1] == '&':
				i++ // one operator, two bytes
				cut()

			// A redirection rather than a separator: 2>&1, 1>&2, &>file. These
			// are the everyday case the rest of this has to leave alone. One
			// case with two conditions, not two cases — a Go case does not fall
			// through, and an empty one here would drop the & and turn 2>&1
			// into a redirect to a file called 1.
			case i > 0 && command[i-1] == '>',
				i+1 < len(command) && command[i+1] == '>':
				b.WriteByte(c)

			default:
				// Everything else. A lone & backgrounds what is on its left and
				// runs what is on its right, so both halves are commands and
				// both have to be judged.
				cut()
			}

		default:
			b.WriteByte(c)
		}
	}
	cut()

	out := parts[:0]
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// computed is a `$` the shell will replace with something, or a substitution in
// backticks. `$(`, `${` and a bare `$NAME` all qualify, because the text does
// not say what the command will be — which is the whole premise the rules rest
// on: they can only judge what they can read.
//
// A bare `$NAME` was the gap. The check named `$(`, backticks, `${` and `$((`
// and let `$HOME` through, so `rm -rf $HOME/build` was waved past a
// `Bash(rm:*)` allow rule in silence, while `rm -rf ${HOME}/build` asked. Same
// expansion, same unreadable command, different answer.
//
// A digit or a `?` after the `$` deliberately does not qualify. `bash -c` is
// given no positional arguments, so `$1` expands to nothing and can compose
// nothing, and `awk '{print $1}'` is far too ordinary to spend a confirmation
// on. Nor does a `$` before a quote: `grep 'x$'` and `sed 's/$//'` are anchors,
// and `$'\x72\x6d'` cannot be *allowed* by a prefix rule in the first place,
// since the text of it never begins with the command it decodes to.
var computed = regexp.MustCompile("\\$[({A-Za-z_]|`")

// buildsItself reports whether a command computes part of itself as it runs.
// What such a command will actually do cannot be read from the text, so it is
// never allowed silently.
func buildsItself(command string) bool {
	return computed.MatchString(command)
}

// matchCommand matches a rule's specifier against one command. "git push:*"
// is the Claude Code spelling for a prefix; "*" and "?" also work, and both
// cross slashes, since a command line is not a path.
func matchCommand(spec, command string) bool {
	command = strings.TrimSpace(command)
	if prefix, ok := strings.CutSuffix(spec, ":*"); ok {
		prefix = strings.TrimSpace(prefix)
		return command == prefix || strings.HasPrefix(command, prefix+" ")
	}
	if strings.ContainsAny(spec, "*?") {
		return glob(spec, command, false)
	}
	return command == spec
}

// matchPath matches a rule's specifier against a path. "*" stops at a slash
// and "**" does not, the way every other tool spells it.
func matchPath(spec, path string) bool {
	spec = strings.TrimPrefix(spec, "./")
	path = strings.TrimPrefix(path, "./")
	return glob(spec, path, true)
}

// glob compiles a pattern once and matches the whole string. stopAtSlash makes
// a single "*" a path wildcard rather than a general one.
func glob(pattern, subject string, stopAtSlash bool) bool {
	any := ".*"
	if stopAtSlash {
		any = "[^/]*"
	}

	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch {
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i++ // the second star
		case pattern[i] == '*':
			b.WriteString(any)
		case pattern[i] == '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(subject)
}
