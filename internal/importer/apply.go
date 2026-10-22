package importer

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/secrets"
)

// Applied1MCP is what Apply1MCP did (or, from Preview1MCP, would do).
type Applied1MCP struct {
	Imported []string // server names written, sorted
	Replaced []string // of them, the ones that overwrite an existing server (force)
	Warnings []string // the result's warnings plus the skipped servers
}

// plan1MCP merges res into c: existing servers are skipped unless force is set.
func plan1MCP(c *config.Config, res Result, force bool) Applied1MCP {
	a := Applied1MCP{Warnings: append([]string(nil), res.Warnings...)}
	if c.Servers == nil {
		c.Servers = map[string]*config.Server{}
	}
	names := make([]string, 0, len(res.Servers))
	for n := range res.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, exists := c.Servers[n]; exists {
			if !force {
				a.Warnings = append(a.Warnings, fmt.Sprintf("skipped %q: already exists (use --force to overwrite)", n))
				continue
			}
			a.Replaced = append(a.Replaced, n)
		}
		c.Servers[n] = res.Servers[n]
		a.Imported = append(a.Imported, n)
	}
	return a
}

// Preview1MCP is what Apply1MCP would do to the config c (which it does not change).
func Preview1MCP(c *config.Config, res Result, force bool) Applied1MCP {
	tmp := &config.Config{Servers: maps.Clone(c.Servers)}
	return plan1MCP(tmp, res, force)
}

// Apply1MCP merges res into the config at cfgPath. Existing servers are skipped unless force is
// set; only the secrets of servers that were actually imported are stored. On an error the
// warnings are still returned.
func Apply1MCP(cfgPath string, res Result, force bool) (Applied1MCP, error) {
	a := Applied1MCP{Warnings: res.Warnings}
	err := config.Update(cfgPath, func(c *config.Config) error {
		a = plan1MCP(c, res, force)
		for _, n := range a.Imported {
			for _, name := range res.SecretsByServer[n] {
				if err := secrets.Set(name, res.Secrets[name]); err != nil {
					return fmt.Errorf("store secret %s: %w", name, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		a.Imported, a.Replaced = nil, nil
	}
	return a, err
}

// ClaudeReport is what ImportClaude did (or, from PreviewClaude, would do).
type ClaudeReport struct {
	Sources        []string // imported source names, sorted
	Replaced       []string // of them, the ones that overwrite an existing source (force)
	LocalAdded     bool     // the "local" source for the copied skills was added
	Skills         []string // copied user skill folder names
	ReplacedSkills []string // of them, the ones that overwrite an existing folder (force)
	Warnings       []string
	Steps          []string // manual follow-ups (uninstall the plugins, remove the copied skills, …)
}

// planClaudeSources merges the sources of r into c, recording it in rep.
func planClaudeSources(c *config.Config, skillsDir string, r ClaudeResult, force bool, rep *ClaudeReport) {
	names := make([]string, 0, len(r.Sources))
	for n := range r.Sources {
		names = append(names, n)
	}
	sort.Strings(names)
	if c.Sources == nil {
		c.Sources = map[string]*config.Source{}
	}
	for _, n := range names {
		if c.Sources[n] != nil {
			if !force {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("skipped source %q: already exists (use --force to overwrite)", n))
				continue
			}
			rep.Replaced = append(rep.Replaced, n)
		}
		c.Sources[n] = r.Sources[n]
		rep.Sources = append(rep.Sources, n)
		for _, p := range r.Uninstall[n] {
			rep.Steps = append(rep.Steps, p.Step())
		}
	}
	if len(r.UserSkills) > 0 {
		switch l := c.Sources["local"]; {
		case l == nil:
			c.Sources["local"] = &config.Source{Type: "local", Path: skillsDir}
			rep.LocalAdded = true
		case l.Type != "local" || l.Path != skillsDir:
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("source \"local\" does not point at %s: the copied skills are in no source", skillsDir))
		}
	}
}

// claudeSkill is a user skill to copy.
type claudeSkill struct{ dir, name, target string }

// planClaudeSkills decides which user skills of r are copied into skillsDir (existing folders only
// with force), recording the skipped ones in rep.
func planClaudeSkills(skillsDir string, r ClaudeResult, force bool, rep *ClaudeReport) []claudeSkill {
	var out []claudeSkill
	for _, dir := range r.UserSkills {
		name := filepath.Base(dir)
		target := filepath.Join(skillsDir, name)
		if _, err := os.Lstat(target); err == nil {
			if !force {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("skipped skill %q: already exists (use --force to overwrite)", name))
				continue
			}
			rep.ReplacedSkills = append(rep.ReplacedSkills, name)
		}
		out = append(out, claudeSkill{dir, name, target})
	}
	return out
}

// skillSteps adds the copied skills' remove steps and r's static steps to rep.
func skillSteps(r ClaudeResult, rep *ClaudeReport) {
	for _, n := range rep.Skills {
		rep.Steps = append(rep.Steps, "remove "+filepath.Join(r.Home, ".claude", "skills", n))
	}
	rep.Steps = append(rep.Steps, r.Steps...)
}

// PreviewClaude is what ImportClaude would do with the config c (which it does not change) and the
// skill folders in skillsDir (only looked at).
func PreviewClaude(c *config.Config, skillsDir string, r ClaudeResult, force bool) ClaudeReport {
	rep := ClaudeReport{Warnings: append([]string(nil), r.Warnings...)}
	planClaudeSources(&config.Config{Sources: maps.Clone(c.Sources)}, skillsDir, r, force, &rep)
	for _, s := range planClaudeSkills(skillsDir, r, force, &rep) {
		rep.Skills = append(rep.Skills, s.name)
	}
	skillSteps(r, &rep)
	return rep
}

// ImportClaude writes the sources of r into the config at cfgPath and copies the user skills into
// skillsDir (ensuring a "local" source for it). Existing sources and skill folders are skipped
// unless force is set. It never runs the steps.
func ImportClaude(cfgPath, skillsDir string, r ClaudeResult, force bool) (ClaudeReport, error) {
	var rep ClaudeReport
	err := config.Update(cfgPath, func(c *config.Config) error {
		rep = ClaudeReport{Warnings: append([]string(nil), r.Warnings...)}
		planClaudeSources(c, skillsDir, r, force, &rep)
		return nil
	})
	if err != nil {
		return ClaudeReport{}, err
	}
	for _, s := range planClaudeSkills(skillsDir, r, force, &rep) {
		if err := installTree(s.dir, s.target); err != nil {
			skillSteps(r, &rep)
			return rep, fmt.Errorf("copy skill %s: %w", s.name, err)
		}
		rep.Skills = append(rep.Skills, s.name)
	}
	skillSteps(r, &rep)
	return rep, nil
}
