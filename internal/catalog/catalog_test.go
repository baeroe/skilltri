package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/rules"
	"github.com/baeroe/skilltri/internal/skilllock"
	"github.com/baeroe/skilltri/internal/skills"
	"github.com/baeroe/skilltri/internal/sources"
)

// gitEnv isolates every git call from the user's git config.
func gitEnv(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Demo Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "demo@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Demo Author")
	t.Setenv("GIT_COMMITTER_EMAIL", "demo@example.com")
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skill(t *testing.T, dir, name string) {
	t.Helper()
	write(t, filepath.Join(dir, name, "SKILL.md"), skillFile(name))
}

// gitRepo creates a repo whose files are given as path → content, committed on main.
func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "upstream")
	for p, c := range files {
		write(t, filepath.Join(dir, p), c)
	}
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func skillFile(name string) string {
	return "---\nname: " + name + "\ndescription: Demo skill " + name + ".\n---\nBody.\n"
}

// demoMarket copies the Task 4 testdata marketplace and points remote-one at a local git repo.
func demoMarket(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "demo-market")
	if err := os.CopyFS(dst, os.DirFS("../marketplace/testdata/demo-market")); err != nil {
		t.Fatal(err)
	}
	remote := gitRepo(t, map[string]string{"skills/gamma/SKILL.md": skillFile("gamma")})
	write(t, filepath.Join(dst, ".claude-plugin", "marketplace.json"), `{
  "name": "demo-market",
  "plugins": [
    { "name": "writing", "source": "./plugins/writing", "skills": "./extra-skills" },
    { "name": "remote-one", "source": { "source": "url", "url": "file://`+remote+`" } },
    { "name": "pkg", "source": { "source": "npm", "package": "@demo/pkg" } }
  ]
}`)
	return dst
}

type fixture struct {
	cfg     *config.Config
	builder *Builder
	market  string
	root    string // fetcher root
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	gitEnv(t)
	local := filepath.Join(t.TempDir(), "local")
	skill(t, local, "alpha")
	repo := gitRepo(t, map[string]string{"skills/beta/SKILL.md": skillFile("beta"), "README.md": "demo"})
	market := demoMarket(t)
	root := filepath.Join(t.TempDir(), "sources")
	return &fixture{
		cfg: &config.Config{Version: 1, Servers: map[string]*config.Server{}, Sources: map[string]*config.Source{
			"loc":    {Type: "local", Path: local},
			"repo":   {Type: "git", URL: "file://" + repo, Subpath: "skills"},
			"market": {Type: "marketplace", Path: market},
		}},
		builder: &Builder{Fetcher: &sources.Fetcher{Root: root}, Lock: filepath.Join(t.TempDir(), "skills.lock")},
		market:  market,
		root:    root,
	}
}

