package cli

import (
	"os"
	"testing"
)

// TestMain stops these tests from starting daemons.
//
// /bg starts one when there is none, and os.Executable() inside a test is the
// test binary — so the launcher would run the whole suite again in a
// subprocess, with -daemon, which go test does not understand. It showed up as
// a background-task test taking five seconds and then finding nothing.
//
// UHAI_DAEMON_SPAWNED is the marker the launcher already uses to stop a chain
// of spawns, and "do not spawn from here" is exactly what a test means.
func TestMain(m *testing.M) {
	os.Setenv("UHAI_DAEMON_SPAWNED", "1")
	os.Exit(m.Run())
}
