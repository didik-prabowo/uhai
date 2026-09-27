// Running commands. One shell, one command, killed with everything it started,
// and only ever the tail of what it printed.
package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// BashTimeout is what a command gets when it asks for nothing. Two minutes so a
// test suite finishes rather than being cut off at the interesting part — the
// old thirty seconds meant the model could not verify its own work on any
// project bigger than a toy. Long enough to be useful, short enough that a
// command waiting on input dies instead of hanging the turn.
//
// Exported because the confirmation has to say how long it is agreeing to, and
// a second copy of the number is a second thing to keep true.
const BashTimeout = 2 * time.Minute

// maxBashTimeout is as long as a command may be given when it asks for more.
//
// Ten minutes is what /check already allows, so this is not a new ceiling — it
// is the existing one made reachable by the model, which could not get past two
// minutes by any route. /check stays what it is: its bargain was never about
// duration but about permission, since the command there is the user's own.
//
// What it costs is a turn held open for ten minutes with nothing on the screen,
// which is why the person approving it is told the number rather than the word
// "command".
const maxBashTimeout = 10 * time.Minute

// BashLimit is how long one call may run: what it asked for, clamped, and the
// default when it asked for nothing. Both the tool and the confirmation read it,
// so the number a person approves is the number that is enforced.
//
// Clamped rather than refused. A model asking for an hour has made a recoverable
// mistake, and spending a turn telling it so costs more than giving it ten
// minutes and naming the limit in whatever the command reports back.
func BashLimit(input string) time.Duration {
	var args struct {
		Timeout int `json:"timeout"`
	}
	if json.Unmarshal([]byte(input), &args) != nil || args.Timeout <= 0 {
		return BashTimeout
	}
	return min(time.Duration(args.Timeout)*time.Second, maxBashTimeout)
}

// maxBashOutput is how much of a command's output is kept, and it is the tail
// that is kept: a suite says what passed before it says what failed, so the end
// is the half worth having.
//
// It is also the memory ceiling, which there was none of. One second of `yes`
// put 868 MiB into a bytes.Buffer, and Execute's truncation only happens once
// the whole of it is already there — in the daemon, the process that runs out
// of memory is holding every project's conversation on the machine.
//
// Below maxResultLen rather than equal to it, so the lines this file puts in
// front of the output — an exit status, a note that the command was killed —
// cannot push the result past Execute's cut, which takes from the front and
// would take the end with it.
const maxBashOutput = maxResultLen - 512

// HiddenEnv are the variables uhai alone owns, removed from what a command
// inherits. A key left in reaches a tool result, then the history, then the
// provider, and then the session on disk — from one `env` that nobody thought
// twice about approving.
//
// Two names, and deliberately not the vendors'. ANTHROPIC_API_KEY and
// OPENAI_API_KEY are the *user's* environment: uhai reads them as a
// convenience, and the project being worked on may need the same variable for
// its own tests. Stripping those would make an authentication failure that
// nothing explains — the shape of failure this repository treats as the worst
// one — to close an exposure the user's own shell already has.
//
// So this is not a secret filter, and must not be read as one:
// AWS_SECRET_ACCESS_KEY, GITHUB_TOKEN and DATABASE_URL all still reach a
// command, and from there a provider. Redacting tool results in general is a
// different and much larger question; see the issue, not this list.
//
// Exported so a test outside this package can name them.
var HiddenEnv = []string{
	"UHAI_API_KEY",
	SearchKeyEnv,
}

// noTerminal is what a command is told about the terminal it does not have.
// Colour escapes are tokens paid for on every turn that carries them and read
// by nobody, and a pager is a command waiting for a keypress that will never
// come. Appended last because exec takes the last value for a duplicate key,
// so these win over whatever the environment already said.
var noTerminal = []string{"NO_COLOR=1", "TERM=dumb", "GIT_PAGER=cat", "PAGER=cat"}

// bashTool is the run_bash tool: what the model is told about it, and the
// thing that runs.
type bashTool struct{}

