package build

import "testing"

// The distinction this package exists to make. It is one function and it is
// worth a test because getting it wrong is silent: the welcome box would show
// a pseudo-version where it should show a build time, and nobody would read
// it as a bug — just as a version they did not recognise.
func TestATagIsNotAPseudoVersion(t *testing.T) {
	releases := []string{
		"v0.1.0",
		"v1.0.0",
		"v0.2.0-rc1",
		"v0.2.0-beta.2",
	}
	for _, v := range releases {
		if !tagged(v) {
			t.Errorf("%s is a tag", v)
		}
	}

	// What the toolchain writes when there is no tag to use: v0.0.0, a UTC
	// timestamp, and twelve hex digits of the commit.
	pseudo := []string{
		"v0.0.0-20260926093716-6f3f73ac1625",
		"v0.0.0-20260926093716-6f3f73ac1625+dirty",
		"v0.1.1-0.20260926093716-abcdef012345",
	}
	for _, v := range pseudo {
		if tagged(v) {
			t.Errorf("%s is a pseudo-version, not a release", v)
		}
	}

	for _, v := range []string{"", "dev", "6f3f73ac1625", "6f3f73ac1625-dirty"} {
		if tagged(v) {
			t.Errorf("%q is not a version at all", v)
		}
	}
}

// Whatever route the binary arrived by, it says something. A blank here would
// print "uhai " and leave the reader with nothing to report.
func TestVersionIsNeverEmpty(t *testing.T) {
	if Version() == "" {
		t.Error("the binary must be able to name itself")
	}
}

// The linker flag is the release path, and it wins over everything the
// runtime would have guessed. Checked here because .goreleaser.yaml names
// this variable by its import path, and nothing else would notice a rename.
func TestTheLinkerFlagWins(t *testing.T) {
	old := version
	defer func() { version = old }()

	version = "v9.9.9"
	if got := Version(); got != "v9.9.9" {
		t.Errorf("version = %q, want the linked one", got)
	}
	if !Released() {
		t.Error("a linked version is a release")
	}
}
