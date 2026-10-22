package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/baeroe/skilltri/internal/actions"
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
	err := actions.SetAdapter(actEnv, id, enable)
	if errors.Is(err, actions.ErrInvalid) {
		fmt.Fprintf(errOut, "skilltri: %v\n", err)
		return 2
	}
	if err != nil {
		return fail(err)
	}
	fmt.Fprintf(w, "%s %s\n", yesNo(enable, "enabled", "disabled"), id)
	return 0
}
