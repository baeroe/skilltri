package adapters

import (
	"bufio"
	"bytes"
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

type manifest struct {
	Links []string `json:"links"`
}

// SyncResult reports a SyncProjectDir run: Linked are the owned links in place afterwards, Removed
// the owned links removed by this run, Skipped the link names left out because a user entry of the
// same name exists. All sorted.
type SyncResult struct {
	Linked, Removed []string
	Skipped         []string // user entries we left alone
}

// SyncProjectDir makes sessionDir/rel hold one symlink per active skill (named after the skill, or
// <plugin>-<name> when two active skills share a name) and nothing else of skilltri's. Links it
// owns are listed in rel/.skilltri.json; owned links that are no longer active are removed, every
// other entry is never touched. rel is created only when there is at least one link. Inside a git
// work tree the manifest and link paths are appended once to the repo's info/exclude. Errors (for
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
	var keep []string
	for _, name := range owned {
		path := filepath.Join(target, name)
		st, err := os.Lstat(path)
		if err != nil || st.Mode()&os.ModeSymlink == 0 {
			continue // gone or replaced by the user: no longer ours
		}
		if dir, ok := want[name]; ok {
			if cur, err := os.Readlink(path); err == nil && cur == dir {
				keep = append(keep, name)
				continue
			}
		}
		if err := os.Remove(path); err != nil {
			errs = append(errs, err)
			keep = append(keep, name) // still ours, retry next time
			continue
		}
		if _, ok := want[name]; !ok {
			res.Removed = append(res.Removed, name)
		}
	}

	names := make([]string, 0, len(want))
	for name := range want {
		names = append(names, name)
	}
	sort.Strings(names)
	created := false
	for _, name := range names {
		if slices.Contains(keep, name) {
			continue
		}
		path := filepath.Join(target, name)
		if _, err := os.Lstat(path); err == nil {
			res.Skipped = append(res.Skipped, name)
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
		keep = append(keep, name)
	}
	sort.Strings(keep)
	res.Linked = keep

	if err := writeManifest(target, keep); err != nil {
		errs = append(errs, err)
	}
	if len(keep) > 0 {
		if err := exclude(sessionDir, clean, keep); err != nil {
			errs = append(errs, err)
		}
	}
	sort.Strings(res.Removed)
	return res, errors.Join(errs...)
}

// linkNames maps each link name to its skill dir. The name is the skill name (the ID part after
// ":"); when two active skills share it, marketplace skills get <plugin>-<name> (plugin = ID part
// before ":"), so a local skill keeps the plain name. A name still taken goes to the first entry;
// names that are not a single path element are skipped.
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
		if _, ok := out[name]; !ok {
			out[name] = e.Skill.Dir
		}
	}
	return out
}

// readManifest returns the owned link names; a missing manifest owns nothing.
func readManifest(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []string
	for _, name := range m.Links {
		if views.SafeName(name) && name != manifestFile && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out, nil
}

// writeManifest stores links in target, or removes the manifest when there are none.
func writeManifest(target string, links []string) error {
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

// exclude appends the manifest and link paths (relative to the work tree root, with a leading "/")
// below excludeMarker to the info/exclude of the git repo containing sessionDir, skipping lines
// already present. Outside a git work tree it does nothing.
func exclude(sessionDir, rel string, links []string) error {
	root, gitDir, err := findGit(sessionDir)
	if err != nil || root == "" {
		return err
	}
	base, err := filepath.Rel(root, filepath.Join(sessionDir, rel))
	if err != nil {
		return err
	}
	lines := []string{"/" + filepath.ToSlash(filepath.Join(base, manifestFile))}
	for _, l := range links {
		lines = append(lines, "/"+filepath.ToSlash(filepath.Join(base, l)))
	}

	path := filepath.Join(gitDir, "info", "exclude")
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	present := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(old))
	for sc.Scan() {
		present[strings.TrimSpace(sc.Text())] = true
	}
	var add []string
	if !present[excludeMarker] {
		add = append(add, excludeMarker)
	}
	for _, l := range lines {
		if !present[l] {
			add = append(add, l)
		}
	}
	if len(add) == 0 || (len(add) == 1 && add[0] == excludeMarker) {
		return nil
	}
	var buf bytes.Buffer
	buf.Write(old)
	if len(old) > 0 && old[len(old)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString(strings.Join(add, "\n") + "\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, buf.Bytes(), 0o644)
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
