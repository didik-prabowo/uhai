// Where uhai keeps its things. One name for the directory, in one file,
// because the name is not code the compiler checks: it is a string, repeated,
// and a rename survives only if every copy is found. Renaming ouhai to uhai
// found four of them and survived on a global search rather than on design.
package config

import (
	"os"
	"path/filepath"
)

// dirName is the folder uhai owns, in the home directory and beside the code.
// Both are the same word on purpose — .uhai/settings.json means the same thing
// wherever it is, and only who it belongs to changes.
const dirName = ".uhai"

// ProjectDir is uhai's folder beside the code: settings, skills, and whatever
// belongs to the repository rather than to the person.
const ProjectDir = dirName

// Dir is ~/.uhai — credentials, saved conversations, and settings that follow
// the person rather than the project. It errors only when there is no home
// directory to speak of, which is worth passing on rather than guessing at.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dirName), nil
}

// InDir joins onto Dir, for the callers that want one file rather than the
// folder holding it.
func InDir(parts ...string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{dir}, parts...)...), nil
}

// homeJoin builds a path in the home directory rather than in ours. It exists
// for the one folder that is not uhai's to name — Claude Code's ~/.claude,
// which is read because a person who already keeps skills there should not
// have to move them.
func homeJoin(parts ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, parts...)...), nil
}
