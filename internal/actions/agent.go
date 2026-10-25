package actions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/fsutil"
	"github.com/baeroe/skilltri/internal/paths"
	"github.com/baeroe/skilltri/internal/skills"
)

// MaxSkillFile is the largest SKILL.md SkillWrite accepts (the limit the skill tools serve).
const MaxSkillFile = 256 << 10

// SetAgentAccess sets settings.agentAccess to level ("off", "read" or "write"); another value
// wraps ErrInvalid.
func SetAgentAccess(e Env, level string) error {
	switch level {
	case config.AgentAccessOff, config.AgentAccessRead, config.AgentAccessWrite:
	default:
		return wrap(ErrInvalid, "unknown agent access %q (want off, read or write)", level)
	}
	return e.edit(func(c *config.Config) error {
		c.Settings.AgentAccess = level
		return nil
	})
}

// SkillWrite replaces the SKILL.md of the catalog skill id with content. Only skills of local
// sources can be written (git and marketplace checkouts belong to their upstream and are replaced
// on update). content must be a valid SKILL.md whose frontmatter name equals the skill's folder;
// it is checked in a temp folder first, so a refused write leaves the file as it was. A skill
// missing from the catalog wraps ErrNotFound.
func SkillWrite(e Env, id, content string) error {
	cfg, err := config.Load(e.configPath())
	if err != nil {
		return err
	}
	entry, ok := findSkill(e.OfflineCatalog(cfg), id)
	if !ok {
		return wrap(ErrNotFound, "no such skill %q", id)
	}
	src := cfg.Sources[entry.Source]
	if src == nil || src.Type != "local" {
		kind := "unknown"
		if src != nil {
			kind = src.Type
		}
		return fmt.Errorf("skill %q comes from the %s source %q: only skills of local sources can be written (git and marketplace skills belong to their upstream and are replaced on update)", id, kind, entry.Source)
	}
	if len(content) > MaxSkillFile {
		return fmt.Errorf("SKILL.md is larger than the 256 KiB limit")
	}
	checkouts := []string{filepath.Join(paths.DataDir(), "sources")}
	if b := e.builder(); b.Fetcher != nil && b.Fetcher.Root != "" {
		checkouts = append(checkouts, b.Fetcher.Root)
	}
	if err := checkWritable(entry.Skill.Dir, src.Path, checkouts); err != nil {
		return fmt.Errorf("skill %q: %w", id, err)
	}
	folder := filepath.Base(entry.Skill.Dir)
	if err := CheckSkillMD(folder, content); err != nil {
		return err
	}
	path := filepath.Join(entry.Skill.Dir, "SKILL.md")
	perm := os.FileMode(0o644)
	if fi, err := os.Lstat(path); err == nil && fi.Mode().IsRegular() {
		perm = fi.Mode().Perm()
	}
	return fsutil.WriteFileAtomic(path, []byte(content), perm)
}

// checkWritable refuses writing dir/SKILL.md when dir resolves (through symlinks) outside the
// source folder root, when root lies in one of skilltri's checkout folders (a local source over
// a git or marketplace checkout, replaced on update) or when SKILL.md is a symlink.
func checkWritable(dir, root string, checkouts []string) error {
	root = expandHome(root)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if !within(realDir, realRoot) {
		return fmt.Errorf("its folder resolves to %s, outside the source folder %s", realDir, root)
	}
	for _, c := range checkouts {
		if rc, err := filepath.EvalSymlinks(c); err == nil {
			c = rc
		}
		if within(realRoot, filepath.Clean(c)) {
			return fmt.Errorf("the source folder %s is inside skilltri's checkouts (%s), which are replaced on update", root, c)
		}
	}
	if fi, err := os.Lstat(filepath.Join(dir, "SKILL.md")); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("its SKILL.md is a symlink: edit the file it points to yourself")
	}
	return nil
}

// within reports whether p is root or below it (both clean, real paths).
func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
		}
	}
	return p
}

// CheckSkillMD parses content as the SKILL.md of a folder named folder in a temp folder and fails
// on any spec problem (missing frontmatter, a name other than folder, a missing description …).
func CheckSkillMD(folder, content string) error {
	tmp, err := os.MkdirTemp("", "skilltri-skill-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dir := filepath.Join(tmp, folder)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		return err
	}
	s, err := skills.Parse(dir)
	if err != nil {
		return err
	}
	if len(s.Warnings) > 0 {
		return fmt.Errorf("invalid SKILL.md: %s", strings.Join(s.Warnings, "; "))
	}
	return nil
}

func findSkill(c *catalog.Catalog, id string) (catalog.Entry, bool) {
	for _, e := range c.Skills {
		if e.ID == id {
			return e, true
		}
	}
	return catalog.Entry{}, false
}
