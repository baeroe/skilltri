package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/baeroe/skilltri/internal/doctor"
	"github.com/baeroe/skilltri/internal/paths"
	"github.com/baeroe/skilltri/internal/skilllock"
)

// Doctor runs the health checks on the real paths and prints the findings (grouped by severity,
// each with an indented fix line), or with jsonOut the findings array. It returns 1 when any
// finding is an error.
func Doctor(w io.Writer, jsonOut bool) int {
	home, _ := os.UserHomeDir()
	environ := os.Environ()
	if wd, err := os.Getwd(); err == nil {
		environ = append(environ, "PWD="+wd) // later entries win
	}
	fs := doctor.Run(context.Background(), doctor.Env{
		Home:       home,
		ConfigPath: paths.ConfigFile(),
		Socket:     paths.SocketPath(),
		CacheDir:   paths.CacheDir(),
		StatePath:  skilllock.Path(),
		Environ:    environ,
	})
	if jsonOut {
		if fs == nil {
			fs = []doctor.Finding{}
		}
		printJSON(w, fs)
	} else {
		renderDoctor(w, fs)
	}
	if doctor.HasError(fs) {
		return 1
	}
	return 0
}

func renderDoctor(w io.Writer, fs []doctor.Finding) {
	if len(fs) == 0 {
		fmt.Fprintln(w, "no problems found")
		return
	}
	symbol := map[string]string{doctor.Error: "✗", doctor.Warning: "!", doctor.Info: "i"}
	for i, f := range fs {
		if i > 0 && fs[i-1].Severity != f.Severity {
			fmt.Fprintln(w)
		}
		head := f.Check
		if f.Subject != f.Check {
			head += " " + f.Subject
		}
		fmt.Fprintf(w, "%s %s: %s\n", symbol[f.Severity], head, f.Message)
		if f.Fix != "" {
			fmt.Fprintf(w, "    fix: %s\n", f.Fix)
		}
	}
}
