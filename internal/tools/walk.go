// Walking the project, and the rules for what not to walk into. Shared by
// glob and grep, which are the same search asked two ways.
package tools

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxMatches = 200

// searchTimeout bounds one search, the way BashTimeout bounds one command.
// maxMatches already stops a search that finds too much; nothing stopped one
// that finds too little in a tree too large, and a grep reads the contents of
// every file it walks. A turn was hostage to it: the model asks, and the only
// choices were to wait or to throw the whole turn away.
//
// Fifteen seconds rather than bash's two minutes because a search is meant to
// be the cheap way to look. What was found by then comes back rather than an
// error — half a monorepo is usually already past where the answer was.
//
// A var so a test can shrink it. Nothing else writes to it.
var searchTimeout = 15 * time.Second

// searchNote explains a result that stopped early, and is empty for one that
// ran to the end. Shared so glob and grep say the same thing about the same
// situation.
func searchNote(err error, found int) (string, bool) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		if found == 0 {
			return fmt.Sprintf("nothing found in the first %s of searching — narrow it with \"path\", or a more specific pattern", searchTimeout), true
		}
		return fmt.Sprintf("(partial: still searching after %s, so there may be more)", searchTimeout), false
	case errors.Is(err, context.Canceled):
		return "the user interrupted this search", true
	}
	return "", false
}

// walk visits every file under root, skipping the noise directories. The
// callback stops the walk by returning false.
//
// It takes a context because it is the one loop in a tool that can run for a
// long time without touching the network or a subprocess: glob and grep read
// the whole tree, and grep reads the contents too. Escape reached the HTTP
// call and reached bash, and stopped at the edge of this function — a grep
// that finds nothing in a large repository read every file to the end no
// matter what the user pressed.
func walk(ctx context.Context, root string, visit func(path string) bool) error {
	if root == "" {
		root = "."
	}
	// What the project itself says not to look at, read once for the walk
	// rather than per entry.
	ignore := readIgnore(root)

	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		// Checked per entry rather than per directory: one huge folder is
		// exactly the case where waiting for the next directory is waiting.
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return nil // an unreadable corner is not a reason to fail the search
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || ignore.dirs[d.Name()] || ignore.ignores(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if ignore.ignores(path) {
			return nil
		}
		if !visit(path) {
			return filepath.SkipAll
		}
		return nil
	})
}

// matcher compiles a name pattern once, for a walk that is about to ask the
// same question of every file under the root.
//
// A pattern with no slash is asked about the file name alone, which is what
// lets "*.go" find a file however deep it sits. One with a slash is asked
// about the whole path.
//
// It builds a regexp because filepath.Match has no notion of "any number of
// folders": it reads ** as two stars in a row, and neither of them crosses a
// separator. "internal/**/*.go" therefore matched exactly one level down and
// silently found nothing below it — the worst shape a tool failure can take,
// since an empty result reads as "there are no such files" and the model
// believes it. The tool's own description offers "**/*_test.go", so the
// pattern is one uhai invites.
//
// ponytail: no brace expansion. "{cmd,internal}/**" is still one literal
// string, and nothing promises otherwise; reach for a glob library if full
// glob syntax is ever actually wanted.
func matcher(pattern string) func(path string) bool {
	byName := !strings.Contains(pattern, "/")

	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				// "**/" spans any number of folders including none, so
				// "internal/**/*.go" finds internal/x.go as well.
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:[^/]*/)*")
					continue
				}
				b.WriteString(".*")
				continue
			}
			b.WriteString("[^/]*")

		case '?':
			b.WriteString("[^/]")

		case '[':
			// Character classes are kept because filepath.Match had them and
			// quietly dropping one would be the same silence being fixed here.
			// Glob spells a negated class [!abc]; a regexp spells it [^abc].
			if end := strings.IndexByte(pattern[i:], ']'); end > 1 {
				class := pattern[i : i+end+1]
				if len(class) > 1 && class[1] == '!' {
					class = "[^" + class[2:]
				}
				b.WriteString(class)
				i += end
				continue
			}
			b.WriteString(regexp.QuoteMeta("["))

		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		// An unmatchable pattern finds nothing, which is what a bad one meant.
		return func(string) bool { return false }
	}
	return func(path string) bool {
		path = filepath.ToSlash(path)
		if byName {
			path = filepath.Base(path)
		}
		return re.MatchString(path)
	}
}

// truncate keeps a matching line readable: a minified bundle on one line would
// otherwise fill the whole result on its own.
func truncate(line string) string {
	const max = 200
	line = strings.TrimRight(line, "\r")
	if len(line) > max {
		return line[:max] + "…"
	}
	return line
}
