// Package config is the on-disk store for skilltri's MCP servers and their folder rules.
// Writes go through Update: an flock on a sidecar file serializes writers, and the file is
// replaced atomically, so the daemon, CLI and TUI can edit it concurrently.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/baeroe/skilltri/internal/fsutil"
	"github.com/baeroe/skilltri/internal/rules"
)

// Config is the content of config.json.
type Config struct {
	Version  int                `json:"version"` // 1
	Servers  map[string]*Server `json:"servers"`
	Settings Settings           `json:"settings"`
	Sources  map[string]*Source `json:"sources,omitempty"`

	// extra keeps unknown top-level keys so newer versions' data survives an Update.
	extra map[string]json.RawMessage
}

// Settings are global options.
type Settings struct {
	IdleTimeoutSec int      `json:"idleTimeoutSec,omitempty"` // 0 → 600
	Adapters       []string `json:"adapters,omitempty"`       // enabled adapter ids; nil → ["claude-code"]
}

// ReservedName is not allowed as a server name: it is the tool prefix of the skill fallback.
const ReservedName = "skilltri"

// Source is one place skills come from.
type Source struct {
	Type        string           `json:"type"`           // "local" | "git" | "marketplace"
	Path        string           `json:"path,omitempty"` // local folder (local; marketplace from a folder)
	URL         string           `json:"url,omitempty"`  // "owner/repo" or a git URL (git, marketplace)
	Ref         string           `json:"ref,omitempty"`  // branch, tag or 40-hex commit (pinned)
	Subpath     string           `json:"subpath,omitempty"`
	UpdateEvery string           `json:"updateEvery,omitempty"` // Go duration; "" → 1h; "0" → manual only
	Disabled    bool             `json:"disabled,omitempty"`
	Rules       []rules.Rule     `json:"rules,omitempty"`
	Plugins     map[string]*Node `json:"plugins,omitempty"` // marketplace: per plugin (absent = enabled)
	Skills      map[string]*Node `json:"skills,omitempty"`  // per skill id
}

// Node overrides a source's rules for a single plugin or skill.
type Node struct {
	Disabled bool         `json:"disabled,omitempty"`
	Rules    []rules.Rule `json:"rules,omitempty"`
}

// Origin records which marketplace plugin server a Server was imported from.
type Origin struct {
	Source  string `json:"source"`
	Plugin  string `json:"plugin"`
	Server  string `json:"server"`
	DefHash string `json:"defHash"`
}

// Interval is the update interval: "" → one hour, "0" → 0 (manual only).
func (s *Source) Interval() (time.Duration, error) {
	switch s.UpdateEvery {
	case "":
		return time.Hour, nil
	case "0":
		return 0, nil
	}
	d, err := time.ParseDuration(s.UpdateEvery)
	if err != nil {
		return 0, fmt.Errorf("updateEvery: %w", err)
	}
	if d < 0 {
		return 0, errors.New("updateEvery must not be negative")
	}
	return d, nil
}

// EnabledAdapters returns the enabled adapter ids; the default is ["claude-code"].
func (c *Config) EnabledAdapters() []string {
	if len(c.Settings.Adapters) == 0 {
		return []string{"claude-code"}
	}
	return c.Settings.Adapters
}

// Server is one downstream MCP server.
type Server struct {
	Type             string            `json:"type"` // "stdio" | "http"
	Command          string            `json:"command,omitempty"`
	Args             []string          `json:"args,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Cwd              string            `json:"cwd,omitempty"`
	URL              string            `json:"url,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	RequestTimeoutMs int               `json:"requestTimeoutMs,omitempty"` // 0 → 60000
	Disabled         bool              `json:"disabled,omitempty"`
	PerSession       bool              `json:"perSession,omitempty"`
	Rules            []rules.Rule      `json:"rules,omitempty"`
	Tools            map[string]*Tool  `json:"tools,omitempty"`
	Origin           *Origin           `json:"origin,omitempty"` // set when imported from a plugin; not part of ConnKey
}

// Tool overrides a server's rules for a single tool.
type Tool struct {
	Disabled bool         `json:"disabled,omitempty"`
	Rules    []rules.Rule `json:"rules,omitempty"`
}