func build(t *testing.T, b *Builder, cfg *config.Config, sync bool) *Catalog {
	t.Helper()
	c, err := b.Build(context.Background(), cfg, sync)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ids(c *Catalog) []string {
	var out []string
	for _, e := range c.Skills {
		out = append(out, e.ID)
	}
	return out
}

func TestBuildLocalGitMarketplace(t *testing.T) {
	f := newFixture(t)
	c := build(t, f.builder, f.cfg, true)

	want := []string{"alpha", "beta", "remote-one:gamma", "writing-tools:outline", "writing-tools:proofread"}
	if got := ids(c); !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v\nwarnings: %v", got, want, c.Warnings)
	}
	for _, e := range c.Skills {
		switch e.ID {
		case "alpha":
			if e.Source != "loc" || e.Plugin != "" {
				t.Errorf("alpha: %+v", e)
			}
		case "writing-tools:outline":
			if e.Source != "market" || e.Plugin != "writing" || e.Skill.Name != "outline" {
				t.Errorf("outline: %+v", e)
			}
		case "remote-one:gamma":
			if e.Plugin != "remote-one" || !strings.HasPrefix(e.Skill.Dir, f.builder.Fetcher.Dir("market/plugins/remote-one")) {
				t.Errorf("gamma: %+v", e)
			}
		}
	}

	var servers []string
	for _, s := range c.Servers {
		servers = append(servers, s.Source+"/"+s.Plugin+"/"+s.Name)
		if s.DefHash != s.Def.ConnKey() || s.DefHash != DefHash(s.Def, "") {
			t.Errorf("server %s: bad DefHash", s.Name)
		}
	}
	if want := []string{"market/writing/docs", "market/writing/notes"}; !slices.Equal(servers, want) {
		t.Fatalf("servers = %v, want %v", servers, want)
	}

	var states []string
	for _, s := range c.Sources {
		states = append(states, s.Name)
		if s.Error != "" {
			t.Errorf("source %s: %s", s.Name, s.Error)
		}
		if (s.Name == "repo" || s.Name == "market/plugins/remote-one") && (len(s.Commit) != 40 || s.FetchedAt.IsZero()) {
			t.Errorf("source %s: missing commit/fetch time: %+v", s.Name, s)
		}
	}
	if want := []string{"loc", "market", "market/plugins/remote-one", "repo"}; !slices.Equal(states, want) {
		t.Fatalf("sources = %v, want %v", states, want)
	}
	wantPlugins := []PluginInfo{
		{Source: "market", Plugin: "pkg"},
		{Source: "market", Plugin: "remote-one", Name: "remote-one", Loaded: true},
		{Source: "market", Plugin: "writing", Name: "writing-tools", Loaded: true},
	}
	if !reflect.DeepEqual(c.Plugins, wantPlugins) {
		t.Fatalf("plugins = %+v, want %+v", c.Plugins, wantPlugins)
	}
	if !slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.Contains(w, "unsupported plugin source npm") }) {
		t.Errorf("npm warning missing: %v", c.Warnings)
	}

	lock, err := skilllock.Load(f.builder.Lock)
	if err != nil {
		t.Fatal(err)
	}
	r := lock.Sources["repo"]
	if len(r.Commit) != 40 || r.Skills["beta"] == "" {
		t.Fatalf("lock repo: %+v", r)
	}
	if lock.Sources["market/plugins/remote-one"].Skills["remote-one:gamma"] == "" {
		t.Fatalf("lock remote plugin: %+v", lock.Sources)
	}

	// An unchanged rebuild yields the same catalog (apart from Built).
	again := build(t, f.builder, f.cfg, false)
	again.Built = c.Built
	if !reflect.DeepEqual(again, c) {
		t.Fatalf("offline rebuild differs:\n%+v\n%+v", again, c)
	}
}

func TestCollision(t *testing.T) {
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	skill(t, a, "alpha")
	skill(t, b, "alpha")
	cfg := &config.Config{Version: 1, Sources: map[string]*config.Source{
		"b-src": {Type: "local", Path: b},
		"a-src": {Type: "local", Path: a},
	}}
	bl := &Builder{Fetcher: &sources.Fetcher{Root: t.TempDir()}, Lock: filepath.Join(t.TempDir(), "skills.lock")}
	for i := 0; i < 5; i++ {
		c := build(t, bl, cfg, false)
		if len(c.Skills) != 1 || c.Skills[0].Source != "a-src" {
			t.Fatalf("skills = %+v", c.Skills)
		}
		if !slices.Equal(c.Warnings, []string{"skill alpha from b-src hidden by a-src"}) {
			t.Fatalf("warnings = %q", c.Warnings)
		}
	}
}

func TestSymlinkEscape(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside")
	skill(t, outside, "evil")
	local := filepath.Join(t.TempDir(), "local")
	skill(t, local, "alpha")
	if err := os.Symlink(filepath.Join(outside, "evil"), filepath.Join(local, "evil")); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Version: 1, Sources: map[string]*config.Source{"loc": {Type: "local", Path: local}}}
	bl := &Builder{Fetcher: &sources.Fetcher{Root: t.TempDir()}, Lock: filepath.Join(t.TempDir(), "skills.lock")}
	c := build(t, bl, cfg, false)
	if got := ids(c); !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("ids = %v", got)
	}
	if !slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.Contains(w, "outside") }) {
		t.Fatalf("no escape warning: %v", c.Warnings)
	}
}

