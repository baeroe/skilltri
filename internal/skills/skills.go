// Package skills parses SKILL.md files (agentskills.io spec) and discovers skills below a folder.
// Spec violations are reported as warnings on the skill, never as errors.
package skills

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Skill is one parsed skill folder.
type Skill struct {
	Name, Description, Dir string
	Warnings               []string
}

const (
	fileName      = "SKILL.md"
	maxNameLen    = 64
	maxDescLength = 1024
)

var nameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Parse reads dir/SKILL.md. It fails only when the file is missing or unreadable;
// a skill without a usable name falls back to its folder name.
func Parse(dir string) (Skill, error) {
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		return Skill{}, err
	}
	folder := filepath.Base(dir)
	s := Skill{Name: folder, Dir: dir}
	warn := func(format string, args ...any) { s.Warnings = append(s.Warnings, fmt.Sprintf(format, args...)) }

	front, ok := frontmatter(data)
	if !ok {
		warn("missing frontmatter")
		return s, nil
	}
	var fields map[string]any
	if err := yaml.Unmarshal(front, &fields); err != nil {
		warn("invalid frontmatter: %v", err)
		return s, nil
	}

	switch v := fields["name"].(type) {
	case nil:
		warn("missing name, using folder name")
	case string:
		if v == "" {
			warn("missing name, using folder name")
			break
		}
		s.Name = v
		if utf8.RuneCountInString(v) > maxNameLen || !nameRe.MatchString(v) {
			warn("invalid name %q: want 1-%d chars of lowercase letters, digits and single hyphens", v, maxNameLen)
		}
		if v != folder {
			warn("name %q differs from folder %q", v, folder)
		}
	default:
		warn("name is not a string, using folder name")
	}

	switch v := fields["description"].(type) {
	case nil:
		warn("missing description")
	case string:
		s.Description = v
		if v == "" {
			warn("empty description")
		} else if utf8.RuneCountInString(v) > maxDescLength {
			warn("description longer than %d chars", maxDescLength)
		}
	default:
		warn("description is not a string")
	}
	return s, nil
}

// frontmatter returns the text between a first line "---" and the next line "---".
func frontmatter(data []byte) ([]byte, bool) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) == 0 || string(bytes.TrimRight(lines[0], " \t")) != "---" {
		return nil, false
	}
	for i := 1; i < len(lines); i++ {
		if string(bytes.TrimRight(lines[i], " \t")) == "---" {
			return bytes.Join(lines[1:i], []byte("\n")), true
		}
	}
	return nil, false
}

// Discover finds every folder below root that contains a SKILL.md. It does not descend into a skill
// folder, skips .git and node_modules, and follows each symlinked directory at most once per real path.
// The result is sorted by Dir; the second value holds warnings of the form "<path>: <problem>".
func Discover(root string) ([]Skill, []string) {
	var found []Skill
	var warns []string
	visited := map[string]bool{}

	var walk func(dir string)
	walk = func(dir string) {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s: %v", dir, err))
			return
		}
		if visited[real] {
			return
		}
		visited[real] = true

		if st, err := os.Stat(filepath.Join(dir, fileName)); err == nil && st.Mode().IsRegular() {
			s, err := Parse(dir)
			if err != nil {
				warns = append(warns, fmt.Sprintf("%s: %v", dir, err))
				return
			}
			for _, w := range s.Warnings {
				warns = append(warns, fmt.Sprintf("%s: %s", dir, w))
			}
			found = append(found, s)
			return
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			warns = append(warns, fmt.Sprintf("%s: %v", dir, err))
			return
		}
		for _, e := range entries {
			name := e.Name()
			if name == ".git" || name == "node_modules" {
				continue
			}
			path := filepath.Join(dir, name)
			if e.IsDir() {
				walk(path)
			} else if e.Type()&os.ModeSymlink != 0 {
				if st, err := os.Stat(path); err == nil && st.IsDir() {
					walk(path)
				}
			}
		}
	}
	walk(root)

	sort.Slice(found, func(i, j int) bool { return found[i].Dir < found[j].Dir })
	return found, warns
}
