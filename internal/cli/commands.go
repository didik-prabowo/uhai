// What the slash commands do, apart from how any one front end shows it: the
// command list itself, the providers on offer, the session on disk, and the
// background tasks.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/didik-prabowo/uhai/internal/agent"
	"github.com/didik-prabowo/uhai/internal/config"
	"github.com/didik-prabowo/uhai/internal/provider"
	"github.com/didik-prabowo/uhai/internal/session"
	"github.com/didik-prabowo/uhai/internal/session/filestore"
	"github.com/didik-prabowo/uhai/internal/task"
	"github.com/didik-prabowo/uhai/internal/tools"
)

// command is one slash command shown in the menu while the user types "/".
type command struct {
	name string
	desc string
}

var commands = []command{
	{"/connect", "connect to a provider (saves the API key)"},
	{"/disconnect", "forget a provider's saved key: /disconnect zai"},
	{"/help", "show the command list"},
	{"/clear", "clear the screen"},
	{"/exit", "quit uhai"},
	{"/model", "select a model for the provider"},
	{"/compact", "summarize the history to free up context"},
	{"/skills", "switch skills on and off, and see what each costs"},
	{"/cost", "what this conversation has cost so far"},
	{"/tasks", "list tasks, or /tasks t1 to read one's report"},
	{"/bg", "run a prompt in the background, read-only"},
	{"/check", "run the project's tests as a task, or /check <command>"},
	{"/stop", "stop a task: /stop t1"},
	{"/mouse", "hand the mouse back to the terminal, for selecting text"},
}

// pipeAnswer is what a slash command does when there is no screen: the few
// that mean something without one, and a plain refusal by name for the rest.
//
// It returns the text rather than printing it, so this file stays free of
// printing and the answer can be tested. Sending "/help" to the model as a
// question is what this replaces — it spent a turn and answered nothing.
func pipeAnswer(prompt string) string {
	name := strings.Fields(prompt)[0]
	switch name {
	case "/skills":
		// Nothing to interact with: it reads the disk and prints. Refusing it
		// here would mean the one way to check a skill was found needs a
		// terminal, which is exactly where scripts cannot look.
		return skillsReport()
	case "/help":
	default:
		return "uhai: " + name + " needs the interactive prompt — run uhai in a terminal"
	}

	var b strings.Builder
	b.WriteString("uhai reads one prompt per line here and answers on stdout.\n")
	for _, c := range commands {
		fmt.Fprintf(&b, "  %-9s %s\n", c.name, c.desc)
	}
	b.WriteString("Only /help, /skills and /exit work without a terminal; the rest need the prompt.")
	return b.String()
}

// skillsReport is what the project's skills look like from outside the model:
// what was found, and where. It exists because a skill in the wrong folder, or
// with frontmatter that did not parse, fails in the one way that cannot be
// debugged — the model simply does not follow it, and nothing anywhere says
// why. This turns that into a line.
//
// One row each, the way /tasks does it: the name is what the eye lands on, and
// everything after it is context. The last row says where it looked, which is
// the answer when the skill you expected is not in the rows above.
func skillsReport() string {
	dirs := config.SkillDirs()
	skills := config.Skills()
	if len(skills) == 0 {
		return dim + "  no skills yet — a folder with a SKILL.md in it, under " +
			shortPath(dirs[0]) + "\n  looked in " + shortDirs(dirs) + reset
	}

	width := 0
	for _, s := range skills {
		if n := len(s.Name); n > width {
			width = n
		}
	}

	var rows []string
	total, offCount := 0, 0
	for _, s := range skills {
		// What a skill costs is the line it puts in the prompt, on every
		// request, whether or not it is ever opened. That is the number worth
		// showing: the description is the model's business, the bill is yours.
		if s.Off {
			offCount++
		} else {
			total += len(s.Line()) / 4
		}
		rows = append(rows, fmt.Sprintf("  %s%-*s%s %s· %s%s",
			accentAt, width, s.Name, reset, dim, skillDetail(s, dirs), reset))
	}

	tally := fmt.Sprintf("%d skill(s)", len(skills))
	if offCount > 0 {
		tally += fmt.Sprintf(", %d off", offCount)
	}
	return strings.Join(rows, "\n") + fmt.Sprintf(
		"\n  %s%s · ~%s tok in every prompt\n  looked in %s%s",
		dim, tally, fmtTokens(total), shortDirs(dirs), reset)
}

// sourceDir is which of the searched directories a skill came from, which is
// more use than its own folder: the question being asked is "why is mine not
// here", and the answer is always about the root, never the leaf.
func sourceDir(path string, dirs []string) string {
	for _, dir := range dirs {
		if strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return dir
		}
	}
	return filepath.Dir(path)
}

