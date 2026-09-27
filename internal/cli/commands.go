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
	"sync"
	"time"

	"github.com/didik-prabowo/uhai/internal/agent"
	"github.com/didik-prabowo/uhai/internal/config"
	"github.com/didik-prabowo/uhai/internal/daemon"
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
	{"/model", "select a model, or /model refresh to look again"},
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

// UseProviderOn is the same for an agent no teaModel owns — the daemon's,
// which changes model over the socket. Exported so the two cannot drift: a
// model swapped without its context window is a session that compacts at the
// wrong moment, silently.
func UseProviderOn(a *agent.Agent, p provider.Provider) {
	a.Provider = p
	a.UseTools = config.SupportsTools(p.Name())
	useModel(a, p.Name())

	// And again whenever the answer names a different model than the last one
	// did. A gateway alias is not a model: "9router/plan-deep" is five of
	// them, and until this the window came from a name nothing was measured
	// against — the fallback 32k, against a model with a million, so a
	// planning conversation was summarised away every 27k tokens while it
	// still had room for thirty times that.
	a.OnModel = func(name string) {
		useModel(a, name)
		a.OnNotice(fmt.Sprintf("answered by %s — %s", name, config.ModelSummary(name)))
	}
}

// useModel applies what follows the model rather than the provider. Called
// with the configured name at first and with the answering one after that.
//
// UseTools is deliberately not in here. Whether the conversation has tools is
// something its history already commits to — there are tool_use blocks in it —
// and switching that off midway would strand them.
func useModel(a *agent.Agent, name string) {
	a.MaxContextTokens = config.ContextWindow(name)
	a.Thinking = config.Thinks(name)
	a.Effort = config.Effort(name)
}

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
		if !config.Known(name) {
			// A custom one has no price bracket to quote and no key page to
			// send anybody to, so it says where it points instead — which is
			// the only thing that distinguishes two of them.
			status += dim + " · " + config.BaseURLOf(name) + reset
		}
		items = append(items, command{name, status})
	}
	// Last, and the only row that is not a provider: everything above can be
	// connected, this one is how a new name gets into that list at all.
	return append(items, command{customRow, dim + "a gateway or your own endpoint" + reset})
}

// sessions is one conversation per project, rewritten after every turn.
//
// It was a single `current`, which is right for a front end — one process, one
// project — and was wrong the moment the daemon began serving several from one
// process: both projects saved into the same id, so the second turn overwrote
// the first project's file, root and all, and its conversation was gone. Not
// mixed up, gone: `LatestIn` for that project then found nothing.
//
// Locked because the daemon saves from a request goroutine per project, so two
// can finish at once.
var sessions = struct {
	sync.Mutex
	byRoot map[string]session.Session
}{byRoot: map[string]session.Session{}}

// sessionFor is a project's conversation, minted on first sight. The id has to
// be made here rather than at startup, since a daemon does not know which
// projects it will serve.
func sessionFor(root string) session.Session {
	key := session.Resolve(root)
	s, ok := sessions.byRoot[key]
	if !ok {
		s = session.New()
		sessions.byRoot[key] = s
	}
	return s
}

// here is the front end's own project. A front end has exactly one, which is
// what lets the commands that speak of "this conversation" ask for it by name.
func here() string {
	dir, _ := os.Getwd()
	return dir
}

// store is where current is kept. The composition root picks one and calls
// UseStore; cli never chooses, and never learns which it got. The default is
// only so a test that does not care still has somewhere to write.
var store session.Store = filestore.New("")

// spent is what this conversation has cost so far: every provider call added
// up, not the shape of the last turn. The status row shows a turn, which is
// the wrong number for "what have I spent" — a turn with ten tool calls bills
// its input ten times, and only the last one is on screen.
//
// Kept per model, and saved with the conversation so -resume carries it. One
// stored total would have to be priced later at whatever model was loaded
// then, which is a confident wrong figure the moment /model is used; a share
// per model is priced with that model's own rates and the shares are added.
//
// It lives on the session rather than beside it, because it belongs to a
// conversation and the daemon holds one per project. As a package-level slice
// it was the twin of the `current` bug: loading project B's session replaced
// it wholesale, and project A's next save wrote B's bill into A's file.
func spent() []session.Spend { return spentIn(here()) }

func spentIn(root string) []session.Spend {
	sessions.Lock()
	defer sessions.Unlock()
	return sessionFor(root).Spend
}

// setSpent replaces a project's bill, for the paths that hand one back whole:
// resuming a conversation, and a test starting from nothing.
func setSpent(root string, sp []session.Spend) {
	sessions.Lock()
	defer sessions.Unlock()
	s := sessionFor(root)
	s.Spend = sp
	sessions.byRoot[session.Resolve(root)] = s
}

func recordUsage(model string, u provider.Usage) { recordUsageIn(here(), model, u) }

