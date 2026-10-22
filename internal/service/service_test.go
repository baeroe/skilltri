package service

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/baeroe/skilltri/internal/paths"
)

func TestLaunchdPlist(t *testing.T) {
	t.Setenv("SKILLTRI_STATE_DIR", "/state")
	p := LaunchdPlist("/opt/bin/skilltri")
	for _, want := range []string{
		"<string>io.github.baeroe.skilltri</string>",
		"<key>ProgramArguments</key>\n\t<array>\n\t\t<string>/opt/bin/skilltri</string>\n\t\t<string>daemon</string>\n\t</array>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>\n\t<true/>",
		"<key>StandardErrorPath</key>\n\t<string>" + paths.LogFile() + "</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q:\n%s", want, p)
		}
	}
	if !strings.Contains(LaunchdPlist("/a&b/skilltri"), "/a&amp;b/skilltri") {
		t.Error("exe path is not XML-escaped")
	}
}

func TestSystemdUnit(t *testing.T) {
	u := SystemdUnit("/opt/bin/skilltri")
	for _, want := range []string{"ExecStart=/opt/bin/skilltri daemon", "Restart=on-failure", "WantedBy=default.target"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
}

type recorder struct {
	cmds [][]string
	fail map[string]bool // first word after the binary, e.g. "bootout"
}

func (r *recorder) run(name string, args ...string) error {
	r.cmds = append(r.cmds, append([]string{name}, args...))
	if len(args) > 0 && r.fail[args[0]] {
		return errors.New("boom")
	}
	return nil
}

// fake points the package at a temp home, a recording runner and the given OS.
func fake(t *testing.T, goosName string) (*recorder, string) {
	t.Helper()
	home := t.TempDir()
	r := &recorder{fail: map[string]bool{}}
	oldOS, oldHome, oldRun, oldUID := goos, homeDir, runCmd, uid
	goos, homeDir, runCmd, uid = goosName, func() (string, error) { return home, nil }, r.run, func() int { return 501 }
	t.Cleanup(func() { goos, homeDir, runCmd, uid = oldOS, oldHome, oldRun, oldUID })
	return r, home
}

func TestInstallUninstallDarwin(t *testing.T) {
	r, home := fake(t, "darwin")
	r.fail["bootout"] = true // not loaded yet: ignored
	if err := Install("/opt/bin/skilltri"); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	b, err := os.ReadFile(plist)
	if err != nil || string(b) != LaunchdPlist("/opt/bin/skilltri") {
		t.Fatalf("plist not written: %v", err)
	}
	want := [][]string{
		{"launchctl", "bootout", "gui/501/" + Label},
		{"launchctl", "bootstrap", "gui/501", plist},
	}
	if !reflect.DeepEqual(r.cmds, want) {
		t.Fatalf("install cmds = %v, want %v", r.cmds, want)
	}

	r.cmds = nil
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Errorf("plist still there: %v", err)
	}
	if want := [][]string{{"launchctl", "bootout", "gui/501/" + Label}}; !reflect.DeepEqual(r.cmds, want) {
		t.Fatalf("uninstall cmds = %v, want %v", r.cmds, want)
	}
	// Uninstalling again is not an error.
	if err := Uninstall(); err != nil {
		t.Errorf("second uninstall: %v", err)
	}
}

func TestBootstrapFailureIsReported(t *testing.T) {
	r, _ := fake(t, "darwin")
	r.fail["bootstrap"] = true
	if err := Install("/x/skilltri"); err == nil {
		t.Fatal("expected error")
	}
}

func TestInstallUninstallLinux(t *testing.T) {
	r, home := fake(t, "linux")
	if err := Install("/opt/bin/skilltri"); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(home, ".config", "systemd", "user", "skilltri.service")
	b, err := os.ReadFile(unit)
	if err != nil || string(b) != SystemdUnit("/opt/bin/skilltri") {
		t.Fatalf("unit not written: %v", err)
	}
	want := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", "skilltri.service"},
	}
	if !reflect.DeepEqual(r.cmds, want) {
		t.Fatalf("install cmds = %v, want %v", r.cmds, want)
	}

	r.cmds = nil
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Errorf("unit still there: %v", err)
	}
	want = [][]string{
		{"systemctl", "--user", "disable", "--now", "skilltri.service"},
		{"systemctl", "--user", "daemon-reload"},
	}
	if !reflect.DeepEqual(r.cmds, want) {
		t.Fatalf("uninstall cmds = %v, want %v", r.cmds, want)
	}
}

func TestUnsupportedOS(t *testing.T) {
	fake(t, "plan9")
	if Install("/x") == nil || Uninstall() == nil {
		t.Fatal("expected errors")
	}
}
