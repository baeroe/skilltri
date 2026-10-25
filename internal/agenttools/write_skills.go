package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/fsutil"
	"github.com/baeroe/skilltri/internal/gateway"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

// catalogNote ends the description of the tools that rebuild the skill catalog.
const catalogNote = " Running sessions get the rebuilt skill catalog at once; the call is recorded in skilltri's audit log."

func init() {
	register(
		writeTool(&mcp.Tool{
			Name:        "skilltri_add_source",
			Description: "Add a skill source: local (a folder of skill folders: path), git (a repository: url as owner/repo or a git URL, optional ref, subpath, updateEvery) or marketplace (a Claude Code plugin marketplace: url or path, optional ref, updateEvery, plugins enabled|disabled). git and marketplace sources are fetched by skilltri_update_sources or the next scheduled update. A relative path is taken below this session's folder.",
			InputSchema: required(object(map[string]any{
				"name":        str("Source name (letters, digits and '-', at most 32 characters)"),
				"type":        map[string]any{"type": "string", "enum": []string{"local", "git", "marketplace"}, "description": "local, git or marketplace"},
				"path":        str("Folder of a local source or of a marketplace on disk"),
				"url":         str("owner/repo or a git URL (git, marketplace)"),
				"ref":         str("Branch, tag or 40-hex commit (git, marketplace)"),
				"subpath":     str("Folder inside the repository that holds the skills (git)"),
				"updateEvery": str("Update interval as a Go duration such as 1h (default 1h); \"0\" updates only on request (git, marketplace)"),
				"plugins":     map[string]any{"type": "string", "enum": []string{"enabled", "disabled"}, "description": "marketplace: whether plugins are on unless set otherwise (disabled: only plugins you enable are fetched)"},
			}), "name", "type"),
		}, addSource),
		writeTool(&mcp.Tool{
			Name:        "skilltri_remove_source",
			Description: "Remove a skill source from skilltri's config (with its rules and plugin and skill settings); its skills disappear from every session. Checkouts stay on disk.",
			InputSchema: required(object(map[string]any{"name": str("Source name")}), "name"),
		}, removeSource),
		writeToolNote(&mcp.Tool{
			Name:        "skilltri_update_sources",
			Description: "Fetch skill sources now (git pull of git and marketplace sources; the named ones, else every enabled one) and rebuild the skill catalog; returns each source's commit or error. The config is not changed.",
			InputSchema: object(map[string]any{"sources": strList("Source names (default: every enabled source)")}),
		}, catalogNote, updateSources),
		writeToolNote(&mcp.Tool{
			Name:        "skilltri_new_skill",
			Description: "Create a skill in skilltri's own skills folder (the local source \"local\", added when missing): writes <name>/SKILL.md with name and description as frontmatter followed by body.",
			InputSchema: required(object(map[string]any{
				"name":        str("Skill name: lowercase letters, digits and single '-', at most 64 characters"),
				"description": str("What the skill does and when to use it (at most 1024 characters); agents pick skills by it"),
				"body":        str("The skill's instructions in Markdown (after the frontmatter)"),
			}), "name", "description", "body"),
		}, catalogNote, newSkill),
		writeToolNote(&mcp.Tool{
			Name:        "skilltri_write_skill",
			Description: "Replace the whole SKILL.md of a skill of a local source (read it first with skilltri_read_skill). content must start with frontmatter whose name equals the skill's folder and that has a description. Skills of git and marketplace sources are refused: they belong to their upstream and are replaced on update.",
			InputSchema: required(object(map[string]any{
				"id":      str("Skill id as skilltri_list_skills shows it"),
				"content": str("The complete new SKILL.md (frontmatter and body)"),
			}), "id", "content"),
		}, catalogNote, writeSkill),
	)
	register(Tool{Def: &mcp.Tool{
		Name:        "skilltri_read_skill",
		Description: "Read the SKILL.md of any catalog skill, active in this session or not, and list its other files (max 256 KiB)." + readOnly,
		InputSchema: required(object(map[string]any{"id": str("Skill id as skilltri_list_skills shows it")}), "id"),
	}, Handle: readSkill})
}

type sourceArgs struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Path        string `json:"path"`
	URL         string `json:"url"`
	Ref         string `json:"ref"`
	Subpath     string `json:"subpath"`
	UpdateEvery string `json:"updateEvery"`
	Plugins     string `json:"plugins"`
}

