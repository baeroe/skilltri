//go:build ignore

// httpmcp.go serves a fake MCP server over Streamable HTTP for the README screenshots (the demo
// "tickets" server, see docs/screenshots.sh). It uses the test fake, so every tool just echoes.
//
//	go build -o httpmcp docs/demo/httpmcp.go && ./httpmcp 127.0.0.1:47391
package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/baeroe/skilltri/internal/testutil/fakemcp"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: httpmcp ADDR")
		os.Exit(2)
	}
	s := fakemcp.New(fakemcp.Spec{
		Name:    "tickets",
		Tools:   []string{"search_tickets", "get_ticket", "create_ticket", "comment_ticket", "delete_ticket"},
		Prompts: []string{"triage"},
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", h)
	if err := http.ListenAndServe(os.Args[1], mux); err != nil {
		fmt.Fprintln(os.Stderr, "httpmcp:", err)
		os.Exit(1)
	}
}
