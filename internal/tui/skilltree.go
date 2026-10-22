package tui

import (
	"slices"
	"strings"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/config"
)

// nodeKind is the kind of a Skills tree node.
type nodeKind int

const (
	nodeSource nodeKind = iota
	nodePlugin
	nodeServer
	nodeSkill
)

// collapseOver is the child count above which a node starts collapsed.
const collapseOver = 10

// treeNode is one node of the Skills tree: a configured source, a plugin of a marketplace source,
// an MCP server of a plugin or a skill of the catalog.
type treeNode struct {
	kind     nodeKind
	key      string // srcKey, pluginKey, serverKey or skillKey
	source   string
	src      *config.Source
	state    *catalog.SourceState  // fetch state of the source or remote plugin (nil: none)
	plugin   *catalog.PluginInfo   // nodePlugin and nodeServer
	server   *catalog.PluginServer // nodeServer
	skill    *catalog.Entry        // nodeSkill
	as       string                // nodeServer: the config server it was enabled as ("": not enabled)
	children []*treeNode
}

// The node keys are the target strings of the nodes (servers get a "/server/<name>" suffix).
func srcKey(source string) string              { return "source/" + source }
func pluginKey(source, entry string) string    { return srcKey(source) + "/plugin/" + entry }
func skillKey(source, id string) string        { return srcKey(source) + "/skill/" + id }
func serverKey(source, entry, n string) string { return pluginKey(source, entry) + "/server/" + n }

// buildTree builds the tree of every configured source (disabled ones included) from the
// snapshot's config and catalog.
func buildTree(s Snapshot) []*treeNode {
	if s.Config == nil {
		return nil
	}
	cat := s.Catalog
	if cat == nil {
		cat = &catalog.Catalog{}
	}
	states := map[string]*catalog.SourceState{}
	for i := range cat.Sources {
		states[cat.Sources[i].Name] = &cat.Sources[i]
	}
	type pk struct{ source, plugin string }
	skillsOf := map[pk][]*catalog.Entry{}
	for i := range cat.Skills {
		e := &cat.Skills[i]
		skillsOf[pk{e.Source, e.Plugin}] = append(skillsOf[pk{e.Source, e.Plugin}], e)
	}
	serversOf := map[pk][]*catalog.PluginServer{}
	for i := range cat.Servers {
		ps := &cat.Servers[i]
		serversOf[pk{ps.Source, ps.Plugin}] = append(serversOf[pk{ps.Source, ps.Plugin}], ps)
	}
	enabledAs := map[[3]string]string{}
	for _, name := range s.serverNames() {
		if o := s.Config.Servers[name].Origin; o != nil {
			k := [3]string{o.Source, o.Plugin, o.Server}
			if _, dup := enabledAs[k]; !dup {
				enabledAs[k] = name
			}
		}
	}
	skillNodes := func(src *config.Source, source string, es []*catalog.Entry) []*treeNode {
		var out []*treeNode
		for _, e := range es {
			out = append(out, &treeNode{kind: nodeSkill, key: skillKey(source, e.ID), source: source, src: src, skill: e})
		}
		return out
	}

	var roots []*treeNode
	for _, name := range sortedSources(s.Config) {
		src := s.Config.Sources[name]
		n := &treeNode{kind: nodeSource, key: srcKey(name), source: name, src: src, state: states[name]}
		if src.Type != "marketplace" {
			n.children = skillNodes(src, name, skillsOf[pk{name, ""}])
			roots = append(roots, n)
			continue
		}
		for i := range cat.Plugins {
			p := &cat.Plugins[i]
			if p.Source != name {
				continue
			}
			pn := &treeNode{kind: nodePlugin, key: pluginKey(name, p.Plugin), source: name, src: src, plugin: p,
				state: states[name+"/plugins/"+p.Plugin]}
			pn.children = skillNodes(src, name, skillsOf[pk{name, p.Plugin}])
			for _, ps := range serversOf[pk{name, p.Plugin}] {
				pn.children = append(pn.children, &treeNode{kind: nodeServer, key: serverKey(name, p.Plugin, ps.Name),
					source: name, src: src, plugin: p, server: ps, as: enabledAs[[3]string{name, p.Plugin, ps.Name}]})
			}
			n.children = append(n.children, pn)
		}
		roots = append(roots, n)
	}
	return roots
}

// sortedSources are the configured source names (nil entries skipped), sorted.
func sortedSources(cfg *config.Config) []string {
	var out []string
	for n, s := range cfg.Sources {
		if s != nil {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// treeRow is one shown row of the tree.
type treeRow struct {
	node   *treeNode
	depth  int
	open   bool // expanded (only meaningful with children)
	match  bool // the node itself matches the filter
	forced bool // shown open by the filter (an ancestor of a match): expand keys do not apply
}

// matches reports whether the node itself matches the lowercased query q: skills by ID and
// description, the others by name.
func (n *treeNode) matches(q string) bool {
	var hay string
	switch n.kind {
	case nodeSource:
		hay = n.source
	case nodePlugin:
		hay = n.plugin.Plugin + "\x00" + n.plugin.Name
	case nodeServer:
		hay = n.server.Name
	case nodeSkill:
		hay = n.skill.ID + "\x00" + n.skill.Skill.Description
	}
	return strings.Contains(strings.ToLower(hay), q)
}

// hasMatch reports whether a descendant of n matches q.
func (n *treeNode) hasMatch(q string) bool {
	return slices.ContainsFunc(n.children, func(c *treeNode) bool { return c.matches(q) || c.hasMatch(q) })
}

// flattenTree lists the shown rows. Without a query a node is open when open says so (else when
// it has at most collapseOver children); with a query only matches and their ancestors are shown,
// the ancestors opened, and a matching node brings its subtree as without a query.
func flattenTree(roots []*treeNode, open map[string]bool, query string) []treeRow {
	q := strings.ToLower(query)
	isOpen := func(n *treeNode) bool {
		if v, ok := open[n.key]; ok {
			return v
		}
		return len(n.children) <= collapseOver
	}
	var out []treeRow
	var all func(n *treeNode, depth int, match bool)
	all = func(n *treeNode, depth int, match bool) {
		o := isOpen(n)
		out = append(out, treeRow{node: n, depth: depth, open: o, match: match})
		if o {
			for _, c := range n.children {
				all(c, depth+1, false)
			}
		}
	}
	var filtered func(n *treeNode, depth int)
	filtered = func(n *treeNode, depth int) {
		switch {
		case n.matches(q):
			all(n, depth, true)
		case n.hasMatch(q):
			out = append(out, treeRow{node: n, depth: depth, open: true, forced: true})
			for _, c := range n.children {
				filtered(c, depth+1)
			}
		}
	}
	for _, r := range roots {
		if q == "" {
			all(r, 0, false)
		} else {
			filtered(r, 0)
		}
	}
	return out
}
