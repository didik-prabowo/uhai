// Text the terminal has to be measured and cut by hand: colour escapes take
// no columns, and a line that fills the last one makes the terminal wrap it on
// its own. These are the pieces both front ends share.
package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The same palette as theme.go, for the few places that assemble a line as a
// string rather than through lipgloss: a box, a task listing. Truecolor rather
// than the terminal's own 8, so the two halves of the interface agree.
const (
	dim      = "\x1b[38;2;148;163;184m" // slate, for what is there but not the point
	accentAt = "\x1b[38;2;167;139;250m" // violet, for what the eye should land on
	green    = "\x1b[38;2;134;239;172m" // a key is in place
	reset    = "\x1b[0m"
)

// markThinking deals with the tags a model uses to fence its working out when
// the provider has no field for it. GLM and the DeepSeek family write
// <think>…</think> straight into the answer, and a gateway that does not map
// it to reasoning_content passes the tags through — so an answer opened with a
// literal "<think>" and ran into its own reasoning with nothing between them.
//
// A block that has closed is dropped whole. Once the answer is there the
// working out is worth nothing, and anything left in its place — the two
// markers, or the one line they were collapsed to — is a leftover of the wait
// rather than part of the reply. The spinner already goes when the wait is
// over; this is the same thing said in the chat.
//
// An unclosed block is the one still streaming, so it keeps its marker and its
// text: while it is all there is, it is worth watching. Plain text and no
// markdown, because this goes through glamour next and a rule of dashes would
// turn the line above it into a heading.
//
// A streamed chunk can split a tag down the middle. Nothing is done about it:
// the stream is redrawn from the whole answer so far, so at worst one frame
// shows half a tag and the next one does not.
func markThinking(text string) string {
	return thinkTags.Replace(thinkBlock.ReplaceAllString(text, ""))
}

// Non-greedy, so two blocks in one answer stay two. The trailing space goes
// with it, or the answer starts on the blank lines the block was padded with.
var thinkBlock = regexp.MustCompile(`(?s)<think(?:ing)?>.*?</think(?:ing)?>\s*`)

// What is left after the blocks are gone is an unpaired tag: an opening one is
// the working out still arriving, a closing one is a block whose start never
// came. Blank lines each side, so each marker is a paragraph of its own —
// without them glamour reflows the whole thing into one and the markers land
// mid-sentence, which is worse than the tags were: at least a tag looked like
// a tag.
var thinkTags = strings.NewReplacer(
	"<think>", "\n\n✻ thinking\n\n",
	"</think>", "\n\n✻ answer\n\n",
	"<thinking>", "\n\n✻ thinking\n\n",
	"</thinking>", "\n\n✻ answer\n\n",
)

// squeezeBlank trims the padding the markers bring to one blank line, for the
// live block: the markers are padded for glamour, which reflows and collapses
// it, and the stream is drawn raw — so a marker arrived with two empty rows
// above and below it and the answer looked like it belonged to another turn.
func squeezeBlank(s string) string {
	return strings.Trim(blankRuns.ReplaceAllString(s, "\n\n"), "\n")
}

var blankRuns = regexp.MustCompile(`\n{3,}`)

// lastLine is the last thing said in a block of text, tags and blank lines
// aside. Empty when there is nothing in it worth a line of the chat.
func lastLine(text string) string {
	lines := strings.Split(squeezeBlank(markThinking(text)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" && line != "✻ thinking" && line != "✻ answer" {
			return line
		}
	}
	return ""
}

// visibleLen counts a line's printed width: colour escapes occupy no columns
// and must not be counted.
func visibleLen(s string) int {
	n := 0
	// esc: 0 = plain text, 1 = just saw ESC, 2 = inside CSI parameters. The
	// CSI terminator sits in '@'..'~', but so does '[' — hence the state
	// machine instead of a single check.
	esc := 0
	for _, r := range s {
		switch {
		case esc == 1:
			esc = 2
		case esc == 2:
			if r >= '@' && r <= '~' {
				esc = 0
			}
		case r == '\x1b':
			esc = 1
		default:
			n++
		}
	}
	return n
}

