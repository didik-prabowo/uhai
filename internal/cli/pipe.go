// What runs when stdin is not a terminal: a prompt per line, answers on
// stdout. There is no screen to own here, and nobody to ask about a tool, so
// this is a loop rather than a front end.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/didik-prabowo/uhai/internal/agent"
)

func runPipe(a *agent.Agent, startupErr error) {
	if startupErr != nil {
		fmt.Fprintln(os.Stderr, "uhai:", startupErr)
	}

	// The answer goes to stdout so it can be piped; everything else to stderr,
	// so it does not pollute that.
	a.OnText = func(text string) { fmt.Println(markThinking(text)) }
	a.OnToolCall = func(name, input string) {
		fmt.Fprintf(os.Stderr, "  ⎿ %s(%s)\n", name, truncate(input, 200))
	}
	a.OnNotice = func(text string) { fmt.Fprintln(os.Stderr, text) }

	// Nobody is here to answer, so anything that writes or runs commands is
	// refused and the model is told why. -p -y is the way to allow it.
	a.Confirm = func(string, string) bool { return false }

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024) // a pasted prompt can be long
	for in.Scan() {
		prompt := strings.TrimSpace(in.Text())
		switch {
		case prompt == "":
			continue
		case prompt == "/exit" || prompt == "/quit":
			return
		case strings.HasPrefix(prompt, "/"):
			// A command is not a question: sending it to the model spends a
			// turn to be told it is not a question.
			fmt.Fprintln(os.Stderr, pipeAnswer(prompt))
			continue
		case a.Provider == nil:
			fmt.Fprintln(os.Stderr, "uhai: no provider connected — run uhai in a terminal and type /connect")
			return
		}

		if err := a.Ask(context.Background(), prompt); err != nil {
			fmt.Fprintln(os.Stderr, "uhai:", err)
		}
		if err := SaveSession(a); err != nil {
			fmt.Fprintln(os.Stderr, "uhai: the session is not being saved:", err)
			return
		}
	}
}
