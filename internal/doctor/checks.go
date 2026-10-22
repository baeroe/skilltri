package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/baeroe/skilltri/internal/adapters"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/gateway"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/skilllock"
)

// daemonTimeout bounds the status request, dial and reply, so a hung socket cannot stall doctor.
const daemonTimeout = time.Second

// oneMCPEndpoint is the local 1MCP gateway skilltri replaces.
const oneMCPEndpoint = "http://127.0.0.1:3050/mcp"

func checkConfig(_ context.Context, s *state) []Finding {
	err := s.cfgErr
	if err == nil {
		err = s.cfg.Validate()
	}
	if err == nil {
		return nil
	}
	return []Finding{{Severity: Error, Check: "config", Subject: s.env.ConfigPath, Message: err.Error(),
		Fix: "fix the file by hand; the daemon keeps its last valid config until then"}}
}

func checkDaemon(_ context.Context, s *state) []Finding {
	f := Finding{Severity: Warning, Check: "daemon", Subject: "daemon"}
	if _, err := os.Stat(s.env.Socket); err != nil {
		f.Message, f.Fix = "daemon not running", "skilltri daemon install (or start a harness session: skilltri mcp starts it)"
		return []Finding{f}
	}
	var reply daemon.StatusReply
	err := daemon.ControlTimeout(s.env.Socket, daemon.Hello{Cmd: "status"}, &reply, daemonTimeout)
	switch {
	case errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED):
		f.Message, f.Fix = "daemon not running (stale socket)", "skilltri daemon install (or start a harness session: skilltri mcp starts it)"
	case err != nil:
		f.Message, f.Fix = "daemon not responding: "+err.Error(), "skilltri daemon stop, then check the daemon log"
	case reply.Version != gateway.Version:
		f.Message = fmt.Sprintf("daemon runs version %s, this binary is %s", reply.Version, gateway.Version)
		f.Fix = "skilltri daemon stop (the next session starts the new version)"
	default:
		return nil
	}
	return []Finding{f}
}

// sourceOf returns the config source of a catalog source state ("<source>/plugins/<entry>" → source).
func sourceOf(key string) string {
	if i := strings.Index(key, "/plugins/"); i >= 0 {
		return key[:i]
	}
	return key
}

func checkSources(_ context.Context, s *state) []Finding {
	if s.cat == nil {
		return nil
	}
	var out []Finding
	for _, st := range s.cat.Sources {
		name := sourceOf(st.Name)
		src := s.cfg.Sources[name]
		fetchedKind := name != st.Name || (src != nil && (src.Type == "git" || (src.Type == "marketplace" && src.Path == "")))
		fix := "skilltri update " + name
		switch {
		case st.Error == "not fetched yet" || (fetchedKind && st.Error == "" && st.FetchedAt.IsZero()):
			out = append(out, Finding{Severity: Warning, Check: "sources", Subject: st.Name, Message: "never fetched", Fix: fix})
		case st.Error != "":
			if !fetchedKind {
				fix = "check the source's path (skilltri source list)"
			}
			out = append(out, Finding{Severity: Error, Check: "sources", Subject: st.Name, Message: st.Error, Fix: fix})
		}
	}
	return out
}

func checkSkills(_ context.Context, s *state) []Finding {
	if s.catErr != nil {
		switch {
		case errors.Is(s.catErr, os.ErrNotExist) && len(s.cfg.Sources) > 0:
			return []Finding{{Severity: Info, Check: "skills", Subject: "catalog", Message: "no skill catalog built yet", Fix: "skilltri update"}}
		case !errors.Is(s.catErr, os.ErrNotExist):
			return []Finding{{Severity: Warning, Check: "skills", Subject: "catalog", Message: s.catErr.Error(), Fix: "skilltri update (rebuilds the catalog)"}}
		}
		return nil
	}
	var out []Finding
	for _, w := range s.cat.Warnings {
		subject := "catalog"
		if name, _, ok := strings.Cut(w, ": "); ok && config.ValidServerName(name) { // "<source>: …"
			subject = name
		}
		out = append(out, Finding{Severity: Warning, Check: "skills", Subject: subject, Message: w})
	}
	return out
}

