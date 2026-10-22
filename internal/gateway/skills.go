package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/baeroe/skilltri/internal/adapters"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The MCP skill fallback (CONCEPT.md "MCP fallback"): sessions whose harness gets no native skill
// view list the active skills in their instructions and offer two tools to read them.
const (
	loadSkillTool     = config.ReservedName + "_load_skill"
	readSkillFileTool = config.ReservedName + "_read_skill_file"
	maxSkillFile      = 256 << 10 // bytes read_skill_file (and load_skill for SKILL.md) serves
	maxSkillFiles     = 200       // other files load_skill lists
	maxSkillList      = 8 << 10   // bytes of the skill list in the instructions
)

var nameSchema = map[string]any{"type": "string", "description": "Skill name as listed in the server instructions"}

// skillTools are the fallback tools; their handlers are bound per session.
var skillTools = []*mcp.Tool{
	{
		Name:        loadSkillTool,
		Description: "Load an agent skill: returns its SKILL.md instructions and lists the skill's other files (read them with " + readSkillFileTool + ").",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"name": nameSchema}, "required": []string{"name"}},
	},
	{
		Name:        readSkillFileTool,
		Description: "Read a file of an agent skill by its path relative to the skill folder (max 256 KiB).",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"name": nameSchema,
			"path": map[string]any{"type": "string", "description": "File path relative to the skill folder, e.g. references/guide.md"},
		}, "required": []string{"name", "path"}},
	},
}

// SetClient records the client's clientInfo name (known after initialize); the next Refresh
// decides the skill fallback with it.
func (s *Session) SetClient(name string) {
	s.mu.Lock()
	s.info.ClientName = name
	s.mu.Unlock()
}

// skillFallback reports whether info's session gets the skill tools: unless its client maps to an
// adapter listed in SkillsNative. Without a client name only an empty SkillsNative turns it on.
func skillFallback(info SessionInfo) bool {
	if len(info.SkillsNative) == 0 {
		return true
	}
	if info.ClientName == "" {
		return false
	}
	a, ok := adapters.ForClient(info.ClientName)
	return !ok || !slices.Contains(info.SkillsNative, a.ID)
}

// addSkillTools puts the fallback tools into w when the session gets them. Called first in a
// Refresh, so a downstream tool can never shadow them (server names cannot contain "_" anyway).
func (s *Session) addSkillTools(info SessionInfo, w *wanted) {
	if s.skills == nil || !skillFallback(info) {
		return
	}
	handlers := map[string]mcp.ToolHandler{loadSkillTool: s.loadSkill, readSkillFileTool: s.readSkillFile}
	for _, t := range skillTools {
		w.tools[t.Name], w.toolSrc[t.Name] = t, route{config.ReservedName, t.Name}
		w.local[t.Name] = handlers[t.Name]
	}
}

// skillInstructions is the instructions' skill list: one line per entry, capped at maxSkillList.
func skillInstructions(entries []catalog.Entry) string {
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Agent skills (load with " + loadSkillTool + "): ")
	for i, e := range entries {
		line := "\n- " + e.ID + ": " + strings.Join(strings.Fields(e.Skill.Description), " ")
		if b.Len()+len(line) > maxSkillList {
			fmt.Fprintf(&b, "\n… and %d more; call %s with a name", len(entries)-i, loadSkillTool)
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// activeSkill returns the skill name is active for the session's current folder.
func (s *Session) activeSkill(name string) (catalog.Entry, error) {
	dir := s.Info().Dir
	for _, e := range s.skills(dir) {
		if e.ID == name {
			return e, nil
		}
	}
	return catalog.Entry{}, fmt.Errorf("skill %q is not active in %s", name, dir)
}

type skillArgs struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func parseSkillArgs(req *mcp.CallToolRequest) (skillArgs, error) {
	var a skillArgs
	if req.Params != nil && len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &a); err != nil {
			return a, fmt.Errorf("invalid arguments: %v", err)
		}
	}
	return a, nil
}

func (s *Session) loadSkill(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	a, err := parseSkillArgs(req)
	if err != nil {
		return errorResult(err), nil
	}
	e, err := s.activeSkill(a.Name)
	if err != nil {
		return errorResult(err), nil
	}
	body, err := readInside(e.Skill.Dir, "SKILL.md")
	if err != nil {
		return errorResult(err), nil
	}
	files, more := skillFiles(e.Skill.Dir)
	var b strings.Builder
	b.WriteString(body)
	if len(files) > 0 {
		fmt.Fprintf(&b, "\n\n---\nOther files of skill %q (folder %s; read them with %s):\n", e.ID, e.Skill.Dir, readSkillFileTool)
		for _, f := range files {
			b.WriteString("- " + f + "\n")
		}
		if more > 0 {
			fmt.Fprintf(&b, "… and %d more\n", more)
		}
	}
	return textResult(b.String()), nil
}

func (s *Session) readSkillFile(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	a, err := parseSkillArgs(req)
	if err != nil {
		return errorResult(err), nil
	}
	e, err := s.activeSkill(a.Name)
	if err != nil {
		return errorResult(err), nil
	}
	text, err := readInside(e.Skill.Dir, a.Path)
	if err != nil {
		return errorResult(err), nil
	}
	return textResult(text), nil
}

// readInside reads the regular file rel below dir. rel must be relative and stay inside dir, also
// after resolving symlinks (Review Focus 5); files over maxSkillFile are refused.
func readInside(dir, rel string) (string, error) {
	clean := filepath.Clean(rel)
	if rel == "" || !filepath.IsLocal(clean) { // "..notes.md" is fine, "../x" is not
		return "", fmt.Errorf("path %q must be relative to the skill folder", rel)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("skill folder: %v", err)
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, clean))
	if err != nil {
		return "", fmt.Errorf("%s: %v", rel, errors.Unwrap(err))
	}
	if r, err := filepath.Rel(root, target); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q leaves the skill folder", rel)
	}
	f, err := os.Open(target)
	if err != nil {
		return "", fmt.Errorf("%s: %v", rel, errors.Unwrap(err))
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", rel)
	}
	if fi.Size() > maxSkillFile {
		return "", fmt.Errorf("%s is larger than the 256 KiB limit", rel)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSkillFile+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxSkillFile {
		return "", fmt.Errorf("%s is larger than the 256 KiB limit", rel)
	}
	return string(b), nil
}

// skillFiles lists the files below dir except SKILL.md, relative and sorted, skipping folders
// whose name starts with "." (such as .git). It returns at most maxSkillFiles and the number left out.
func skillFiles(dir string) (files []string, more int) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "SKILL.md" {
			return nil
		}
		if len(files) == maxSkillFiles {
			more++
			return nil
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	slices.Sort(files)
	return files, more
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
}
