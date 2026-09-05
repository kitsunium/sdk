// Package config_test — the environment Source as a caller configures it.
package config_test

import (
	"testing"

	cfg "github.com/kitsunium/sdk/internal/service/config"
)

// TestEnvSource pins the namespace rules, which are where a typo costs the most.
//
// EnvSource("APP") and EnvSource("APP_") have to name the same namespace: both
// spellings are natural to write, and the second used to build the prefix
// "APP__", which matches nothing — Load then returned an empty configuration and
// a nil error, so the mistake was completely silent.
//
// The mirror-image trap is an all-underscore prefix. Absorbing its separators
// would leave "", and "" is not "a namespace whose name is empty" — it is the
// WHOLE environment. A caller who wrote a prefix asked to be scoped.
func TestEnvSource(t *testing.T) {
	type tc struct {
		name string
		//: the prefix handed to EnvSource.
		prefix string
		//: the environment keys to set, and the short keys expected back.
		env  map[string]string
		want map[string]any
		//: short keys that must NOT come back, because the variable behind
		//: them lives in another namespace.
		wantAbsent []string
	}
	tests := []tc{
		{
			name:       "a bare prefix",
			prefix:     "APP",
			env:        map[string]string{"APP_PORT": "8080", "OTHER_X": "ignored"},
			want:       map[string]any{"port": int64(8080)},
			wantAbsent: []string{"x", "other_x"},
		},
		{
			//: the same namespace, written with the separator the caller
			//: expected to have to supply.
			name:   "a prefix with a trailing separator",
			prefix: "APP_",
			env:    map[string]string{"APP_PORT": "8080", "OTHER_X": "ignored"},
			want:   map[string]any{"port": int64(8080)},
		},
		{
			name:   "a prefix with several trailing separators",
			prefix: "APP___",
			env:    map[string]string{"APP_PORT": "8080"},
			want:   map[string]any{"port": int64(8080)},
		},
		{
			name:   "keys are lower-cased",
			prefix: "APP",
			env:    map[string]string{"APP_LOG_LEVEL": "debug"},
			want:   map[string]any{"log_level": "debug"},
		},
		{
			//: an all-underscore prefix names the "_" namespace, not everything.
			name:       "an underscore-only prefix",
			prefix:     "_",
			env:        map[string]string{"_HIDDEN": "yes", "APP_PORT": "8080"},
			want:       map[string]any{"hidden": "yes"},
			wantAbsent: []string{"app_port", "port"},
		},
		{
			name:       "a namespace nobody populated",
			prefix:     "SDK_ABSENT_NAMESPACE",
			env:        map[string]string{"APP_PORT": "8080"},
			want:       map[string]any{},
			wantAbsent: []string{"port", "app_port"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: clear the namespaces every case reads so a sibling case's variables
		//: cannot leak in; t.Setenv restores them afterwards.
		for _, key := range []string{"APP_PORT", "APP_LOG_LEVEL", "OTHER_X", "_HIDDEN"} {
			t.Setenv(key, "")
		}
		for key, val := range c.env {
			t.Setenv(key, val)
		}

		got, err := cfg.EnvSource(c.prefix).Load()
		//: reading the environment cannot fail.
		if err != nil {
			t.Fatalf("Load = %v, want nil", err)
		}
		for key, want := range c.want {
			if got[key] != want {
				t.Errorf("%q = %#v, want %#v", key, got[key], want)
			}
		}
		//: a key from another namespace must never appear. Checking named
		//: intruders rather than "everything unexpected" is deliberate: the
		//: process environment carries variables no test controls.
		for _, key := range c.wantAbsent {
			if val, present := got[key]; present && val != "" {
				t.Errorf("Load returned %q = %#v from outside the namespace", key, val)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestEnvSourceEmptyPrefixReadsEverything pins the one way to opt out of
// scoping: writing no prefix at all. It has to keep working, because it is the
// documented escape hatch, and it has to be reachable ONLY that way — which is
// what the underscore-only case above guards.
func TestEnvSourceEmptyPrefixReadsEverything(t *testing.T) {
	type tc struct {
		name string
		key  string
		val  string
		want string
	}
	tests := []tc{
		{"a prefixed variable", "APP_PORT", "8080", "app_port"},
		{"an unprefixed variable", "PORT", "9090", "port"},
		{"a variable starting with an underscore", "_HIDDEN", "yes", "_hidden"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		t.Setenv(c.key, c.val)

		got, err := cfg.EnvSource("").Load()
		if err != nil {
			t.Fatalf("Load = %v, want nil", err)
		}
		if _, present := got[c.want]; !present {
			t.Errorf("Load with no prefix omitted %q", c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}