// shortPath puts the home directory back as ~. An absolute path to a personal
// skill is half the width of the terminal and says nothing the ~ does not.
func shortPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(path, home) {
		return path
	}
	return "~" + strings.TrimPrefix(path, home)
}

func shortDirs(dirs []string) string {
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		out = append(out, shortPath(dir))
	}
	return strings.Join(out, ", ")
}

// skillRows is one line per skill for the picker: what it costs and where it
// came from, the same two facts skillsReport prints. Kept here so the list and
// the report cannot describe the same skill differently.
func skillRows() []command {
	dirs := config.SkillDirs()
	var out []command
	for _, s := range config.Skills() {
		out = append(out, command{s.Name, skillDetail(s, dirs)})
	}
	return out
}

// skillDetail is everything on a row except the name: whether it is on, what
// it costs while it is, and where it came from.
//
// "on" is written out rather than left as the absence of "off" — a row that
// says nothing about its state reads as a row whose state you have to work
// out, and the whole reason to open this list is to see which is which.
func skillDetail(s config.Skill, dirs []string) string {
	where := shortPath(sourceDir(s.Path, dirs))
	if s.Off {
		return "off · " + where
	}
	// Padded to the width of "off", so the columns after it line up in the
	// report; in the list it is one space nobody sees.
	detail := fmt.Sprintf("%-3s · ~%s tok · %s", "on", fmtTokens(len(s.Line())/4), where)
	if s.Description == "" {
		// The model picks a skill by its description, so one without it is
		// found, paid for, and never opened.
		detail += " · no description"
	}
	return detail
}

// skillIsOff reports the state a skill is actually in, which is not always the
// state just written: off accumulates across the home and project files, so a
// skill your own settings turned off stays off however the project votes.
func skillIsOff(name string) bool {
	for _, s := range config.Skills() {
		if s.Name == name {
			return s.Off
		}
	}
	return false
}

// matches returns the commands whose name starts with the input. The menu
// only opens for input starting with "/" — plain text is left alone.
func matches(input string) []command {
	if !strings.HasPrefix(input, "/") {
		return nil
	}
	var out []command
	for _, c := range commands {
		if strings.HasPrefix(c.name, input) {
			out = append(out, c)
		}
	}
	return out
}

const maxRecommendedModelsPerProvider = 6

// providerLabel shows the provider in use, or why there is none yet.
func providerLabel(p provider.Provider) string {
	if p != nil {
		return p.Name()
	}
	return "not connected — type /connect"
}

// providerItems lists every known provider with its credential status.
func providerItems() []command {
	var items []command
	for _, name := range config.Providers() {
		status := dim + "○ no key yet" + reset
		if config.APIKey(name) != "" {
			status = green + "●" + reset + dim + " ready" + reset
		} else if !config.NeedsKey(name) {
			status = dim + "○ local, no key needed" + reset
		}
		items = append(items, command{name, status})
	}
	return items
}

// current is this conversation, rewritten after every turn. Its id is minted
// here, at startup, so the line printed on the way out names what was actually
// written.
var current = session.New()

// store is where current is kept. The composition root picks one and calls
// UseStore; cli never chooses, and never learns which it got. The default is
// only so a test that does not care still has somewhere to write.
var store session.Store = filestore.New("")

// spent is what this conversation has cost so far: every provider call added
// up, not the shape of the last turn. The status row shows a turn, which is
// the wrong number for "what have I spent" — a turn with ten tool calls bills
// its input ten times, and only the last one is on screen.
//
// In memory, so it starts again with the process. Persisting it would mean
// storing tokens and pricing them later at whatever model is loaded then,
// which is a confident wrong figure the moment /model is used.
var spent provider.Usage

func recordUsage(u provider.Usage) {
	spent.Input += u.Input
	spent.Output += u.Output
	spent.CacheRead += u.CacheRead
	spent.CacheWrite += u.CacheWrite
}

// spentReport is the running total, "" when nothing has been asked yet.
func spentReport(p provider.Provider) string {
	if spent.Input == 0 && spent.Output == 0 && spent.CacheRead == 0 {
		return ""
	}
	line := fmt.Sprintf("↑%s ↓%s tokens", fmtTokens(spent.Input+spent.CacheRead+spent.CacheWrite), fmtTokens(spent.Output))
	if spent.CacheRead > 0 {
		// Worth its own figure: it is the difference between this session and
		// the same session without a cache breakpoint.
		line += fmt.Sprintf(" · %s from cache", fmtTokens(spent.CacheRead))
	}
	if p != nil {
		if cost := config.CostUSD(p.Name(), spent); cost != "" {
			line += " · " + cost
		}
	}
	return line
}

