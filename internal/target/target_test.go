package target

import (
	"testing"

	"github.com/baeroe/skilltri/internal/config"
	"github.com/baeroe/skilltri/internal/rules"
)

func TestParse(t *testing.T) {
	ok := map[string]Target{
		"server/a":                        {Kind: "server", Server: "a"},
		"server/a/tool/t":                 {Kind: "tool", Server: "a", Tool: "t"},
		"source/m":                        {Kind: "source", Source: "m"},
		"source/m/plugin/p":               {Kind: "plugin", Source: "m", Plugin: "p"},
		"source/m/skill/sp:brainstorming": {Kind: "skill", Source: "m", Skill: "sp:brainstorming"},
	}
	for in, want := range ok {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("%q: got %+v, %v; want %+v", in, got, err, want)
		}
		if got.String() != in {
			t.Errorf("String(%q) = %q", in, got.String())
		}
	}
	for _, in := range []string{"", "server", "server/a/tool", "server/a/tool/", "server//tool/t", "x/a", "source/m/foo/p", "server/a/tool/t/x", "source/m/plugin"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("%q: want error", in)
		}
	}
}

func TestResolveCreate(t *testing.T) {
	cfg := &config.Config{Version: 1,
		Servers: map[string]*config.Server{"a": {Type: "stdio", Command: "x"}},
		Sources: map[string]*config.Source{"m": {Type: "marketplace", URL: "o/r"}}}

	ref, err := Resolve(cfg, Target{Kind: "plugin", Source: "m", Plugin: "p"}, true)
	if err != nil {
		t.Fatal(err)
	}
	*ref.Rules = append(*ref.Rules, rules.Rule{Path: "/w", Mode: rules.Tree})
	*ref.Disabled = true
	n := cfg.Sources["m"].Plugins["p"]
	if n == nil || len(n.Rules) != 1 || !n.Disabled {
		t.Fatalf("plugin node: %+v", n)
	}

	// Under pluginsDefault "disabled" a created plugin node keeps the plugin off (a rule alone
	// must not enable it).
	cfg.Sources["m"].PluginsDefault = config.PluginsDisabled
	if _, err := Resolve(cfg, Target{Kind: "plugin", Source: "m", Plugin: "q"}, true); err != nil {
		t.Fatal(err)
	}
	if n := cfg.Sources["m"].Plugins["q"]; n == nil || !n.Disabled || cfg.Sources["m"].PluginEnabled("q") {
		t.Fatalf("created node under default disabled: %+v", n)
	}

	ref, err = Resolve(cfg, Target{Kind: "tool", Server: "a", Tool: "t"}, true)
	if err != nil {
		t.Fatal(err)
	}
	*ref.Disabled = true
	if !cfg.Servers["a"].Tools["t"].Disabled {
		t.Fatal("tool not created")
	}

	ref, err = Resolve(cfg, Target{Kind: "skill", Source: "m", Skill: "p:s"}, true)
	if err != nil {
		t.Fatal(err)
	}
	*ref.Rules = []rules.Rule{{Path: "/w", Mode: rules.Only}}
	if len(cfg.Sources["m"].Skills["p:s"].Rules) != 1 {
		t.Fatal("skill node not created")
	}

	ref, err = Resolve(cfg, Target{Kind: "source", Source: "m"}, false)
	if err != nil {
		t.Fatal(err)
	}
	*ref.Disabled = true
	if !cfg.Sources["m"].Disabled {
		t.Fatal("source ref not live")
	}
}

func TestResolveErrors(t *testing.T) {
	cfg := &config.Config{Version: 1,
		Servers: map[string]*config.Server{"a": {Type: "stdio", Command: "x"}},
		Sources: map[string]*config.Source{"m": {Type: "marketplace", URL: "o/r"}}}
	for _, tc := range []struct {
		t      Target
		create bool
	}{
		{Target{Kind: "source", Source: "nope"}, true},
		{Target{Kind: "plugin", Source: "nope", Plugin: "p"}, true},
		{Target{Kind: "server", Server: "nope"}, true},
		{Target{Kind: "tool", Server: "nope", Tool: "t"}, true},
		{Target{Kind: "tool", Server: "a", Tool: "t"}, false},
		{Target{Kind: "plugin", Source: "m", Plugin: "p"}, false},
		{Target{Kind: "bogus"}, true},
	} {
		if _, err := Resolve(cfg, tc.t, tc.create); err == nil {
			t.Errorf("%+v create=%v: want error", tc.t, tc.create)
		}
	}
	if cfg.Servers["a"].Tools != nil || cfg.Sources["m"].Plugins != nil {
		t.Fatal("failed resolve must not create entries")
	}
}
