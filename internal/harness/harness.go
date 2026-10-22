// Package harness renders the snippets that register `skilltri mcp` with agent harnesses (ported
// from tussh) and detects, by reading the harnesses' user config files only, whether skilltri is
// registered and which harnesses still point at 1MCP.
package harness

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Harness ids (Snippet.Harness, Registered, OneMCPRegistered), in display order.
const (
	Claude   = "claude"
	Codex    = "codex"
	Gemini   = "gemini"
	OpenCode = "opencode"
	Cursor   = "cursor"
)

// IDs lists every harness id in display order.
var IDs = []string{Claude, Codex, Gemini, OpenCode, Cursor}

// OneMCPEndpoint is the URL of the local 1MCP gateway skilltri replaces.
const OneMCPEndpoint = "http://127.0.0.1:3050/mcp"

// Snippet is one registration command or config fragment.
type Snippet struct {
	Harness string // harness id
	Name    string // display name
	How     string // where to put it
	Text    string
	Note    string // verification caveat, if any
}

func shQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func pretty(v any) string {
	data, _ := json.MarshalIndent(v, "", "  ")
	return string(data)
}

// Snippets returns the snippets that register the skilltri binary at bin (absolute path) as the
// stdio MCP server `bin mcp`. Formats follow each harness's docs as of October 2026 (Note says
// what is unverified).
func Snippets(bin string) []Snippet {
	q := shQuote(bin)
	js, _ := json.Marshal(bin)
	return []Snippet{
		{Claude, "Claude Code", "run in a shell", "claude mcp add --scope user skilltri -- " + q + " mcp", ""},
		{Codex, "Codex CLI", "run in a shell", "codex mcp add skilltri -- " + q + " mcp", ""},
		{Codex, "Codex CLI (config)", "add to ~/.codex/config.toml", "[mcp_servers.skilltri]\ncommand = " + string(js) + "\nargs = [\"mcp\"]", ""},
		{Gemini, "Gemini CLI", "run in a shell", "gemini mcp add -s user skilltri " + q + " mcp",
			"unverified: whether `gemini mcp add` defaults to stdio for a path"},
		{Gemini, "Gemini CLI (config)", "merge into ~/.gemini/settings.json",
			pretty(map[string]any{"mcpServers": map[string]any{"skilltri": map[string]any{"command": bin, "args": []string{"mcp"}}}}), ""},
		{OpenCode, "opencode", "merge into ~/.config/opencode/opencode.json",
			pretty(map[string]any{"$schema": "https://opencode.ai/config.json",
				"mcp": map[string]any{"skilltri": map[string]any{"type": "local", "command": []string{bin, "mcp"}, "enabled": true}}}), ""},
		{Cursor, "Cursor", "merge into ~/.cursor/mcp.json",
			pretty(map[string]any{"mcpServers": map[string]any{"skilltri": map[string]any{"type": "stdio", "command": bin, "args": []string{"mcp"}}}}),
			"unverified: whether Cursor still accepts entries without \"type\""},
	}
}

// Entry is one MCP server entry of a harness config, reduced to what the detection needs.
type Entry struct {
	Name    string
	Project string   // Claude Code: the project folder of a per-project entry ("" = user scope)
	Command string   // the command (opencode: the first element of its command array)
	Args    []string // its arguments
	Strings []string // every string value of the entry (URLs, env values, …)
}

// Skilltri reports whether e registers skilltri: named "skilltri", or running `…/skilltri mcp`.
func (e Entry) Skilltri() bool {
	if e.Name == "skilltri" {
		return true
	}
	return filepath.Base(e.Command) == "skilltri" && len(e.Args) > 0 && e.Args[0] == "mcp"
}

// OneMCP reports whether e points at the 1MCP endpoint or runs a 1mcp command.
func (e Entry) OneMCP() bool {
	ep := strings.TrimPrefix(OneMCPEndpoint, "http://")
	for _, s := range e.Strings {
		if strings.Contains(s, ep) || strings.Contains(s, "localhost:3050/mcp") {
			return true
		}
	}
	for _, s := range append([]string{e.Command}, e.Args...) {
		if strings.Contains(strings.ToLower(s), "1mcp") {
			return true
		}
	}
	return false
}

// entries reads the MCP server entries of harness id under home (nil when there are none or the
// files are missing or invalid).
func entries(home, id string) []Entry {
	switch id {
	case Claude:
		out, _ := ClaudeEntries(home)
		return out
	case Gemini:
		return jsonEntries(dig(readJSON(filepath.Join(home, ".gemini", "settings.json")), "mcpServers"))
	case OpenCode:
		dir := filepath.Join(home, ".config", "opencode")
		return append(jsonEntries(dig(readJSON(filepath.Join(dir, "opencode.json")), "mcp")),
			jsonEntries(dig(readJSON(filepath.Join(dir, "opencode.jsonc")), "mcp"))...)
	case Cursor:
		return jsonEntries(dig(readJSON(filepath.Join(home, ".cursor", "mcp.json")), "mcpServers"))
	case Codex:
		return codexEntries(filepath.Join(home, ".codex", "config.toml"))
	}
	return nil
}

// Registered reports, per harness id, whether its user config under home has an MCP server entry
// that registers skilltri (named "skilltri", or any entry running `…/skilltri mcp`; for Claude Code
// also in its per-project entries). It only reads files (never runs a harness CLI); missing or
// invalid files count as not registered.
func Registered(home string) map[string]bool {
	out := map[string]bool{}
	for _, id := range IDs {
		out[id] = false
		for _, e := range entries(home, id) {
			if e.Skilltri() {
				out[id] = true
				break
			}
		}
	}
	return out
}

