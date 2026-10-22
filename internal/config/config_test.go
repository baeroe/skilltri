package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baeroe/skilltri/internal/rules"
)

func tmpPath(t *testing.T) string {
	return filepath.Join(t.TempDir(), "sub", "config.json")
}

func TestLoadMissing(t *testing.T) {
	c, err := Load(tmpPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != 1 || len(c.Servers) != 0 || c.Servers == nil {
		t.Fatalf("unexpected config: %+v", c)
	}
}

func TestUpdateRoundTrip(t *testing.T) {
	p := tmpPath(t)
	err := Update(p, func(c *Config) error {
		c.Servers["demo"] = &Server{Type: "stdio", Command: "demo-mcp", Args: []string{"--x"},
			Rules: []rules.Rule{{Path: "/work", Mode: rules.Tree}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	s := c.Servers["demo"]
	if s == nil || s.Command != "demo-mcp" || len(s.Rules) != 1 || s.Rules[0].Mode != rules.Tree {
		t.Fatalf("server not round-tripped: %+v", s)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestUpdateConcurrent(t *testing.T) {
	p := tmpPath(t)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Update(p, func(c *Config) error {
				c.Servers[fmt.Sprintf("srv-%d", i)] = &Server{Type: "http", URL: "https://example.invalid/mcp"}
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Servers) != 20 {
		t.Fatalf("got %d servers, want 20", len(c.Servers))
	}
}

func TestValidate(t *testing.T) {
	stdio := func() *Server { return &Server{Type: "stdio", Command: "demo"} }
	tests := []struct {
		name    string
		server  string
		s       *Server
		wantErr bool
	}{
		{"ok stdio", "demo", stdio(), false},
		{"ok http", "demo", &Server{Type: "http", URL: "https://example.invalid"}, false},
		{"stdio without command", "demo", &Server{Type: "stdio"}, true},
		{"http without url", "demo", &Server{Type: "http"}, true},
		{"type sse", "demo", &Server{Type: "sse", URL: "https://example.invalid"}, true},
		{"bad name", "bad name", stdio(), true},
		{"invalid rule", "demo", &Server{Type: "stdio", Command: "demo", Rules: []rules.Rule{{Path: "rel", Mode: rules.Only}}}, true},
		{"invalid tool rule", "demo", &Server{Type: "stdio", Command: "demo",
			Tools: map[string]*Tool{"t": {Rules: []rules.Rule{{Path: "/x", Mode: "bogus"}}}}}, true},
		{"literal secret", "demo", &Server{Type: "stdio", Command: "demo", Env: map[string]string{"TOKEN": "hunter2"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{Version: 1, Servers: map[string]*Server{tt.server: tt.s}}
			err := c.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), tt.server) {
				t.Fatalf("error %q does not mention server %q", err, tt.server)
			}
		})
	}
}

func TestUnknownKeysPreserved(t *testing.T) {
	p := tmpPath(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	in := `{"version":1,"servers":{},"future":{"a":{"b":[1,2]}}}`
	if err := os.WriteFile(p, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Update(p, func(c *Config) error {
		c.Servers["demo"] = &Server{Type: "stdio", Command: "demo"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `"future"`) || !strings.Contains(string(b), `"b"`) {
		t.Fatalf("unknown key lost: %s", b)
	}
	c, _ := Load(p)
	if c.Servers["demo"] == nil {
		t.Fatal("server lost")
	}
}

func TestConnKey(t *testing.T) {
	base := func() *Server {
		return &Server{Type: "stdio", Command: "demo", Args: []string{"a"}, Env: map[string]string{"A": "1", "B": "2"}}
	}
	k := base().ConnKey()
	if k == "" {
		t.Fatal("empty key")
	}
	same := base()
	same.Rules = []rules.Rule{{Path: "/x", Mode: rules.Only}}
	same.Disabled = true
	same.PerSession = true
	same.RequestTimeoutMs = 5
	if same.ConnKey() != k {
		t.Fatal("rules/disabled must not change the key")
	}
	for i := 0; i < 20; i++ { // map order must not matter
		if base().ConnKey() != k {
			t.Fatal("key unstable")
		}
	}
	args := base()
	args.Args = []string{"b"}
	if args.ConnKey() == k {
		t.Fatal("args must change the key")
	}
	env := base()
	env.Env["A"] = "other"
	if env.ConnKey() == k {
		t.Fatal("env value must change the key")
	}
}

func TestValidServerName(t *testing.T) {
	for name, want := range map[string]bool{"a": true, "a-b1": true, "": false, "-a": false, "a b": false, "a_b": false, strings.Repeat("a", 32): true, strings.Repeat("a", 33): false} {
		if ValidServerName(name) != want {
			t.Errorf("ValidServerName(%q) != %v", name, want)
		}
	}
}

func TestSourceValidate(t *testing.T) {
	cases := []struct {
		name string
		c    Config
		ok   bool
	}{
		{"local without path", Config{Version: 1, Sources: map[string]*Source{"a": {Type: "local"}}}, false},
		{"git without url", Config{Version: 1, Sources: map[string]*Source{"a": {Type: "git"}}}, false},
		{"unknown type", Config{Version: 1, Sources: map[string]*Source{"a": {Type: "zip", Path: "/x"}}}, false},
		{"bad updateEvery", Config{Version: 1, Sources: map[string]*Source{"a": {Type: "local", Path: "/x", UpdateEvery: "soon"}}}, false},
		{"bad source name", Config{Version: 1, Sources: map[string]*Source{"a b": {Type: "local", Path: "/x"}}}, false},
		{"bad node rule", Config{Version: 1, Sources: map[string]*Source{"a": {Type: "local", Path: "/x",
			Skills: map[string]*Node{"s": {Rules: []rules.Rule{{Path: "/x", Mode: "bogus"}}}}}}}, false},
		{"reserved server name", Config{Version: 1, Servers: map[string]*Server{"skilltri": {Type: "stdio", Command: "x"}}}, false},
		{"marketplace with path only", Config{Version: 1, Sources: map[string]*Source{"m": {Type: "marketplace", Path: "/x"}}}, true},
		{"git ok", Config{Version: 1, Sources: map[string]*Source{"g": {Type: "git", URL: "o/r", UpdateEvery: "30m"}}}, true},
		{"plugins default disabled", Config{Version: 1, Sources: map[string]*Source{"m": {Type: "marketplace", Path: "/x", PluginsDefault: "disabled"}}}, true},
		{"bad plugins default", Config{Version: 1, Sources: map[string]*Source{"m": {Type: "marketplace", Path: "/x", PluginsDefault: "off"}}}, false},
		{"plugins default on git", Config{Version: 1, Sources: map[string]*Source{"g": {Type: "git", URL: "o/r", PluginsDefault: "disabled"}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.Validate()
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v, err=%v", tc.ok, err)
			}
		})
	}
}

func TestPluginEnabled(t *testing.T) {
	on, off := &Source{}, &Source{PluginsDefault: "disabled"}
	for _, s := range []*Source{on, off} {
		s.Plugins = map[string]*Node{"yes": {}, "no": {Disabled: true}, "nil": nil}
	}
	for _, tc := range []struct {
		src   *Source
		entry string
		want  bool
	}{
		{on, "other", true}, {on, "yes", true}, {on, "no", false}, {on, "nil", true},
		{off, "other", false}, {off, "yes", true}, {off, "no", false}, {off, "nil", false},
	} {
		if got := tc.src.PluginEnabled(tc.entry); got != tc.want {
			t.Errorf("default %q, %s: got %v, want %v", tc.src.PluginsDefault, tc.entry, got, tc.want)
		}
	}
}

func TestSourceInterval(t *testing.T) {
	for in, want := range map[string]time.Duration{"": time.Hour, "0": 0, "30m": 30 * time.Minute} {
		got, err := (&Source{UpdateEvery: in}).Interval()
		if err != nil || got != want {
			t.Errorf("%q: got %v, %v; want %v", in, got, err, want)
		}
	}
}

func TestSourcesRoundTrip(t *testing.T) {
	p := tmpPath(t)
	err := Update(p, func(c *Config) error {
		c.Servers["x"] = &Server{Type: "stdio", Command: "x",
			Origin: &Origin{Source: "m", Plugin: "p", Server: "x", DefHash: "abc"}}
		c.Sources = map[string]*Source{"m": {Type: "marketplace", URL: "o/r",
			Plugins: map[string]*Node{"p": {Disabled: true}},
			Skills:  map[string]*Node{"p:s": {Rules: []rules.Rule{{Path: "/w", Mode: rules.Tree}}}}}}
		c.Settings.Adapters = []string{"claude-code", "agents"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	m := c.Sources["m"]
	if m == nil || !m.Plugins["p"].Disabled || len(m.Skills["p:s"].Rules) != 1 {
		t.Fatalf("sources lost: %+v", m)
	}
	if o := c.Servers["x"].Origin; o == nil || o.DefHash != "abc" || o.Plugin != "p" {
		t.Fatalf("origin lost: %+v", o)
	}
	if len(c.Settings.Adapters) != 2 {
		t.Fatalf("adapters lost: %v", c.Settings.Adapters)
	}
}

func TestEnabledAdaptersDefault(t *testing.T) {
	c := &Config{}
	if got := c.EnabledAdapters(); len(got) != 1 || got[0] != "claude-code" {
		t.Fatalf("default: %v", got)
	}
	c.Settings.Adapters = []string{"agents"}
	if got := c.EnabledAdapters(); len(got) != 1 || got[0] != "agents" {
		t.Fatalf("explicit: %v", got)
	}
}

func TestConnKeyIgnoresOrigin(t *testing.T) {
	a := &Server{Type: "stdio", Command: "x"}
	b := &Server{Type: "stdio", Command: "x", Origin: &Origin{Source: "m", DefHash: "h"}}
	if a.ConnKey() != b.ConnKey() {
		t.Fatal("origin must not change the ConnKey")
	}
}
