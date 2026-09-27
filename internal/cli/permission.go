// Whether a tool may run, and how the question is put when it has to be asked.
// Both halves belong together: the answer is worth nothing if what it approves
// cannot be read, and a path with a diff can be judged where a blob of JSON can
// only be trusted.
//
// Three sources say yes, in this order: the project's settings (permission per
// tool, or an allowed command prefix), this session's own "always", and the
// person at the keyboard.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"

	"github.com/didik-prabowo/uhai/internal/config"
	"github.com/didik-prabowo/uhai/internal/tools"
)

// diffContext is how many unchanged lines are shown around a change: enough to
// place it in the file, few enough to stay a glance.
const diffContext = 2

// diffMaxLines caps a diff. A rewrite of a large file is not something to read
// in a prompt; the path and the size of it are the decision.
const diffMaxLines = 40

// decide is what happens to one tool call before anyone is asked: allow it,
// refuse it, or put the question. Read from the agent's goroutine while the
// model works, so everything it consults is either read-only or locked.
//
// The order is deliberate. What a project denied cannot be waved through by a
// session, and what a project allowed is not asked about again.
func (m *teaModel) decide(name, input string) string {
	// What the call wants to act on, so a rule can be about that rather than
	// about the whole tool. Asked of config rather than worked out here: the
	// same answer is now needed before a tool is offered at all, and two copies
	// of it drift into one of them ignoring a rule somebody wrote.
	subject := config.Subject(name, input)

	if rule := config.Permission(name, subject); rule != config.PermAsk {
		return rule
	}
	if m.allowed.has(name) {
		return config.PermAllow
	}
	return config.PermAsk
}

// settled is the half of a confirmation nobody has to be asked about: what the
// project's settings say, and what this session already waved through. It
// lives here rather than in each front end because attaching moved where the
// agent runs, not who decides — the daemon's question used to skip all of it,
// so a project rule was ignored and "always" remembered nothing.
func (m *teaModel) settled(name, input string) (allow, decided bool) {
	switch m.decide(name, input) {
	case config.PermAllow:
		return true, true
	case config.PermDeny:
		// Refused by the project, so nobody is asked and the model is told
		// plainly rather than left to guess at a silent failure.
		m.program.Send(teaNoteMsg("refused by this project's settings: " + name))
		return false, true
	}
	return false, false
}

// allowSet is what the user has waved through for the rest of the session.
type allowSet struct {
	mu    sync.Mutex
	names map[string]bool
}

func (a *allowSet) add(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.names == nil {
		a.names = map[string]bool{}
	}
	a.names[name] = true
}

func (a *allowSet) has(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.names[name]
}

// confirmBodyLines caps what the panel shows of a change. A rewrite of a large
// file is not read in a prompt; the path and the shape of it are the decision.
const confirmBodyLines = 14

// confirmPanel is the question as it appears below the chat: what is about to
// happen, and the answers, in a box of its own.
//
// Numbered, and answered by moving to one — a letter you have to remember is a
// letter you eventually stop reading, and a permission prompt that is not read
// is decoration.
func (m *teaModel) confirmPanel() []string {
	width := m.cols() - 2 // the border takes a column on each side

	rows := []string{confirmHeading.Render(confirmTitle(m.confirm.name, m.confirm.input)), ""}

	body := strings.Split(confirmDetail(m.confirm.name, m.confirm.input), "\n")
	if len(body) > confirmBodyLines {
		body = append(body[:confirmBodyLines], teaDim.Render("… and more"))
	}
	for _, line := range body {
		rows = append(rows, wrapLine(line, width-2)...)
	}

	rows = append(rows, "", confirmQuestion.Render("Do you want to proceed?"))
	for i, choice := range m.confirmChoices() {
		row := fmt.Sprintf("  %d. %s", i+1, choice)
		if i == m.confirm.choice {
			row = confirmChosen.Render(fmt.Sprintf("❯ %d. %s", i+1, choice))
		} else {
			row = confirmChoice.Render(row)
		}
		rows = append(rows, row)
	}

	panel := confirmBox.Width(width).Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
	return strings.Split(panel, "\n")
}