// UseStore points the front ends at a store. It sits beside ContinueSession
// for the same reason: cli owns the live conversation, and orchestrator is the
// one place allowed to say how a running uhai is put together.
func UseStore(st session.Store) { store = st }

// savedSessionID is this conversation's id, "" when nothing was said and so
// nothing was written.
func savedSessionID() string {
	if len(current.Messages) == 0 {
		return ""
	}
	return current.ID
}

// ContinueSession makes the saves go back into a conversation that was
// resumed, keeping its id and its start time. Without it every -resume forks
// a fresh copy of the history and the id nobody could hold on to.
func ContinueSession(s session.Session) {
	current.ID, current.Started, current.Model = s.ID, s.Started, s.Model
}

// SaveSession writes the conversation, so every way of asking keeps it
// recoverable the same way. Exported because -p answers from orchestrator,
// which is outside this package and was the one door that saved nothing.
func SaveSession(a *agent.Agent) error {
	current.Messages = a.History
	if a.Provider != nil {
		current.Model = a.Provider.Name()
	}
	return store.Save(current)
}

// reported remembers which finished tasks the user has already been told
// about, so each one is announced once.
var reported = map[string]bool{}

// ended reports whether a task is over, either way. Queued and running both
// mean there is nothing to read yet.
func ended(t task.Task) bool {
	return t.Status != task.StatusQueued && t.Status != task.StatusRunning
}

// finishedTasks returns the tasks that have ended since it was last asked, and
// remembers them, so each is announced once whichever front end announces it.
func finishedTasks(a *agent.Agent) []task.Task {
	var out []task.Task
	for _, t := range a.Tasks.Snapshot() {
		if !ended(t) || reported[t.ID] {
			continue
		}
		reported[t.ID] = true
		out = append(out, t)
	}
	return out
}

// finishedReports is that, as lines to print.
func finishedReports(a *agent.Agent) []string {
	var lines []string
	for _, t := range finishedTasks(a) {
		lines = append(lines, finishedLine(t))
	}
	return lines
}

func finishedLine(t task.Task) string {
	return fmt.Sprintf("%s  %s %s — %s in %s · /tasks %s to read it%s",
		dim, t.ID, t.Description, t.Status, t.Elapsed.Round(time.Second), t.ID, reset)
}

// taskNote is how a finished task reads to the model. It travels folded into
// the next prompt rather than as a message of its own: the Messages API wants
// the roles to alternate, and two user messages in a row would be refused.
func taskNote(t task.Task) string {
	head := fmt.Sprintf("Background task %s (%s) %s.", t.ID, t.Description, t.Status)
	if t.Err != nil {
		head = fmt.Sprintf("Background task %s (%s) failed: %v", t.ID, t.Description, t.Err)
	}
	if strings.TrimSpace(t.Report) == "" {
		return head
	}
	return head + "\n\n" + t.Report
}

// spawnBackground starts one prompt as a task of its own and does not wait for
// it. The error is what to tell the user; both front ends word it themselves.
func spawnBackground(a *agent.Agent, prompt string) error {
	if a.Provider == nil {
		return errors.New("no provider connected — type /connect")
	}
	if prompt == "" {
		return errors.New("usage: /bg <what the task should do>")
	}

	// Read what the task needs before starting it: /connect and /model may
	// replace the provider while the task is still running.
	p, system := a.Provider, a.System
	window, useTools := a.MaxContextTokens, a.UseTools

	go func() {
		a.Tasks.Run(context.Background(), truncate(prompt, 40), func(ctx context.Context, t task.Task) (string, int, error) {
			sub := agent.New(p)
			sub.System = system
			// The same model, so the same window and the same answer about
			// tools: a task compacting at 32k on a 200k model throws away
			// history for nothing.
			sub.MaxContextTokens, sub.UseTools = window, useTools
			// Nobody is watching to answer a confirmation, so anything that
			// writes or runs commands is refused and the model is told why.
			sub.Confirm = func(string, string) bool { return false }

			var report string
			sub.OnText = func(text string) { report = text }

			// The answer as it is written, so a task can be looked in on.
			sub.OnDelta = func(delta string) { a.Tasks.Progress(t.ID, delta) }

			err := sub.Ask(ctx, prompt)
			return report, sub.Tokens(), err
		})
	}()
	return nil
}