func TestEvaluateHierarchy(t *testing.T) {
	f := newFixture(t)
	p := filepath.Join(t.TempDir(), "p")
	q := filepath.Join(t.TempDir(), "q")
	other := t.TempDir()
	for _, d := range []string{p, q} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m := f.cfg.Sources["market"]
	m.Rules = []rules.Rule{{Path: p, Mode: rules.Tree}}
	m.Skills = map[string]*config.Node{"writing-tools:outline": {Rules: []rules.Rule{{Path: q, Mode: rules.Only}}}}
	c := build(t, f.builder, f.cfg, true)

	active := func(dir string) []string {
		var out []string
		for _, e := range Active(c, f.cfg, rules.Normalize(dir)) {
			out = append(out, e.ID)
		}
		return out
	}
	if got, want := active(q), []string{"alpha", "beta", "writing-tools:outline"}; !slices.Equal(got, want) {
		t.Fatalf("active in q = %v, want %v", got, want)
	}
	if got, want := active(p), []string{"alpha", "beta", "remote-one:gamma", "writing-tools:proofread"}; !slices.Equal(got, want) {
		t.Fatalf("active in p = %v, want %v", got, want)
	}
	if got, want := active(other), []string{"alpha", "beta"}; !slices.Equal(got, want) {
		t.Fatalf("active elsewhere = %v, want %v", got, want)
	}

	all := Evaluate(c, f.cfg, rules.Normalize(other))
	if len(all) != len(c.Skills) || !slices.IsSortedFunc(all, func(a, b Decided) int { return strings.Compare(a.Entry.ID, b.Entry.ID) }) {
		t.Fatalf("evaluate: %+v", all)
	}
	for _, d := range all {
		if d.Decision.Reason == "" {
			t.Errorf("%s: empty reason", d.Entry.ID)
		}
	}

	lv := Levels(f.cfg, c.Skills[slices.IndexFunc(c.Skills, func(e Entry) bool { return e.ID == "writing-tools:outline" })])
	if len(lv) != 3 || len(lv[0].Rules) != 1 || len(lv[2].Rules) != 1 {
		t.Fatalf("levels = %+v", lv)
	}
	if lv := Levels(f.cfg, c.Skills[0]); len(lv) != 2 { // alpha: source, skill
		t.Fatalf("local levels = %+v", lv)
	}

	// Plugin disabled (config keys are entry names): its skills are gone, its servers stay.
	m.Plugins = map[string]*config.Node{"writing": {Disabled: true}}
	if got := active(q); slices.Contains(got, "writing-tools:outline") {
		t.Fatalf("disabled plugin still active in cached catalog: %v", got)
	}
	c = build(t, f.builder, f.cfg, false)
	if slices.ContainsFunc(c.Skills, func(e Entry) bool { return e.Plugin == "writing" }) {
		t.Fatalf("disabled plugin skills listed: %v", ids(c))
	}
	if len(c.Servers) != 2 {
		t.Fatalf("servers of disabled plugin dropped: %+v", c.Servers)
	}

	// Disabled source: skipped entirely.
	f.cfg.Sources["repo"].Disabled = true
	c = build(t, f.builder, f.cfg, false)
	if slices.Contains(ids(c), "beta") || slices.ContainsFunc(c.Sources, func(s SourceState) bool { return s.Name == "repo" }) {
		t.Fatalf("disabled source present: %v %+v", ids(c), c.Sources)
	}
}

// gitScript writes a fake git binary that logs its arguments to log and then runs body.
func gitScript(t *testing.T, log, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "git")
	write(t, p, "#!/bin/sh\necho \"$@\" >> '"+log+"'\n"+body+"\n")
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildOfflineUsesCheckout(t *testing.T) {
	f := newFixture(t)
	first := build(t, f.builder, f.cfg, true)

	log := filepath.Join(t.TempDir(), "git.log")
	offline := &Builder{Fetcher: &sources.Fetcher{Root: f.root, Git: gitScript(t, log, "exit 1")}, Lock: f.builder.Lock}
	c := build(t, offline, f.cfg, false)
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		b, _ := os.ReadFile(log)
		t.Fatalf("git ran offline: %s", b)
	}
	if !slices.Equal(ids(c), ids(first)) {
		t.Fatalf("offline ids = %v, want %v", ids(c), ids(first))
	}
	for i, s := range c.Sources {
		if s != first.Sources[i] {
			t.Fatalf("source state %+v, want %+v", s, first.Sources[i])
		}
	}
}

