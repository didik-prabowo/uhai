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

	"github.com/charmbracelet/lipgloss"

	"github.com/didik-prabowo/ouhai/internal/config"
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
	// about the whole tool: the command for a shell call, the path for a file.
	var args struct {
		Command string `json:"command"`
		Path    string `json:"path"`
	}
	json.Unmarshal([]byte(input), &args)

	subject := args.Path
	if name == "run_bash" {
		subject = args.Command
	}

	if rule := config.Permission(name, subject); rule != config.PermAsk {
		return rule
	}
	if m.allowed.has(name) {
		return config.PermAllow
	}
	return config.PermAsk
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
func toolLine(name, input string) string {
	var args struct {
		Path    string `json:"path"`
		Command string `json:"command"`
		Pattern string `json:"pattern"`
		Include string `json:"include"`
	}
	if json.Unmarshal([]byte(input), &args) != nil {
		return name + " " + truncate(input, 120)
	}

	var subject string
	switch name {
	case "run_bash":
		subject = args.Command
	case "read_file", "write_file", "edit_file":
		subject = args.Path
	case "glob", "grep":
		subject = args.Pattern
		if args.Include != "" {
			subject += " in " + args.Include
		}
		if args.Path != "" {
			subject += " under " + args.Path
		}
	default:
		return name + " " + truncate(input, 120)
	}

	if subject == "" {
		return name
	}
	return name + " " + truncate(subject, 120)
}

// confirmTitle names the kind of thing being approved, since the first line of
// a question should say what sort of answer it wants.
func confirmTitle(name, input string) string {
	var args struct {
		Path string `json:"path"`
	}
	json.Unmarshal([]byte(input), &args)

	switch name {
	case "run_bash":
		return "Shell command"
	case "edit_file":
		return "Edit " + args.Path
	case "write_file":
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
	case "run_bash":
		return teaDim.Render("  $ ") + args.Command
	case "edit_file":
		// Where in the file this lands. The provider never says — it sends
		// the text to replace, not where it is — so it is counted here.
		return diff(args.Old, args.New, lineOf(args.Path, args.Old))
	case "write_file":
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

// diff shows what changes between two pieces of text, as the lines removed and
// the lines added, with a little of what surrounds them.
//
// ponytail: matching only equal heads and tails, not a real line diff. A change
// in the middle of a long block therefore reads as the whole block being
// replaced, which is honest if verbose; reach for a proper diff when that
// starts costing more than it says.
func diff(before, after string, start int) string {
	old, new := splitLines(before), splitLines(after)

	// Trim what the two have in common, so the change is what is left.
	head := 0
	for head < len(old) && head < len(new) && old[head] == new[head] {
		head++
	}
	tail := 0
	for tail < len(old)-head && tail < len(new)-head &&
		old[len(old)-1-tail] == new[len(new)-1-tail] {
		tail++
	}

	// Two counters. A removed line keeps its number in the file as it stands;
	// everything else — the lines added and the ones around them — is
	// numbered as the file will read once the change is made, which is the
	// file you will be looking at next.
	oldLine, newLine := start, start

	var rows []string
	from := head - diffContext
	if from < 0 {
		from = 0
	}
	oldLine, newLine = oldLine+from, newLine+from
	for _, line := range around(old, from, head) {
		rows = append(rows, teaDim.Render(gutter(newLine, " ")+line))
		oldLine, newLine = oldLine+1, newLine+1
	}
	for _, line := range old[head : len(old)-tail] {
		rows = append(rows, teaRemoved.Render(gutter(oldLine, "-")+line))
		oldLine++
	}
	for _, line := range new[head : len(new)-tail] {
		rows = append(rows, teaAdded.Render(gutter(newLine, "+")+line))
		newLine++
	}
	for _, line := range around(old, len(old)-tail, len(old)-tail+diffContext) {
		rows = append(rows, teaDim.Render(gutter(newLine, " ")+line))
		oldLine, newLine = oldLine+1, newLine+1
	}

	if len(rows) > diffMaxLines {
		rows = append(rows[:diffMaxLines], teaDim.Render("  … and more"))
	}
	if len(rows) == 0 {
		return teaDim.Render("  (no change)")
	}
	return strings.Join(rows, "\n")
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

func around(lines []string, from, to int) []string {
	if from < 0 {
		from = 0
	}
	if to > len(lines) {
		to = len(lines)
	}
	if from >= to {
		return nil
	}
	return lines[from:to]
}
