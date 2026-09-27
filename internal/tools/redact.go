// What counts as a credential here, answered two ways: the names of files that
// hold one, and the shapes of the ones that can be recognised inside a file.
// Three callers need one or the other — config refuses to read a file by its
// name, grep skips the same files while searching, and Execute takes recognised
// shapes out of every result.
//
// A tool result goes four places — the model reads it, the history keeps it, the next request sends it
// to a provider, and the session file writes it to disk in plaintext — so a key
// that reaches one of them reaches all four, and the only place to catch it is
// where every tool's output passes through.
package tools

import (
	"path/filepath"
	"regexp"
	"strings"
)

// redactedPrefix opens the marker that replaces a credential. It is a prefix
// rather than a whole string because the marker names the kind, and edit_file
// reads it to explain a failure nobody else could: text that came back redacted
// cannot be used as the text to replace.
const redactedPrefix = "[redacted: "

// credentials are the shapes that cannot be anything else.
//
// Every one of these is a prefix a vendor publishes and reserves — `ghp_`,
// `AKIA`, `sk-ant-` — so matching is recognition rather than inference. That
// distinction is the whole design: detecting a secret by how random it looks
// false-positives on every hash, uuid and base64 blob in a repository, and
// reading repositories is what this program does. A registered prefix cannot be
// a git sha by accident.
//
// Each is anchored at a word boundary, which is load-bearing rather than tidy:
// without it `sk-` matched inside `task-`, and a CI URL ending in
// `/task-a1b2c3…` came back redacted as an OpenAI key. `MSG.` did the same to
// SendGrid's shape. A false positive here is worse than it looks — the model
// reads a marker where a value was, and starts repairing a file that is fine.
//
// What this deliberately does not catch is a secret with no shape —
// `password: hunter2` in a yaml file. Nothing catches that without guessing,
// and the file-name default in config is the other half of the answer: a
// conventionally named file is refused by path, whatever is inside it.
var credentials = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"private key", regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)},
	{"anthropic api key", regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}`)},
	{"openai api key", regexp.MustCompile(`\bsk-proj-[A-Za-z0-9_-]{20,}`)},
	// Classic OpenAI keys are alphanumeric after the dash, which is also what
	// keeps this from swallowing `sk-ant-…`: that has a dash three characters
	// in, so the run of twenty is never there.
	{"openai api key", regexp.MustCompile(`\bsk-[A-Za-z0-9]{32,}`)},
	{"github token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}`)},
	{"github token", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`)},
	{"gitlab token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`)},
	{"aws access key id", regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}`)},
	{"google api key", regexp.MustCompile(`\bAIza[A-Za-z0-9_-]{35}`)},
	{"slack token", regexp.MustCompile(`\bxox[bpaso]-[A-Za-z0-9-]{10,}`)},
	{"stripe key", regexp.MustCompile(`\b[sr]k_live_[A-Za-z0-9]{20,}`)},
	{"digitalocean token", regexp.MustCompile(`\bdop_v1_[a-f0-9]{64}`)},
	{"sendgrid key", regexp.MustCompile(`\bSG\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}`)},
}

// Redact replaces the credentials it can recognise with a marker naming the
// kind. Visible rather than silent: a value quietly swapped out makes the model
// reason about a file that does not exist, and it will try to repair the config
// it thinks is broken. The marker says what was there and that nothing on disk
// changed.
func Redact(text string) string {
	for _, c := range credentials {
		text = c.re.ReplaceAllString(text, redactedPrefix+c.kind+"]")
	}
	return text
}

// secretNames are the files that hold a credential by convention, matched
// against the base name of a path.
//
// A list rather than an inference, for the reason above. Documented in
// docs/guide/permissions.md, which a test holds to this list.
var secretNames = []string{
	".env", ".env.*",
	"*.pem", "*.key", "*.p12", "*.pfx", "*.jks", "*.keystore",
	"id_rsa*", "id_dsa*", "id_ecdsa*", "id_ed25519*",
	".netrc", ".npmrc", ".pypirc", ".dockercfg",
	"credentials", // ~/.aws/credentials, and gcloud's
	"auth.json",   // uhai's own
	"hosts.yml",   // gh's
	"*.kdbx",
}

// secretDirs hold nothing but keys, so the directory is the answer and the file
// names inside it do not have to be guessed at.
var secretDirs = []string{".ssh", ".gnupg"}

// openSecrets look exactly like the list above and are checked into
// repositories on purpose: a template with the values taken out, read constantly
// and never sensitive. Without them ".env.*" would refuse .env.example, and a
// default that fires on an ordinary file is how a person learns to override the
// whole category.
var openSecrets = []string{".env.example", ".env.sample", ".env.template", ".env.dist", ".env.defaults"}

// secretMatch compiles the name patterns once. grep asks this of every file in
// the tree, and matcher builds a regexp per pattern — twenty compiles per file
// would have made searching pay for this on every result.
var secretMatch = func() []func(string) bool {
	out := make([]func(string) bool, 0, len(secretNames))
	for _, name := range secretNames {
		out = append(out, matcher(name))
	}
	return out
}()

// SecretPath reports whether a path is a credential by convention. Exported for
// config, which refuses to read one, from the package that knows what a file is.
func SecretPath(path string) bool {
	if path == "" {
		return false
	}
	clean := filepath.ToSlash(path)
	base := filepath.Base(clean)

	for _, open := range openSecrets {
		if strings.EqualFold(base, open) {
			return false
		}
	}
	for _, dir := range secretDirs {
		if strings.Contains(clean, "/"+dir+"/") || strings.HasPrefix(clean, dir+"/") {
			return true
		}
	}
	for _, match := range secretMatch {
		if match(base) {
			return true
		}
	}
	return false
}