// checkTimeout bounds a check. A test suite is allowed to be slow; it is not
// allowed to hold a task slot for the rest of the session.
const checkTimeout = 10 * time.Minute

// checkReportLines is how much of the output is kept. A failing suite says
// what went wrong at the end, and /tasks is not a place to read thousands of
// lines.
const checkReportLines = 40

// spawnCheck runs a command as a task of its own and does not wait for it.
// There is no model involved: it is the other kind of work this registry can
// hold, and it runs with no provider connected at all.
//
// The command is the user's own, so it needs no permission — the same bargain
// as typing it into a shell. Nothing here is offered to the model.
func spawnCheck(a *agent.Agent, command string) (string, error) {
	if command == "" {
		command = projectCheck()
	}
	if command == "" {
		return "", errors.New("nothing obvious to run here — try /check <command>")
	}

	go func() {
		a.Tasks.Run(context.Background(), truncate(command, 40), func(ctx context.Context, t task.Task) (string, int, error) {
			ctx, cancel := context.WithTimeout(ctx, checkTimeout)
			defer cancel()

			// Reported line by line, so /tasks t1 shows a slow suite working
			// rather than nothing at all until it is over.
			out, err := tools.Shell(ctx, command, func(line string) {
				a.Tasks.Progress(t.ID, line+"\n")
			})
			return lastLines(out, checkReportLines), 0, err
		})
	}()
	return command, nil
}

// projectCheck is how this project verifies itself: what it says in its own
// settings if it says anything, and otherwise a guess from the files present,
// in the order that answers first. An unknown project says so rather than
// guessing wrong.
func projectCheck() string {
	if command := config.CheckCommand(); command != "" {
		return command
	}
	for _, known := range []struct{ file, command string }{
		{"go.mod", "go test ./..."},
		{"package.json", "npm test"},
		{"Makefile", "make test"},
	} {
		if _, err := os.Stat(known.file); err == nil {
			return known.command
		}
	}
	return ""
}

// lastLines keeps the end of the output, which is where a failing command says
// what was wrong.
func lastLines(out string, n int) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return "…\n" + strings.Join(lines[len(lines)-n:], "\n")
}

// stopTask cancels one task and says what happened, for whichever front end
// asked. A task that has already ended is not an error worth a fuss — it is
// simply too late.
func stopTask(a *agent.Agent, id string) string {
	if id == "" {
		return dim + "  usage: /stop t1" + reset
	}
	if !a.Tasks.Stop(id) {
		return fmt.Sprintf("%s  nothing to stop: %s is not running%s", dim, id, reset)
	}
	return fmt.Sprintf("%s  stopping %s%s", dim, id, reset)
}

// tasksReport is that listing as text, so a front end that does not print
// straight to the terminal can show it too.
func tasksReport(a *agent.Agent, id string) string {
	list := a.Tasks.Snapshot()
	if len(list) == 0 {
		return dim + "  no tasks yet — spawn one with /bg, or let the model do it" + reset
	}

	if id != "" {
		for _, t := range list {
			if t.ID != id {
				continue
			}
			if !ended(t) {
				head := fmt.Sprintf("%s  %s is still %s%s", dim, t.ID, t.Status, reset)
				if strings.TrimSpace(t.Output) == "" {
					return head
				}
				return head + "\n" + lastLines(t.Output, checkReportLines)
			}
			head := fmt.Sprintf("%s⏺ %s%s %s%s%s", accentAt, t.ID, reset, dim, t.Description, reset)
			if t.Err != nil {
				// What it printed is why it failed, and a check that fails is
				// exactly the one worth reading.
				head += fmt.Sprintf("%s failed: %v%s", dim, t.Err, reset)
			}
			if strings.TrimSpace(t.Report) == "" {
				// A failed task reports nothing, but it was writing something
				// when it died and that is the only account of the work.
				if partial := strings.TrimSpace(t.Output); partial != "" {
					return head + "\n" + lastLines(t.Output, checkReportLines)
				}
				return head
			}
			return head + "\n" + t.Report
		}
		return fmt.Sprintf("%s  no task %s%s", dim, id, reset)
	}

	var rows []string
	for _, t := range list {
		row := fmt.Sprintf("  %s%s%s %s%s · %s", accentAt, t.ID, reset, dim, t.Description, t.Status)
		if ended(t) {
			row += fmt.Sprintf(" · %s · ~%s tokens", t.Elapsed.Round(time.Second), fmtTokens(t.Tokens))
		}
		if t.Err != nil {
			row += " · " + t.Err.Error()
		}
		rows = append(rows, row+reset)
	}
	return strings.Join(rows, "\n")
}
