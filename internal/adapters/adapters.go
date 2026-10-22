// Package adapters is the harness registry (how each harness gets its skills, how to detect it,
// which MCP client names it uses) and the opt-in sync of skill links into a session folder for
// harnesses that only read a project skill folder such as .agents/skills.
package adapters

import (
	"os"
	"path/filepath"
	"slices"
)

// Delivery methods.
const (
	MethodClaudePluginDirs = "claude-plugin-dirs" // views.Claude dirs in CLAUDE_CODE_PLUGIN_DIRS
	MethodProjectDir       = "project-dir"        // SyncProjectDir into <session>/<ProjectDir>
)

// agentsSkills is the project skill folder shared by the .agents/skills harnesses.
const agentsSkills = ".agents/skills"

// Adapter describes how skilltri delivers skills to one harness.
type Adapter struct {
	ID, Display string
	Binary      string   // the harness command `skilltri run` starts
	Method      string   // MethodClaudePluginDirs | MethodProjectDir
	ProjectDir  string   // folder below the session folder for MethodProjectDir
	ClientNames []string // MCP clientInfo names of this harness
	DetectPaths []string // relative to HOME; any existing → detected
}

// Registry lists every known harness.
var Registry = []Adapter{
	{ID: "claude-code", Display: "Claude Code", Binary: "claude", Method: MethodClaudePluginDirs, ClientNames: []string{"claude-code"}, DetectPaths: []string{".claude"}},
	{ID: "codex", Display: "Codex", Binary: "codex", Method: MethodProjectDir, ProjectDir: agentsSkills, DetectPaths: []string{".codex"}},
	{ID: "gemini-cli", Display: "Gemini CLI", Binary: "gemini", Method: MethodProjectDir, ProjectDir: agentsSkills, DetectPaths: []string{".gemini"}},
	{ID: "opencode", Display: "OpenCode", Binary: "opencode", Method: MethodProjectDir, ProjectDir: agentsSkills, DetectPaths: []string{".config/opencode"}},
	{ID: "cursor", Display: "Cursor", Binary: "cursor-agent", Method: MethodProjectDir, ProjectDir: agentsSkills, DetectPaths: []string{".cursor"}},
	{ID: "github-copilot", Display: "GitHub Copilot CLI", Binary: "copilot", Method: MethodProjectDir, ProjectDir: agentsSkills, DetectPaths: []string{".copilot"}},
	{ID: "amp", Display: "Amp", Binary: "amp", Method: MethodProjectDir, ProjectDir: agentsSkills, DetectPaths: []string{".config/amp"}},
}

// ByID returns the adapter with id.
func ByID(id string) (Adapter, bool) {
	for _, a := range Registry {
		if a.ID == id {
			return a, true
		}
	}
	return Adapter{}, false
}

// Detected returns the adapters (in Registry order) of which at least one DetectPath exists in home.
func Detected(home string) []Adapter {
	var out []Adapter
	for _, a := range Registry {
		for _, p := range a.DetectPaths {
			if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(p))); err == nil {
				out = append(out, a)
				break
			}
		}
	}
	return out
}

// ForClient returns the adapter whose harness reports clientName as its MCP clientInfo name.
func ForClient(clientName string) (Adapter, bool) {
	for _, a := range Registry {
		if slices.Contains(a.ClientNames, clientName) {
			return a, true
		}
	}
	return Adapter{}, false
}
