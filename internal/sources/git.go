// Package sources keeps git checkouts of skill sources below a root folder, using the git CLI.
// A failed update never touches the last good checkout.
package sources

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// gitTimeout bounds every single git call.
const gitTimeout = 120 * time.Second

var (
	shorthand = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+$`)
	sha       = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	shortSHA  = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// RepoURL expands the GitHub shorthand "owner/repo" to "https://github.com/owner/repo.git";
// anything else is returned unchanged.
func RepoURL(s string) string {
	if shorthand.MatchString(s) && !strings.HasSuffix(s, "/.") && !strings.HasSuffix(s, "/..") {
		return "https://github.com/" + s + ".git"
	}
	return s
}

// Pinned reports whether ref is a full commit SHA (40 hex characters).
func Pinned(ref string) bool { return sha.MatchString(ref) }

// Fetcher manages checkouts below Root (paths.DataDir()/sources). Git is the git binary ("" → "git").
// Sync calls for the same key are serialized within the process and, via a flock, across processes.
type Fetcher struct {
	Root string
	Git  string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Checkout is a checked-out source: its working tree and the commit HEAD points at.
type Checkout struct {
	Dir    string
	Commit string
}

// Dir returns the working tree of key: Root/<key>/repo. key may contain "/" ("<source>/plugins/<plugin>");
// it must be a valid key (see Sync), Dir itself does not check it.
func (f *Fetcher) Dir(key string) string {
	return filepath.Join(f.Root, filepath.FromSlash(key), "repo")
}

// Current returns the existing checkout of key without any network access.
func (f *Fetcher) Current(key string) (Checkout, bool) {
	if validKey(key) != nil {
		return Checkout{}, false
	}
	dir := f.Dir(key)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return Checkout{}, false
	}
	commit, err := f.git(context.Background(), dir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return Checkout{}, false
	}
	return Checkout{Dir: dir, Commit: commit}, true
}

// Sync brings the checkout of key to ref (branch, tag, commit SHA; "" = the remote's default branch)
// of url, cloning it on first use. On error the previous checkout stays as it was.
// key is a relative slash path without empty, "." or ".." segments; anything else is an error.
func (f *Fetcher) Sync(ctx context.Context, key, url, ref string) (Checkout, error) {
	if err := validKey(key); err != nil {
		return Checkout{}, err
	}
	unlock, err := f.lock(key)
	if err != nil {
		return Checkout{}, err
	}
	defer unlock()

	dir := f.Dir(key)
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := f.clone(ctx, dir, url, ref); err != nil {
			return Checkout{}, err
		}
	} else if err != nil {
		return Checkout{}, err
	} else if err := f.update(ctx, dir, url, ref); err != nil {
		return Checkout{}, err
	}
	commit, err := f.git(ctx, dir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return Checkout{}, err
	}
	return Checkout{Dir: dir, Commit: commit}, nil
}

// clone clones url into a temp dir next to dir, checks out ref there and renames it into place,
// so a failed first clone leaves nothing behind.
func (f *Fetcher) clone(ctx context.Context, dir, url, ref string) error {
	parent := filepath.Dir(dir)
	tmp, err := os.MkdirTemp(parent, ".clone-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp) // no-op after a successful rename
	if _, err := f.git(ctx, "", "clone", "--filter=blob:none", "--no-checkout", "--", url, tmp); err != nil {
		return err
	}
	if err := f.checkout(ctx, tmp, ref); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}

// update fetches (unless ref is a commit that is already present) and checks out ref.
func (f *Fetcher) update(ctx context.Context, dir, url, ref string) error {
	if cur, err := f.git(ctx, dir, "remote", "get-url", "origin"); err != nil || cur != url {
		if _, err := f.git(ctx, dir, "remote", "set-url", "--", "origin", url); err != nil {
			return err
		}
	}
	if !Pinned(ref) || !f.hasCommit(ctx, dir, ref) {
		if _, err := f.git(ctx, dir, "fetch", "--prune", "--tags", "origin"); err != nil {
			return err
		}
	}
	return f.checkout(ctx, dir, ref)
}

// checkout resolves ref and detaches HEAD at it, discarding local changes.
func (f *Fetcher) checkout(ctx context.Context, dir, ref string) error {
	target, err := f.resolve(ctx, dir, ref)
	if err != nil {
		return err
	}
	_, err = f.git(ctx, dir, "checkout", "--detach", "--force", target)
	return err
}

// resolve maps ref to a commit: origin/HEAD for "", else the remote branch, the tag, or, for a hex ref
// only, the commit itself. Local branches are never used, so a deleted upstream branch is an error
// instead of a silent rollback to the branch clone created.
func (f *Fetcher) resolve(ctx context.Context, dir, ref string) (string, error) {
	candidates := []string{"refs/remotes/origin/HEAD"}
	if ref != "" {
		candidates = []string{"refs/remotes/origin/" + ref, "refs/tags/" + ref}
		if shortSHA.MatchString(strings.ToLower(ref)) {
			candidates = append(candidates, ref)
		}
	}
	for _, c := range candidates {
		if commit, err := f.revParse(ctx, dir, c); err == nil {
			return commit, nil
		}
	}
	if ref == "" {
		return "", errors.New("remote has no default branch (origin/HEAD)")
	}
	return "", fmt.Errorf("ref %q not found (no branch, tag or commit)", ref)
}

func (f *Fetcher) hasCommit(ctx context.Context, dir, ref string) bool {
	_, err := f.revParse(ctx, dir, ref)
	return err == nil
}

// revParse resolves rev to the commit it names.
func (f *Fetcher) revParse(ctx context.Context, dir, rev string) (string, error) {
	return f.git(ctx, dir, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
}

// validKey rejects keys that could leave Root: empty, absolute, backslashes, or empty/"."/".." segments.
func validKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.Contains(key, `\`) {
		return fmt.Errorf("invalid source key %q", key)
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid source key %q", key)
		}
	}
	return nil
}

// lock serializes work on key: a per-key mutex in this process plus a flock on Root/<key>.lock.
func (f *Fetcher) lock(key string) (func(), error) {
	f.mu.Lock()
	if f.locks == nil {
		f.locks = map[string]*sync.Mutex{}
	}
	m := f.locks[key]
	if m == nil {
		m = &sync.Mutex{}
		f.locks[key] = m
	}
	f.mu.Unlock()

	m.Lock()
	base := filepath.Join(f.Root, filepath.FromSlash(key))
	if err := os.MkdirAll(base, 0o700); err != nil {
		m.Unlock()
		return nil, err
	}
	unlock, err := lockFile(base + ".lock")
	if err != nil {
		m.Unlock()
		return nil, err
	}
	return func() { unlock(); m.Unlock() }, nil
}

// git runs one git command (in dir unless empty) without prompts and with a timeout,
// returning trimmed stdout or an error carrying git's stderr.
func (f *Fetcher) git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	bin := f.Git
	if bin == "" {
		bin = "git"
	}
	sub := args[0]
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = commandEnv()
	cmd.WaitDelay = 5 * time.Second // grandchildren (ssh, helpers) holding the pipes cannot outlive the timeout
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if ctx.Err() != nil {
			msg = ctx.Err().Error()
		}
		return "", fmt.Errorf("git %s: %w: %s", sub, err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// commandEnv is the environment for git: no terminal or askpass prompts, and ssh in batch mode
// unless the user configured GIT_SSH_COMMAND.
func commandEnv() []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "SSH_ASKPASS_REQUIRE=never")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	return env
}
