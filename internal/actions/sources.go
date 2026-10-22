package actions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/baeroe/skilltri/internal/config"
)

// SourceAdd adds the source name. A git or marketplace source is not fetched here.
func SourceAdd(e Env, name string, s config.Source) error {
	if !config.ValidServerName(name) {
		return fmt.Errorf("invalid source name %q (use letters, digits and '-', max 32 characters)", name)
	}
	return e.edit(func(c *config.Config) error {
		if c.Sources[name] != nil {
			return wrap(ErrExists, "source %q already exists", name)
		}
		if c.Sources == nil {
			c.Sources = map[string]*config.Source{}
		}
		c.Sources[name] = &s
		return nil
	})
}

// SourceUpdate replaces the settings of the source name with s but keeps Disabled (SetDisabled
// changes it) and, when s has them nil, Rules, Plugins and Skills of the existing entry. A missing
// source wraps ErrNotFound.
func SourceUpdate(e Env, name string, s config.Source) error {
	return e.edit(func(c *config.Config) error {
		old := c.Sources[name]
		if old == nil {
			return wrap(ErrNotFound, "no such source %q", name)
		}
		s.Disabled = old.Disabled
		if s.Rules == nil {
			s.Rules = old.Rules
		}
		if s.Plugins == nil {
			s.Plugins = old.Plugins
		}
		if s.Skills == nil {
			s.Skills = old.Skills
		}
		c.Sources[name] = &s
		return nil
	})
}

// SourceRemove removes the source name from the config (checkouts stay on disk). A missing source
// wraps ErrNotFound.
func SourceRemove(e Env, name string) error {
	return e.edit(func(c *config.Config) error {
		if c.Sources[name] == nil {
			return wrap(ErrNotFound, "no such source %q", name)
		}
		delete(c.Sources, name)
		return nil
	})
}

var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// SkillNew creates <config dir>/skills/name/SKILL.md, makes sure the "local" source points at
// <config dir>/skills and returns the skill's folder. An existing folder wraps ErrExists.
func SkillNew(e Env, name string) (dir string, err error) {
	if len(name) > 64 || !skillName.MatchString(name) {
		return "", fmt.Errorf("invalid skill name %q (lowercase letters, digits and '-', max 64 characters)", name)
	}
	root := filepath.Join(e.configDir(), "skills")
	dir = filepath.Join(root, name)
	if _, err := os.Stat(dir); err == nil {
		return "", wrap(ErrExists, "%s already exists", dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	body := "---\nname: " + name + "\ndescription: TODO\n---\n\n# " + name + "\n\nTODO: describe when and how to use this skill.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		return "", err
	}
	err = e.edit(func(c *config.Config) error {
		if c.Sources["local"] == nil {
			if c.Sources == nil {
				c.Sources = map[string]*config.Source{}
			}
			c.Sources["local"] = &config.Source{Type: "local", Path: root}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return dir, nil
}
