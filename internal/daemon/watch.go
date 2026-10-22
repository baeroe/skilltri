package daemon

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/baeroe/skilltri/internal/config"
)

// sig identifies a version of the config file; a missing file has the zero sig.
type sig struct {
	mtime time.Time
	size  int64
}

func fileSig(path string) (sig, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return sig{}, nil
	}
	if err != nil {
		return sig{}, err
	}
	return sig{fi.ModTime(), fi.Size()}, nil
}

// watch polls the config file's mtime and size and reloads it on change.
func (d *Daemon) watch(ctx context.Context, last sig) {
	t := time.NewTicker(d.o.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cur, err := fileSig(d.o.ConfigPath)
		if err != nil || cur == last {
			continue
		}
		last = cur
		d.reload()
	}
}

// reload loads the config. A parse error keeps the last good config and is reported in status;
// otherwise servers whose connection changed or that were removed restart, and every session
// recomputes its set.
func (d *Daemon) reload() {
	c, err := config.Load(d.o.ConfigPath)
	if err != nil {
		d.log.Printf("config: %v (keeping the last good config)", err)
		d.mu.Lock()
		d.configErr = err.Error()
		d.mu.Unlock()
		return
	}
	old := d.cfg.Swap(c)
	d.mu.Lock()
	d.configErr = ""
	d.mu.Unlock()
	for name, prev := range old.Servers {
		next, ok := c.Servers[name]
		if !ok || next == nil || prev == nil || next.ConnKey() != prev.ConnKey() {
			d.mgr.Restart(name)
		}
	}
	d.log.Printf("config reloaded (%d servers)", len(c.Servers))
	d.refreshAll()
}
