// Package service installs the daemon as a login service: a launchd agent on macOS and a systemd
// user unit on Linux. Both restart the daemon when it dies; the shim still starts one on demand,
// so the service only makes the daemon survive reboots and crashes. The service runs
// `daemon --wait`, so it queues behind a shim-started daemon instead of failing and respawning,
// and gets PATH, HOME and LANG from install time because launchd hands agents a bare PATH.
package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/baeroe/skilltri/internal/paths"
)

// Label is the launchd label of the agent.
const Label = "io.github.baeroe.skilltri"

const unitName = "skilltri.service"

// bootstrapAttempts bounds the launchctl bootstrap retries right after a bootout, which fails
// with "Input/output error" while launchd still tears the old agent down.
const bootstrapAttempts = 10

// envKeys are the variables the service gets from the environment at install time.
var envKeys = []string{"PATH", "HOME", "LANG"}

// Swapped by tests so nothing touches the real LaunchAgents folder, systemd or launchctl.
var (
	goos           = runtime.GOOS
	homeDir        = os.UserHomeDir
	uid            = os.Getuid
	bootstrapDelay = 200 * time.Millisecond
	runCmd         = func(name string, args ...string) error {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
)

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// installEnv is PATH, HOME and LANG from the current environment; unset ones are left out.
func installEnv() map[string]string {
	env := map[string]string{}
	for _, k := range envKeys {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	return env
}

func sortedKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// LaunchdPlist is the launchd agent that runs `exe daemon --wait` at login with env and restarts
// it when it fails. A clean exit (`skilltri daemon stop`) is not restarted.
func LaunchdPlist(exe string, env map[string]string) string {
	var envXML string
	if len(env) > 0 {
		envXML = "\t<key>EnvironmentVariables</key>\n\t<dict>\n"
		for _, k := range sortedKeys(env) {
			envXML += "\t\t<key>" + xmlEscape(k) + "</key>\n\t\t<string>" + xmlEscape(env[k]) + "</string>\n"
		}
		envXML += "\t</dict>\n"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + xmlEscape(exe) + `</string>
		<string>daemon</string>
		<string>--wait</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
` + envXML + `	<key>StandardErrorPath</key>
	<string>` + xmlEscape(paths.LogFile()) + `</string>
</dict>
</plist>
`
}

// systemdQuote double-quotes s for a unit file: backslash and quote are escaped, and % is
// doubled so systemd does not read it as a specifier.
func systemdQuote(s string) string {
	s = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(s)
	return `"` + s + `"`
}

// SystemdUnit is the systemd user unit that runs `exe daemon --wait` with env.
func SystemdUnit(exe string, env map[string]string) string {
	var envLines string
	for _, k := range sortedKeys(env) {
		envLines += "Environment=" + systemdQuote(k+"="+env[k]) + "\n"
	}
	return `[Unit]
Description=skilltri MCP gateway daemon

[Service]
ExecStart=` + systemdQuote(exe) + ` daemon --wait
` + envLines + `Restart=on-failure

[Install]
WantedBy=default.target
`
}

func plistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

func unitPath(home string) string {
	return filepath.Join(home, ".config", "systemd", "user", unitName)
}

func domain() string { return "gui/" + strconv.Itoa(uid()) }

// Install writes the service file for exe and starts the service. It replaces an installed one.
func Install(exe string) error {
	home, err := homeDir()
	if err != nil {
		return err
	}
	switch goos {
	case "darwin":
		if err := write(plistPath(home), LaunchdPlist(exe, installEnv())); err != nil {
			return err
		}
		_ = runCmd("launchctl", "bootout", domain()+"/"+Label) // not loaded is fine
		for i := 1; ; i++ {
			err := runCmd("launchctl", "bootstrap", domain(), plistPath(home))
			if err == nil || i == bootstrapAttempts {
				return err
			}
			time.Sleep(bootstrapDelay)
		}
	case "linux":
		if err := write(unitPath(home), SystemdUnit(exe, installEnv())); err != nil {
			return err
		}
		if err := runCmd("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		return runCmd("systemctl", "--user", "enable", "--now", unitName)
	}
	return fmt.Errorf("daemon install: unsupported OS %s", goos)
}

// Uninstall stops the service and removes its file. A missing service is not an error.
func Uninstall() error {
	home, err := homeDir()
	if err != nil {
		return err
	}
	switch goos {
	case "darwin":
		_ = runCmd("launchctl", "bootout", domain()+"/"+Label)
		return remove(plistPath(home))
	case "linux":
		_ = runCmd("systemctl", "--user", "disable", "--now", unitName)
		if err := remove(unitPath(home)); err != nil {
			return err
		}
		return runCmd("systemctl", "--user", "daemon-reload")
	}
	return fmt.Errorf("daemon uninstall: unsupported OS %s", goos)
}

func write(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