func TestSyncDueAndFailure(t *testing.T) {
	f := newFixture(t)
	build(t, f.builder, f.cfg, true)
	log := filepath.Join(t.TempDir(), "git.log")
	f.builder.Fetcher = &sources.Fetcher{Root: f.root, Git: gitScript(t, log, `exec git "$@"`)}

	// Nothing is due within the default interval.
	build(t, f.builder, f.cfg, true)
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		b, _ := os.ReadFile(log)
		t.Fatalf("git ran though nothing was due: %s", b)
	}

	// Manual sources are never due, but run when listed.
	f.cfg.Sources["repo"].UpdateEvery = "0"
	f.cfg.Sources["market"].UpdateEvery = "0"
	f.builder.Lock = rewindLock(t, f.builder.Lock)
	build(t, f.builder, f.cfg, true)
	if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("manual source synced automatically")
	}
	st := f.builder.SyncSources(context.Background(), f.cfg, []string{"market"})
	if len(st) != 1 || st[0].Name != "market/plugins/remote-one" || st[0].Error != "" {
		t.Fatalf("states = %+v", st)
	}

	// A failing update keeps the last good checkout and records the error.
	f.cfg.Sources["repo"].URL = "file://" + filepath.Join(t.TempDir(), "missing")
	st = f.builder.SyncSources(context.Background(), f.cfg, []string{"repo", "nope"})
	if len(st) != 2 || st[0].Name != "nope" || st[0].Error == "" || st[1].Name != "repo" || st[1].Error == "" || len(st[1].Commit) != 40 {
		t.Fatalf("states = %+v", st)
	}
	c := build(t, f.builder, f.cfg, false)
	if !slices.Contains(ids(c), "beta") {
		t.Fatalf("beta disappeared after a failed update: %v", ids(c))
	}
	i := slices.IndexFunc(c.Sources, func(s SourceState) bool { return s.Name == "repo" })
	if c.Sources[i].Error == "" || len(c.Sources[i].Commit) != 40 {
		t.Fatalf("repo state = %+v", c.Sources[i])
	}
}

