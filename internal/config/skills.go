// Skills: instructions a project keeps aside for the jobs that need them,
// rather than in the notes every session carries. Only the name and the
// one-line description travel with each prompt; the body is a file the model
// opens when the work turns out to be that work.
//
// The layout is the one Claude Code uses — a folder per skill with a SKILL.md
// inside, under .uhai/skills or .claude/skills — so a project that already
// has them needs nothing added here.
package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// skillDirs are where skills are looked for, ours first. The other two are
// what projects already use — a repository should not have to move its files
// to be read here.
var skillDirs = []string{".uhai/skills", ".claude/skills", ".agents/skills"}

// Skill is one of them, as the model is told about it.
type Skill struct {
	Name        string
	Description string
	Path        string
}

// Skills lists what this project has, sorted by name so the prompt does not
// change from run to run. A skill nobody described is still listed: the name
// and the path are enough to decide whether to open it.
func Skills() []Skill {
	seen := map[string]bool{}
	var out []Skill

	dirs := skillDirs
	if s, err := LoadSettings(); err == nil {
		// A project that keeps them somewhere of its own says so, rather than
		// moving its files to suit this.
		dirs = append(append([]string{}, s.SkillDirs...), skillDirs...)
	}

	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			path, ok := skillFile(dir, entry.Name(), entry.IsDir())
			if !ok {
				continue
			}
			skill := readSkill(path, entry.Name())
			if seen[skill.Name] {
				continue // ours wins: the first directory searched keeps the name
			}
			seen[skill.Name] = true
			out = append(out, skill)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// skillFile is where a skill's instructions live: SKILL.md in a folder of its
// own, which is the usual shape, or a plain markdown file for a project that
// keeps them flat.
func skillFile(dir, name string, isDir bool) (string, bool) {
	if isDir {
		path := filepath.Join(dir, name, "SKILL.md")
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
		return "", false
	}
	if strings.HasSuffix(name, ".md") && !strings.EqualFold(name, "README.md") {
		return filepath.Join(dir, name), true
	}
	return "", false
}

// readSkill takes the name and description from the frontmatter, falling back
// to the file's own name. Only the head of the file is read: the body is for
// the model to open later, and reading it here would defeat the point.
func readSkill(path, fallback string) Skill {
	skill := Skill{
		Name: strings.TrimSuffix(strings.TrimSuffix(fallback, ".md"), "/"),
		Path: path,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return skill
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return skill
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			if value != "" {
				skill.Name = value
			}
		case "description":
			skill.Description = value
		}
	}
	return skill
}

// SkillNotes is the list as the model is given it: what there is, and where to
// read it. Empty when the project has none.
func SkillNotes() string {
	skills := Skills()
	if len(skills) == 0 {
		return ""
	}

	var b strings.Builder
	for _, skill := range skills {
		b.WriteString("- " + skill.Name)
		if skill.Description != "" {
			b.WriteString(" — " + skill.Description)
		}
		b.WriteString(" (read " + skill.Path + ")\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
