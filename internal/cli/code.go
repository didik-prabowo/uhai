// Fenced code blocks, drawn here rather than by glamour.
//
// Glamour renders a code block into the flow of the document: its own left
// margin, its width the width of the page, and chroma's colours painted only
// on the characters. Three rounds of patching that output taught the same
// thing each time — the block is a shape, and a shape has to be drawn, not
// repaired. So the fences are cut out of the answer before glamour sees it,
// and what comes back here is a box: as wide as its longest line, numbered
// down the side, and one colour throughout.
package cli

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// codeGutter is the width of the line-number column, which stops growing at
// four digits. A block long enough to need five is not one anybody reads in a
// chat.
const codeGutter = 4

// codeTab is what a tab is worth. Four rather than eight: the block is as wide
// as its widest line, and eight columns of indent makes a narrow function look
// like a wide one.
const codeTab = 4

// codeIndent lines the box up with the paragraph above it. Glamour gives the
// document two columns of margin, and a block flush against the edge while the
// prose is inset reads as a mistake.
const codeIndent = 2

// fence is a line that opens or closes a code block: three or more backticks
// or tildes, and nothing before them but spaces.
func fence(line string) (marker, lang string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	for _, mark := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, mark) {
			run := len(trimmed) - len(strings.TrimLeft(trimmed, mark[:1]))
			return trimmed[:run], strings.TrimSpace(trimmed[run:]), true
		}
	}
	return "", "", false
}

// splitFences cuts an answer into the prose around its code blocks and the
// code inside them. A block that never closes is still a block: the model was
// cut off mid-answer, and showing the half it wrote as code is truer than
// showing its fence.
func splitFences(text string) []codePart {
	var parts []codePart
	var prose, code []string
	open, lang := "", ""

	flush := func() {
		if len(prose) > 0 {
			parts = append(parts, codePart{text: strings.Join(prose, "\n")})
			prose = nil
		}
	}
	for _, line := range strings.Split(text, "\n") {
		mark, l, isFence := fence(line)
		switch {
		case open == "" && isFence:
			flush()
			open, lang = mark, l
		case open != "" && isFence && strings.HasPrefix(mark, open):
			parts = append(parts, codePart{text: strings.Join(code, "\n"), lang: lang, code: true})
			code, open, lang = nil, "", ""
		case open != "":
			code = append(code, line)
		default:
			prose = append(prose, line)
		}
	}
	if open != "" {
		parts = append(parts, codePart{text: strings.Join(code, "\n"), lang: lang, code: true})
	}
	flush()
	return parts
}

type codePart struct {
	text string
	lang string
	code bool
}

// codeBlock draws one block: numbered, syntax-coloured, and banded to the
// width of its own longest line rather than to the width of the terminal — a
// box the eye can see the end of, not a stripe across the screen.
func codeBlock(source, lang string, max int) string {
	// A tab is one character and several columns, so it has to go before
	// anything is measured: left in, the band is drawn one width and the
	// terminal paints another, and the block comes apart on exactly the lines
	// that are indented.
	source = strings.ReplaceAll(source, "\t", strings.Repeat(" ", codeTab))

	lines := strings.Split(strings.Trim(source, "\n"), "\n")
	painted := highlight(source, lang)
	if len(painted) != len(lines) {
		painted = lines // the lexer disagreed about line count; show it plainly
	}

	// The box runs from the same column the prose starts at to very nearly the
	// right edge — lined up with the paragraph above it, rather than fitted to
	// whatever the longest line happens to be, which made every block a
	// different width and the answer look ragged.
	// One column short of the edge, deliberately: a line as wide as the
	// terminal wraps on its own, which scrolls the screen out from under the
	// renderer.
	width := max - codeIndent - codeGutter - 3
	if width < 8 {
		width = 8
	}

	band := lipgloss.NewStyle().Background(codeBG)
	number := lipgloss.NewStyle().Background(codeBG).Foreground(codeNumber)
	if on, _ := bandEscapes(band); on == "" {
		return strings.Join(painted, "\n") // no colour: the text, and nothing drawn
	}

	indent := strings.Repeat(" ", codeIndent)
	blank := indent + band.Render(strings.Repeat(" ", codeGutter+width+2))

	rows := []string{blank}
	for i, line := range painted {
		gutter := number.Render(fmt.Sprintf("%*s ", codeGutter-1, strconv.Itoa(i+1)))
		// Cut by columns, not by characters: a coloured line is mostly escape
		// codes, and one longer than the block would wrap and take the shape
		// with it.
		body := keepBand(cutVisible(line, width), band)
		if pad := width - visibleLen(lines[i]); pad > 0 {
			body += band.Render(strings.Repeat(" ", pad))
		}
		rows = append(rows, indent+gutter+band.Render(" ")+body+band.Render(" "))
	}
	return strings.Join(append(rows, blank), "\n")
}

// highlight runs chroma over the source and returns it one line at a time. An
// unknown language is not an error — it is most of what gets pasted into a
// chat — and comes back uncoloured rather than not at all.
func highlight(source, lang string) []string {
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Analyse(source)
	}
	if lexer == nil {
		return strings.Split(strings.Trim(source, "\n"), "\n")
	}
	iterator, err := lexer.Tokenise(nil, strings.Trim(source, "\n")+"\n")
	if err != nil {
		return strings.Split(strings.Trim(source, "\n"), "\n")
	}
	var out strings.Builder
	if err := formatters.TTY256.Format(&out, styles.Get("catppuccin-mocha"), iterator); err != nil {
		return strings.Split(strings.Trim(source, "\n"), "\n")
	}
	return strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
}

// keepBand turns the band back on after every reset chroma writes. Without it
// the colour dies in the gaps between tokens and the block reads as stripes.
func keepBand(line string, band lipgloss.Style) string {
	on, off := bandEscapes(band)
	if on == "" {
		return line
	}
	return on + strings.ReplaceAll(line, off, off+on)
}

// bandEscapes asks lipgloss what turning the band on and off looks like, by
// rendering one space and taking it apart. Both are needed: chroma writes a
// reset after every token, and the band has to be turned back on after each
// one or it dies in the gaps.
//
// Asked rather than written down because the answer changes. lipgloss v1 wrote
// the reset as ESC[0m and v2 writes ESC[m, and a hardcoded ESC[0m went on
// compiling and quietly stopped matching — the block lost its colour and no
// test could see it.
func bandEscapes(band lipgloss.Style) (on, off string) {
	painted := band.Render(" ")
	space := strings.Index(painted, " ")
	if space < 0 || space == 0 {
		return "", "" // no colour to draw with
	}
	return painted[:space], painted[space+1:]
}
