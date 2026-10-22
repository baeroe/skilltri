package adapters

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/baeroe/skilltri/internal/catalog"
	"github.com/baeroe/skilltri/internal/fsutil"
	"github.com/baeroe/skilltri/internal/views"
)

// manifestFile lists the links skilltri owns in a project skill folder.
const manifestFile = ".skilltri.json"

// excludeMarker heads the lines skilltri appends to .git/info/exclude.
const excludeMarker = "# skilltri"

// manifest maps each owned link name to the target skilltri created it with. The legacy format, a
// plain list of names, is still read (names with an empty target).
type manifest struct {
	Links map[string]string `json:"links"`
}

// ProjectDirAllowed reports whether skilltri may link skills into a project folder of sessionDir:
// only inside a git work tree (where info/exclude keeps the links out of commits), and never in
// home or any of its ancestors (a dotfiles repo makes home a work tree too). Paths are compared
// after resolving symlinks; a folder that cannot be resolved is not allowed.
func ProjectDirAllowed(sessionDir, home string) bool {
	dir, err := filepath.EvalSymlinks(sessionDir)
	if err != nil {
		return false
	}
	if home != "" {
		h, err := filepath.EvalSymlinks(home)
		if err != nil {
			h = filepath.Clean(home)
		}
		if h == dir || strings.HasPrefix(h, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator)) {
			return false
		}
	}
	root, _, err := findGit(dir)
	return err == nil && root != ""
}

// SyncResult reports a SyncProjectDir run: Linked are the owned links in place afterwards, Removed
// the owned links removed by this run, Skipped the names left alone because a user entry of that
// name exists (including a manifest-listed link that no longer points where skilltri pointed it).
// All sorted.
type SyncResult struct {
	Linked, Removed []string
	Skipped         []string // user entries we left alone
}

// SyncProjectDir makes sessionDir/rel hold one symlink per active skill (named after the skill, or
// <plugin>-<name> when two active skills share a name) and nothing else of skilltri's. Links it
// owns are recorded with their target in rel/.skilltri.json; an entry counts as owned only while it
// is a symlink to the recorded target. Owned links that are no longer active are removed, every
// other entry is never touched. rel is created only when there is at least one link. Inside a git
// work tree the manifest and link paths are added once to the repo's info/exclude. Errors (for
// example a read-only folder) are returned for the caller to log; the sync never panics.
func SyncProjectDir(sessionDir, rel string, active []catalog.Entry) (SyncResult, error) {
	var res SyncResult
	clean := filepath.Clean(filepath.FromSlash(rel))
	if rel == "" || filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return res, fmt.Errorf("project dir %q must be relative inside the session folder", rel)
	}
	target := filepath.Join(sessionDir, clean)
	want := linkNames(active)
	owned, err := readManifest(filepath.Join(target, manifestFile))
	if err != nil {
		return res, err
	}

	var errs []error
	keep := map[string]string{}
	skipped := map[string]bool{}
	for _, name := range sortedKeys(owned) {
		recorded := owned[name]
		if recorded == "" { // legacy entry: ours only if it already points at the wanted target
			recorded = want[name]
		}
		path := filepath.Join(target, name)
		st, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		cur, _ := os.Readlink(path)
		if st.Mode()&os.ModeSymlink == 0 || recorded == "" || cur != recorded {
			skipped[name] = true // replaced by the user or not ours: no longer owned, never touched
			continue
		}
		if dir, ok := want[name]; ok && dir == cur {
			keep[name] = cur
			continue
		}
		if err := os.Remove(path); err != nil {
			errs = append(errs, err)
			keep[name] = cur // still ours, retry next time
			continue
		}
		if _, ok := want[name]; !ok {
			res.Removed = append(res.Removed, name)
		}
	}

	created := false
	for _, name := range sortedKeys(want) {
		if _, ok := keep[name]; ok || skipped[name] {
			continue
		}
		path := filepath.Join(target, name)
		if _, err := os.Lstat(path); err == nil {
			skipped[name] = true
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		if !created {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return res, errors.Join(append(errs, err)...)
			}
			created = true
		}
		if err := os.Symlink(want[name], path); err != nil {
			errs = append(errs, err)
			continue
		}
		keep[name] = want[name]
	}
	res.Linked = sortedKeys(keep)
	res.Skipped = sortedKeys(skipped)

	if err := writeManifest(target, keep); err != nil {
		errs = append(errs, err)
	}
	if len(keep) > 0 {
		if err := exclude(sessionDir, clean, res.Linked); err != nil {
			errs = append(errs, err)
		}
	}
	sort.Strings(res.Removed)
	return res, errors.Join(errs...)
}

