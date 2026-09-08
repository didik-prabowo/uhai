// Running commands. One shell, one command, killed with everything it started.
package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// bashTimeout bounds one command the model runs. Two minutes so a test suite
// finishes rather than being cut off at the interesting part — the old thirty
// seconds meant the model could not verify its own work on any project bigger
// than a toy. Long enough to be useful, short enough that a command waiting on
// input dies instead of hanging the turn; /check is the way to run something
// that takes longer than this.
const bashTimeout = 2 * time.Minute

// bashTool is the run_bash tool: what the model is told about it, and the
// thing that runs.
type bashTool struct{}

func (bashTool) Name() string       { return NameBash }
func (bashTool) NeedsConfirm() bool { return true }
func (bashTool) Description() string {
	return "Run one shell (bash) command and return its stdout+stderr. It is killed after two minutes, so it must not wait for input."
}
func (bashTool) Schema() json.RawMessage {
	return json.RawMessage(`{
			"type": "object",
			"properties": {
				"command": {"type": "string", "description": "Shell command to run"}
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

	ctx, cancel := context.WithTimeout(ctx, bashTimeout)
	defer cancel()

	out, err := Shell(ctx, root, args.Command, nil)
	switch ctx.Err() {
	case context.DeadlineExceeded:
		return "timeout: the command took too long", true
	case context.Canceled:
		return "the user interrupted this command", true
	}
	if err != nil {
		return fmt.Sprintf("exit error: %v\noutput:\n%s", err, out), true
	}
	return out, false
}

// Shell runs one command and returns everything it printed, whether it
// succeeded or not. onLine, when given, is called with each line as it is
// printed, so a long command can be watched rather than waited for.
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

	// Its own process group, so cancelling kills what the command started as
	// well as the command. Killing the shell alone leaves "go test" compiling
	// happily in the background, which is not what stopping means.
	ownGroup(cmd)

	if onLine == nil {
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
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
	cmd.Stderr = cmd.Stdout // one stream, in the order it was printed
	if err := cmd.Start(); err != nil {
		return "", err
	}
	defer killGroup(ctx, cmd)()

	var out strings.Builder
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // one line can be long
	for sc.Scan() {
		line := sc.Text()
		out.WriteString(line + "\n")
		onLine(line)
	}
	return out.String(), cmd.Wait()
}
