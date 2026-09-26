// Command uhai is a minimal CLI coding agent. Everything it does lives in
// internal/; this is only the entry point.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/didik-prabowo/uhai/internal/build"
	"github.com/didik-prabowo/uhai/internal/orchestrator"
)

func main() {
	prompt := flag.String("p", "", "answer one prompt and exit, instead of opening the UI")
	allowTools := flag.Bool("y", false, "with -p, let tools write files and run commands without asking")
	resume := flag.Bool("resume", false, "carry on from a saved conversation: the newest, or -resume <id>")
	list := flag.Bool("sessions", false, "list the saved conversations and exit")
	daemon := flag.Bool("daemon", false, "run the agent as a process the terminal can outlive")
	attach := flag.Bool("attach", false, "talk to the conversation the daemon holds, not a new one here")
	stop := flag.Bool("daemon-stop", false, "stop the running daemon, ending every project's background work")
	taskPrompt := flag.String("task", "", "answer one prompt as a background worker, reporting on stdout")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("uhai", build.Version())
		return
	}

	if *taskPrompt != "" {
		// The daemon spawns this; a person has no reason to. It is here
		// rather than hidden because a flag nobody can see is a flag nobody
		// can debug, and running it by hand is exactly how its output was
		// first read.
		if err := orchestrator.RunTask(*taskPrompt); err != nil {
			fmt.Fprintln(os.Stderr, "uhai:", err)
			os.Exit(1)
		}
		return
	}

	if *stop {
		if err := orchestrator.StopDaemon(); err != nil {
			fmt.Fprintln(os.Stderr, "uhai:", err)
			os.Exit(1)
		}
		return
	}
	if *daemon {
		if err := orchestrator.RunDaemon(); err != nil {
			fmt.Fprintln(os.Stderr, "uhai:", err)
			os.Exit(1)
		}
		return
	}
	if *list {
		if err := orchestrator.ListSessions(); err != nil {
			fmt.Fprintln(os.Stderr, "uhai:", err)
			os.Exit(1)
		}
		return
	}
	if *attach {
		if err := orchestrator.RunAttached(); err != nil {
			fmt.Fprintln(os.Stderr, "uhai:", err)
			os.Exit(1)
		}
		return
	}
	if *prompt == "" {
		// -resume is a bool so it can be given alone; the id, when there is
		// one, is the argument after it.
		orchestrator.Run(*resume, flag.Arg(0))
		return
	}
	if err := orchestrator.RunOnce(*prompt, *allowTools); err != nil {
		fmt.Fprintln(os.Stderr, "uhai:", err)
		os.Exit(1)
	}
}
