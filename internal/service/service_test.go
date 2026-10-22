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

var demoEnv = map[string]string{"PATH": "/opt/demo/bin:/usr/bin:/bin", "HOME": "/home/demo", "LANG": "en_US.UTF-8"}

func TestLaunchdPlist(t *testing.T) {
	t.Setenv("SKILLTRI_STATE_DIR", "/state")
	p := LaunchdPlist("/opt/bin/skilltri", demoEnv)
	for _, want := range []string{
		"<string>io.github.baeroe.skilltri</string>",
		"<key>ProgramArguments</key>\n\t<array>\n\t\t<string>/opt/bin/skilltri</string>\n\t\t<string>daemon</string>\n\t\t<string>--wait</string>\n\t</array>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>",
		"<key>EnvironmentVariables</key>\n\t<dict>\n" +
			"\t\t<key>HOME</key>\n\t\t<string>/home/demo</string>\n" +
			"\t\t<key>LANG</key>\n\t\t<string>en_US.UTF-8</string>\n" +
			"\t\t<key>PATH</key>\n\t\t<string>/opt/demo/bin:/usr/bin:/bin</string>\n\t</dict>",
		"<key>StandardErrorPath</key>\n\t<string>" + paths.LogFile() + "</string>",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q:\n%s", want, p)
		}
	}
	if !strings.Contains(LaunchdPlist("/a&b/skilltri", nil), "/a&amp;b/skilltri") {
		t.Error("exe path is not XML-escaped")
	}
	if !strings.Contains(LaunchdPlist("/x", map[string]string{"PATH": "/a<b"}), "<string>/a&lt;b</string>") {
		t.Error("env value is not XML-escaped")
	}
	if strings.Contains(LaunchdPlist("/x", nil), "EnvironmentVariables") {
		t.Error("empty env written")
	}
}

func TestSystemdUnit(t *testing.T) {
	u := SystemdUnit("/opt/bin/skilltri", demoEnv)
	for _, want := range []string{
		`ExecStart="/opt/bin/skilltri" daemon --wait`,
		`Environment="HOME=/home/demo"` + "\n" + `Environment="LANG=en_US.UTF-8"` + "\n" + `Environment="PATH=/opt/demo/bin:/usr/bin:/bin"`,
		"Restart=on-failure", "WantedBy=default.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
	odd := SystemdUnit(`/opt/my bin/100%/sk"i\lltri`, map[string]string{"PATH": "/a%b"})
	for _, want := range []string{`ExecStart="/opt/my bin/100%%/sk\"i\\lltri" daemon --wait`, `Environment="PATH=/a%%b"`} {
		if !strings.Contains(odd, want) {
			t.Errorf("unit lacks %q:\n%s", want, odd)
		}
	}
}

func TestInstallEnv(t *testing.T) {
	fake(t, "darwin")
	t.Setenv("PATH", "/opt/demo/bin:/usr/bin")
	t.Setenv("HOME", "/home/demo")
	t.Setenv("LANG", "")
	t.Setenv("CLAUDECODE", "1")
	got := installEnv()
	want := map[string]string{"PATH": "/opt/demo/bin:/usr/bin", "HOME": "/home/demo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("installEnv = %v, want %v", got, want)
	}
}

type recorder struct {
	cmds [][]string
	fail map[string]int // first word after the binary, e.g. "bootout": failures left, -1 always
}

func (r *recorder) run(name string, args ...string) error {
	r.cmds = append(r.cmds, append([]string{name}, args...))
	if len(args) > 0 && r.fail[args[0]] != 0 {
		if r.fail[args[0]] > 0 {
			r.fail[args[0]]--
		}
		return errors.New("boom")
	}
	return nil
}

// fake points the package at a temp home, a recording runner and the given OS.
func fake(t *testing.T, goosName string) (*recorder, string) {
	t.Helper()
	home := t.TempDir()
	r := &recorder{fail: map[string]int{}}
	oldOS, oldHome, oldRun, oldUID, oldDelay := goos, homeDir, runCmd, uid, bootstrapDelay
	goos, homeDir, runCmd, uid, bootstrapDelay = goosName, func() (string, error) { return home, nil }, r.run, func() int { return 501 }, 0
	t.Cleanup(func() { goos, homeDir, runCmd, uid, bootstrapDelay = oldOS, oldHome, oldRun, oldUID, oldDelay })
	return r, home
}

func TestInstallUninstallDarwin(t *testing.T) {
	r, home := fake(t, "darwin")
	r.fail["bootout"] = -1 // not loaded yet: ignored
	if err := Install("/opt/bin/skilltri"); err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	b, err := os.ReadFile(plist)
	if err != nil || string(b) != LaunchdPlist("/opt/bin/skilltri", installEnv()) {
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
	r.fail["bootstrap"] = -1
	if err := Install("/x/skilltri"); err == nil {
		t.Fatal("expected error")
	}
	if n := countCmd(r, "bootstrap"); n != bootstrapAttempts {
		t.Fatalf("bootstrap tried %d times, want %d", n, bootstrapAttempts)
	}
}

// After bootout, launchd may still be tearing the old agent down and bootstrap fails for a moment.
func TestBootstrapRetriesAfterBootout(t *testing.T) {
	r, _ := fake(t, "darwin")
	r.fail["bootstrap"] = 2
	if err := Install("/x/skilltri"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if n := countCmd(r, "bootstrap"); n != 3 {
		t.Fatalf("bootstrap tried %d times, want 3", n)
	}
}

func countCmd(r *recorder, sub string) int {
	n := 0
	for _, c := range r.cmds {
		if len(c) > 1 && c[1] == sub {
			n++
		}
	}
	return n
}

func TestInstallUninstallLinux(t *testing.T) {
	r, home := fake(t, "linux")
	if err := Install("/opt/bin/skilltri"); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(home, ".config", "systemd", "user", "skilltri.service")
	b, err := os.ReadFile(unit)
	if err != nil || string(b) != SystemdUnit("/opt/bin/skilltri", installEnv()) {
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

func TestFileAndInstalled(t *testing.T) {
	for _, os_ := range []string{"darwin", "linux"} {
		_, home := fake(t, os_)
		f, err := File()
		if err != nil || !strings.HasPrefix(f, home) {
			t.Fatalf("%s: File = %q, %v", os_, f, err)
		}
		if Installed() {
			t.Fatalf("%s: installed before Install", os_)
		}
		if err := Install("/opt/bin/skilltri"); err != nil {
			t.Fatal(err)
		}
		if !Installed() {
			t.Fatalf("%s: not installed after Install", os_)
		}
		if err := Uninstall(); err != nil || Installed() {
			t.Fatalf("%s: installed after Uninstall (%v)", os_, err)
		}
	}
	fake(t, "plan9")
	if _, err := File(); err == nil || Installed() {
		t.Fatal("plan9: expected no service file")
	}
}
