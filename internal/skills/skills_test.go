package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		name     string
		folder   string
		content  string
		wantName string
		wantDesc string
		warn     string // substring expected in a warning; "" = no warnings
	}{
		{"valid", "pdf-tools", "---\nname: pdf-tools\ndescription: Work with PDFs.\n---\n# Body\n", "pdf-tools", "Work with PDFs.", ""},
		{"folded", "pdf-tools", "---\nname: pdf-tools\ndescription: >\n  First line\n  second line\n---\nbody\n", "pdf-tools", "First line second line", ""},
		{"quoted", "pdf-tools", "---\nname: \"pdf-tools\"\ndescription: 'Quoted: value'\n---\n", "pdf-tools", "Quoted: value", ""},
		{"crlf and bom", "pdf-tools", "\xef\xbb\xbf---\r\nname: pdf-tools\r\ndescription: Hi\r\n---\r\nbody", "pdf-tools", "Hi", ""},
		{"unknown keys ignored", "pdf-tools", "---\nname: pdf-tools\ndescription: Hi\nlicense: MIT\nmetadata:\n  a: b\n---\n", "pdf-tools", "Hi", ""},
		{"no frontmatter", "pdf-tools", "# Just a body\n", "pdf-tools", "", "missing frontmatter"},
		{"unclosed frontmatter", "pdf-tools", "---\nname: pdf-tools\n", "pdf-tools", "", "missing frontmatter"},
		{"invalid yaml", "pdf-tools", "---\nname: [oops\n---\n", "pdf-tools", "", "frontmatter"},
		{"bad name", "Bad_Name", "---\nname: Bad_Name\ndescription: Hi\n---\n", "Bad_Name", "Hi", "name"},
		{"name differs from folder", "folder", "---\nname: other\ndescription: Hi\n---\n", "other", "Hi", "folder"},
		{"long description", "pdf-tools", "---\nname: pdf-tools\ndescription: " + strings.Repeat("x", 1025) + "\n---\n", "pdf-tools", strings.Repeat("x", 1025), "description"},
		{"empty description", "pdf-tools", "---\nname: pdf-tools\n---\n", "pdf-tools", "", "description"},
		{"missing name uses folder", "pdf-tools", "---\ndescription: Hi\n---\n", "pdf-tools", "Hi", "name"},
		{"non-string name", "pdf-tools", "---\nname: [a, b]\ndescription: Hi\n---\n", "pdf-tools", "Hi", "name"},
		{"non-string description", "pdf-tools", "---\nname: pdf-tools\ndescription: {a: b}\n---\n", "pdf-tools", "", "description"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), c.folder)
			writeSkill(t, dir, c.content)
			s, err := Parse(dir)
			if err != nil {
				t.Fatal(err)
			}
			if s.Name != c.wantName || s.Description != c.wantDesc || s.Dir != dir {
				t.Errorf("got name=%q desc=%q dir=%q", s.Name, s.Description, s.Dir)
			}
			joined := strings.Join(s.Warnings, "\n")
			if c.warn == "" && len(s.Warnings) != 0 {
				t.Errorf("unexpected warnings: %v", s.Warnings)
			}
			if c.warn != "" && !strings.Contains(joined, c.warn) {
				t.Errorf("warnings %v lack %q", s.Warnings, c.warn)
			}
		})
	}
}

func TestParseMissingFile(t *testing.T) {
	if _, err := Parse(t.TempDir()); err == nil {
		t.Fatal("want error for missing SKILL.md")
	}
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	ok := "---\nname: %s\ndescription: Hi\n---\n"
	writeSkill(t, filepath.Join(root, "a"), strings.Replace(ok, "%s", "a", 1))
	writeSkill(t, filepath.Join(root, "a", "references", "x"), strings.Replace(ok, "%s", "x", 1))
	writeSkill(t, filepath.Join(root, "group", "b"), strings.Replace(ok, "%s", "b", 1))
	writeSkill(t, filepath.Join(root, ".git", "c"), strings.Replace(ok, "%s", "c", 1))
	writeSkill(t, filepath.Join(root, "node_modules", "d"), strings.Replace(ok, "%s", "d", 1))

	got, warns := Discover(root)
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "a,b" {
		t.Fatalf("names = %v", names)
	}
	if got[0].Dir != filepath.Join(root, "a") || got[1].Dir != filepath.Join(root, "group", "b") {
		t.Errorf("dirs = %q %q", got[0].Dir, got[1].Dir)
	}
}

func TestDiscoverWarningsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "bad"), "no frontmatter")
	other := t.TempDir()
	writeSkill(t, filepath.Join(other, "linked"), "---\nname: linked\ndescription: Hi\n---\n")
	if err := os.Symlink(other, filepath.Join(root, "ext")); err != nil {
		t.Fatal(err)
	}
	// loop: symlink back to root, and a second link to the same target
	if err := os.Symlink(root, filepath.Join(other, "back")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(root, "ext2")); err != nil {
		t.Fatal(err)
	}

	got, warns := Discover(root)
	if len(got) != 2 {
		t.Fatalf("got %d skills: %+v", len(got), got)
	}
	found := false
	for _, w := range warns {
		if strings.HasPrefix(w, filepath.Join(root, "bad")+": ") && strings.Contains(w, "missing frontmatter") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v", warns)
	}
}

func TestDiscoverMissingRoot(t *testing.T) {
	got, warns := Discover(filepath.Join(t.TempDir(), "nope"))
	if len(got) != 0 || len(warns) == 0 {
		t.Errorf("got %v %v", got, warns)
	}
}
