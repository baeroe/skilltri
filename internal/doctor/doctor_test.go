package doctor

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/gateway"
	"github.com/baeroe/skilltri/internal/skilllock"
	"github.com/baeroe/skilltri/internal/skills"
)

// newEnv returns an Env below a short temp dir (macOS limits socket paths to 104 bytes) with an
// empty valid config, the shell hook set and an empty working folder.
func newEnv(t *testing.T) Env {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dct")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	e := Env{
		Home:       filepath.Join(dir, "home"),
		ConfigPath: filepath.Join(dir, "config.json"),
		Socket:     filepath.Join(dir, "d.sock"),
		CacheDir:   filepath.Join(dir, "cache"),
		StatePath:  filepath.Join(dir, "state", "skills.lock"),
		Environ:    []string{"SKILLTRI_HOOK=zsh", "SHELL=/bin/zsh", "PWD=" + filepath.Join(dir, "work")},
	}
	for _, d := range []string{e.Home, e.CacheDir, filepath.Join(dir, "work")} {
		mkdir(t, d)
	}
	write(t, e.ConfigPath, `{"version":1,"servers":{}}`)
	return e
}

func workDir(e Env) string { return strings.TrimPrefix(e.Environ[2], "PWD=") }

func mkdir(t *testing.T, d string) {
	t.Helper()
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	mkdir(t, filepath.Dir(link))
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func saveCatalog(t *testing.T, e Env, c *catalog.Catalog) {
	t.Helper()
	if err := catalog.Save(filepath.Join(e.CacheDir, "catalog.json"), c); err != nil {
		t.Fatal(err)
	}
}

// only returns the findings of check.
func only(fs []Finding, check string) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

// expect asserts that check produced exactly the given severity/subject pairs (in order).
func expect(t *testing.T, fs []Finding, check string, want ...[2]string) []Finding {
	t.Helper()
	got := only(fs, check)
	if len(got) != len(want) {
		t.Fatalf("%s findings = %+v, want %v", check, got, want)
	}
	for i, w := range want {
		if got[i].Severity != w[0] || got[i].Subject != w[1] {
			t.Errorf("%s finding %d = %+v, want %s %s", check, i, got[i], w[0], w[1])
		}
		if got[i].Message == "" {
			t.Errorf("%s finding %d has no message", check, i)
		}
	}
	return got
}

func run(e Env) []Finding { return Run(context.Background(), e) }

func TestConfigCheck(t *testing.T) {
	e := newEnv(t)
	expect(t, run(e), "config")
	write(t, e.ConfigPath, `{bad`)
	expect(t, run(e), "config", [2]string{"error", e.ConfigPath})
	write(t, e.ConfigPath, `{"version":1,"servers":{"docs":{"type":"carrier-pigeon"}}}`)
	expect(t, run(e), "config", [2]string{"error", e.ConfigPath})
}

// fakeDaemon answers every control request on e.Socket with reply; hang=true never answers.
func fakeDaemon(t *testing.T, e Env, reply string, hang bool) {
	t.Helper()
	ln, err := net.Listen("unix", e.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if _, err := bufio.NewReader(c).ReadBytes('\n'); err != nil {
					return
				}
				if hang {
					time.Sleep(5 * time.Second)
					return
				}
				c.Write([]byte(reply + "\n"))
			}()
		}
	}()
}

func TestDaemonCheck(t *testing.T) {
	e := newEnv(t)
	f := expect(t, run(e), "daemon", [2]string{"warning", "daemon"})
	if !strings.Contains(f[0].Message, "not running") {
		t.Errorf("message = %q", f[0].Message)
	}

	e = newEnv(t)
	fakeDaemon(t, e, `{"result":{"PID":7,"Version":"`+gateway.Version+`"}}`, false)
	expect(t, run(e), "daemon")

	e = newEnv(t)
	fakeDaemon(t, e, `{"result":{"PID":7,"Version":"0.0.1-old"}}`, false)
	f = expect(t, run(e), "daemon", [2]string{"warning", "daemon"})
	if !strings.Contains(f[0].Message, "0.0.1-old") || !strings.Contains(f[0].Fix, "daemon stop") {
		t.Errorf("finding = %+v", f[0])
	}
}

