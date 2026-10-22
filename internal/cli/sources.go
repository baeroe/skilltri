package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/paths"
)

// editConfig applies fn to the config under the config lock and then refreshes the catalog.
func editConfig(fn func(*config.Config) error) error {
	if err := config.Update(paths.ConfigFile(), fn); err != nil {
		return err
	}
	refreshCatalog()
	return nil
}

// refreshCatalog rebuilds and saves the catalog offline (no git) when no daemon runs; a running
// daemon watches the config and rebuilds on its own. Failures are reported but not fatal.
func refreshCatalog() {
	if err := daemon.Control(paths.SocketPath(), daemon.Hello{Cmd: "status"}, nil); !notRunning(err) {
		return
	}
	cfg, err := config.Load(paths.ConfigFile())
	if err != nil {
		return
	}
	if err := catalog.Save(catalog.Path(), offlineCatalog(cfg)); err != nil {
		fmt.Fprintf(errOut, "skilltri: save catalog: %v\n", err)
	}
}

// offlineCatalog builds the catalog of cfg from the existing checkouts without running git.
func offlineCatalog(cfg *config.Config) *catalog.Catalog {
	c, _ := daemon.DefaultBuilder().Build(context.Background(), cfg, false)
	return c
}

func fail(err error) int {
	fmt.Fprintf(errOut, "skilltri: %v\n", err)
	return 1
}

// SourceAdd adds the source name. A git or marketplace source is not fetched here.
func SourceAdd(w io.Writer, name string, src config.Source) int {
	if !config.ValidServerName(name) {
		return fail(fmt.Errorf("invalid source name %q (use letters, digits and '-', max 32 characters)", name))
	}
	err := editConfig(func(c *config.Config) error {
		if c.Sources[name] != nil {
			return fmt.Errorf("source %q already exists", name)
		}
		if c.Sources == nil {
			c.Sources = map[string]*config.Source{}
		}
		c.Sources[name] = &src
		return nil
	})
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(w, "added source %s\n", name)
	if src.Type != "local" && src.Path == "" {
		fmt.Fprintf(w, "run \"skilltri update %s\" to fetch now\n", name)
	}
	return 0
}

// SourceRemove removes the source name from the config (checkouts stay on disk).
func SourceRemove(w io.Writer, name string) int {
	err := editConfig(func(c *config.Config) error {
		if c.Sources[name] == nil {
			return fmt.Errorf("no such source %q", name)
		}
		delete(c.Sources, name)
		return nil
	})
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(w, "removed source %s\n", name)
	return 0
}

// SourceRow is one line of `source list`.
type SourceRow struct {
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Location  string    `json:"location"` // URL or path
	Ref       string    `json:"ref,omitempty"`
	Disabled  bool      `json:"disabled,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	FetchedAt time.Time `json:"fetchedAt,omitzero"`
	Error     string    `json:"error,omitempty"`
	Skills    int       `json:"skills"`
}

// SourceList prints every source with its fetch state and skill count.
func SourceList(w io.Writer, jsonOut bool) int {
	cfg, err := config.Load(paths.ConfigFile())
	if err != nil {
		return fail(err)
	}
	c := offlineCatalog(cfg)
	states := map[string]catalog.SourceState{}
	for _, s := range c.Sources {
		states[s.Name] = s
	}
	count := map[string]int{}
	for _, e := range c.Skills {
		count[e.Source]++
	}
	rows := []SourceRow{}
	for _, name := range sortedNames(cfg.Sources) {
		s := cfg.Sources[name]
		st := states[name]
		row := SourceRow{Name: name, Type: s.Type, Location: s.URL, Ref: s.Ref, Disabled: s.Disabled,
			Commit: st.Commit, FetchedAt: st.FetchedAt, Error: st.Error, Skills: count[name]}
		if row.Location == "" {
			row.Location = s.Path
		}
		rows = append(rows, row)
	}
	if jsonOut {
		printJSON(w, rows)
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no sources")
		return 0
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, r := range rows {
		commit, at := dash(r.Commit), "-"
		if len(commit) > 7 {
			commit = commit[:7]
		}
		if !r.FetchedAt.IsZero() {
			at = r.FetchedAt.Local().Format(time.DateTime)
		}
		name := r.Name
		if r.Disabled {
			name += " (disabled)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d skills\t%s\n", name, r.Type, r.Location, dash(r.Ref), commit, at, r.Skills, r.Error)
	}
	tw.Flush()
	return 0
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// SkillNew creates ConfigDir/skills/name/SKILL.md and makes sure the "local" source points at
// ConfigDir/skills.
func SkillNew(w io.Writer, name string) int {
	if len(name) > 64 || !skillName.MatchString(name) {
		return fail(fmt.Errorf("invalid skill name %q (lowercase letters, digits and '-', max 64 characters)", name))
	}
	root := filepath.Join(paths.ConfigDir(), "skills")
	dir := filepath.Join(root, name)
	file := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(dir); err == nil {
		return fail(fmt.Errorf("%s already exists", dir))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail(err)
	}
	body := "---\nname: " + name + "\ndescription: TODO\n---\n\n# " + name + "\n\nTODO: describe when and how to use this skill.\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		return fail(err)
	}
	err := editConfig(func(c *config.Config) error {
		if c.Sources["local"] == nil {
			if c.Sources == nil {
				c.Sources = map[string]*config.Source{}
			}
			c.Sources["local"] = &config.Source{Type: "local", Path: root}
		}
		return nil
	})
	if err != nil {
		return fail(err)
	}
	fmt.Fprintln(w, file)
	return 0
}

// LocalPath turns the argument of `source add local` into a stored path: ~ stays, others become absolute.
func LocalPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return p
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
