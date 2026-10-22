// Package service installs the daemon as a login service: a launchd agent on macOS and a systemd
// user unit on Linux. Both restart the daemon when it dies; the shim still starts one on demand,
// so the service only makes the daemon survive reboots and crashes.
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
	"strconv"
	"strings"

	"github.com/baeroe/skilltri/internal/paths"
)

// Label is the launchd label of the agent.
const Label = "io.github.baeroe.skilltri"

const unitName = "skilltri.service"

// Swapped by tests so nothing touches the real LaunchAgents folder, systemd or launchctl.
var (
	goos    = runtime.GOOS
	homeDir = os.UserHomeDir
	uid     = os.Getuid
	runCmd  = func(name string, args ...string) error {
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

// LaunchdPlist is the launchd agent that runs `exe daemon` at login and keeps it alive.
func LaunchdPlist(exe string) string {
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
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(paths.LogFile()) + `</string>
</dict>
</plist>
`
}

// SystemdUnit is the systemd user unit that runs `exe daemon`.
func SystemdUnit(exe string) string {
	return `[Unit]
Description=skilltri MCP gateway daemon

[Service]
ExecStart=` + exe + ` daemon
Restart=on-failure

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
		if err := write(plistPath(home), LaunchdPlist(exe)); err != nil {
			return err
		}
		_ = runCmd("launchctl", "bootout", domain()+"/"+Label) // not loaded is fine
		return runCmd("launchctl", "bootstrap", domain(), plistPath(home))
	case "linux":
		if err := write(unitPath(home), SystemdUnit(exe)); err != nil {
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