// source checks the fields for the source type, like `skilltri source add` does.
func (a sourceArgs) source(c Call) (config.Source, error) {
	s := config.Source{Type: a.Type, URL: a.URL, Ref: a.Ref, Subpath: a.Subpath, UpdateEvery: a.UpdateEvery}
	if a.Path != "" {
		s.Path = a.Path
		if s.Path != "~" && !strings.HasPrefix(s.Path, "~/") && !filepath.IsAbs(s.Path) {
			s.Path = filepath.Join(c.Dir, s.Path)
		}
	}
	if a.Plugins != "" && (a.Type != "marketplace" || (a.Plugins != "enabled" && a.Plugins != "disabled")) {
		return s, errors.New("plugins is enabled or disabled, for marketplace sources only")
	}
	switch a.Type {
	case "local":
		if s.Path == "" {
			return s, errors.New("path is required for a local source")
		}
		if a.URL != "" || a.Ref != "" || a.Subpath != "" || a.UpdateEvery != "" {
			return s, errors.New("a local source takes only path")
		}
	case "git":
		if a.URL == "" {
			return s, errors.New("url is required for a git source")
		}
		if a.Path != "" {
			return s, errors.New("a git source takes url, not path")
		}
	case "marketplace":
		if (a.URL == "") == (a.Path == "") {
			return s, errors.New("a marketplace source takes url or path")
		}
		if a.Subpath != "" {
			return s, errors.New("a marketplace source takes no subpath")
		}
		if a.Plugins == "disabled" {
			s.PluginsDefault = config.PluginsDisabled
		}
	default:
		return s, fmt.Errorf("type %q: want local, git or marketplace", a.Type)
	}
	if _, err := s.Interval(); err != nil {
		return s, fmt.Errorf("invalid updateEvery %q (want a Go duration such as 1h, or \"0\")", a.UpdateEvery)
	}
	return s, nil
}

func addSource(_ context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	var a sourceArgs
	if err := parseArgs(args, &a); err != nil {
		return err
	}
	if a.Name == "" {
		return errors.New("name is required")
	}
	ch.target = "source/" + a.Name
	if isURL(a.URL) {
		ch.secrets = urlLiterals(a.URL)
	}
	where := a.Path
	if a.URL != "" {
		where = maskSourceURL(a.URL)
	}
	ch.summary = strings.TrimSpace("add " + a.Type + " " + where)
	s, err := a.source(c)
	if err != nil {
		return err
	}
	if err := actions.SourceAdd(callEnv(b, c), a.Name, s); err != nil {
		return err
	}
	ch.message = fmt.Sprintf("Added the %s source %s.%s", a.Type, a.Name, applied)
	if s.Type != "local" && s.Path == "" {
		ch.message += " Run skilltri_update_sources to fetch it now."
	}
	return nil
}

func removeSource(_ context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	var a struct {
		Name string `json:"name"`
	}
	if err := parseArgs(args, &a); err != nil {
		return err
	}
	if a.Name == "" {
		return errors.New("name is required")
	}
	ch.target, ch.summary = "source/"+a.Name, "remove"
	if err := actions.SourceRemove(callEnv(b, c), a.Name); err != nil {
		return err
	}
	ch.message = fmt.Sprintf("Removed the source %s (its checkout stays on disk).%s", a.Name, applied)
	return nil
}

func updateSources(ctx context.Context, b Backend, _ Call, args json.RawMessage, ch *change) error {
	var a struct {
		Sources []string `json:"sources"`
	}
	if err := parseArgs(args, &a); err != nil {
		return err
	}
	names := slices.Clone(a.Sources)
	switch len(names) {
	case 0:
		ch.target, ch.summary = "sources", "update all enabled sources"
	case 1:
		ch.target, ch.summary = "source/"+names[0], "update "+names[0]
	default:
		sorted := slices.Sorted(slices.Values(names))
		ch.target, ch.summary = "sources", "update "+strings.Join(sorted, ", ")
	}
	cfg := b.Config()
	for _, n := range names {
		if cfg.Sources[n] == nil {
			return fmt.Errorf("no such source %q", n)
		}
	}
	states, err := b.Update(ctx, names)
	if err != nil {
		return errors.New(maskText(err.Error(), nil))
	}
	if len(states) == 0 {
		ch.message = "No sources to update."
		return nil
	}
	lines := []string{"Updated the skill sources; running sessions get the new catalog at once:"}
	for _, s := range sortedBy(states, func(s catalog.SourceState) string { return s.Name }) {
		switch commit := s.Commit; {
		case s.Error != "":
			lines = append(lines, fmt.Sprintf("- %s: error: %s", s.Name, maskText(s.Error, nil)))
		case commit != "":
			if len(commit) > 7 {
				commit = commit[:7]
			}
			lines = append(lines, fmt.Sprintf("- %s: commit %s", s.Name, commit))
		default:
			lines = append(lines, fmt.Sprintf("- %s: ok", s.Name))
		}
	}
	ch.message = strings.Join(lines, "\n")
	return nil
}