func checkLock(_ context.Context, s *state) []Finding {
	if s.cat == nil {
		return nil
	}
	lock, err := skilllock.Load(s.env.StatePath)
	if err != nil {
		return []Finding{{Severity: Warning, Check: "lock", Subject: filepath.Base(s.env.StatePath), Message: err.Error(),
			Fix: "skilltri update (rewrites the lock)"}}
	}
	var out []Finding
	for _, e := range s.cat.Skills {
		src := s.cfg.Sources[e.Source]
		if src == nil || (src.Type != "git" && src.Type != "marketplace") {
			continue
		}
		keys := []string{e.Source}
		if e.Plugin != "" {
			keys = []string{e.Source + "/plugins/" + e.Plugin, e.Source}
		}
		for _, k := range keys {
			want, ok := lock.Sources[k].Skills[e.ID]
			if !ok {
				continue
			}
			if got, err := skilllock.HashDir(e.Skill.Dir); err == nil && got != want {
				out = append(out, Finding{Severity: Warning, Check: "lock", Subject: e.ID,
					Message: "modified checkout: " + e.Skill.Dir + " differs from the fetched version",
					Fix:     "local edits are overwritten by the next update; move them into a local source"})
			}
			break
		}
	}
	return out
}

func checkViews(_ context.Context, s *state) []Finding {
	root := filepath.Join(s.env.CacheDir, "views")
	links, _ := filepath.Glob(filepath.Join(root, "*", "claude", "*", "skills", "*"))
	var out []Finding
	for _, l := range links {
		rel, _ := filepath.Rel(root, l)
		if strings.HasPrefix(rel, ".") { // a builder's temp dir
			continue
		}
		st, err := os.Lstat(l)
		if err != nil || st.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if _, err := os.Stat(l); err != nil {
			target, _ := os.Readlink(l)
			out = append(out, Finding{Severity: Error, Check: "views", Subject: filepath.ToSlash(rel),
				Message: "broken skill link to " + target,
				Fix:     "open a new shell or cd . to build a fresh view; stale views are garbage-collected"})
		}
	}
	return out
}

func checkProjectDirs(_ context.Context, s *state) []Finding {
	if s.dir == "" {
		return nil
	}
	var out []Finding
	seen := map[string]bool{}
	for _, a := range adapters.Registry {
		rel := a.ProjectDir
		if a.Method != adapters.MethodProjectDir || seen[rel] {
			continue
		}
		seen[rel] = true
		links, err := adapters.OwnedLinks(s.dir, rel)
		if err != nil {
			out = append(out, Finding{Severity: Warning, Check: "project-dirs", Subject: rel, Message: err.Error(),
				Fix: "fix or delete " + filepath.Join(s.dir, rel, ".skilltri.json")})
			continue
		}
		names := make([]string, 0, len(links))
		for n := range links {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if _, err := os.Stat(filepath.Join(s.dir, rel, n)); err != nil {
				out = append(out, Finding{Severity: Warning, Check: "project-dirs", Subject: rel + "/" + n,
					Message: "broken skill link to " + links[n],
					Fix:     "cd . to resync the folder (links of removed skills are deleted), or delete the link"})
			}
		}
	}
	return out
}

// invalidSourceChars matches what the Claude import replaces in marketplace names.
var invalidSourceChars = regexp.MustCompile(`[^A-Za-z0-9-]`)

func checkDuplicates(_ context.Context, s *state) []Finding {
	var out []Finding
	if s.cat != nil {
		active := map[string]bool{}
		for _, e := range catalog.Active(s.cat, s.cfg, rules.Normalize(s.dir)) {
			if e.Plugin == "" {
				active[e.ID] = true
			}
		}
		dir := filepath.Join(s.env.Home, ".claude", "skills")
		entries, _ := os.ReadDir(dir)
		for _, d := range entries {
			if n := d.Name(); n != "synced" && active[n] {
				out = append(out, Finding{Severity: Warning, Check: "duplicates", Subject: n,
					Message: "Claude Code loads " + filepath.Join(dir, n) + " itself, and skilltri delivers a skill of the same name",
					Fix:     "remove " + filepath.Join(dir, n)})
			}
		}
	}
	var settings struct {
		Enabled map[string]bool `json:"enabledPlugins"`
	}
	if b, err := os.ReadFile(filepath.Join(s.env.Home, ".claude", "settings.json")); err == nil && json.Unmarshal(b, &settings) == nil {
		for key, on := range settings.Enabled {
			i := strings.LastIndex(key, "@")
			if !on || i < 0 {
				continue
			}
			plugin, mk := key[:i], invalidSourceChars.ReplaceAllString(key[i+1:], "-")
			src := s.cfg.Sources[mk]
			if src == nil || src.Type != "marketplace" || src.Disabled {
				continue
			}
			if !src.PluginEnabled(plugin) {
				continue
			}
			out = append(out, Finding{Severity: Warning, Check: "duplicates", Subject: key,
				Message: "Claude Code has plugin " + key + " enabled itself, and skilltri delivers it from source " + mk,
				Fix:     "claude plugin uninstall " + key})
		}
	}
	return out
}

