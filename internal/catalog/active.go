package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/rules"
)

// Decided is one catalog entry with its decision for a folder.
type Decided struct {
	Entry    Entry
	Decision rules.Decision
}

// Levels returns the rule hierarchy of e: [source, plugin (marketplace only), skill]. The plugin
// level is config.Source.Plugins[e.Plugin] (entry name; off per Source.PluginEnabled), the skill level config.Source.Skills[e.ID].
// A source missing from cfg yields a disabled source level, so stale catalog entries stay off.
func Levels(cfg *config.Config, e Entry) []rules.Level {
	src := cfg.Sources[e.Source]
	if src == nil {
		return []rules.Level{{Name: e.Source, Disabled: true}}
	}
	levels := []rules.Level{{Name: e.Source, Disabled: src.Disabled, Rules: src.Rules}}
	if e.Plugin != "" {
		l := rules.Level{Name: e.Source + "/" + e.Plugin, Disabled: !src.PluginEnabled(e.Plugin)}
		if n := src.Plugins[e.Plugin]; n != nil {
			l.Rules = n.Rules
		}
		levels = append(levels, l)
	}
	l := rules.Level{Name: e.Source + "/" + e.ID}
	if n := src.Skills[e.ID]; n != nil {
		l.Disabled, l.Rules = n.Disabled, n.Rules
	}
	return append(levels, l)
}

// Evaluate decides every catalog entry for dir (already normalized, see rules.Normalize), sorted by ID
// (also for a catalog that is not sorted; c itself is not modified).
func Evaluate(c *Catalog, cfg *config.Config, dir string) []Decided {
	out := make([]Decided, 0, len(c.Skills))
	for _, e := range c.Skills {
		out = append(out, Decided{Entry: e, Decision: rules.Evaluate(Levels(cfg, e), dir)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Entry.ID < out[j].Entry.ID })
	return out
}

// Active returns the entries active for dir (already normalized), sorted by ID.
func Active(c *Catalog, cfg *config.Config, dir string) []Entry {
	var out []Entry
	for _, d := range Evaluate(c, cfg, dir) {
		if d.Decision.Active {
			out = append(out, d.Entry)
		}
	}
	return out
}

// DefHash identifies a plugin server definition: its connection key, plus code (the commit of
// the plugin checkout it runs code from, "" when it runs none), so an update of that code needs
// review like a changed definition.
func DefHash(s config.Server, code string) string {
	if code == "" {
		return s.ConnKey()
	}
	sum := sha256.Sum256([]byte(s.ConnKey() + "\x00" + code))
	return hex.EncodeToString(sum[:])
}

// ReviewState compares every config server imported from a plugin (Origin set) with the catalog:
// "ok" if the plugin still defines it unchanged, "needs-review" if the definition (or the plugin
// code it runs, see DefHash) changed, "source disabled" if its source is disabled, "not fetched
// yet" if the plugin or its marketplace has no checkout yet (or its fetch failed), and "gone" if
// the catalog no longer has it (plugin, server or source removed).
func ReviewState(cfg *config.Config, c *Catalog) map[string]string {
	type id struct{ source, plugin, name string }
	defs := make(map[id]string, len(c.Servers))
	for _, s := range c.Servers {
		defs[id{s.Source, s.Plugin, s.Name}] = s.DefHash
	}
	noCheckout := map[string]bool{} // state names without a checkout whose fetch failed or is pending
	for _, st := range c.Sources {
		if st.Commit == "" && st.Error != "" {
			noCheckout[st.Name] = true
		}
	}
	out := map[string]string{}
	for name, s := range cfg.Servers {
		if s == nil || s.Origin == nil {
			continue
		}
		o := s.Origin
		src := cfg.Sources[o.Source]
		h, ok := defs[id{o.Source, o.Plugin, o.Server}]
		switch {
		case src != nil && src.Disabled:
			out[name] = "source disabled"
		case ok && h == o.DefHash:
			out[name] = "ok"
		case ok:
			out[name] = "needs-review"
		case src != nil && ((src.URL != "" && noCheckout[o.Source]) || noCheckout[o.Source+"/plugins/"+o.Plugin]):
			out[name] = "not fetched yet"
		default:
			out[name] = "gone"
		}
	}
	return out
}
