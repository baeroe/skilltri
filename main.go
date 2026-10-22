// Command skilltri is an MCP gateway (one daemon, many harness sessions) and agent skill manager.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/baeroe/skilltri/internal/adapters"
	"github.com/baeroe/skilltri/internal/cli"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/gateway"
	"github.com/baeroe/skilltri/internal/hook"
	"github.com/baeroe/skilltri/internal/importer"
	"github.com/baeroe/skilltri/internal/paths"
	"github.com/baeroe/skilltri/internal/secrets"
	"github.com/baeroe/skilltri/internal/service"
	"github.com/baeroe/skilltri/internal/shim"
)

var version = "0.1.0"

const usage = `skilltri - MCP gateway and agent skill manager

Usage:
  skilltri                  open the TUI (arrives in a later version)
  skilltri mcp [--cwd DIR]  run the MCP shim on stdio (started by your agent harness)
  skilltri daemon [--wait]  run the gateway daemon in the foreground
                            (--wait: wait for a running daemon to exit, then take over)
  skilltri daemon stop      stop the running daemon
  skilltri daemon install   install and start the daemon as a login service
  skilltri daemon uninstall stop and remove the login service
  skilltri status [--json]  show daemon, servers and config errors
  skilltri which [dir] [--json]
                            show which servers and tools a session in dir gets
  skilltri import 1mcp [file] [--force]
                            import servers from a 1MCP mcp.json
  skilltri env [dir] [--shell zsh|bash|fish]
                            print the skill variables for a folder (used by the hook)
  skilltri shell-init SHELL print the shell hook (zsh, bash, fish), for your rc file:
                            eval "$(skilltri shell-init zsh)"
  skilltri run HARNESS [args...]
                            start a harness with the skills of the working directory
  skilltri secret set NAME  store a secret in the keyring
  skilltri secret delete NAME
  skilltri version
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "":
		fmt.Print(usage)
		fmt.Println("\nThe TUI arrives in a later version.")
		return 0
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	case "version", "-v", "--version":
		fmt.Println(version)
		return 0
	case "daemon":
		return runDaemon(args[1:])
	case "mcp":
		return runMCP(args[1:])
	case "status":
		return runStatus(args[1:])
	case "which":
		return runWhich(args[1:])
	case "import":
		return runImport(args[1:])
	case "secret":
		return runSecret(args[1:])
	case "env":
		return runEnv(args[1:], os.Stdout, os.Stderr)
	case "shell-init":
		return runShellInit(args[1:])
	case "run":
		return runHarness(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "skilltri: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}

// runDaemon runs the gateway daemon in the foreground until SIGINT, SIGTERM or `daemon stop`,
// logging to stderr. With --wait it queues behind a running daemon instead of exiting.
func runDaemon(args []string) int {
	wait := false
	if len(args) > 0 {
		switch {
		case len(args) == 1 && (args[0] == "install" || args[0] == "uninstall"):
			return runService(args[0])
		case len(args) == 1 && args[0] == "stop":
			return cli.Stop(os.Stdout)
		case len(args) == 1 && args[0] == "--wait":
			wait = true
		default:
			fmt.Fprint(os.Stderr, "usage: skilltri daemon [--wait | stop | install | uninstall]\n")
			return 2
		}
	}
	gateway.Version = version
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	d := daemon.New(daemon.Options{
		ConfigPath: paths.ConfigFile(),
		SocketPath: paths.SocketPath(),
		LockPath:   paths.DaemonLock(),
		Version:    version,
		Logger:     log.New(os.Stderr, "skilltri daemon: ", log.LstdFlags),
		Wait:       wait,
	})
	if err := d.Run(ctx); err != nil {
		if errors.Is(err, daemon.ErrAlreadyRunning) {
			fmt.Fprintln(os.Stderr, "skilltri: daemon already running")
		} else {
			fmt.Fprintf(os.Stderr, "skilltri: %v\n", err)
		}
		return 1
	}
	return 0
}

// runMCP is the stdio shim: it connects to the daemon (starting it if needed) and relays the
// harness's MCP traffic. The session folder is --cwd or $SKILLTRI_CWD (explicit: the client's
// roots never override it), else the working directory.
func runMCP(args []string) int {
	const usageMsg = "usage: skilltri mcp [--cwd DIR]\n"
	h := daemon.Hello{V: 1, Kind: "session"}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--cwd" && i+1 < len(args):
			i++
			h.Dir = args[i]
		case strings.HasPrefix(a, "--cwd="):
			h.Dir = strings.TrimPrefix(a, "--cwd=")
		default:
			fmt.Fprint(os.Stderr, usageMsg)
			return 2
		}
	}
	if h.Dir == "" {
		h.Dir = os.Getenv("SKILLTRI_CWD")
	}
	var err error
	if h.Dir != "" {
		h.DirExplicit = true
		h.Dir, err = filepath.Abs(h.Dir)
	} else {
		h.Dir, err = os.Getwd()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "skilltri mcp: %v\n", err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "skilltri mcp: %v\n", err)
		return 1
	}
	conn, err := shim.Dial(paths.SocketPath(), func() error { return shim.StartDetached(exe) })
	if err != nil {
		fmt.Fprintf(os.Stderr, "skilltri mcp: %v (daemon log: %s)\n", err, paths.LogFile())
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := shim.Run(ctx, os.Stdin, os.Stdout, conn, h); err != nil {
		fmt.Fprintf(os.Stderr, "skilltri mcp: %v\n", err)
		return 1
	}
	return 0
}

func runSecret(args []string) int {
	if len(args) != 2 || (args[0] != "set" && args[0] != "delete") {
		fmt.Fprint(os.Stderr, "usage: skilltri secret set NAME | skilltri secret delete NAME\n")
		return 2
	}
	action, name := args[0], args[1]
	if !secrets.ValidName(name) {
		fmt.Fprintf(os.Stderr, "skilltri: invalid secret name %q (allowed: A-Z a-z 0-9 . _ -)\n", name)
		return 2
	}
	if action == "delete" {
		if err := secrets.Delete(name); err != nil {
			fmt.Fprintf(os.Stderr, "skilltri: %v\n", err)
			return 1
		}
		fmt.Printf("deleted secret %s\n", name)
		return 0
	}
	value, err := readSecret(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skilltri: %v\n", err)
		return 1
	}
	if value == "" {
		fmt.Fprintln(os.Stderr, "skilltri: empty secret, nothing stored")
		return 1
	}
	if err := secrets.Set(name, value); err != nil {
		fmt.Fprintf(os.Stderr, "skilltri: %v\n", err)
		return 1
	}
	fmt.Printf("stored secret %s (reference it as %s)\n", name, secrets.Ref(name))
	return 0
}

// readSecret prompts without echo on a terminal, otherwise reads stdin to EOF.
func readSecret(name string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprintf(os.Stderr, "Value for %s: ", name)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return string(b), err
	}
	b, err := io.ReadAll(os.Stdin)
	return strings.TrimRight(string(b), "\r\n"), err
}

func runImport(args []string) int {
	const usageMsg = "usage: skilltri import 1mcp [file] [--force]\n"
	if len(args) == 0 || args[0] != "1mcp" {
		fmt.Fprint(os.Stderr, usageMsg)
		return 2
	}
	force, file := false, ""
	for _, a := range args[1:] {
		switch {
		case a == "--force":
			force = true
		case file == "" && !strings.HasPrefix(a, "-"):
			file = a
		default:
			fmt.Fprint(os.Stderr, usageMsg)
			return 2
		}
	}
	if file == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "skilltri: %v\n", err)
			return 1
		}
		file = filepath.Join(home, ".config", "1mcp", "mcp.json")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skilltri: %v\n", err)
		return 1
	}
	res, err := importer.From1MCP(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skilltri: %v\n", err)
		return 1
	}

	return importInto(paths.ConfigFile(), res, force, os.Stdout, os.Stderr)
}

// runStatus implements `skilltri status [--json]`.
func runStatus(args []string) int {
	if len(args) > 1 || (len(args) == 1 && args[0] != "--json") {
		fmt.Fprint(os.Stderr, "usage: skilltri status [--json]\n")
		return 2
	}
	return cli.Status(os.Stdout, len(args) == 1)
}

// runWhich implements `skilltri which [dir] [--json]`; dir defaults to the working directory.
func runWhich(args []string) int {
	jsonOut, dir := false, ""
	for _, a := range args {
		switch {
		case a == "--json":
			jsonOut = true
		case dir == "" && !strings.HasPrefix(a, "-"):
			dir = a
		default:
			fmt.Fprint(os.Stderr, "usage: skilltri which [dir] [--json]\n")
			return 2
		}
	}
	return cli.Which(os.Stdout, dir, jsonOut)
}

// runService installs or uninstalls the login service for the running binary.
func runService(action string) int {
	var err error
	if action == "install" {
		var exe string
		if exe, err = os.Executable(); err == nil {
			if exe, err = filepath.EvalSymlinks(exe); err == nil {
				err = service.Install(exe)
			}
		}
	} else {
		err = service.Uninstall()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "skilltri: daemon %s: %v\n", action, err)
		return 1
	}
	if action == "install" {
		fmt.Println("daemon service installed and started")
	} else {
		fmt.Println("daemon service stopped and removed")
	}
	return 0
}

// runEnv implements `skilltri env [dir] [--shell zsh|bash|fish]`: stdout gets only shell
// statements (written at once), notes go to stderr. Any problem with the folder, config or catalog
// still exits 0 with "no skill env". The shell defaults to $SKILLTRI_HOOK, else the zsh/bash form.
func runEnv(args []string, stdout, stderr io.Writer) int {
	const usageMsg = "usage: skilltri env [dir] [--shell zsh|bash|fish]\n"
	shell, dir := "", ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--shell" && i+1 < len(args):
			i++
			shell = args[i]
		case strings.HasPrefix(a, "--shell="):
			shell = strings.TrimPrefix(a, "--shell=")
		case dir == "" && !strings.HasPrefix(a, "-"):
			dir = a
		default:
			fmt.Fprint(stderr, usageMsg)
			return 2
		}
	}
	if shell == "" {
		if _, err := hook.Init(os.Getenv("SKILLTRI_HOOK")); err == nil {
			shell = os.Getenv("SKILLTRI_HOOK")
		}
	} else if _, err := hook.Init(shell); err != nil {
		fmt.Fprint(stderr, usageMsg)
		return 2
	}
	if dir == "" {
		dir = workDir()
	}
	e := hook.Compute(dir, os.Environ())
	for _, n := range e.Notes {
		fmt.Fprintf(stderr, "skilltri env: %s\n", n)
	}
	io.WriteString(stdout, hook.Render(e, shell))
	return 0
}

// workDir is the working directory, or $PWD when it cannot be determined (for example because the
// folder was deleted while the shell was in it).
func workDir() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return os.Getenv("PWD")
}

// runShellInit implements `skilltri shell-init SHELL`.
func runShellInit(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, "usage: skilltri shell-init zsh|bash|fish\n")
		return 2
	}
	s, err := hook.Init(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "skilltri: %v\n", err)
		return 2
	}
	fmt.Print(s)
	return 0
}

// runHarness implements `skilltri run HARNESS [args...]`: it computes the skill env of the working
// directory and execs the harness binary (claude for claude-code, else the adapter id) with it,
// passing args through unchanged.
func runHarness(args []string) int {
	var ids []string
	for _, a := range adapters.Registry {
		ids = append(ids, a.ID)
	}
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: skilltri run HARNESS [args...] (HARNESS: %s)\n", strings.Join(ids, ", "))
		return 2
	}
	a, ok := adapters.ByID(args[0])
	if !ok {
		fmt.Fprintf(os.Stderr, "skilltri run: unknown harness %q (known: %s)\n", args[0], strings.Join(ids, ", "))
		return 2
	}
	bin := a.ID
	if a.ID == "claude-code" {
		bin = "claude"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skilltri run: %s not found on PATH\n", bin)
		return 127
	}
	e := hook.Compute(workDir(), os.Environ())
	for _, n := range e.Notes {
		fmt.Fprintf(os.Stderr, "skilltri run: %s\n", n)
	}
	argv := append([]string{bin}, args[1:]...)
	err = syscall.Exec(path, argv, mergeEnv(os.Environ(), e))
	fmt.Fprintf(os.Stderr, "skilltri run: exec %s: %v\n", path, err)
	return 126
}

// mergeEnv applies e to environ: unset variables are dropped, set ones replace or add an entry.
func mergeEnv(environ []string, e hook.Env) []string {
	drop := map[string]bool{}
	for _, k := range e.Unset {
		drop[k] = true
	}
	for k := range e.Set {
		drop[k] = true
	}
	out := make([]string, 0, len(environ)+len(e.Set))
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(e.Set))
	for k := range e.Set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+e.Set[k])
	}
	return out
}
