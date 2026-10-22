package main

import (
	"fmt"
	"io"
	"sort"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/importer"
	"github.com/baeroe/skilltri/internal/secrets"
)

// importInto merges res into the config at cfgPath. Existing servers are skipped unless force
// is set; only the secrets of servers that were actually imported are stored.
func importInto(cfgPath string, res importer.Result, force bool, out, errOut io.Writer) int {
	var imported, warnings []string
	err := config.Update(cfgPath, func(c *config.Config) error {
		imported = nil
		warnings = append([]string(nil), res.Warnings...)
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
				warnings = append(warnings, fmt.Sprintf("skipped %q: already exists (use --force to overwrite)", n))
				continue
			}
			c.Servers[n] = res.Servers[n]
			imported = append(imported, n)
		}
		for _, n := range imported {
			for _, name := range res.SecretsByServer[n] {
				if err := secrets.Set(name, res.Secrets[name]); err != nil {
					return fmt.Errorf("store secret %s: %w", name, err)
				}
			}
		}
		return nil
	})
	if warnings == nil {
		warnings = res.Warnings
	}
	for _, w := range warnings {
		fmt.Fprintf(errOut, "warning: %s\n", w)
	}
	if err != nil {
		fmt.Fprintf(errOut, "skilltri: %v\n", err)
		return 1
	}
	for _, n := range imported {
		fmt.Fprintf(out, "imported %s\n", n)
	}
	fmt.Fprintf(out, "%d server(s) imported\n", len(imported))
	return 0
}
