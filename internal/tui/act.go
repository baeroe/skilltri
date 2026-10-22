package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/baeroe/skilltri/internal/actions"
	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/daemon"
	"github.com/baeroe/skilltri/internal/target"
)

// actionMsg is the result of an action started with Model.act (toggle, remove, restart …).
type actionMsg struct {
	key    string // busy key (Model.begin) of the action
	text   string
	err    error
	reload bool // the config changed: reload the snapshot
	status bool // the daemon state changed: refresh the status
}

// act runs fn in a command as the busy work key (nil while key is already running); the Model
// reports its text or error in the status line and reloads the snapshot when reload is set and fn
// succeeded.
func (m *Model) act(key, busy string, reload bool, fn func() (string, error)) tea.Cmd {
	if !m.begin(key, busy) {
		return nil
	}
	return func() tea.Msg {
		text, err := fn()
		return actionMsg{key: key, text: text, err: err, reload: reload && err == nil}
	}
}

// actionDone handles an actionMsg.
func (m *Model) actionDone(msg actionMsg) tea.Cmd {
	m.end(msg.key)
	if msg.err != nil {
		m.flash(msg.err.Error(), true)
	} else {
		m.flash(msg.text, false)
	}
	var cmds []tea.Cmd
	if msg.reload {
		cmds = append(cmds, m.reload())
	}
	if msg.status {
		cmds = append(cmds, m.refreshStatus())
	}
	return tea.Batch(cmds...)
}

// toggleCmd enables or disables the node tg in a command; what names it in the status line and
// note is appended to the result text.
func toggleCmd(m *Model, tg target.Target, disabled bool, what, note string) tea.Cmd {
	env := m.opts.Env
	verb := "enabling "
	if disabled {
		verb = "disabling "
	}
	return m.act(tg.String(), verb+what+" …", true, func() (string, error) {
		warns, err := actions.SetDisabled(env, tg, disabled)
		text := "enabled " + what + note
		if disabled {
			text = "disabled " + what + note
		}
		if len(warns) > 0 {
			text += " (" + strings.Join(warns, "; ") + ")"
		}
		return text, err
	})
}

// needConfig flashes why the config cannot be changed (false) when it did not load.
func needConfig(m *Model) bool {
	if m.snap.Config == nil {
		msg := "config not loaded yet"
		if m.snap.ConfigErr != nil {
			msg = "fix the config first: " + m.snap.ConfigErr.Error()
		}
		m.flash(msg, true)
		return false
	}
	return true
}

// updateSources is the default Options.Update: the daemon's "update" (cancelled with ctx, which
// only stops waiting: the daemon finishes on its own), else an offline sync with Env.Builder whose
// catalog is saved unless ctx was cancelled.
func (o Options) updateSources(ctx context.Context, names []string, viaDaemon bool) (daemon.UpdateReply, error) {
	if viaDaemon {
		var r daemon.UpdateReply
		err := daemon.ControlContext(ctx, o.socket(), daemon.Hello{Cmd: "update", Args: names}, &r)
		if !daemonStopped(err) {
			return r, err
		}
	}
	cfg, err := config.Load(o.configPath())
	if err != nil {
		return daemon.UpdateReply{}, err
	}
	b := o.Env.Builder
	if b == nil {
		b = daemon.DefaultBuilder()
	}
	r, c := daemon.Update(ctx, b, cfg, names)
	if err := ctx.Err(); err != nil {
		return r, err
	}
	if err := catalog.Save(o.catalogPath(), c); err != nil {
		return r, fmt.Errorf("save catalog: %w", err)
	}
	return r, nil
}
