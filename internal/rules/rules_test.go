package rules

import (
	"os"
	"path/filepath"
	"testing"
)

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEvaluate(t *testing.T) {
	tmp := Normalize(t.TempDir())
	p := filepath.Join(tmp, "proj")
	a := filepath.Join(tmp, "a")
	b := filepath.Join(tmp, "b")
	sub := filepath.Join(p, "sub")
	deep := filepath.Join(p, "sub", "deep")
	other := filepath.Join(tmp, "other")
	projX := filepath.Join(tmp, "projX")
	mkdirs(t, sub, deep, other, projX, a, b, filepath.Join(a, "x"))

	r := func(m Mode, path string) Rule { return Rule{Path: path, Mode: m} }
	one := func(rs ...Rule) []Level { return []Level{{Name: "skill", Rules: rs}} }

	tests := []struct {
		name   string
		levels []Level
		dir    string
		active bool
		reason string
	}{
		{"no levels", nil, p, true, "no rules: active everywhere"},
		{"level without rules", []Level{{Name: "skill"}}, p, true, "no rules: active everywhere"},
		{"only exact", one(r(Only, p)), p, true, "skill: included by only " + p},
		{"only below", one(r(Only, p)), sub, false, "skill: no include matches"},
		{"only other", one(r(Only, p)), other, false, "skill: no include matches"},
		{"tree exact", one(r(Tree, p)), p, true, "skill: included by tree " + p},
		{"tree deep", one(r(Tree, p)), deep, true, "skill: included by tree " + p},
		{"tree prefix sibling", one(r(Tree, p)), projX, false, "skill: no include matches"},
		{"not exact", one(r(Not, p)), p, false, "skill: excluded by not " + p},
		{"not below", one(r(Not, p)), sub, true, "skill: no exclude matches"},
		{"not other", one(r(Not, p)), other, true, "skill: no exclude matches"},
		{"not-tree exact", one(r(NotTree, p)), p, false, "skill: excluded by not-tree " + p},
		{"not-tree below", one(r(NotTree, p)), sub, false, "skill: excluded by not-tree " + p},
		{"not-tree other", one(r(NotTree, p)), other, true, "skill: no exclude matches"},
		{"exclude wins", one(r(Tree, tmp), r(NotTree, p)), sub, false, "skill: excluded by not-tree " + p},
		{"exclude wins, other", one(r(Tree, tmp), r(NotTree, p)), other, true, "skill: included by tree " + tmp},
		{"two includes", one(r(Only, a), r(Only, b)), b, true, "skill: included by only " + b},
		{"root tree", one(r(Tree, "/")), deep, true, "skill: included by tree /"},
		{"nearest level decides", []Level{{Name: "source", Rules: []Rule{r(Tree, a)}}, {Name: "skill", Rules: []Rule{r(Only, b)}}}, a, false, "skill: no include matches"},
		{"nearest level includes", []Level{{Name: "source", Rules: []Rule{r(Tree, a)}}, {Name: "skill", Rules: []Rule{r(Only, b)}}}, b, true, "skill: included by only " + b},
		{"inherits", []Level{{Name: "source", Rules: []Rule{r(Tree, a)}}, {Name: "skill"}}, filepath.Join(a, "x"), true, "source: included by tree " + a},
		{"disabled with matching rules", []Level{{Name: "plugin", Disabled: true, Rules: []Rule{r(Tree, a)}}, {Name: "skill"}}, a, false, "disabled: plugin"},
		{"disabled outer", []Level{{Name: "source", Disabled: true}, {Name: "skill", Rules: []Rule{r(Tree, a)}}}, a, false, "disabled: source"},
		{"disabled inner", []Level{{Name: "source"}, {Name: "skill", Disabled: true}}, a, false, "disabled: skill"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(tc.levels, tc.dir)
			if got.Active != tc.active || got.Reason != tc.reason {
				t.Errorf("Evaluate = %+v, want {Active:%v Reason:%q}", got, tc.active, tc.reason)
			}
		})
	}
}

func TestEvaluateNormalizesRulePaths(t *testing.T) {
	tmp := Normalize(t.TempDir())
	real := filepath.Join(tmp, "real")
	mkdirs(t, filepath.Join(real, "sub"))
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	levels := []Level{{Name: "server", Rules: []Rule{{Path: link, Mode: Tree}}}}
	if d := Evaluate(levels, filepath.Join(real, "sub")); !d.Active {
		t.Errorf("rule via symlink should match real dir, got %+v", d)
	}
}

func TestNormalize(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("tilde", func(t *testing.T) {
		if got, want := Normalize("~/x"), Normalize(filepath.Join(home, "x")); got != want {
			t.Errorf("got %q want %q", got, want)
		}
		if got, want := Normalize("~"), Normalize(home); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})

	tmp := Normalize(t.TempDir())
	p := filepath.Join(tmp, "proj")
	mkdirs(t, filepath.Join(p, "sub"))

	t.Run("clean", func(t *testing.T) {
		if got, want := Normalize(p+"/../proj/./sub"), filepath.Join(p, "sub"); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		l := filepath.Join(tmp, "L")
		if err := os.Symlink(p, l); err != nil {
			t.Fatal(err)
		}
		if got, want := Normalize(filepath.Join(l, "sub")), Normalize(filepath.Join(p, "sub")); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
	t.Run("missing below symlinked parent", func(t *testing.T) {
		l := filepath.Join(tmp, "L2")
		if err := os.Symlink(p, l); err != nil {
			t.Fatal(err)
		}
		got := Normalize(filepath.Join(l, "nope", "deeper"))
		want := filepath.Join(p, "nope", "deeper")
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
	t.Run("macOS tmp alias", func(t *testing.T) {
		if _, err := os.Readlink("/tmp"); err != nil {
			t.Skip("/tmp is not a symlink")
		}
		if got, want := Normalize("/tmp/skilltri-x/y"), Normalize("/private/tmp/skilltri-x/y"); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	})
}

func TestRuleValidate(t *testing.T) {
	tests := []struct {
		name    string
		rule    Rule
		wantErr bool
	}{
		{"unknown mode", Rule{Path: "/a", Mode: "subtree"}, true},
		{"empty mode", Rule{Path: "/a"}, true},
		{"empty path", Rule{Mode: Only}, true},
		{"relative", Rule{Path: "relative/path", Mode: Only}, true},
		{"tilde", Rule{Path: "~/a", Mode: Tree}, false},
		{"absolute", Rule{Path: "/a", Mode: NotTree}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.rule.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
