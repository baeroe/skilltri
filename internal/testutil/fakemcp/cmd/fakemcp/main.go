// Command fakemcp runs a fakemcp server over stdio, configured through FAKEMCP_* environment variables.
// It is built by tests; it is not part of skilltri.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/baeroe/skilltri/internal/testutil/fakemcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	spec := fakemcp.Spec{
		Name:      os.Getenv("FAKEMCP_NAME"),
		Tools:     list("FAKEMCP_TOOLS"),
		Prompts:   list("FAKEMCP_PROMPTS"),
		Resources: list("FAKEMCP_RESOURCES"),
	}
	if slow := os.Getenv("FAKEMCP_SLOW"); slow != "" {
		name, ms, _ := strings.Cut(slow, ":")
		spec.SlowTool = name
		spec.SlowMs, _ = strconv.Atoi(ms)
	}
	if f := os.Getenv("FAKEMCP_CWD_FILE"); f != "" {
		wd, _ := os.Getwd()
		if err := os.WriteFile(f, []byte(wd), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "fakemcp:", err)
			os.Exit(1)
		}
	}
	s := fakemcp.New(spec)
	if n, err := strconv.Atoi(os.Getenv("FAKEMCP_EXIT_AFTER")); err == nil {
		s.AddReceivingMiddleware(exitAfter(n))
	}
	fmt.Fprintf(os.Stderr, "fakemcp %s started (pid %d)\n", os.Getenv("FAKEMCP_NAME"), os.Getpid())
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "fakemcp:", err)
		os.Exit(1)
	}
}

// exitAfter lets n tool calls through and exits the process when call n+1 arrives, simulating a crash.
func exitAfter(n int) mcp.Middleware {
	calls := 0
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/call" {
				calls++
				if calls > n {
					fmt.Fprintf(os.Stderr, "fakemcp: crashing after %d tool calls\n", n)
					os.Exit(3)
				}
			}
			return next(ctx, method, req)
		}
	}
}

func list(key string) []string {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}
