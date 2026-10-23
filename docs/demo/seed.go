//go:build ignore

// seed.go fills a sandbox HOME with demo data for the README screenshots (see docs/screenshots.sh).
//
// It writes the config through skilltri's own config package, so the file has exactly the format
// skilltri reads, plus the skill sources (a local folder, a git repo behind a local bare repo and a
// plugin marketplace folder), two demo project folders and a Claude Code registration. It refuses to
// run unless HOME is the sandbox HOME named by SKILLTRI_DEMO_HOME and the sandbox is still empty, so it
// can never touch a real setup. Build it with the real HOME (Go caches) and run it with the sandbox one:
//
//	go build -o seed docs/demo/seed.go && HOME="$SKILLTRI_DEMO_HOME" ./seed
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/paths"
	"github.com/baeroe/skilltri/internal/rules"
)

var home string

func main() {
	home = os.Getenv("SKILLTRI_DEMO_HOME")
	if home == "" || os.Getenv("HOME") != home {
		fail("HOME must be the sandbox HOME named by SKILLTRI_DEMO_HOME")
	}
	for _, v := range []string{"SKILLTRI_CONFIG_DIR", "SKILLTRI_STATE_DIR", "SKILLTRI_DATA_DIR", "SKILLTRI_CACHE_DIR", "XDG_CONFIG_HOME"} {
		if os.Getenv(v) != "" {
			fail("%s must not be set (the demo uses the defaults under the sandbox HOME)", v)
		}
	}
	if !strings.HasPrefix(paths.ConfigFile(), home+string(filepath.Separator)) {
		fail("config file %s is outside the sandbox", paths.ConfigFile())
	}
	if _, err := os.Stat(paths.ConfigFile()); err == nil {
		fail("%s already exists; seed only an empty sandbox", paths.ConfigFile())
	}

	// demo projects
	for _, d := range []string{"dev/acme-shop", "dev/side-project"} {
		mkdir(d)
		git(filepath.Join(home, d), "init", "-q")
	}
	write("dev/acme-shop/README.md", "# acme-shop\n\nDemo online shop.\n")
	write("dev/side-project/README.md", "# side-project\n\nA weekend project.\n")

	// local skills in the config folder
	local := filepath.Join(paths.ConfigDir(), "skills")
	skill(local, "commit-message", "Write commit messages in the house style: one summary line, then a list of changes.")
	skill(local, "release-notes", "Turn the merged pull requests since the last tag into user-facing release notes.")
	skill(local, "sql-review", "Review SQL migrations for locking, missing indexes and irreversible changes.")

	// git source: a work tree pushed to a local bare repo, fetched by the daemon over file://
	team := filepath.Join(home, "src/team-skills")
	skill(team, "api-design", "Design REST endpoints that follow the team's API guidelines (naming, errors, paging).")
	skill(team, "incident-report", "Write a blameless incident report: timeline, impact, root cause, follow-ups.")
	bare := filepath.Join(home, "src/team-skills.git")
	git(team, "init", "-q", "-b", "main")
	git(team, "add", ".")
	git(team, "commit", "-q", "-m", "Add api-design and incident-report")
	git(home, "clone", "-q", "--bare", team, bare)

	// marketplace folder with four plugins
	mk := filepath.Join(home, "src/demo-market")
	write("src/demo-market/.claude-plugin/marketplace.json", `{
  "name": "demo-market",
  "owner": { "name": "Demo Owner" },
  "plugins": [
    { "name": "writing", "source": "./plugins/writing", "description": "Outlines and proofreading" },
    { "name": "review-kit", "source": "./plugins/review-kit", "description": "Code and security review" },
    { "name": "knowledge", "source": "./plugins/knowledge", "description": "Team wiki access" },
    { "name": "design-kit", "source": "./plugins/design-kit", "description": "UI design helpers" }
  ]
}
`)
	plugin(mk, "writing", "writing-tools", map[string]string{
		"outline":   "Draft a structured outline before writing a longer document.",
		"proofread": "Proofread text for spelling, grammar and consistent terminology.",
	}, `{ "mcpServers": { "notes": { "command": "notes-mcp", "args": ["--vault", "demo"] } } }`)
	plugin(mk, "review-kit", "review-kit", map[string]string{
		"code-review":    "Review a diff for bugs, missing tests and unclear naming.",
		"security-check": "Check a change for injection, secrets in code and unsafe defaults.",
	}, "")
	plugin(mk, "knowledge", "knowledge", map[string]string{
		"wiki-search": "Search the team wiki before answering questions about internal processes.",
	}, `{ "mcpServers": { "wiki": { "command": "wiki-mcp", "args": ["--space", "team", "--read-only"] } } }`)
	plugin(mk, "design-kit", "design-kit", map[string]string{
		"color-palette": "Suggest an accessible color palette for a UI.",
	}, "")

	// the wiki plugin server was enabled at an older definition (without --read-only): needs review
	oldWiki := config.Server{Type: "stdio", Command: "wiki-mcp", Args: []string{"--space", "team"}}

	tree := func(p string) rules.Rule { return rules.Rule{Path: p, Mode: rules.Tree} }
	err := config.Update(paths.ConfigFile(), func(c *config.Config) error {
		c.Servers = map[string]*config.Server{
			"docs": {Type: "stdio", Command: "docs-mcp", RequestTimeoutMs: 30000,
				Tools: map[string]*config.Tool{"list_versions": {Disabled: true}}},
			"browser": {Type: "stdio", Command: "browser-mcp", Args: []string{"--headless"}, PerSession: true,
				Rules: []rules.Rule{tree("~/dev"), {Path: "~/dev/side-project", Mode: rules.NotTree}}},
			"tickets": {Type: "http", URL: "http://127.0.0.1:47391/mcp", Headers: map[string]string{"X-Project": "acme-shop"},
				Rules: []rules.Rule{tree("~/dev/acme-shop")},
				Tools: map[string]*config.Tool{"delete_ticket": {Rules: []rules.Rule{{Path: "~/dev/acme-shop", Mode: rules.Only}}}}},
			"notes": {Type: "stdio", Command: "notes-mcp", Env: map[string]string{"NOTES_VAULT": "~/notes"},
				Rules: []rules.Rule{{Path: "~/dev/acme-shop", Mode: rules.NotTree}}},
			"billing": {Type: "http", URL: "https://billing.acme.example/mcp", Disabled: true,
				Headers: map[string]string{"Authorization": "Bearer ${secret:billing-token}"},
				Rules:   []rules.Rule{tree("~/dev/acme-shop")}},
			"wiki": {Type: "stdio", Command: oldWiki.Command, Args: oldWiki.Args,
				Rules:  []rules.Rule{tree("~/dev")},
				Origin: &config.Origin{Source: "demo-market", Plugin: "knowledge", Server: "wiki", DefHash: catalog.DefHash(oldWiki, "")}},
		}
		c.Sources = map[string]*config.Source{
			"my-skills": {Type: "local", Path: "~/.config/skilltri/skills",
				Skills: map[string]*config.Node{"sql-review": {Rules: []rules.Rule{tree("~/dev/acme-shop")}}}},
			"team-skills": {Type: "git", URL: "file://" + bare, Ref: "main",
				Rules: []rules.Rule{tree("~/dev/acme-shop")}},
			"demo-market": {Type: "marketplace", Path: "~/src/demo-market", UpdateEvery: "0",
				Plugins: map[string]*config.Node{
					"review-kit": {Rules: []rules.Rule{{Path: "~/dev/side-project", Mode: rules.NotTree}}},
					"design-kit": {Disabled: true},
				}},
		}
		return nil
	})
	if err != nil {
		fail("config: %v", err)
	}

	// a Claude Code setup: skilltri registered, plus an entry that duplicates the docs server
	mkdir(".claude")
	write(".claude.json", fmt.Sprintf(`{
  "mcpServers": {
    "skilltri": { "type": "stdio", "command": %q, "args": ["mcp"] },
    "docs": { "type": "stdio", "command": "docs-mcp", "args": [] }
  }
}
`, filepath.Join(home, ".local/bin/skilltri")))
	fmt.Println("seeded", paths.ConfigFile())
}