// rewindLock moves every FetchedAt in the lock a day back, so interval-based sources are due.
func rewindLock(t *testing.T, path string) string {
	t.Helper()
	l, err := skilllock.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for k, s := range l.Sources {
		s.FetchedAt = s.FetchedAt.Add(-24 * time.Hour)
		l.Sources[k] = s
	}
	if err := skilllock.Save(path, l); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReviewState(t *testing.T) {
	f := newFixture(t)
	c := build(t, f.builder, f.cfg, true)
	def := func(name string) config.Server {
		for _, s := range c.Servers {
			if s.Name == name {
				return s.Def
			}
		}
		t.Fatalf("server %s missing", name)
		return config.Server{}
	}
	notes, docs := def("notes"), def("docs")
	notes.Origin = &config.Origin{Source: "market", Plugin: "writing", Server: "notes", DefHash: DefHash(notes, "")}
	docs.Origin = &config.Origin{Source: "market", Plugin: "writing", Server: "docs", DefHash: DefHash(docs, "")}
	f.cfg.Servers = map[string]*config.Server{"notes": &notes, "my-docs": &docs, "plain": {Type: "stdio", Command: "demo"}}

	if got, want := ReviewState(f.cfg, c), map[string]string{"notes": "ok", "my-docs": "ok"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("review = %v, want %v", got, want)
	}

	mcp := filepath.Join(f.market, "plugins", "writing", ".mcp.json")
	b, err := os.ReadFile(mcp)
	if err != nil {
		t.Fatal(err)
	}
	write(t, mcp, string(bytes.Replace(b, []byte(`"MODE": "demo"`), []byte(`"MODE": "changed"`), 1)))
	c = build(t, f.builder, f.cfg, false)
	if got, want := ReviewState(f.cfg, c), map[string]string{"notes": "needs-review", "my-docs": "ok"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("review after change = %v, want %v", got, want)
	}

	// not fetched: a remote plugin without checkout, or a marketplace source without checkout
	if err := os.RemoveAll(f.builder.Fetcher.Dir("market/plugins/remote-one")); err != nil {
		t.Fatal(err)
	}
	remote := config.Server{Type: "stdio", Command: "demo", Origin: &config.Origin{Source: "market", Plugin: "remote-one", Server: "x", DefHash: "h"}}
	f.cfg.Servers["remote"] = &remote
	f.cfg.Sources["unfetched"] = &config.Source{Type: "marketplace", URL: "demo/unfetched"}
	away := config.Server{Type: "stdio", Command: "demo", Origin: &config.Origin{Source: "unfetched", Plugin: "p", Server: "x", DefHash: "h"}}
	f.cfg.Servers["away"] = &away
	c = build(t, f.builder, f.cfg, false)
	if got := ReviewState(f.cfg, c); got["remote"] != "not fetched yet" || got["away"] != "not fetched yet" {
		t.Fatalf("review of unfetched = %v", got)
	}
	f.cfg.Sources["market"].Disabled = true
	c = build(t, f.builder, f.cfg, false)
	if got := ReviewState(f.cfg, c); got["notes"] != "source disabled" || got["remote"] != "source disabled" {
		t.Fatalf("review with the source disabled = %v", got)
	}
	f.cfg.Sources["market"].Disabled = false
	delete(f.cfg.Servers, "remote")
	delete(f.cfg.Servers, "away")

	write(t, filepath.Join(f.market, ".claude-plugin", "marketplace.json"), `{"name": "demo-market", "plugins": []}`)
	c = build(t, f.builder, f.cfg, false)
	if got, want := ReviewState(f.cfg, c), map[string]string{"notes": "gone", "my-docs": "gone"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("review after removal = %v, want %v", got, want)
	}
	delete(f.cfg.Sources, "market")
	if got := ReviewState(f.cfg, c); got["notes"] != "gone" {
		t.Fatalf("review with the source removed = %v", got)
	}
}

func TestSaveLoad(t *testing.T) {
	t.Setenv("SKILLTRI_CACHE_DIR", filepath.Join(t.TempDir(), "cache"))
	if Path() != filepath.Join(os.Getenv("SKILLTRI_CACHE_DIR"), "catalog.json") {
		t.Fatalf("Path = %s", Path())
	}
	if _, err := Load(Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing catalog: %v", err)
	}
	c := &Catalog{
		Skills:   []Entry{{ID: "writing-tools:outline", Source: "market", Plugin: "writing", Skill: skills.Skill{Name: "outline", Description: "Draft an outline.", Dir: "/demo/outline"}}},
		Servers:  []PluginServer{{Source: "market", Plugin: "writing", Name: "notes", Def: config.Server{Type: "stdio", Command: "demo", Env: map[string]string{"B": "2", "A": "1"}}, DefHash: "h"}},
		Sources:  []SourceState{{Name: "market", Commit: strings.Repeat("a", 40), FetchedAt: time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)}},
		Warnings: []string{"w"},
		Built:    time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC),
	}
	if err := Save(Path(), c); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(Path())
	got, err := Load(Path())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("round trip:\n%+v\n%+v", got, c)
	}
	if err := Save(Path(), got); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(Path())
	if !bytes.Equal(first, second) {
		t.Fatal("saving the same catalog twice gives different bytes")
	}
	if st, _ := os.Stat(Path()); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode().Perm())
	}
}

func TestFailedFirstFetchRetriedAfterInterval(t *testing.T) {
	f := newFixture(t)
	good := f.cfg.Sources["repo"].URL
	f.cfg.Sources["repo"].URL = "file://" + filepath.Join(t.TempDir(), "missing")
	c := build(t, f.builder, f.cfg, true)
	if slices.Contains(ids(c), "beta") {
		t.Fatal("beta without a checkout")
	}
	f.cfg.Sources["repo"].URL = good
	c = build(t, f.builder, f.cfg, true) // within the interval: the failed clone waits (backoff)
	if slices.Contains(ids(c), "beta") {
		t.Fatalf("failed first fetch retried within the interval: %v", ids(c))
	}
	rewindLock(t, f.builder.Lock)
	c = build(t, f.builder, f.cfg, true)
	if !slices.Contains(ids(c), "beta") {
		t.Fatalf("failed first fetch not retried after the interval: %v %+v", ids(c), c.Sources)
	}

	// A checkout that vanished without an error is fetched again at once.
	if err := os.RemoveAll(f.builder.Fetcher.Dir("repo")); err != nil {
		t.Fatal(err)
	}
	if c = build(t, f.builder, f.cfg, true); !slices.Contains(ids(c), "beta") {
		t.Fatalf("vanished checkout not re-fetched: %v", ids(c))
	}
}

