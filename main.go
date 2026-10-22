// Command skilltri is an MCP gateway (one daemon, many harness sessions) and agent skill manager.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/term"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/importer"
	"github.com/baeroe/skilltri/internal/paths"
	"github.com/baeroe/skilltri/internal/secrets"
)

var version = "0.1.0"

const usage = `skilltri - MCP gateway and agent skill manager

Usage:
  skilltri                  open the TUI (arrives in a later version)
  skilltri mcp              run the MCP shim on stdio (started by your agent harness)
  skilltri daemon           run the gateway daemon in the foreground
  skilltri daemon install   install and start the daemon as a login service
  skilltri daemon uninstall stop and remove the login service
  skilltri status           show daemon, servers and config errors
  skilltri which            show the resolved config, state and data paths
  skilltri import 1mcp [file] [--force]
                            import servers from a 1MCP mcp.json
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
	case "mcp", "daemon", "status", "which":
		fmt.Fprintf(os.Stderr, "skilltri %s: not implemented yet\n", cmd)
		return 1
	case "import":
		return runImport(args[1:])
	case "secret":
		return runSecret(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "skilltri: unknown command %q\n\n%s", cmd, usage)
		return 2
	}
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

	var imported []string
	err = config.Update(paths.ConfigFile(), func(c *config.Config) error {
		if c.Servers == nil {
			c.Servers = map[string]*config.Server{}
		}
		names := make([]string, 0, len(res.Servers))
		for n := range res.Servers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if _, exists := c.Servers[n]; exists && !force {
				res.Warnings = append(res.Warnings, fmt.Sprintf("skipped %q: already exists (use --force to overwrite)", n))
				continue
			}
			c.Servers[n] = res.Servers[n]
			imported = append(imported, n)
		}
		// Store only the secrets that imported servers reference.
		for _, n := range imported {
			for _, ref := range serverRefs(res.Servers[n]) {
				name := strings.TrimSuffix(strings.TrimPrefix(ref, "${secret:"), "}")
				if err := secrets.Set(name, res.Secrets[name]); err != nil {
					return fmt.Errorf("store secret %s: %w", name, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "skilltri: %v\n", err)
		return 1
	}
	for _, n := range imported {
		fmt.Printf("imported %s\n", n)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	fmt.Printf("%d server(s) imported\n", len(imported))
	return 0
}

// serverRefs returns the ${secret:NAME} references in a server's headers and env.
func serverRefs(s *config.Server) []string {
	var refs []string
	for _, m := range []map[string]string{s.Headers, s.Env} {
		for _, v := range m {
			if strings.HasPrefix(v, "${secret:") {
				refs = append(refs, v)
			}
		}
	}
	return refs
}
