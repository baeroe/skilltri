// Package paths resolves skilltri's config, state, data and cache directories and the files in them.
// Each directory has its own override variable, then the XDG variable, then a fixed default under $HOME
// (also on macOS, so the layout is the same everywhere).
package paths

import (
	"os"
	"path/filepath"
)

const app = "skilltri"

// dir resolves one directory: override env, then $xdgEnv/skilltri, then $HOME/<fallback>/skilltri.
func dir(override, xdgEnv, fallback string) string {
	if v := os.Getenv(override); v != "" {
		return v
	}
	if v := os.Getenv(xdgEnv); v != "" {
		return filepath.Join(v, app)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, fallback, app)
}

// ConfigDir holds config.json.
func ConfigDir() string { return dir("SKILLTRI_CONFIG_DIR", "XDG_CONFIG_HOME", ".config") }

// StateDir holds the daemon socket, lock and log.
func StateDir() string { return dir("SKILLTRI_STATE_DIR", "XDG_STATE_HOME", ".local/state") }

// DataDir holds the skill store.
func DataDir() string { return dir("SKILLTRI_DATA_DIR", "XDG_DATA_HOME", ".local/share") }

// CacheDir holds disposable caches.
func CacheDir() string { return dir("SKILLTRI_CACHE_DIR", "XDG_CACHE_HOME", ".cache") }

// ConfigFile is the path of config.json.
func ConfigFile() string { return filepath.Join(ConfigDir(), "config.json") }

// SocketPath is $SKILLTRI_SOCKET if set, else the daemon socket in the state dir.
func SocketPath() string {
	if v := os.Getenv("SKILLTRI_SOCKET"); v != "" {
		return v
	}
	return filepath.Join(StateDir(), "daemon.sock")
}

// DaemonLock is the lock file that guarantees a single daemon.
func DaemonLock() string { return filepath.Join(StateDir(), "daemon.lock") }

// LogFile is the daemon log.
func LogFile() string { return filepath.Join(StateDir(), "daemon.log") }

// AuditLog is the log of the agent tools' write calls (one JSON object per line).
func AuditLog() string { return filepath.Join(StateDir(), "audit.jsonl") }
