---
name: drawing
description: How the terminal is painted: the code block, the theme, and what Charm v2 moved.
---

# Drawing

Every colour comes from `theme.go` — slate for structure, one violet accent,
softened red and green for a diff — and a test fails the build if a view writes
one of its own. That test exists because the palette had already drifted across
two files once.

Four rules the layout keeps, each of which took a bug to learn:

- **Nothing fills the last column.** A line as wide as the terminal makes it
  wrap on its own, which scrolls the screen under the renderer. Everything is
  built to `m.cols()`, one short.
- **Nothing is drawn on the last row.** Once the chat is long enough to scroll,
  that row is the one the renderer loses track of; it is left blank on purpose.
- **A line that already fits is never re-wrapped.** Narrowing it to make room
  for a hanging indent is what turned the welcome box into rubble.
- **Anything the chat draws to a fixed width is cut by columns, not by
  characters.** A coloured line is mostly escape codes.

A fenced code block is drawn in `code.go`, not by glamour. The fences are cut
out of the answer before glamour sees it, and what comes back is a box: lined
up with the paragraph above it, reaching to one column short of the edge,
numbered down the side, one grey throughout. `codeBG` is deliberately not
`band`, which is the question's and is tinted with the accent — the two sit
within a screen of each other and the eye should not have to work out which is
which.

A blank line sits at each edge. Without them the block leans against the
sentence that introduced it, and two blocks with one line of prose between them
read as a single block with a caption inside — which is exactly how a
`go run` line after a program looked.

It was fitted to its longest line for one revision, which made every block a
different width and the answer read as ragged. Lining the left edge up with the
prose and running almost to the right one is what makes a page of answer look
like one thing.

Patching glamour's output was tried three times and each round taught the same
thing. A background set on the code block is ignored, because chroma paints the
characters. `IndentToken`, which would have drawn a gutter, is not honoured for
code blocks. And even with the colour forced onto every chroma token, the band
starts after glamour's margin, stops where the code stops, and dies in every
gap, because each token ends in a full reset. A block is a shape; a shape has
to be drawn, not repaired.

So chroma is called directly for the colours and lipgloss for the box. Two
things that only show up once you measure: a tab is one character and several
columns, so tabs are expanded before anything is measured or the band is drawn
one width and painted another; and a line wider than the screen is cut by
columns rather than wrapped, since a wrapped line takes the shape with it.

The box only appears once the answer is finished. While the text is streaming,
`streamRows` shows it raw, fences and all, because the closing fence has not
arrived and there is nothing yet that is a block.

A model that thinks shows its working out live, dimmed, above the answer, and
the moment the answer starts it collapses to `✻ thought for 12s`. Both halves
are the point: keeping all of it buries the reply, since a thinking model
writes more working out than reply; dropping it silently leaves the wait
unexplained.

The rhythm is a blank line before each question, one after it, and one before
the line that closes the turn. A tool call is one row, cut to the width: it is
a note that something happened, not the thing itself.