func TestPluginNameCollision(t *testing.T) {
	f := newFixture(t)
	write(t, filepath.Join(f.market, ".claude-plugin", "marketplace.json"), `{"name": "demo-market", "plugins": [
    { "name": "writing", "source": "./plugins/writing" },
    { "name": "writing-copy", "source": "./plugins/writing" }
  ]}`)
	for i := 0; i < 3; i++ {
		c := build(t, f.builder, f.cfg, false)
		i := slices.IndexFunc(c.Skills, func(e Entry) bool { return e.ID == "writing-tools:outline" })
		if i < 0 || c.Skills[i].Plugin != "writing" {
			t.Fatalf("skills = %+v", c.Skills)
		}
		want := "skill writing-tools:outline from market/writing-copy hidden by market/writing"
		if !slices.Contains(c.Warnings, want) {
			t.Fatalf("warnings = %q, want %q", c.Warnings, want)
		}
	}
}

func TestLockProblemsAreWarnings(t *testing.T) {
	f := newFixture(t)
	write(t, f.builder.Lock, "not json")
	c := build(t, f.builder, f.cfg, false)
	if !slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.HasPrefix(w, "skill lock ignored") }) {
		t.Fatalf("corrupt lock not reported: %q", c.Warnings)
	}

	blocker := filepath.Join(t.TempDir(), "file")
	write(t, blocker, "x")
	f.builder.Lock = filepath.Join(blocker, "skills.lock") // cannot be read or written
	c, err := f.builder.Build(context.Background(), f.cfg, true)
	if err != nil || c == nil {
		t.Fatalf("Build = %v, %v", c, err)
	}
	if !slices.Contains(ids(c), "beta") || !slices.ContainsFunc(c.Warnings, func(w string) bool { return strings.HasPrefix(w, "save skill lock") }) {
		t.Fatalf("ids %v, warnings %q", ids(c), c.Warnings)
	}
	for _, st := range f.builder.SyncSources(context.Background(), f.cfg, []string{"repo"}) {
		if !strings.Contains(st.Error, "skill lock ignored") || !strings.Contains(st.Error, "save skill lock") {
			t.Fatalf("state %+v", st)
		}
	}
}

func TestEvaluateSortsUnsortedCatalog(t *testing.T) {
	cfg := &config.Config{Version: 1, Sources: map[string]*config.Source{"loc": {Type: "local", Path: "/x"}}}
	c := &Catalog{Skills: []Entry{{ID: "b", Source: "loc"}, {ID: "a", Source: "loc"}}}
	got := Evaluate(c, cfg, "/")
	if got[0].Entry.ID != "a" || got[1].Entry.ID != "b" || c.Skills[0].ID != "b" {
		t.Fatalf("got %+v, catalog %+v", got, c.Skills)
	}
}

// bigMarket is a marketplace (from a folder) with n remote plugins p00…; the first half point at
// good, the rest at a repo that does not exist. It returns the marketplace folder.
func bigMarket(t *testing.T, n int, good, dead string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "big-market")
	var entries []string
	for i := range n {
		url := good
		if i >= n/2 {
			url = dead
		}
		entries = append(entries, fmt.Sprintf(`{"name": "p%02d", "source": {"source": "url", "url": "file://%s"}}`, i, url))
	}
	write(t, filepath.Join(dir, ".claude-plugin", "marketplace.json"), `{"name": "big", "plugins": [`+strings.Join(entries, ",\n")+`]}`)
	return dir
}