// recordUsageIn adds one provider call to a project's bill. The daemon prices
// several conversations at once, so which project is being billed cannot be
// left to a global.
func recordUsageIn(root, model string, u provider.Usage) {
	sessions.Lock()
	defer sessions.Unlock()
	s := sessionFor(root)
	for i := range s.Spend {
		if s.Spend[i].Model == model {
			s.Spend[i].Usage.Input += u.Input
			s.Spend[i].Usage.Output += u.Output
			s.Spend[i].Usage.CacheRead += u.CacheRead
			s.Spend[i].Usage.CacheWrite += u.CacheWrite
			sessions.byRoot[session.Resolve(root)] = s
			return
		}
	}
	s.Spend = append(s.Spend, session.Spend{Model: model, Usage: u})
	sessions.byRoot[session.Resolve(root)] = s
}

// spentTotals adds the token counts up across models, which is the one figure
// that means the same thing whichever model earned it.
func spentTotals() (in, out, cached int) {
	for _, s := range spent() {
		in += s.Usage.Input + s.Usage.CacheRead + s.Usage.CacheWrite
		out += s.Usage.Output
		cached += s.Usage.CacheRead
	}
	return in, out, cached
}

// spentReport is the running total, "" when nothing has been asked yet.
func spentReport(p provider.Provider) string {
	in, out, cached := spentTotals()
	if in == 0 && out == 0 {
		return ""
	}
	line := fmt.Sprintf("↑%s ↓%s tokens", fmtTokens(in), fmtTokens(out))
	if cached > 0 {
		// Worth its own figure: it is the difference between this session and
		// the same session without a cache breakpoint.
		line += fmt.Sprintf(" · %s from cache", fmtTokens(cached))
	}
	// Each share at its own model's price. The provider argument is no longer
	// what prices anything; it is only here so a caller without one still
	// gets the tokens.
	var usd float64
	for _, s := range spent() {
		usd += config.CostOf(s.Model, s.Usage)
	}
	if cost := config.FormatUSD(usd); cost != "" {
		line += " · " + cost
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
	sessions.Lock()
	defer sessions.Unlock()
	s := sessionFor(here())
	if len(s.Messages) == 0 {
		return ""
	}
	return s.ID
}

// ContinueSession makes the saves go back into a conversation that was
// resumed, keeping its id and its start time. Without it every -resume forks
// a fresh copy of the history and the id nobody could hold on to.
func ContinueSession(s session.Session) {
	ContinueSessionIn(s, here())
}

// ContinueSessionIn is ContinueSession for a named project, which the daemon
// needs: it picks up a conversation per project and its own working directory
// says nothing about whose.
func ContinueSessionIn(s session.Session, root string) {
	sessions.Lock()
	defer sessions.Unlock()
	cur := sessionFor(root)
	cur.ID, cur.Started, cur.Model = s.ID, s.Started, s.Model
	cur.Messages, cur.Spend = s.Messages, s.Spend
	sessions.byRoot[session.Resolve(root)] = cur
}

// SaveSession writes the conversation, so every way of asking keeps it
// recoverable the same way. Exported because -p answers from orchestrator,
// which is outside this package and was the one door that saved nothing.
func SaveSession(a *agent.Agent) error {
	here, _ := os.Getwd()
	return saveSession(a, here)
}

// SaveSessionIn is SaveSession for a named project, which a daemon serving
// several needs: its own working directory says nothing about whose turn just
// finished.
func SaveSessionIn(a *agent.Agent, root string) error { return saveSession(a, root) }

func saveSession(a *agent.Agent, root string) error {
	sessions.Lock()
	defer sessions.Unlock()

	s := sessionFor(root)
	s.Messages = a.History
	// Spend is already on s: recordUsageIn put it there as the calls happened.
	// Stamped on every save rather than at the start: a session resumed in a
	// different tree belongs to the tree it is being worked in now.
	s.Root = root
	if a.Provider != nil {
		s.Model = a.Provider.Name()
	}
	sessions.byRoot[session.Resolve(root)] = s
	return store.Save(s)
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

	// The daemon first, when there is one. A task started here is a goroutine
	// in this process: closing the terminal kills it mid-flight and loses the
	// report, which is the whole reason the daemon exists. When there is no
	// daemon the old way still works, because requiring one to run a
	// background task would be a worse trade than losing one on exit.
	if c, err := daemonFor(); c != nil {
		_, err := c.StartTask(context.Background(), prompt)
		return err
	} else if err != nil {
		// Said rather than swallowed: a task that quietly went local is one
		// the user will lose on exit without ever being told why.
		a.OnNotice("could not use the daemon, running this task here instead: " + err.Error())
	}

	// Read what the task needs before starting it: /connect and /model may
	// replace the provider while the task is still running.
	p, system := a.Provider, a.System
	window, useTools := a.MaxContextTokens, a.UseTools
	allow := a.AllowTool

	go func() {
		a.Tasks.Run(context.Background(), truncate(prompt, 40), func(ctx context.Context, t task.Task) (string, int, error) {
			sub := taskAgent(p, system, window, useTools, allow)

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
			out, err := tools.Shell(ctx, here(), command, func(line string) {
				a.Tasks.Progress(t.ID, line+"\n")
			})
			return lastLines(out, checkReportLines), 0, err
		})
	}()
	return command, nil
}

// taskAgent builds the agent a background task runs as, and exists so that what
// a task inherits is written down in one place.
//
// It was five assignments inline, and one of them was missing: AllowTool, the
// function that answers what the project has refused. `internal/agent/task.go`
// had the same fault once and its skill notes record it — "a background task was
// handed tools the settings had refused" — and this is the other place the same
// copy is made. It matters more than it did: since the credential-by-convention
// default, AllowTool is the *only* thing refusing to read a .env, because the
// tools that merely look never reach a confirmation.
//
// Confirm is not inherited, deliberately: nobody is watching a task, so anything
// that writes or runs commands is refused outright rather than asked about in a
// window with no one in front of it.
func taskAgent(p provider.Provider, system string, window int, useTools bool,
	allow func(name, input string) bool) *agent.Agent {

	sub := agent.New(p)
	sub.System = system
	// The same model, so the same window and the same answer about tools: a task
	// compacting at 32k on a 200k model throws away history for nothing.
	sub.MaxContextTokens, sub.UseTools = window, useTools
	sub.AllowTool = allow
	sub.Confirm = func(string, string) bool { return false }
	return sub
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
	if strings.HasPrefix(id, daemonTaskPrefix) {
		c, ok := daemonClient()
		if !ok {
			return fmt.Sprintf("%s  %s belongs to a daemon that is no longer running%s", dim, id, reset)
		}
		if err := c.StopTask(context.Background(), strings.TrimPrefix(id, daemonTaskPrefix)); err != nil {
			return fmt.Sprintf("%s  nothing to stop: %v%s", dim, err, reset)
		}
		return fmt.Sprintf("%s  stopping %s%s", dim, id, reset)
	}
	if !a.Tasks.Stop(id) {
		return fmt.Sprintf("%s  nothing to stop: %s is not running%s", dim, id, reset)
	}
	return fmt.Sprintf("%s  stopping %s%s", dim, id, reset)
}

// tasksReport is that listing as text, so a front end that does not print
// straight to the terminal can show it too.
func tasksReport(a *agent.Agent, id string) string {
	list := allTasks(a)
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

// RecordUsage lets the headless front end add a call to the running total.
// Exported for the same reason SaveSession is: -p answers from orchestrator,
// and it was the one door that recorded nothing.
func RecordUsage(a *agent.Agent, u provider.Usage) { RecordUsageIn(a, u, here()) }

// RecordUsageIn is RecordUsage for a named project. The daemon runs the turn,
// so the daemon is what has to bill it: its own working directory says nothing
// about whose conversation just spent the tokens, and it was publishing usage
// to the terminals without recording it anywhere — so a turn run by the daemon
// was saved with no cost against it at all.
func RecordUsageIn(a *agent.Agent, u provider.Usage, root string) {
	if a.Provider == nil {
		return
	}
	recordUsageIn(root, a.Provider.Name(), u)
}

// daemonClient is the daemon, when one is already running. Looked up per call
// rather than held: a daemon can be started or stopped while the front end is
// open, and a handle kept from startup would be wrong either way round.
//
// It never starts one. Reading a task list is not a reason to leave a process
// behind on a machine that had none.
func daemonClient() (*daemon.Client, bool) {
	socket, err := daemon.SocketHere()
	if err != nil {
		return nil, false
	}
	return daemon.Running(socket)
}

// daemonFor is the daemon to run a background task in, started if there is
// none. This is the one call that starts one, because /bg is the one thing
// that is worse without it: a task here dies with the terminal.
//
// A nil client with a nil error means there is no daemon and no complaint —
// nothing to report, run it locally.
func daemonFor() (*daemon.Client, error) {
	socket, err := daemon.SocketHere()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c, err := daemon.Ensure(ctx, socket)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// daemonTaskPrefix marks a task the daemon is holding rather than this
// process. Both registries number from t1, so without it /stop t1 would be
// ambiguous the moment a session has one of each — which happens as soon as
// the model spawns a task while a daemon is running.
const daemonTaskPrefix = "d"

// allTasks is everything the front end knows about: its own, and whatever the
// daemon is holding. Both, because a session can have started tasks either
// way — the model's spawn_task still runs here, and /bg no longer does.
func allTasks(a *agent.Agent) []task.Task {
	list := a.Tasks.Snapshot()
	c, ok := daemonClient()
	if !ok {
		return list
	}
	views, err := c.Tasks(context.Background())
	if err != nil {
		// A daemon that answered a moment ago and cannot now is not worth an
		// error in the middle of a task list: what is local is still true.
		return list
	}
	for _, v := range views {
		list = append(list, fromView(v))
	}
	return list
}

// fromView turns a task on the wire back into one the front end can print.
func fromView(v daemon.TaskView) task.Task {
	t := task.Task{
		ID: daemonTaskPrefix + v.ID, Description: v.Description,
		Status: task.Status(v.Status), Elapsed: v.Elapsed,
		Tokens: v.Tokens, Report: v.Report, Output: v.Output,
	}
	if v.Err != "" {
		t.Err = errors.New(v.Err)
	}
	return t
}