// toolLine is one tool call as a line of the chat: what it does, not the JSON
// it arrived as. A path or a command can be read at a glance; an escaped blob
// has to be decoded before it says anything.
//
// A verb rather than the tool's own name, which is the model's word for it and
// not a person's: "Reading internal/cli/tea.go" says what is happening to
// somebody who has never read this project's tool table. The name is still
// what is shown when there is nothing else to say, since a bare verb says
// less than the name did.
func toolLine(name, input string) string {
	var args struct {
		Path    string `json:"path"`
		Command string `json:"command"`
		Pattern string `json:"pattern"`
		Include string `json:"include"`
		URL     string `json:"url"`
	}
	if json.Unmarshal([]byte(input), &args) != nil {
		if strings.TrimSpace(input) == "" {
			return name // nothing to say about it, so nothing is said
		}
		return name + " " + truncate(input, 120)
	}

	var subject string
	switch name {
	case tools.NameBash:
		subject = args.Command
	case tools.NameRead, tools.NameWrite, tools.NameEdit:
		subject = args.Path
	case tools.NameFetch:
		subject = args.URL
	case tools.NameGlob:
		subject = args.Pattern
		if args.Path != "" {
			subject += " under " + args.Path
		}
	case tools.NameGrep:
		subject = args.Pattern
		if args.Include != "" {
			subject += " in " + args.Include
		}
		if args.Path != "" {
			subject += " under " + args.Path
		}
	default:
		if strings.TrimSpace(input) == "" {
			return name
		}
		return name + " " + truncate(input, 120)
	}

	if subject == "" {
		return name
	}
	return toolVerb[name] + " " + truncate(subject, 120)
}

// What each tool is doing, in the tense of the moment the line is drawn: the
// call has been made and is running. Only the tools above are here — anything
// else falls through to its own name and its arguments.
var toolVerb = map[string]string{
	tools.NameBash:  "Running",
	tools.NameRead:  "Reading",
	tools.NameWrite: "Writing",
	tools.NameEdit:  "Editing",
	tools.NameGlob:  "Finding",
	tools.NameGrep:  "Searching",
	tools.NameFetch: "Fetching",
}

// confirmTitle names the kind of thing being approved, since the first line of
// a question should say what sort of answer it wants.
func confirmTitle(name, input string) string {
	var args struct {
		Path string `json:"path"`
	}
	json.Unmarshal([]byte(input), &args)

	switch name {
	case tools.NameBash:
		// The duration only when it is not the usual one. A number on every
		// shell prompt is a number that stops being read, and the whole reason
		// the question beats a blob of JSON is that it is read.
		if limit := tools.BashLimit(input); limit != tools.BashTimeout {
			return "Shell command, up to " + limit.String()
		}
		return "Shell command"
	case tools.NameFetch:
		return "Fetch a page"
	case tools.NameEdit:
		return "Edit " + args.Path
	case tools.NameWrite:
		if _, err := os.Stat(args.Path); err == nil {
			return "Rewrite " + args.Path
		}
		return "Create " + args.Path
	}
	return name
}

