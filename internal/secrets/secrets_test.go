package secrets

import (
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestExpand(t *testing.T) {
	keyring.MockInit()
	for name, v := range map[string]string{"tok": "abc", "user.name": "demo", "a-b_c": "x"} {
		if err := Set(name, v); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct{ in, want string }{
		{"Bearer ${secret:tok}", "Bearer abc"},
		{"${secret:user.name}:${secret:tok}", "demo:abc"},
		{"${secret:a-b_c}", "x"},
		{"no refs $HOME ${env:X}", "no refs $HOME ${env:X}"},
		{"", ""},
	}
	for _, tt := range tests {
		got, err := Expand(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("Expand(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestExpandMissing(t *testing.T) {
	keyring.MockInit()
	_, err := Expand("Bearer ${secret:tok}")
	if err == nil || !strings.Contains(err.Error(), "tok") {
		t.Fatalf("err = %v, want mention of tok", err)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestSetGetDelete(t *testing.T) {
	keyring.MockInit()
	if _, err := Get("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing: %v, want ErrNotFound", err)
	}
	if err := Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if got, err := Get("k"); err != nil || got != "v" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := Delete("k"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: %v", err)
	}
	if err := Delete("k"); err != nil {
		t.Errorf("Delete missing should be nil, got %v", err)
	}
}

func TestRef(t *testing.T) {
	if got := Ref("tok"); got != "${secret:tok}" {
		t.Errorf("Ref = %q", got)
	}
}

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{"tok": true, "a.b_c-1": true, "": false, "a b": false, "a}": false} {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v", name, got)
		}
	}
}
