// Package build answers what the running binary is. One string, and three
// ways of finding it out, because a binary arrives here by three routes and
// only one of them is a person running `make build`.
//
// It is a package of its own rather than a variable in main, because
// `cmd/uhai` holds flag parsing and nothing else — and rather than a file in
// config, which is about what the user has configured, where this is about
// what the compiler was given.
package build

import (
	"runtime/debug"
	"strings"
)

// version is set at link time by the release build:
//
//	-X github.com/didik-prabowo/uhai/internal/build.version=v0.1.0
//
// Empty in every other build, which is the signal to go and ask the runtime.
// A var with no writer in the source is exactly the shape a linker flag
// needs, and exactly the shape a reader finds suspicious — hence this note.
var version string

// Version is what the binary calls itself: a tag on a released build, the
// module version when it was fetched with `go install`, and the commit it was
// built from when it came from a clone.
//
// "dev" only when all three fail, which means a build with its VCS stamping
// switched off. It is a truthful answer rather than a blank: something is
// running, and nothing can say what.
func Version() string {
	if version != "" {
		return version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if tagged(info.Main.Version) {
		return info.Main.Version
	}

	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if revision == "" {
		return "dev"
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	// A dirty tree is the common case while working, and the one where a
	// version lies loudest: the commit it names is not what is running.
	if modified == "true" {
		return revision + "-dirty"
	}
	return revision
}

// Released reports whether this binary came from a tagged release, which is
// the only case where the version is a promise rather than a description.
func Released() bool {
	return version != "" || tagged(Version())
}

// tagged reports whether a module version is a real tag rather than the
// pseudo-version the toolchain synthesises when there is no tag to use.
//
// The distinction is the whole point, and "starts with v" does not make it:
// a build straight from this working tree reports
// `v0.0.0-20260926093716-6f3f73ac1625+dirty`, which begins with a v, is not
// a release, and would have put that whole string where the welcome box says
// when the binary was built.
//
// A pseudo-version always ends in the twelve hex digits of a commit, with an
// optional `+dirty`. Nothing else in a version number has that shape.
func tagged(v string) bool {
	if !strings.HasPrefix(v, "v") {
		return false
	}
	i := strings.LastIndex(v, "-")
	if i < 0 {
		return true // v0.1.0 — nothing to mistake it for
	}
	suffix := strings.TrimSuffix(v[i+1:], "+dirty")
	if len(suffix) != 12 {
		return true // v0.1.0-rc1 and friends: a tag with a prerelease on it
	}
	for _, c := range suffix {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return true
		}
	}
	return false
}