var serverName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,31}$`)

// ValidServerName reports whether name is usable as a server name.
func ValidServerName(name string) bool { return serverName.MatchString(name) }

type plain Config // same fields, no custom (un)marshalers

// UnmarshalJSON decodes the known fields and stashes the unknown top-level keys.
func (c *Config) UnmarshalJSON(b []byte) error {
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return err
	}
	for _, k := range []string{"version", "servers", "settings", "sources"} {
		delete(all, k)
	}
	*c = Config(p)
	if len(all) > 0 {
		c.extra = all
	}
	return nil
}

// MarshalJSON encodes the known fields plus the preserved unknown keys.
func (c Config) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(plain(c))
	if err != nil || len(c.extra) == 0 {
		return b, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	for k, v := range c.extra {
		if _, known := all[k]; !known {
			all[k] = v
		}
	}
	return json.Marshal(all)
}

// Load reads the config at path. A missing file yields an empty version-1 config.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{Version: 1, Servers: map[string]*Server{}}, nil
	}
	if err != nil {
		return nil, err
	}
	c := &Config{}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Servers == nil {
		c.Servers = map[string]*Server{}
	}
	return c, nil
}

// Update loads the config under an exclusive lock, applies fn, validates and writes it atomically (0600).
func Update(path string, fn func(*Config) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	unlock, err := lockFile(path + ".lock")
	if err != nil {
		return err
	}
	defer unlock()

	c, err := Load(path)
	if err != nil {
		return err
	}
	if err := fn(c); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, append(b, '\n'), 0o600)
}

// Validate returns the first problem found (servers in name order), naming the server.
func (c *Config) Validate() error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported config version %d", c.Version)
	}
	names := make([]string, 0, len(c.Servers))
	for n := range c.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := c.Servers[n].validate(n); err != nil {
			return fmt.Errorf("server %q: %w", n, err)
		}
	}
	snames := make([]string, 0, len(c.Sources))
	for n := range c.Sources {
		snames = append(snames, n)
	}
	sort.Strings(snames)
	for _, n := range snames {
		if err := c.Sources[n].validate(n); err != nil {
			return fmt.Errorf("source %q: %w", n, err)
		}
	}
	return nil
}

func (s *Source) validate(name string) error {
	if !ValidServerName(name) {
		return errors.New("invalid source name (use letters, digits and '-', max 32 characters)")
	}
	if s == nil {
		return errors.New("source is empty")
	}
	switch s.Type {
	case "local":
		if s.Path == "" {
			return errors.New("local source needs a path")
		}
	case "git":
		if s.URL == "" {
			return errors.New("git source needs a url")
		}
	case "marketplace":
		if s.URL == "" && s.Path == "" {
			return errors.New("marketplace source needs a url or a path")
		}
	default:
		return fmt.Errorf("unknown type %q (want local, git or marketplace)", s.Type)
	}
	if _, err := s.Interval(); err != nil {
		return err
	}
	for _, r := range s.Rules {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	for kind, nodes := range map[string]map[string]*Node{"plugin": s.Plugins, "skill": s.Skills} {
		for id, n := range nodes {
			if n == nil {
				continue
			}
			for _, r := range n.Rules {
				if err := r.Validate(); err != nil {
					return fmt.Errorf("%s %q: %w", kind, id, err)
				}
			}
		}
	}
	return nil
}

func (s *Server) validate(name string) error {
	if !ValidServerName(name) {
		return errors.New("invalid server name (use letters, digits and '-', max 32 characters)")
	}
	if name == ReservedName {
		return fmt.Errorf("server name %q is reserved", ReservedName)
	}
	if s == nil {
		return errors.New("server is empty")
	}
	switch s.Type {
	case "stdio":
		if s.Command == "" {
			return errors.New("stdio server needs a command")
		}
	case "http":
		if s.URL == "" {
			return errors.New("http server needs a url")
		}
	default:
		return fmt.Errorf("unknown type %q (want stdio or http)", s.Type)
	}
	for _, r := range s.Rules {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	for tn, t := range s.Tools {
		if t == nil {
			continue
		}
		for _, r := range t.Rules {
			if err := r.Validate(); err != nil {
				return fmt.Errorf("tool %q: %w", tn, err)
			}
		}
	}
	return nil
}

// ConnKey is a stable hash of the connection fields; a changed key means the server must restart.
// Rules, Disabled, PerSession and timeouts are deliberately excluded. encoding/json sorts map
// keys, so map order does not matter.
func (s *Server) ConnKey() string {
	b, _ := json.Marshal(struct {
		Type    string            `json:"type"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
		Cwd     string            `json:"cwd"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}{s.Type, s.Command, s.Args, s.Env, s.Cwd, s.URL, s.Headers})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
