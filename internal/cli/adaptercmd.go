package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/baeroe/skilltri/internal/adapters"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/paths"
)

// AdapterRow is one line of `adapter list`.
type AdapterRow struct {
	ID       string `json:"id"`
	Display  string `json:"display"`
	Method   string `json:"method"`
	Detected bool   `json:"detected"`
	Enabled  bool   `json:"enabled"`
}

// AdapterList prints every known adapter with its delivery method, whether the harness is found in
// the home folder and whether it is enabled.
func AdapterList(w io.Writer, jsonOut bool) int {
	cfg, err := config.Load(paths.ConfigFile())
	if err != nil {
		return fail(err)
	}
	home, _ := os.UserHomeDir()
	detected := map[string]bool{}
	for _, a := range adapters.Detected(home) {
		detected[a.ID] = true
	}
	enabled := cfg.EnabledAdapters()
	rows := []AdapterRow{}
	for _, a := range adapters.Registry {
		rows = append(rows, AdapterRow{a.ID, a.Display, a.Method, detected[a.ID], slices.Contains(enabled, a.ID)})
	}
	if jsonOut {
		printJSON(w, rows)
		return 0
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Display, r.Method, yesNo(r.Detected, "detected", "not found"), yesNo(r.Enabled, "enabled", "disabled"))
	}
	tw.Flush()
	return 0
}

func yesNo(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// AdapterSet enables or disables the adapter id in settings.adapters. An unknown id exits 2; the
// last enabled adapter cannot be disabled (an empty list would mean the default again).
func AdapterSet(w io.Writer, id string, enable bool) int {
	if _, ok := adapters.ByID(id); !ok {
		fmt.Fprintf(errOut, "skilltri: unknown adapter %q\n", id)
		return 2
	}
	err := editConfig(func(c *config.Config) error {
		list := slices.Clone(c.EnabledAdapters())
		switch has := slices.Contains(list, id); {
		case enable && !has:
			list = append(list, id)
		case !enable && has:
			list = slices.DeleteFunc(list, func(s string) bool { return s == id })
		}
		if len(list) == 0 {
			return fmt.Errorf("cannot disable %s: at least one adapter must stay enabled", id)
		}
		slices.SortStableFunc(list, func(a, b string) int { return registryIndex(a) - registryIndex(b) })
		c.Settings.Adapters = list
		return nil
	})
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(w, "%s %s\n", yesNo(enable, "enabled", "disabled"), id)
	return 0
}

func registryIndex(id string) int {
	for i, a := range adapters.Registry {
		if a.ID == id {
			return i
		}
	}
	return len(adapters.Registry)
}
