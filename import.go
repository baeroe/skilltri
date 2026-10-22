package main

import (
	"fmt"
	"io"

	"github.com/baeroe/skilltri/internal/importer"
)

// importInto merges res into the config at cfgPath (importer.Apply1MCP) and prints the result.
func importInto(cfgPath string, res importer.Result, force bool, out, errOut io.Writer) int {
	a, err := importer.Apply1MCP(cfgPath, res, force)
	for _, w := range a.Warnings {
		fmt.Fprintf(errOut, "warning: %s\n", w)
	}
	if err != nil {
		fmt.Fprintf(errOut, "skilltri: %v\n", err)
		return 1
	}
	for _, n := range a.Imported {
		fmt.Fprintf(out, "imported %s\n", n)
	}
	fmt.Fprintf(out, "%d server(s) imported\n", len(a.Imported))
	return 0
}
