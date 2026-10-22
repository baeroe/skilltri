package shim

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// keptVars are taken from the shim's environment into the daemon's; everything else the harness
// set (project PATH, CLAUDECODE, direnv secrets) stays out, because every downstream server
// inherits the daemon's environment.
var keptVars = []string{"HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TMPDIR", "SHELL"}

// basePath is the PATH a login shell starts from, so its profile builds a clean PATH.
const basePath = "/usr/bin:/bin:/usr/sbin:/sbin"

// loginPathTimeout bounds the login shell started to read PATH; tests shorten it.
var loginPathTimeout = 3 * time.Second

// daemonEnv is the environment of a shim-started daemon: keptVars and SKILLTRI_* (without the
// session-specific SKILLTRI_CWD) from environ, and PATH from loginPath($SHELL), falling back to
// environ's PATH. Sorted by name.
func daemonEnv(environ []string, loginPath func(shell string) (string, error)) []string {
	vars := map[string]string{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			vars[k] = v
		}
	}
	out := map[string]string{}
	for _, k := range keptVars {
		if v := vars[k]; v != "" {
			out[k] = v
		}
	}
	for k, v := range vars {
		if strings.HasPrefix(k, "SKILLTRI_") && k != "SKILLTRI_CWD" {
			out[k] = v
		}
	}
	path := vars["PATH"]
	if shell := vars["SHELL"]; shell != "" {
		if p, err := loginPath(shell); err == nil && p != "" {
			path = p
		}
	}
	if path != "" {
		out["PATH"] = path
	}
	env := make([]string, 0, len(out))
	for k, v := range out {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	return env
}

// loginShellPath asks `shell -lc` for the PATH its login profile sets up. The shell runs with a
// minimal environment so the harness's PATH does not leak into the result. Only the last output
// line counts, in case the profile prints something.
func loginShellPath(shell string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), loginPathTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-lc", `printf %s "$PATH"`)
	cmd.Env = []string{"PATH=" + basePath}
	for _, k := range keptVars {
		if v := os.Getenv(k); v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.WaitDelay = 500 * time.Millisecond // a profile's background child may hold stdout open
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(out))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return "", errors.New("login shell printed no PATH")
	}
	return s, nil
}
