package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"

	"github.com/baeroe/skilltri/internal/audit"
	"github.com/baeroe/skilltri/internal/paths"
)

// Audit prints the last n entries of the agent audit log (the changes agents made through
// skilltri's management tools), oldest first: time, client, tool, target and ok or the error. With
// jsonOut it prints the entries as a JSON array.
func Audit(w io.Writer, n int, jsonOut bool) int {
	es, err := audit.Read(paths.AuditLog(), n)
	if err != nil {
		return fail(err)
	}
	if jsonOut {
		if es == nil {
			es = []audit.Entry{}
		}
		printJSON(w, es)
		return 0
	}
	if len(es) == 0 {
		fmt.Fprintln(w, "no agent changes recorded")
		return 0
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, e := range es {
		result := "ok"
		if !e.OK {
			result = "error: " + oneLine(e.Error)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Time.Local().Format(time.DateTime), dash(oneLine(e.Client)), e.Tool, dash(oneLine(e.Target)), result)
	}
	tw.Flush()
	return 0
}

// oneLine makes s safe for one terminal line: control characters (newlines, escape sequences
// from a client name) become spaces.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
