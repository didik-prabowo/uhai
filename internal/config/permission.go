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
	"regexp"
	"strings"

	"github.com/didik-prabowo/ouhai/internal/tools"
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

// toolNames maps what a rule may call a tool to what the code calls it. Both
// spellings are accepted: the familiar one from Claude Code, and ouhai's own.
var toolNames = map[string]string{
	"bash": "run_bash", "run_bash": "run_bash",
	"read": "read_file", "read_file": "read_file",
	"write": "write_file", "write_file": "write_file",
	"edit": "edit_file", "edit_file": "edit_file",
	"glob": "glob",
	"grep": "grep",
	"*":    "*",
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

	tool, ok := toolNames[strings.ToLower(strings.TrimSpace(name))]
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
	if tool == "run_bash" {
		return matchCommand(r.spec, subject)
	}
	return matchPath(r.spec, subject)
}

// Permission answers one call: the tool, and what it wants to act on — the
// command for run_bash, the path for the file tools, "" when there is nothing
// to narrow by.
//
// Deny is checked first and wins outright, then ask, then allow. A rule with a
// specifier is more specific than a bare tool name, so "deny Bash(rm:*)" and
// "allow Bash" together mean everything but rm.
func Permission(tool, subject string) string {
	s, err := LoadSettings()
	if err != nil {
		return defaultPermission(tool)
	}

	// A chained command is only as safe as its least safe part.
	if tool == "run_bash" {
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
	return defaultPermission(tool)
}

// commandPermission decides a whole command line, which may be several
// commands. Every part has to be allowed for the line to be allowed, any part
// denied denies the line, and a command that builds itself at runtime is never
// waved through — the rules can only judge what they can read.
func commandPermission(s Settings, command string) string {
	parts := splitCommand(command)
	if len(parts) == 0 {
		return defaultPermission("run_bash")
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
	case bestMatch(s.Permissions.Deny, "run_bash", command) >= 0:
		return PermDeny
	case bestMatch(s.Permissions.Ask, "run_bash", command) >= 0:
		return PermAsk
	case bestMatch(s.Permissions.Allow, "run_bash", command) >= 0:
		return PermAllow
	}
	return defaultPermission("run_bash")
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
func ToolDenied(tool string) bool {
	s, err := LoadSettings()
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

// defaultPermission is what applies when no rule mentions a tool: the ones
// that escape this process ask, the rest do not.
func defaultPermission(tool string) string {
	if tools.NeedsConfirm(tool) {
		return PermAsk
	}
	return PermAllow
}

// shellOperators are where one command ends and the next begins. Splitting on
// them is what stops "git status && rm -rf /" from being allowed because it
// starts with git.
var shellOperators = regexp.MustCompile(`&&|\|\||;|\||\n`)

func splitCommand(command string) []string {
	var out []string
	for _, part := range shellOperators.Split(command, -1) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// buildsItself reports whether a command computes part of itself as it runs,
// through substitution or an expansion of a variable. What such a command will
// actually do cannot be read from the text, so it is never allowed silently.
func buildsItself(command string) bool {
	return strings.Contains(command, "$(") ||
		strings.Contains(command, "`") ||
		strings.Contains(command, "${") ||
		strings.Contains(command, "$((")
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
