package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/paths"
)

var actEnv actions.Env

// RefreshCatalog lets other packages refresh the catalog after editing the config. A failure is
// reported but not fatal.
func RefreshCatalog() {
	if err := actEnv.RefreshCatalog(); err != nil {
		fmt.Fprintf(errOut, "skilltri: %v\n", err)
	}
}

// offlineCatalog builds the catalog of cfg from the existing checkouts without running git.
func offlineCatalog(cfg *config.Config) *catalog.Catalog { return actEnv.OfflineCatalog(cfg) }

func fail(err error) int {
	fmt.Fprintf(errOut, "skilltri: %v\n", err)
	return 1
}

// SourceAdd adds the source name. A git or marketplace source is not fetched here.
func SourceAdd(w io.Writer, name string, src config.Source) int {
	if err := actions.SourceAdd(actEnv, name, src); err != nil {
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
	if err := actions.SourceRemove(actEnv, name); err != nil {
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

// SkillNew creates ConfigDir/skills/name/SKILL.md and makes sure the "local" source points at
// ConfigDir/skills.
func SkillNew(w io.Writer, name string) int {
	dir, err := actions.SkillNew(actEnv, name)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintln(w, filepath.Join(dir, "SKILL.md"))
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