func skill(root, name, desc string) {
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail("%v", err)
	}
	body := fmt.Sprintf("---\nname: %s\ndescription: %q\n---\n\n# %s\n\n%s\n", name, desc, name, desc)
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		fail("%v", err)
	}
}

func plugin(mk, entry, name string, skills map[string]string, mcp string) {
	dir := filepath.Join(mk, "plugins", entry)
	rel, _ := filepath.Rel(home, dir)
	write(filepath.Join(rel, ".claude-plugin/plugin.json"), fmt.Sprintf(`{ "name": %q, "version": "1.2.0" }`+"\n", name))
	for n, d := range skills {
		skill(filepath.Join(dir, "skills"), n, d)
	}
	if mcp != "" {
		write(filepath.Join(rel, ".mcp.json"), mcp+"\n")
	}
}

func mkdir(rel string) {
	if err := os.MkdirAll(filepath.Join(home, rel), 0o755); err != nil {
		fail("%v", err)
	}
}

func write(rel, content string) {
	p := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		fail("%v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		fail("%v", err)
	}
}

// git runs git in dir with a demo identity (sandbox repos only; HOME is the sandbox, so no user config).
func git(dir string, args ...string) {
	cmd := exec.Command("git", append([]string{"-c", "user.name=Demo", "-c", "user.email=demo@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		fail("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "seed: "+format+"\n", a...)
	os.Exit(1)
}
