package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/doctor"
)

// fakeDoctor records the runs of Options.Doctor and returns findings.
type fakeDoctor struct {
	findings []doctor.Finding
	runs     int
	ctx      context.Context // of the last run
	env      doctor.Env
}

func (f *fakeDoctor) install(h *harness) {
	h.m.opts.Doctor = func(ctx context.Context, e doctor.Env) []doctor.Finding {
		f.runs++
		f.ctx, f.env = ctx, e
		return f.findings
	}
}

func demoFindings() []doctor.Finding {
	return []doctor.Finding{
		{Severity: doctor.Error, Check: "sources", Subject: "demo-src", Message: "fetch failed: demo remote unreachable", Fix: "skilltri update demo-src"},
		{Severity: doctor.Warning, Check: "plugin-servers", Subject: "demo-1", Message: "plugin definition changed, needs review", Fix: "skilltri plugin-server review demo-1"},
		{Severity: doctor.Info, Check: "shell-hook", Subject: "SKILLTRI_HOOK", Message: "the shell hook is not loaded", Fix: `add eval "$(skilltri shell-init zsh)" to ~/.zshrc`},
	}
}

func doctorConfig() *config.Config {
	cfg := demoConfig(1)
	cfg.Sources["demo-src"] = &config.Source{Type: "local", Path: "/work/demo-skills"}
	return cfg
}

func TestDoctorRunsOnFirstOpenAndRerun(t *testing.T) {
	h := newHarness(t, TabServers)
	fd := &fakeDoctor{findings: demoFindings()}
	fd.install(h)
	h.writeConfig(doctorConfig())
	h.load()
	if fd.runs != 0 {
		t.Fatal("doctor ran before its tab was opened")
	}
	h.key("5")
	if fd.runs != 1 {
		t.Fatalf("runs = %d after opening", fd.runs)
	}
	if fd.env.ConfigPath != h.m.opts.Env.ConfigPath || fd.env.Home != h.m.opts.Home || fd.env.Socket != h.m.opts.Env.Socket ||
		!strings.HasSuffix(fd.env.Environ[len(fd.env.Environ)-1], "PWD=/work/demo") {
		t.Errorf("env = %+v", fd.env)
	}
	v := screen(h)
	for _, want := range []string{"Errors", "✗ demo-src", "Warnings", "! demo-1", "Hints", "i SKILLTRI_HOOK",
		"fix:", "skilltri update demo-src", "1 error, 1 warning, 1 hint"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	h.key("j")
	if !strings.Contains(screen(h), "skilltri plugin-server review demo-1") {
		t.Errorf("details do not follow the selection:\n%s", screen(h))
	}
	h.key("1", "5")
	if fd.runs != 1 {
		t.Fatalf("reopening ran again: %d", fd.runs)
	}
	fd.findings = nil
	h.key("r")
	if fd.runs != 2 || !strings.Contains(screen(h), "✓ no problems found") {
		t.Fatalf("rerun: %d\n%s", fd.runs, screen(h))
	}
}

func TestDoctorInitialTab(t *testing.T) {
	h := newHarness(t, TabDoctor)
	fd := &fakeDoctor{findings: demoFindings()}
	fd.install(h)
	h.load()
	if fd.runs != 1 || !strings.Contains(screen(h), "demo-src") {
		t.Fatalf("runs = %d\n%s", fd.runs, screen(h))
	}
}

func TestDoctorJump(t *testing.T) {
	h := newHarness(t, TabDoctor)
	fd := &fakeDoctor{findings: demoFindings()}
	fd.install(h)
	h.writeConfig(doctorConfig())
	h.load()
	h.key("enter")
	if h.m.tab != TabSkills || h.m.tabs[TabSkills].(*skillsTab).sel != srcKey("demo-src") {
		t.Fatalf("source jump: tab %v sel %q", h.m.tab, h.m.tabs[TabSkills].(*skillsTab).sel)
	}
	h.key("5", "j", "enter")
	if h.m.tab != TabServers || h.m.tabs[TabServers].(*serversTab).sel != "demo-1" {
		t.Fatalf("server jump: tab %v", h.m.tab)
	}
	h.key("5", "j", "enter")
	if h.m.tab != TabDoctor || !strings.Contains(screen(h), "is not a server or source") {
		t.Errorf("jump from a hint:\n%s", screen(h))
	}
}

// Review Focus 4: esc cancels a running doctor (its context is done, the late result dropped) and
// quitting cancels it too.
func TestDoctorCancel(t *testing.T) {
	h := newHarness(t, TabServers)
	fd := &fakeDoctor{findings: demoFindings()}
	fd.install(h)
	h.load()
	_, run := h.m.Update(keyMsg("5"))
	if !strings.Contains(screen(h), "running the doctor … (esc on Doctor cancels)") || !strings.Contains(screen(h), "running the checks") {
		t.Fatalf("no spinner:\n%s", screen(h))
	}
	h.key("esc")
	if h.m.busy(doctorWork) || !strings.Contains(screen(h), "doctor cancelled") {
		t.Fatalf("esc did not cancel:\n%s", screen(h))
	}
	h.run(run)
	if fd.ctx.Err() == nil {
		t.Error("cancelled run got a live context")
	}
	if strings.Contains(screen(h), "demo-src") {
		t.Errorf("cancelled result shown:\n%s", screen(h))
	}
	// a new run supersedes; quit cancels it
	_, run = h.m.Update(keyMsg("r"))
	h.m.Update(keyMsg("ctrl+c"))
	h.run(run)
	if fd.ctx.Err() == nil {
		t.Error("quit left the run's context live")
	}
}

func TestDoctorOverflow(t *testing.T) {
	h := newHarness(t, TabDoctor)
	long := strings.Repeat("n", 60)
	fd := &fakeDoctor{}
	for i := 0; i < 30; i++ {
		fd.findings = append(fd.findings, doctor.Finding{Severity: []string{doctor.Error, doctor.Warning, doctor.Info}[i%3], Check: "views",
			Subject: long, Message: strings.Repeat("message ", 30), Fix: "/" + strings.Repeat("deep/", 40)})
	}
	fd.install(h)
	h.load()
	for _, sz := range [][2]int{{40, 12}, {89, 20}, {90, 20}, {300, 50}, {60, 30}} {
		h.size(sz[0], sz[1])
		for _, keys := range [][]string{nil, {"j", "j"}, {"k"}} {
			h.key(keys...)
			lines := strings.Split(ansi.Strip(h.m.View()), "\n")
			if len(lines) != sz[1] {
				t.Errorf("%dx%d: %d lines", sz[0], sz[1], len(lines))
			}
			for _, l := range lines {
				if width(l) > sz[0] {
					t.Errorf("%dx%d: line of %d cells: %q", sz[0], sz[1], width(l), l)
				}
			}
		}
	}
}
