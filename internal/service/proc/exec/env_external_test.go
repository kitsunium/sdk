package exec_test

import (
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"

	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// TestEnvAllow pins the allow-list a consumer used to rewrite (#935): the
// entries kept are those whose key is named, in the parent's order; a key
// repeated in the parent keeps its last value, at the place of its last
// occurrence (the value a child's getenv would see); the parent slice is not
// modified.
func TestEnvAllow(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		parent []string
		keys   []string
		want   []string
	}
	tests := []tc{
		{"keeps the named keys in the parent's order", []string{"HOME=/h", "SECRET=s", "PATH=/bin"}, []string{"PATH", "HOME"}, []string{"HOME=/h", "PATH=/bin"}},
		{"a repeated key keeps its last value", []string{"PATH=/a", "HOME=/h", "PATH=/b"}, []string{"PATH", "HOME"}, []string{"HOME=/h", "PATH=/b"}},
		{"no key keeps nothing", []string{"HOME=/h"}, nil, []string{}},
		{"a key absent from the parent adds nothing", []string{"HOME=/h"}, []string{"PATH"}, []string{}},
		{"an entry without = is a key with an empty value", []string{"FLAG", "HOME=/h"}, []string{"FLAG"}, []string{"FLAG"}},
		{"a prefix is not the key", []string{"PATHX=1", "PATH=/bin"}, []string{"PATH"}, []string{"PATH=/bin"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		parent := slices.Clone(c.parent)
		got := svcexec.EnvAllow(parent, c.keys...)
		if !slices.Equal(got, c.want) {
			t.Errorf("EnvAllow(%q, %q) = %q, want %q", c.parent, c.keys, got, c.want)
		}
		if !slices.Equal(parent, c.parent) {
			t.Errorf("EnvAllow modified its parent: %q", parent)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestEnvDeny pins the deny-list twin of EnvAllow: every entry whose key is
// not named, in order, a repeated key keeping its last value.
func TestEnvDeny(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		parent []string
		keys   []string
		want   []string
	}
	tests := []tc{
		{"drops the named keys", []string{"HOME=/h", "SECRET=s", "PATH=/bin"}, []string{"SECRET"}, []string{"HOME=/h", "PATH=/bin"}},
		{"a repeated key keeps its last value", []string{"PATH=/a", "HOME=/h", "PATH=/b"}, []string{"HOME"}, []string{"PATH=/b"}},
		{"no key keeps everything, deduplicated", []string{"A=1", "A=2"}, nil, []string{"A=2"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := svcexec.EnvDeny(slices.Clone(c.parent), c.keys...)
		if !slices.Equal(got, c.want) {
			t.Errorf("EnvDeny(%q, %q) = %q, want %q", c.parent, c.keys, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestEnvAllowOverTheProcessEnvironment runs the acceptance row of #935 on
// the real environment: EnvAllow(os.Environ(), "PATH", "HOME") yields exactly
// those keys, in the parent's order, and nothing else.
func TestEnvAllowOverTheProcessEnvironment(t *testing.T) {
	t.Parallel()
	parent := os.Environ()
	got := svcexec.EnvAllow(parent, "PATH", "HOME")
	var want []string
	for _, kv := range parent {
		key, _, _ := strings.Cut(kv, "=")
		if sameKey(key, "PATH") || sameKey(key, "HOME") {
			want = append(want, kv)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("EnvAllow(os.Environ(), PATH, HOME) = %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// sameKey compares environment keys as the platform does.
func sameKey(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