// OneMCPRegistered returns the ids (in IDs order) of the harnesses whose config still has an MCP
// server entry pointing at OneMCPEndpoint or running a 1mcp command.
func OneMCPRegistered(home string) []string {
	var out []string
	for _, id := range IDs {
		for _, e := range entries(home, id) {
			if e.OneMCP() {
				out = append(out, id)
				break
			}
		}
	}
	return out
}

// ClaudeEntries reads the MCP server entries of ~/.claude.json under home: the user ones and the
// per-project ones (Entry.Project set), sorted by project and name. A missing file yields none; an
// unreadable or invalid one an error.
func ClaudeEntries(home string) ([]Entry, error) {
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(stripJSONC(data), &v); err != nil {
		return nil, err
	}
	out := jsonEntries(dig(v, "mcpServers"))
	if projects, ok := dig(v, "projects").(map[string]any); ok {
		for dir, p := range projects {
			for _, e := range jsonEntries(dig(p, "mcpServers")) {
				e.Project = dir
				out = append(out, e)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// --- JSON ----------------------------------------------------------------------------------

func readJSON(path string) any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var v any
	if json.Unmarshal(stripJSONC(data), &v) != nil {
		return nil
	}
	return v
}

func dig(v any, key string) any {
	m, _ := v.(map[string]any)
	return m[key]
}

func jsonEntries(v any) []Entry {
	servers, _ := v.(map[string]any)
	var out []Entry
	for name, raw := range servers {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		e := Entry{Name: name, Strings: allStrings(m)}
		switch c := m["command"].(type) {
		case string:
			e.Command = c
			e.Args = strList(m["args"])
		case []any: // opencode: the command with its arguments
			if l := strList(c); len(l) > 0 {
				e.Command, e.Args = l[0], l[1:]
			}
		}
		out = append(out, e)
	}
	return out
}

func strList(v any) []string {
	l, _ := v.([]any)
	var out []string
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func allStrings(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			out = append(out, allStrings(x)...)
		}
		return out
	case map[string]any:
		var out []string
		for _, x := range v {
			out = append(out, allStrings(x)...)
		}
		return out
	}
	return nil
}

// stripJSONC removes // and /* */ comments outside strings (opencode and Cursor accept JSONC).
func stripJSONC(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
		} else if c == '/' && i+1 < len(b) && b[i+1] == '/' {
			for i < len(b) && b[i] != '\n' {
				i++
			}
			continue
		} else if c == '/' && i+1 < len(b) && b[i+1] == '*' {
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
			continue
		}
		out = append(out, c)
	}
	return out
}

// --- Codex TOML ------------------------------------------------------------------------------

var (
	tomlTableRE  = regexp.MustCompile(`^\s*\[([^\[\]]*)\]\s*(#.*)?$`)
	codexTableRE = regexp.MustCompile(`^\s*mcp_servers\s*\.\s*("(?:[^"\\]|\\.)*"|'[^']*'|[A-Za-z0-9_-]+)\s*$`)
	tomlKeyRE    = regexp.MustCompile(`^\s*([A-Za-z0-9_-]+)\s*=\s*(.*)$`)
	tomlStrRE    = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|'([^']*)'`)
)

// tomlStrings returns the quoted strings in s, in order.
func tomlStrings(s string) []string {
	var out []string
	for _, m := range tomlStrRE.FindAllStringSubmatch(s, -1) {
		if strings.HasPrefix(m[0], "'") {
			out = append(out, m[2])
			continue
		}
		v, err := strconv.Unquote(`"` + m[1] + `"`)
		if err != nil {
			v = m[1]
		}
		out = append(out, v)
	}
	return out
}

// codexEntries reads the [mcp_servers.NAME] tables of a Codex config.toml line by line: their
// command, args (also a multi-line array) and the strings of their other keys. Subtables such as
// [mcp_servers.NAME.env] are not part of the entry.
func codexEntries(path string) []Entry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Entry
	cur := -1 // index in out of the table being read, -1 outside an mcp_servers table
	inArgs := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if inArgs {
			out[cur].Args = append(out[cur].Args, tomlStrings(line)...)
			out[cur].Strings = append(out[cur].Strings, tomlStrings(line)...)
			inArgs = !closesArray(line)
			continue
		}
		if m := tomlTableRE.FindStringSubmatch(line); m != nil {
			cur = -1
			if t := codexTableRE.FindStringSubmatch(m[1]); t != nil {
				name := t[1]
				if s := tomlStrings(name); len(s) == 1 {
					name = s[0]
				}
				out = append(out, Entry{Name: name})
				cur = len(out) - 1
			}
			continue
		}
		m := tomlKeyRE.FindStringSubmatch(line)
		if cur < 0 || m == nil {
			continue
		}
		vals := tomlStrings(m[2])
		out[cur].Strings = append(out[cur].Strings, vals...)
		switch m[1] {
		case "command":
			if len(vals) > 0 {
				out[cur].Command = vals[0]
			}
		case "args":
			out[cur].Args = vals
			inArgs = strings.HasPrefix(strings.TrimSpace(m[2]), "[") && !closesArray(m[2])
		}
	}
	if sc.Err() != nil { // a line too long or a read error: treat the file as not readable
		return nil
	}
	return out
}

// closesArray reports whether s has a ] outside quoted strings.
func closesArray(s string) bool { return strings.Contains(tomlStrRE.ReplaceAllString(s, ""), "]") }