func checkPluginServers(_ context.Context, s *state) []Finding {
	if s.cat == nil {
		return nil
	}
	var out []Finding
	for name, st := range catalog.ReviewState(s.cfg, s.cat) {
		switch st {
		case "needs-review":
			out = append(out, Finding{Severity: Warning, Check: "plugin-servers", Subject: name,
				Message: "the plugin changed this server's definition", Fix: "skilltri plugin-server accept " + name})
		case "gone":
			out = append(out, Finding{Severity: Warning, Check: "plugin-servers", Subject: name,
				Message: "the plugin no longer defines this server", Fix: "skilltri disable server/" + name + " or delete it from the config"})
		}
	}
	return out
}

// mcpEntry is one server entry in ~/.claude.json.
type mcpEntry struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
	URL     string   `json:"url"`
}

func (m mcpEntry) isSkilltri() bool {
	return filepath.Base(m.Command) == "skilltri" && len(m.Args) > 0 && m.Args[0] == "mcp"
}

func (m mcpEntry) is1MCP() bool {
	if m.URL == oneMCPEndpoint {
		return true
	}
	return slices.ContainsFunc(append([]string{m.Command}, m.Args...), func(a string) bool {
		return strings.Contains(a, "1mcp") || strings.Contains(a, strings.TrimPrefix(oneMCPEndpoint, "http://"))
	})
}

func checkHarness(_ context.Context, s *state) []Finding {
	path := filepath.Join(s.env.Home, ".claude.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil // Claude Code not set up (or unreadable): nothing to check
	}
	var file struct {
		MCPServers map[string]mcpEntry `json:"mcpServers"`
		Projects   map[string]struct {
			MCPServers map[string]mcpEntry `json:"mcpServers"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		return []Finding{{Severity: Info, Check: "harness", Subject: ".claude.json", Message: "cannot read Claude Code's MCP servers: " + err.Error()}}
	}
	var out []Finding
	registered := false
	scan := func(project string, servers map[string]mcpEntry) {
		for name, m := range servers {
			registered = registered || m.isSkilltri()
			if !m.is1MCP() {
				continue
			}
			f := Finding{Severity: Warning, Check: "harness", Subject: name,
				Message: "1MCP still registered in " + path + ": skilltri replaces it",
				Fix:     "claude mcp remove --scope user " + name}
			if project != "" {
				f.Subject = project + ": " + name
				f.Fix = "claude mcp remove --scope local " + name + " (run in " + project + ")"
			}
			out = append(out, f)
		}
	}
	scan("", file.MCPServers)
	for p, pr := range file.Projects {
		scan(p, pr.MCPServers)
	}
	if !registered {
		out = append(out, Finding{Severity: Info, Check: "harness", Subject: "skilltri",
			Message: "Claude Code has no MCP server running skilltri mcp",
			Fix:     "claude mcp add --scope user skilltri -- skilltri mcp"})
	}
	return out
}

func checkShellHook(_ context.Context, s *state) []Finding {
	if s.vars["SKILLTRI_HOOK"] != "" {
		return nil
	}
	fix := `add eval "$(skilltri shell-init zsh)" to ~/.zshrc`
	switch filepath.Base(s.vars["SHELL"]) {
	case "bash":
		fix = `add eval "$(skilltri shell-init bash)" to ~/.bashrc`
	case "fish":
		fix = "add skilltri shell-init fish | source to ~/.config/fish/config.fish"
	}
	return []Finding{{Severity: Info, Check: "shell-hook", Subject: "SKILLTRI_HOOK",
		Message: "the shell hook is not loaded: harnesses started from this shell get no folder skills", Fix: fix}}
}

func checkAdapters(_ context.Context, s *state) []Finding {
	enabled := s.cfg.EnabledAdapters()
	var out []Finding
	for _, a := range adapters.Detected(s.env.Home) {
		if !slices.Contains(enabled, a.ID) {
			out = append(out, Finding{Severity: Info, Check: "adapters", Subject: a.ID,
				Message: a.Display + " is installed but gets no skills from skilltri", Fix: "skilltri adapter enable " + a.ID})
		}
	}
	return out
}