func TestDaemonCheckHungSocket(t *testing.T) {
	e := newEnv(t)
	fakeDaemon(t, e, "", true)
	start := time.Now()
	fs := run(e)
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Run took %v with a hung socket", d)
	}
	expect(t, fs, "daemon", [2]string{"warning", "daemon"})
}

func TestSourcesCheck(t *testing.T) {
	e := newEnv(t)
	write(t, e.ConfigPath, `{"version":1,"servers":{},"sources":{
 "alpha":{"type":"git","url":"file:///demo/alpha.git"},
 "beta":{"type":"git","url":"file:///demo/beta.git"},
 "gamma":{"type":"git","url":"file:///demo/gamma.git"},
 "mk":{"type":"marketplace","path":"/demo/mk"}}}`)
	saveCatalog(t, e, &catalog.Catalog{Sources: []catalog.SourceState{
		{Name: "alpha", Error: "clone failed: repository not found", FetchedAt: time.Now()},
		{Name: "beta", Error: "not fetched yet"},
		{Name: "gamma", Commit: "abc", FetchedAt: time.Now()},
		{Name: "mk"},
		{Name: "mk/plugins/remote", Error: "not fetched yet"},
	}})
	f := expect(t, run(e), "sources",
		[2]string{"error", "alpha"}, [2]string{"warning", "beta"}, [2]string{"warning", "mk/plugins/remote"})
	if !strings.Contains(f[0].Message, "repository not found") || !strings.Contains(f[0].Fix, "skilltri update alpha") {
		t.Errorf("alpha = %+v", f[0])
	}
	if !strings.Contains(f[2].Fix, "skilltri update mk") {
		t.Errorf("plugin fix = %q", f[2].Fix)
	}
}

func TestSkillsCheck(t *testing.T) {
	e := newEnv(t)
	saveCatalog(t, e, &catalog.Catalog{Warnings: []string{"alpha: skill demo: name does not match its folder", "skill lock ignored: bad json"}})
	expect(t, run(e), "skills", [2]string{"warning", "alpha"}, [2]string{"warning", "catalog"})
}