func countLines(t *testing.T, log, prefix string) int {
	t.Helper()
	b, err := os.ReadFile(log)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func TestDisabledPluginsNeverFetched(t *testing.T) {
	gitEnv(t)
	good := gitRepo(t, map[string]string{"skills/gamma/SKILL.md": skillFile("gamma")})
	dead := filepath.Join(t.TempDir(), "dead")
	market := bigMarket(t, 60, good, dead)
	log := filepath.Join(t.TempDir(), "git.log")
	root := filepath.Join(t.TempDir(), "sources")
	b := &Builder{Fetcher: &sources.Fetcher{Root: root, Git: gitScript(t, log, `exec git "$@"`)}, Lock: filepath.Join(t.TempDir(), "skills.lock")}
	cfg := &config.Config{Version: 1, Servers: map[string]*config.Server{}, Sources: map[string]*config.Source{
		"big": {Type: "marketplace", Path: market, PluginsDefault: config.PluginsDisabled,
			Plugins: map[string]*config.Node{"p01": {}, "p45": {}, "p02": {Disabled: true}}},
	}}

	c := build(t, b, cfg, true)
	if n := countLines(t, log, "clone"); n != 2 {
		t.Fatalf("clones = %d, want 2 (only the enabled p01 and p45)", n)
	}
	var states []string
	for _, s := range c.Sources {
		states = append(states, s.Name)
	}
	if want := []string{"big", "big/plugins/p01", "big/plugins/p45"}; !slices.Equal(states, want) {
		t.Fatalf("sources = %v, want %v", states, want)
	}
	if got := ids(c); !slices.Equal(got, []string{"p01:gamma"}) {
		t.Fatalf("ids = %v", got)
	}

	// The dead plugin is not retried within its interval although it has no checkout.
	build(t, b, cfg, true)
	if n := countLines(t, log, "clone"); n != 2 {
		t.Fatalf("clones after a second sync = %d, want 2", n)
	}
	// After the interval it is retried.
	rewindLock(t, b.Lock)
	build(t, b, cfg, true)
	if n := countLines(t, log, "clone"); n != 3 {
		t.Fatalf("clones after the interval = %d, want 3", n)
	}

	// The default "" enables every plugin without a node.
	cfg.Sources["big"].PluginsDefault = ""
	st := b.SyncSources(context.Background(), cfg, []string{"big"})
	if len(st) != 59 { // p02 stays disabled
		t.Fatalf("synced %d plugins, want 59", len(st))
	}

	// A disabled plugin with a checkout keeps its servers and state, but no skills or errors.
	cfg.Sources["big"].PluginsDefault = config.PluginsDisabled
	cfg.Sources["big"].Plugins = map[string]*config.Node{"p45": {}}
	c = build(t, b, cfg, false)
	i := slices.IndexFunc(c.Sources, func(s SourceState) bool { return s.Name == "big/plugins/p01" })
	if i < 0 || c.Sources[i].Error != "" || len(c.Sources[i].Commit) != 40 {
		t.Fatalf("disabled plugin with checkout: %+v", c.Sources)
	}
	if slices.ContainsFunc(c.Sources, func(s SourceState) bool { return s.Name == "big/plugins/p50" }) {
		t.Fatalf("disabled plugin without checkout has a state: %+v", c.Sources)
	}
	if len(c.Skills) != 0 {
		t.Fatalf("skills of disabled plugins: %v", ids(c))
	}
}

// TestReviewStateCodeChange: a server that runs code from the plugin folder needs review when the
// plugin's checkout moves, even though its definition text is unchanged.
func TestReviewStateCodeChange(t *testing.T) {
	gitEnv(t)
	plugin := gitRepo(t, map[string]string{
		".mcp.json": `{"local":{"command":"${CLAUDE_PLUGIN_ROOT}/bin/serve","args":["--demo"]},"remote":{"command":"demo-remote"}}`,
		"bin/serve": "#!/bin/sh\necho v1\n",
	})
	market := filepath.Join(t.TempDir(), "market")
	write(t, filepath.Join(market, ".claude-plugin", "marketplace.json"),
		`{"name": "m", "plugins": [{"name": "code", "source": {"source": "url", "url": "file://`+plugin+`"}}]}`)
	cfg := &config.Config{Version: 1, Servers: map[string]*config.Server{}, Sources: map[string]*config.Source{
		"mk": {Type: "marketplace", Path: market},
	}}
	b := &Builder{Fetcher: &sources.Fetcher{Root: filepath.Join(t.TempDir(), "sources")}, Lock: filepath.Join(t.TempDir(), "skills.lock")}
	c := build(t, b, cfg, true)
	for _, s := range c.Servers {
		srv := s.Def
		srv.Origin = &config.Origin{Source: s.Source, Plugin: s.Plugin, Server: s.Name, DefHash: s.DefHash}
		cfg.Servers[s.Name] = &srv
	}
	if got, want := ReviewState(cfg, c), map[string]string{"local": "ok", "remote": "ok"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("review = %v, want %v", got, want)
	}

	write(t, filepath.Join(plugin, "bin", "serve"), "#!/bin/sh\necho v2\n")
	run(t, plugin, "commit", "-q", "-am", "new server code")
	b.SyncSources(context.Background(), cfg, []string{"mk"})
	c = build(t, b, cfg, false)
	if got, want := ReviewState(cfg, c), map[string]string{"local": "needs-review", "remote": "ok"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("review after a code change = %v, want %v", got, want)
	}
}