func (bashTool) Name() string       { return NameBash }
func (bashTool) NeedsConfirm() bool { return true }
func (bashTool) Description() string {
	return "Run one shell (bash) command and return its stdout+stderr. It must not wait for input: there is no terminal and nobody to type. " +
		"It is killed after two minutes unless timeout says otherwise."
}
func (bashTool) Schema() json.RawMessage {
	return json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {"type": "string", "description": "Shell command to run"},
				"timeout": {"type": "integer", "description": "Seconds to allow, default 120, maximum 600. Ask for more only when a build or a test suite needs it; the turn waits with nothing on screen while it runs"}
			},
			"required": ["command"]
		}`)
}

func (bashTool) Run(ctx context.Context, root string, input json.RawMessage) (string, bool) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(input, &args); err != nil {
		return err.Error(), true
	}

	limit := BashLimit(string(input))
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	out, err := Shell(ctx, root, args.Command, nil)

	// Binary is nothing a model can read, and 8,000 characters of it is a
	// turn's worth of tokens spent on noise. grep refuses it for this reason
	// and says so in the same words.
	if strings.IndexByte(out, 0) >= 0 {
		return fmt.Sprintf("the command printed %d bytes of binary output, which is not shown", len(out)), true
	}

	// The output goes back however the command ended. It was paid for, it was
	// on the screen, and for a suite killed at the two-minute mark the failures
	// it had already printed are the only part worth having — this used to
	// return the sentence alone and throw them away. searchNote does the same
	// thing for a grep that runs out of time.
	switch {
	case ctx.Err() == context.Canceled:
		// Authoritative whatever the command did: the person asked for it to
		// stop, and a command that squeezed past that is not a reason to
		// report success.
		return "the user interrupted this command. Output up to then:\n" + out, true
	case ctx.Err() == context.DeadlineExceeded && err != nil:
		return fmt.Sprintf("timeout: killed after %s. Output up to then:\n%s", limit, out), true
	case err != nil:
		return fmt.Sprintf("exit error: %v\noutput:\n%s", err, out), true
	}
	// Not the clock: a command that finished as the deadline passed has
	// finished, and reporting a timeout over a complete answer sends the model
	// to run it again.
	return out, false
}

// Shell runs one command and returns the tail of everything it printed,
// whether it succeeded or not. onLine, when given, is called with each line as
// it is printed, so a long command can be watched rather than waited for.
//
// It sets no deadline of its own: a tool call and a run of the whole test
// suite want very different ones, and the caller knows which it is. dir is
// the project to run in, and empty means the process's own directory.
func Shell(ctx context.Context, dir, command string, onLine func(string)) (string, error) {
	cmd := exec.Command("bash", "-c", command)
	// In the project asked about, not wherever this process was started. The
	// daemon serves every project from the one directory it was spawned in,
	// so without this a command ran — and wrote — in whichever project
	// happened to wake the daemon first. Empty keeps the process's own.
	cmd.Dir = dir
	cmd.Env = commandEnv()

	// Its own process group, so cancelling kills what the command started as
	// well as the command. Killing the shell alone leaves "go test" compiling
	// happily in the background, which is not what stopping means.
	ownGroup(cmd)

	out := &tailBuffer{keep: maxBashOutput}

	if onLine == nil {
		// ponytail: one stream for both, so the model cannot tell a build log
		// from an error. Two buffers would separate them and lose the order they
		// were printed in, which is how a failure is read — the line before it
		// is usually the reason. Interleaving is the more useful half, and
		// nothing has been seen mistaking a log line for a failure. Give them
		// their own buffers, tagged, when something is.
		cmd.Stdout, cmd.Stderr = out, out
		if err := cmd.Start(); err != nil {
			return "", err
		}
		defer killGroup(ctx, cmd)()

		// Wait first: reading the buffer in the return statement would read it
		// before the command had finished filling it.
		err := cmd.Wait()
		return out.String(), err
	}

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = cmd.Stdout // one stream, for the reason above
	if err := cmd.Start(); err != nil {
		return "", err
	}
	defer killGroup(ctx, cmd)()

	// ReadSlice rather than a bufio.Scanner. A Scanner has a maximum token
	// size, and a line past it ended the loop with ErrTooLong — which nothing
	// read — and then blocked in Wait forever, because the pipe it had stopped
	// draining filled up and the child could no longer write to it. A single
	// 2 MiB line therefore returned *nothing at all* and hung until the
	// deadline: ten minutes of it under /check. ReadSlice hands back what it
	// has and says the line is still going, so a line of any length costs one
	// fixed buffer and never stalls.
	r := bufio.NewReaderSize(pipe, 64*1024)
	for {
		chunk, err := r.ReadSlice('\n')
		if len(chunk) > 0 {
			out.Write(chunk)
			onLine(strings.TrimRight(string(chunk), "\n"))
		}
		if err == bufio.ErrBufferFull {
			continue // the same line, still arriving
		}
		if err != nil {
			break
		}
	}
	return out.String(), cmd.Wait()
}

// commandEnv is the environment a command runs in: the one uhai was started
// with, less the keys uhai reads for itself, plus the few variables that say
// there is no terminal on the other end.
func commandEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+len(noTerminal))
	for _, kv := range env {
		if name, _, ok := strings.Cut(kv, "="); ok && slices.Contains(HiddenEnv, name) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, noTerminal...)
}

// tailBuffer keeps the last keep bytes written to it and counts what it drops.
// A command's output has no ceiling of its own, and the end of it is the half
// worth keeping.
type tailBuffer struct {
	buf     []byte
	keep    int
	dropped int
}

// Write never fails, the way a bytes.Buffer never fails: there is nowhere for
// the bytes to fail to go.
func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	// Trimmed at twice the ceiling rather than on every write, so a command
	// printing one line at a time does not copy the whole buffer per line.
	// Memory is bounded at twice keep plus one write either way.
	if over := len(t.buf) - 2*t.keep; over > 0 {
		t.dropped += over
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

// String is the tail, headed by a line saying what was dropped. The count is
// there because silence reads as "this is all of it", and a model that
// believes it is looking at a whole test run draws conclusions from the part
// it was handed.
func (t *tailBuffer) String() string {
	buf, dropped := t.buf, t.dropped
	if over := len(buf) - t.keep; over > 0 {
		buf, dropped = buf[over:], dropped+over
	}
	if dropped == 0 {
		return string(buf)
	}
	return fmt.Sprintf("...[%d earlier bytes dropped]\n", dropped) + string(buf)
}