// OwnedLinks returns the links skilltri owns in sessionDir/rel (name → link target): manifest
// entries that are still symlinks to their recorded target (legacy entries: any symlink). It only
// reads; a missing folder or manifest owns nothing.
func OwnedLinks(sessionDir, rel string) (map[string]string, error) {
	target := filepath.Join(sessionDir, filepath.Clean(filepath.FromSlash(rel)))
	owned, err := readManifest(filepath.Join(target, manifestFile))
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for name, recorded := range owned {
		cur, err := os.Readlink(filepath.Join(target, name))
		if err != nil || (recorded != "" && cur != recorded) {
			continue
		}
		out[name] = cur
	}
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// linkNames maps each link name to its skill dir. The name is the skill name (the ID part after
// ":"); when two active skills share it, marketplace skills get <plugin>-<name> (plugin = ID part
// before ":"), so a local skill keeps the plain name. A name still taken goes to the first entry;
// names that are not a single path element are skipped. Dirs are made absolute.
func linkNames(active []catalog.Entry) map[string]string {
	split := func(e catalog.Entry) (plugin, name string) {
		if i := strings.Index(e.ID, ":"); i >= 0 {
			return e.ID[:i], e.ID[i+1:]
		}
		return "", e.ID
	}
	count := map[string]int{}
	for _, e := range active {
		_, name := split(e)
		count[name]++
	}
	out := map[string]string{}
	for _, e := range active {
		plugin, name := split(e)
		if count[name] > 1 && plugin != "" {
			if !views.SafeName(plugin) {
				continue
			}
			name = plugin + "-" + name
		}
		if !views.SafeName(name) || name == manifestFile || e.Skill.Dir == "" {
			continue
		}
		dir, err := filepath.Abs(e.Skill.Dir)
		if err != nil {
			continue
		}
		if _, ok := out[name]; !ok {
			out[name] = dir
		}
	}
	return out
}

// readManifest returns the owned links (name → recorded target, "" for legacy entries); a missing
// manifest owns nothing.
func readManifest(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var raw struct {
		Links json.RawMessage `json:"links"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	links := map[string]string{}
	if len(raw.Links) > 0 && raw.Links[0] == '[' {
		var names []string
		if err := json.Unmarshal(raw.Links, &names); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		for _, n := range names {
			links[n] = ""
		}
	} else if len(raw.Links) > 0 && string(raw.Links) != "null" {
		if err := json.Unmarshal(raw.Links, &links); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	out := map[string]string{}
	for name, t := range links {
		if views.SafeName(name) && name != manifestFile {
			out[name] = t
		}
	}
	return out, nil
}

// writeManifest stores links in target, or removes the manifest when there are none.
func writeManifest(target string, links map[string]string) error {
	path := filepath.Join(target, manifestFile)
	if len(links) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(manifest{Links: links}, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(b, '\n'), 0o644)
}

// exclude adds the manifest and link paths (relative to the work tree root, with a leading "/") to
// the info/exclude of the git repo containing sessionDir, skipping lines already present. New lines
// go at the end of the block under excludeMarker (the marker and the non-empty, non-comment lines
// after it); without a marker a new block is appended. Outside a work tree, or when the git dir does
// not exist, it does nothing.
func exclude(sessionDir, rel string, links []string) error {
	root, gitDir, err := findGit(sessionDir)
	if err != nil || root == "" {
		return err
	}
	if st, err := os.Stat(gitDir); err != nil || !st.IsDir() {
		return nil
	}
	base, err := filepath.Rel(root, filepath.Join(sessionDir, rel))
	if err != nil {
		return err
	}
	want := []string{"/" + filepath.ToSlash(filepath.Join(base, manifestFile))}
	for _, l := range links {
		want = append(want, "/"+filepath.ToSlash(filepath.Join(base, l)))
	}

	path := filepath.Join(gitDir, "info", "exclude")
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var lines []string // no line length limit, unlike bufio.Scanner
	if len(old) > 0 {
		lines = strings.Split(strings.TrimSuffix(string(old), "\n"), "\n")
	}
	present := map[string]bool{}
	for _, l := range lines {
		present[strings.TrimSpace(l)] = true
	}
	var add []string
	for _, l := range want {
		if !present[l] {
			add = append(add, l)
		}
	}
	if len(add) == 0 {
		return nil
	}
	at := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) == excludeMarker })
	if at < 0 {
		lines = append(lines, excludeMarker)
		at = len(lines) - 1
	}
	end := at + 1
	for end < len(lines) {
		l := strings.TrimSpace(lines[end])
		if l == "" || strings.HasPrefix(l, "#") {
			break
		}
		end++
	}
	lines = slices.Insert(lines, end, add...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// findGit walks up from dir to the first folder with a .git entry and returns that work tree root
// and the git dir whose info/ applies: .git itself, or for a .git file ("gitdir: <path>", worktrees
// and submodules) the dir it points to, or that dir's commondir when it has one. root is "" outside
// a work tree.
func findGit(dir string) (root, gitDir string, err error) {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		p := filepath.Join(d, ".git")
		st, err := os.Stat(p)
		if err == nil {
			if st.IsDir() {
				return d, p, nil
			}
			g, err := readGitFile(p)
			return d, g, err
		}
		if filepath.Dir(d) == d {
			return "", "", nil
		}
	}
}

func readGitFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	g, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if !ok {
		return "", fmt.Errorf("%s: not a gitdir file", path)
	}
	g = strings.TrimSpace(g)
	if !filepath.IsAbs(g) {
		g = filepath.Join(filepath.Dir(path), g)
	}
	if c, err := os.ReadFile(filepath.Join(g, "commondir")); err == nil {
		common := strings.TrimSpace(string(c))
		if !filepath.IsAbs(common) {
			common = filepath.Join(g, common)
		}
		return filepath.Clean(common), nil
	}
	return filepath.Clean(g), nil
}