// rebuild rebuilds the catalog after a skill file changed (the daemon watches the config, not
// the skill folders), when the daemon's config already has source; else the config watch does
// it. A failure is a warning: the file is written.
func rebuild(ctx context.Context, b Backend, source string, ch *change) {
	if b.Config().Sources[source] == nil {
		return
	}
	if _, err := b.Update(ctx, []string{source}); err != nil {
		ch.warnings = append(ch.warnings, "the skill catalog was not rebuilt: "+maskText(err.Error(), nil))
	}
}

func newSkill(ctx context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	var a struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Body        string `json:"body"`
	}
	if err := parseArgs(args, &a); err != nil {
		return err
	}
	if a.Name == "" {
		return errors.New("name is required")
	}
	ch.target, ch.summary = "skill/"+a.Name, "new skill"
	desc := strings.TrimSpace(a.Description)
	switch {
	case desc == "":
		return errors.New("description is required")
	case utf8.RuneCountInString(desc) > 1024:
		return errors.New("description is longer than 1024 characters")
	}
	front, err := yaml.Marshal(struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}{a.Name, desc})
	if err != nil {
		return err
	}
	content := "---\n" + string(front) + "---\n\n" + strings.TrimLeft(a.Body, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if len(content) > actions.MaxSkillFile {
		return errors.New("SKILL.md would be larger than the 256 KiB limit")
	}
	e := callEnv(b, c)
	dir, err := actions.SkillNew(e, a.Name)
	if err != nil {
		return err
	}
	file := filepath.Join(dir, "SKILL.md")
	if err := fsutil.WriteFileAtomic(file, []byte(content), 0o644); err != nil {
		return err
	}
	ch.message = fmt.Sprintf("Created the skill %s in %s.", a.Name, file)
	if w := localElsewhere(e, filepath.Dir(dir)); w != "" {
		ch.warnings = append(ch.warnings, w)
		return nil
	}
	rebuild(ctx, b, "local", ch)
	return nil
}

// localElsewhere warns when the source "local" does not point at root (then the new skill is in
// no source).
func localElsewhere(e actions.Env, root string) string {
	cfg, err := config.Load(e.ConfigPath)
	if err != nil || cfg.Sources["local"] == nil {
		return ""
	}
	p := cfg.Sources["local"].Path
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
		}
	}
	if cfg.Sources["local"].Type == "local" && filepath.Clean(p) == filepath.Clean(root) {
		return ""
	}
	return fmt.Sprintf("the source \"local\" points at %s, not at %s: the skill is not in the catalog until a source points at that folder", cfg.Sources["local"].Path, root)
}

func writeSkill(ctx context.Context, b Backend, c Call, args json.RawMessage, ch *change) error {
	var a struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	if err := parseArgs(args, &a); err != nil {
		return err
	}
	if a.ID == "" {
		return errors.New("id is required")
	}
	ch.target, ch.summary = "skill/"+a.ID, fmt.Sprintf("write SKILL.md (%d bytes)", len(a.Content))
	if err := actions.SkillWrite(callEnv(b, c), a.ID, a.Content); err != nil {
		return err
	}
	ch.message = fmt.Sprintf("Wrote the SKILL.md of %s; sessions that load it get the new text.", a.ID)
	if e, ok := catalogSkill(b.Catalog(), a.ID); ok {
		rebuild(ctx, b, e.Source, ch)
	}
	return nil
}

func catalogSkill(c *catalog.Catalog, id string) (catalog.Entry, bool) {
	for _, e := range c.Skills {
		if e.ID == id {
			return e, true
		}
	}
	return catalog.Entry{}, false
}

func readSkill(_ context.Context, b Backend, _ Call, args json.RawMessage) (*mcp.CallToolResult, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := parseArgs(args, &a); err != nil {
		return errorResult(err), nil
	}
	if a.ID == "" {
		return errorResult(errors.New("id is required")), nil
	}
	e, ok := catalogSkill(b.Catalog(), a.ID)
	if !ok {
		return errorResult(fmt.Errorf("no such skill %q (see skilltri_list_skills)", a.ID)), nil
	}
	body, err := gateway.ReadSkillFile(e.Skill.Dir, "SKILL.md")
	if err != nil {
		return errorResult(err), nil
	}
	var sb strings.Builder
	sb.WriteString(body)
	files, more := gateway.SkillFiles(e.Skill.Dir)
	if len(files) > 0 {
		fmt.Fprintf(&sb, "\n\n---\nOther files of skill %q (folder %s):\n", e.ID, e.Skill.Dir)
		for _, f := range files {
			sb.WriteString("- " + f + "\n")
		}
		if more > 0 {
			fmt.Fprintf(&sb, "… and %d more\n", more)
		}
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: sb.String()}}}, nil
}