func TestLockCheck(t *testing.T) {
	e := newEnv(t)
	write(t, e.ConfigPath, `{"version":1,"servers":{},"sources":{
 "alpha":{"type":"git","url":"file:///demo/alpha.git"},
 "loc":{"type":"local","path":"/demo/loc"}}}`)
	root := filepath.Dir(e.ConfigPath)
	var dirs []string
	for _, n := range []string{"changed", "clean", "mine"} {
		d := filepath.Join(root, "skills", n)
		write(t, filepath.Join(d, "SKILL.md"), "---\nname: "+n+"\ndescription: demo\n---\n")
		dirs = append(dirs, d)
	}
	clean, err := skilllock.HashDir(dirs[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := skilllock.Save(e.StatePath, &skilllock.Lock{Version: 1, Sources: map[string]skilllock.Source{
		"alpha": {Skills: map[string]string{"changed": "0000", "clean": clean}},
		"loc":   {Skills: map[string]string{"mine": "0000"}},
	}}); err != nil {
		t.Fatal(err)
	}
	saveCatalog(t, e, &catalog.Catalog{Skills: []catalog.Entry{
		{ID: "changed", Source: "alpha", Skill: skills.Skill{Name: "changed", Dir: dirs[0]}},
		{ID: "clean", Source: "alpha", Skill: skills.Skill{Name: "clean", Dir: dirs[1]}},
		{ID: "mine", Source: "loc", Skill: skills.Skill{Name: "mine", Dir: dirs[2]}},
	}})
	f := expect(t, run(e), "lock", [2]string{"warning", "changed"})
	if !strings.Contains(f[0].Message, "modified checkout") {
		t.Errorf("message = %q", f[0].Message)
	}
}

func TestViewsCheck(t *testing.T) {
	e := newEnv(t)
	good := filepath.Join(e.CacheDir, "skill-good")
	mkdir(t, good)
	skillsDir := filepath.Join(e.CacheDir, "views", "0123456789abcdef", "claude", "skilltri", "skills")
	symlink(t, good, filepath.Join(skillsDir, "good"))
	symlink(t, filepath.Join(e.CacheDir, "gone"), filepath.Join(skillsDir, "gone"))
	expect(t, run(e), "views", [2]string{"error", "0123456789abcdef/claude/skilltri/skills/gone"})
}

func TestProjectDirsCheck(t *testing.T) {
	e := newEnv(t)
	work := workDir(e)
	target := filepath.Join(work, ".agents", "skills")
	ok := filepath.Join(e.CacheDir, "skill-ok")
	mkdir(t, ok)
	gone := filepath.Join(e.CacheDir, "skill-gone")
	symlink(t, ok, filepath.Join(target, "ok"))
	symlink(t, gone, filepath.Join(target, "gone"))
	symlink(t, gone, filepath.Join(target, "users-own")) // not skilltri's: never reported
	b, _ := json.Marshal(map[string]any{"links": map[string]string{"ok": ok, "gone": gone}})
	write(t, filepath.Join(target, ".skilltri.json"), string(b))
	expect(t, run(e), "project-dirs", [2]string{"warning", ".agents/skills/gone"})
}

func TestDuplicatesCheck(t *testing.T) {
	e := newEnv(t)
	write(t, e.ConfigPath, `{"version":1,"servers":{},"sources":{
 "loc":{"type":"local","path":"/demo/loc"},
 "off":{"type":"local","path":"/demo/off","disabled":true},
 "mk":{"type":"marketplace","path":"/demo/mk","plugins":{"dis":{"disabled":true}}}}}`)
	saveCatalog(t, e, &catalog.Catalog{Skills: []catalog.Entry{
		{ID: "demo", Source: "loc", Skill: skills.Skill{Name: "demo", Dir: "/demo/loc/demo"}},
		{ID: "quiet", Source: "off", Skill: skills.Skill{Name: "quiet", Dir: "/demo/off/quiet"}},
		{ID: "tool:helper", Source: "mk", Plugin: "tool", Skill: skills.Skill{Name: "helper", Dir: "/demo/mk/tool/skills/helper"}},
	}})
	for _, n := range []string{"demo", "quiet", "synced", "helper"} {
		mkdir(t, filepath.Join(e.Home, ".claude", "skills", n))
	}
	write(t, filepath.Join(e.Home, ".claude", "settings.json"),
		`{"enabledPlugins":{"tool@mk":true,"dis@mk":true,"gone@mk":false,"other@elsewhere":true}}`)
	f := expect(t, run(e), "duplicates", [2]string{"warning", "demo"}, [2]string{"warning", "tool@mk"})
	if !strings.Contains(f[1].Fix, "claude plugin uninstall tool@mk") {
		t.Errorf("fix = %q", f[1].Fix)
	}
}

func TestPluginServersCheck(t *testing.T) {
	e := newEnv(t)
	write(t, e.ConfigPath, `{"version":1,"servers":{
 "changed":{"type":"stdio","command":"demo","origin":{"source":"mk","plugin":"tool","server":"a","defHash":"old"}},
 "gone":{"type":"stdio","command":"demo","origin":{"source":"mk","plugin":"tool","server":"b","defHash":"x"}},
 "same":{"type":"stdio","command":"demo","origin":{"source":"mk","plugin":"tool","server":"c","defHash":"h"}},
 "plain":{"type":"stdio","command":"demo"}}}`)
	saveCatalog(t, e, &catalog.Catalog{Servers: []catalog.PluginServer{
		{Source: "mk", Plugin: "tool", Name: "a", DefHash: "new"},
		{Source: "mk", Plugin: "tool", Name: "c", DefHash: "h"},
	}})
	f := expect(t, run(e), "plugin-servers", [2]string{"warning", "changed"}, [2]string{"warning", "gone"})
	if !strings.Contains(f[0].Fix, "skilltri plugin-server accept changed") {
		t.Errorf("fix = %q", f[0].Fix)
	}
}

func TestHarnessCheck(t *testing.T) {
	cases := []struct {
		name, file string
		want       [][2]string
	}{
		{"missing file", "", nil},
		{"not registered", `{"mcpServers":{"docs":{"command":"demo-docs"}}}`, [][2]string{{"info", "skilltri"}}},
		{"registered", `{"mcpServers":{"skilltri":{"command":"/opt/bin/skilltri","args":["mcp"]}}}`, nil},
		{"registered in a project", `{"projects":{"/demo/p":{"mcpServers":{"st":{"command":"skilltri","args":["mcp"]}}}}}`, nil},
		{"1mcp url", `{"mcpServers":{"skilltri":{"command":"skilltri","args":["mcp"]},"hub":{"type":"http","url":"http://127.0.0.1:3050/mcp"}}}`,
			[][2]string{{"warning", "hub"}}},
		{"1mcp command", `{"mcpServers":{"skilltri":{"command":"skilltri","args":["mcp"]}},"projects":{"/demo/p":{"mcpServers":{"agg":{"command":"pnpm","args":["dlx","@1mcp/agent"]}}}}}`,
			[][2]string{{"warning", "/demo/p: agg"}}},
		{"unparseable", `{not json`, [][2]string{{"info", ".claude.json"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			if c.file != "" {
				write(t, filepath.Join(e.Home, ".claude.json"), c.file)
			}
			f := expect(t, run(e), "harness", c.want...)
			for _, x := range f {
				if x.Severity == "warning" && !strings.Contains(x.Message, "1MCP still registered") {
					t.Errorf("message = %q", x.Message)
				}
			}
		})
	}
}

func TestShellHookCheck(t *testing.T) {
	e := newEnv(t)
	expect(t, run(e), "shell-hook")
	e.Environ = e.Environ[1:] // drop SKILLTRI_HOOK
	f := expect(t, run(e), "shell-hook", [2]string{"info", "SKILLTRI_HOOK"})
	if !strings.Contains(f[0].Fix, `eval "$(skilltri shell-init zsh)"`) {
		t.Errorf("fix = %q", f[0].Fix)
	}
}

func TestAdaptersCheck(t *testing.T) {
	e := newEnv(t)
	mkdir(t, filepath.Join(e.Home, ".claude"))
	mkdir(t, filepath.Join(e.Home, ".codex"))
	f := expect(t, run(e), "adapters", [2]string{"info", "codex"})
	if !strings.Contains(f[0].Fix, "skilltri adapter enable codex") {
		t.Errorf("fix = %q", f[0].Fix)
	}
	write(t, e.ConfigPath, `{"version":1,"servers":{},"settings":{"adapters":["claude-code","codex"]}}`)
	expect(t, run(e), "adapters")
}

func TestRunSorted(t *testing.T) {
	e := newEnv(t)
	e.Environ = e.Environ[1:]                                                // info: shell-hook
	mkdir(t, filepath.Join(e.Home, ".codex"))                                // info: adapters
	write(t, e.ConfigPath, `{"version":1,"servers":{"x":{"type":"bogus"}}}`) // error: config
	// warning: daemon (not running)
	symlink(t, "/demo/none", filepath.Join(e.CacheDir, "views", "ff", "claude", "skilltri", "skills", "b")) // error: views
	symlink(t, "/demo/none", filepath.Join(e.CacheDir, "views", "ee", "claude", "skilltri", "skills", "a")) // error: views
	fs := run(e)
	rank := map[string]int{"error": 0, "warning": 1, "info": 2}
	for i := 1; i < len(fs); i++ {
		a, b := fs[i-1], fs[i]
		if ra, rb := rank[a.Severity], rank[b.Severity]; ra > rb ||
			(ra == rb && (a.Check > b.Check || (a.Check == b.Check && a.Subject > b.Subject))) {
			t.Errorf("not sorted at %d: %+v before %+v", i, a, b)
		}
	}
	var sev []string
	for _, f := range fs {
		sev = append(sev, f.Severity+"/"+f.Check)
	}
	got := strings.Join(sev, ",")
	want := "error/config,error/views,error/views,warning/daemon,info/adapters,info/shell-hook"
	if got != want {
		t.Errorf("order = %s, want %s", got, want)
	}
}