// wrapLine splits one line into screen rows. Escapes are carried along
// verbatim but never counted as columns.
func wrapLine(s string, cols int) []string {
	if visibleLen(s) <= cols {
		return []string{s}
	}

	var rows []string
	var cur strings.Builder
	n, esc := 0, 0
	for _, r := range s {
		cur.WriteRune(r)
		switch {
		case esc == 1:
			esc = 2
		case esc == 2:
			if r >= '@' && r <= '~' {
				esc = 0
			}
		case r == '\x1b':
			esc = 1
		default:
			n++
			if n == cols {
				rows = append(rows, cur.String())
				cur.Reset()
				n = 0
			}
		}
	}
	if cur.Len() > 0 {
		rows = append(rows, cur.String())
	}
	return rows
}

// wrapHanging wraps a line and keeps the continuations under the start of the
// text, rather than letting them fall back to the left edge. A message that
// runs to a second row otherwise breaks the margin every other line holds, and
// the block stops looking like a block.
func wrapHanging(line string, cols int) []string {
	// A line that already fits is left exactly as it is. Narrowing it to make
	// room for an indent it will never use is how a box drawn to the full
	// width — the welcome box — ends up wrapped into rubble.
	if visibleLen(line) <= cols {
		return []string{line}
	}

	// A line that starts at the edge still gets its continuations tucked in:
	// a hanging indent is what says "this is still the same line".
	indent := leadingSpaces(line)
	if indent == 0 {
		indent = 2
	}
	if cols-indent < 8 {
		return wrapLine(line, cols) // too narrow to hang anything
	}

	rows := wrapLine(line, cols-indent)
	for i := 1; i < len(rows); i++ {
		rows[i] = strings.Repeat(" ", indent) + rows[i]
	}
	return rows
}

// leadingSpaces counts the spaces a line starts with, looking past the colour
// escapes that come before them.
func leadingSpaces(s string) int {
	n, esc := 0, 0
	for _, r := range s {
		switch {
		case esc == 1:
			esc = 2
		case esc == 2:
			if r >= '@' && r <= '~' {
				esc = 0
			}
		case r == '\x1b':
			esc = 1
		case r == ' ':
			n++
		default:
			return n
		}
	}
	return n
}

// cutVisible shortens a line to a number of screen columns, which is not the
// same as a number of characters: the colour escapes in it take no space, and
// counting them cuts a coloured line to a fraction of its width.
func cutVisible(s string, cols int) string {
	if visibleLen(s) <= cols {
		return s
	}
	rows := wrapLine(s, cols-1)
	if len(rows) == 0 {
		return s
	}
	return rows[0] + "…" + reset
}

// truncate shortens long text so the confirmation prompt is not flooded by a file's contents.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// fmtTokens renders a token count short enough for the status line: 934, 12.4k.
func fmtTokens(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	// A conversation only reaches millions now that the total survives a
	// resume; before this it started again with the process, and "2000.0k"
	// was a number nobody ever saw.
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// boxLines assembles a box cols wide.
func boxLines(cols int, lines ...string) []string {
	inner := cols - 4
	out := []string{dim + "╭" + strings.Repeat("─", cols-2) + "╮" + reset}
	for _, l := range lines {
		// Cut to fit: a line longer than the box pushes the right wall off the
		// screen, and a box with one wall is not a box. A long path is the
		// usual culprit.
		l = cutVisible(l, inner)
		pad := inner - visibleLen(l)
		if pad < 0 {
			pad = 0
		}
		out = append(out, dim+"│"+reset+" "+l+strings.Repeat(" ", pad)+" "+dim+"│"+reset)
	}
	return append(out, dim+"╰"+strings.Repeat("─", cols-2)+"╯"+reset)
}
