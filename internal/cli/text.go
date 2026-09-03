// Text the terminal has to be measured and cut by hand: colour escapes take
// no columns, and a line that fills the last one makes the terminal wrap it on
// its own. These are the pieces both front ends share.
package cli

import (
	"fmt"
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
	if n < 1000 {
		return strconv.Itoa(n)
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