// confirmDetail describes one pending tool call, so what is being approved can
// be read at a glance.
func confirmDetail(name, input string) string {
	var args struct {
		Path    string `json:"path"`
		Old     string `json:"old"`
		New     string `json:"new"`
		Content string `json:"content"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return teaDim.Render("  " + name + " " + truncate(input, 200))
	}

	switch name {
	case tools.NameBash:
		return teaDim.Render("  $ ") + args.Command
	case tools.NameEdit:
		// Where in the file this lands. The provider never says — it sends
		// the text to replace, not where it is — so it is counted here.
		return diff(args.Old, args.New, lineOf(args.Path, args.Old))
	case tools.NameWrite:
		before, err := os.ReadFile(args.Path)
		if err != nil {
			// A new file has nothing to compare against: what it will contain
			// is the whole of the change.
			return diff("", args.Content, 1)
		}
		return diff(string(before), args.Content, 1)
	}
	return teaDim.Render("  " + name + " " + truncate(input, 200))
}

// diff shows what changes between two pieces of text: the lines removed, the
// lines added, and a little of what surrounds them.
//
// It matches lines that are really equal rather than assuming everything
// between the common head and tail changed, because the diff is the whole
// reason a confirmation is worth reading. One renamed variable in the middle
// of a function used to read as the function being replaced, and a diff that
// cries wolf gets answered with y without being read.
func diff(before, after string, start int) string {
	ops := lineDiff(splitLines(before), splitLines(after))

	// Two counters. A removed line keeps its number in the file as it stands;
	// everything else — the lines added and the ones around them — is
	// numbered as the file will read once the change is made, which is the
	// file you will be looking at next.
	oldLine, newLine := start, start

	var rows []string
	skipped := false
	for i, op := range ops {
		if op.kind == ' ' && !nearAChange(ops, i) {
			// Far from anything that changed: not worth a row, but the gap
			// has to be visible or the line numbers look like a mistake.
			oldLine, newLine, skipped = oldLine+1, newLine+1, true
			continue
		}
		if skipped {
			rows = append(rows, teaDim.Render("  ⋮"))
			skipped = false
		}

		switch op.kind {
		case '-':
			rows = append(rows, teaRemoved.Render(gutter(oldLine, "-")+op.text))
			oldLine++
		case '+':
			rows = append(rows, teaAdded.Render(gutter(newLine, "+")+op.text))
			newLine++
		default:
			rows = append(rows, teaDim.Render(gutter(newLine, " ")+op.text))
			oldLine, newLine = oldLine+1, newLine+1
		}
	}

	if len(rows) > diffMaxLines {
		rows = append(rows[:diffMaxLines], teaDim.Render("  … and more"))
	}
	if len(rows) == 0 {
		return teaDim.Render("  (no change)")
	}
	return strings.Join(rows, "\n")
}

// diffOp is one line of the answer: kept, removed or added.
type diffOp struct {
	kind byte // ' ', '-' or '+'
	text string
}

// nearAChange reports whether a kept line is close enough to a change to be
// worth its row.
func nearAChange(ops []diffOp, at int) bool {
	for i := max(0, at-diffContext); i <= min(len(ops)-1, at+diffContext); i++ {
		if ops[i].kind != ' ' {
			return true
		}
	}
	return false
}

// diffBudget caps the table middleDiff is willing to build. Beyond it the
// change is far too large to read in a confirmation anyway, and the cheap
// honest answer — all of the old, then all of the new — is what a rewrite is.
const diffBudget = 250_000

// lineDiff turns old into new as a list of operations.
func lineDiff(old, new []string) []diffOp {
	// The common head and tail are matched directly. Nearly every edit here
	// changes a few lines of a long file, which leaves the table below tiny.
	head := 0
	for head < len(old) && head < len(new) && old[head] == new[head] {
		head++
	}
	tail := 0
	for tail < len(old)-head && tail < len(new)-head &&
		old[len(old)-1-tail] == new[len(new)-1-tail] {
		tail++
	}

	ops := make([]diffOp, 0, len(old)+len(new))
	for _, line := range old[:head] {
		ops = append(ops, diffOp{' ', line})
	}
	ops = append(ops, middleDiff(old[head:len(old)-tail], new[head:len(new)-tail])...)
	for _, line := range old[len(old)-tail:] {
		ops = append(ops, diffOp{' ', line})
	}
	return ops
}

// middleDiff settles the part the head and tail could not: a longest common
// subsequence, walked back into operations.
func middleDiff(old, new []string) []diffOp {
	n, m := len(old), len(new)
	if n == 0 || m == 0 || n*m > diffBudget {
		ops := make([]diffOp, 0, n+m)
		for _, line := range old {
			ops = append(ops, diffOp{'-', line})
		}
		for _, line := range new {
			ops = append(ops, diffOp{'+', line})
		}
		return ops
	}

	// table[i][j] is the length of the longest common subsequence of old[i:]
	// and new[j:], filled from the end so the walk below can go forwards and
	// keep the lines in file order.
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if old[i] == new[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = max(table[i+1][j], table[i][j+1])
			}
		}
	}

	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case old[i] == new[j]:
			ops = append(ops, diffOp{' ', old[i]})
			i, j = i+1, j+1
		case table[i+1][j] >= table[i][j+1]:
			// Removed before added, so a replaced line sits next to the line
			// replacing it — which is how a change reads.
			ops = append(ops, diffOp{'-', old[i]})
			i++
		default:
			ops = append(ops, diffOp{'+', new[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', old[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', new[j]})
	}
	return ops
}

// gutter is the number and the marker down the left of a diff. A line nobody
// can place in the file is half an answer, so the number is worth the columns
// — unless there is none to give, in which case the space is not.
func gutter(line int, marker string) string {
	if line <= 0 {
		return "  " + marker + " "
	}
	return fmt.Sprintf("%4d %s ", line, marker)
}

// lineOf is which line of a file some text starts on, 0 when it cannot be
// found — a file that has moved on since the model read it, most often, which
// is a refusal waiting to happen rather than something to guess at.
func lineOf(path, text string) int {
	if text == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	at := strings.Index(string(data), text)
	if at < 0 {
		return 0
	}
	return strings.Count(string(data[:at]), "\n") + 1
}

// splitLines treats empty text as no lines at all, rather than as one empty
// one, so writing a new file does not open with a phantom deletion.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}
