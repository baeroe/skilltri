package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"time"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/paths"
	"github.com/baeroe/skilltri/internal/skilllock"
	"github.com/baeroe/skilltri/internal/sources"
	"github.com/baeroe/skilltri/internal/views"
)

const (
	defaultUpdateTick = time.Minute
	// updateTimeout bounds one `update` control request (a sync of every source), both sides.
	updateTimeout = 15 * time.Minute
	gcEvery       = 24 * time.Hour     // views.GC at most this often
	viewMaxAge    = 7 * 24 * time.Hour // views unused this long are removed
)

// errNoConfig answers updates while the daemon has never loaded a valid config.
var errNoConfig = errors.New("no valid config loaded yet (see status)")

// UpdateReply answers the "update" control command: the state of every source (and remote plugin)
// that was synced.
type UpdateReply struct {
	Sources []catalog.SourceState
}

// DefaultBuilder builds catalogs from the checkouts in paths.DataDir()/sources with the default lock.
func DefaultBuilder() *catalog.Builder {
	return &catalog.Builder{Fetcher: &sources.Fetcher{Root: filepath.Join(paths.DataDir(), "sources")}, Lock: skilllock.Path()}
}

// Update syncs the sources in names (every enabled source when names is empty) and builds the
// catalog from the resulting checkouts. A failing source keeps its last good checkout, so its
// skills stay in the catalog; its error is in the reply and in the catalog's SourceState.
func Update(ctx context.Context, b *catalog.Builder, cfg *config.Config, names []string) (UpdateReply, *catalog.Catalog) {
	if len(names) == 0 {
		for n, s := range cfg.Sources {
			if s != nil && !s.Disabled {
				names = append(names, n)
			}
		}
		sort.Strings(names)
	}
	var r UpdateReply
	if len(names) > 0 {
		r.Sources = b.SyncSources(ctx, cfg, names)
	}
	c, _ := b.Build(ctx, cfg, false) // Build never fails; problems are in c.Sources and c.Warnings
	return r, c
}

// Catalog is the current skill catalog (empty before Run built the first one).
func (d *Daemon) Catalog() *catalog.Catalog {
	if c := d.cat.Load(); c != nil {
		return c
	}
	return &catalog.Catalog{}
}

// initCatalog builds the first catalog from the existing checkouts (sync=false, so serving starts
// at once) and saves it. Without a valid config it uses the cached catalog, if any.
func (d *Daemon) initCatalog(ctx context.Context) {
	if d.haveConfig.Load() {
		d.rebuild(ctx)
		return
	}
	if c, err := catalog.Load(catalog.Path()); err == nil {
		d.cat.Store(c)
	}
}

// updater syncs due sources at start and every UpdateTick, rebuilds the catalog when the config
// changed and collects old views once a day. It is the only goroutine that runs scheduled syncs.
func (d *Daemon) updater(ctx context.Context) {
	var lastGC time.Time
	tick := func() {
		d.syncDue(ctx)
		if ctx.Err() == nil && time.Since(lastGC) >= gcEvery {
			lastGC = time.Now()
			if err := views.GC(paths.CacheDir(), viewMaxAge); err != nil {
				d.log.Printf("views gc: %v", err)
			}
		}
	}
	tick()
	t := time.NewTicker(d.o.UpdateTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		case <-d.rebuildCh:
			d.rebuild(ctx)
		}
	}
}

// syncDue syncs the sources whose interval passed; it skips the tick while an update runs.
func (d *Daemon) syncDue(ctx context.Context) {
	if !d.haveConfig.Load() || !d.updMu.TryLock() {
		return
	}
	defer d.updMu.Unlock()
	c, _ := d.o.Builder.Build(ctx, d.cfg.Load(), true)
	d.setCatalog(ctx, c, false)
}

// rebuild builds the catalog from the existing checkouts (no git) and saves it, after a config change.
func (d *Daemon) rebuild(ctx context.Context) {
	if !d.haveConfig.Load() {
		return
	}
	d.updMu.Lock()
	defer d.updMu.Unlock()
	c, _ := d.o.Builder.Build(ctx, d.cfg.Load(), false)
	d.setCatalog(ctx, c, true)
}

// requestRebuild asks the updater for a rebuild; a pending request covers this one.
func (d *Daemon) requestRebuild() {
	select {
	case d.rebuildCh <- struct{}{}:
	default:
	}
}

// update answers the "update" control command. Concurrent updates run one after another.
func (d *Daemon) update(names []string) (UpdateReply, error) {
	if !d.haveConfig.Load() {
		return UpdateReply{}, errNoConfig
	}
	ctx, cancel := context.WithTimeout(d.ctx, updateTimeout)
	defer cancel()
	d.updMu.Lock()
	defer d.updMu.Unlock()
	r, c := Update(ctx, d.o.Builder, d.cfg.Load(), names)
	for _, s := range r.Sources {
		if s.Error != "" {
			d.log.Printf("update %s: %s", s.Name, s.Error)
		}
	}
	d.setCatalog(ctx, c, true)
	return r, nil
}

// setCatalog makes c the current catalog and saves it when it changed (ignoring Built) or when
// save is set; sessions refresh only when it changed. A build cut short by shutdown is dropped.
func (d *Daemon) setCatalog(ctx context.Context, c *catalog.Catalog, save bool) {
	if ctx.Err() != nil || c == nil {
		return
	}
	old := d.cat.Load()
	changed := old == nil || !sameCatalog(old, c)
	if !changed && !save {
		return
	}
	if err := catalog.Save(catalog.Path(), c); err != nil {
		d.log.Printf("save catalog: %v", err)
	}
	if !changed {
		return
	}
	d.cat.Store(c)
	if old != nil {
		d.log.Printf("catalog changed (%d skills)", len(c.Skills))
	}
	d.refreshAll() // also for the first catalog: sessions may already be serving

}

// sameCatalog reports whether a and b differ at most in Built.
func sameCatalog(a, b *catalog.Catalog) bool {
	x, y := *a, *b
	x.Built, y.Built = time.Time{}, time.Time{}
	bx, err1 := json.Marshal(x)
	by, err2 := json.Marshal(y)
	return err1 == nil && err2 == nil && bytes.Equal(bx, by)
}
