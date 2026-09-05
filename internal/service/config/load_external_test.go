// Package config_test — the merge + decode + validate loader.
package config_test

import (
	"errors"
	"maps"
	"testing"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	cfg "github.com/kitsunium/sdk/internal/service/config"

	_ "github.com/kitsunium/sdk/internal/service/codec/json" // register "json"
)

// appConf is the target every loader case decodes into.
type appConf struct {
	Port int    `json:"port"`
	Host string `json:"host"`
}

// Validate implements core/config.Validator: a zero port is not a usable
// configuration, and finding out at startup beats finding out at first bind.
func (c appConf) Validate() error {
	//: a zero port is invalid.
	if c.Port == 0 {
		return errs.NewRuntime(coreconfig.CodeConfigValidationFailed,
			"PORT_REQUIRED", "port required", "test: port is zero")
	}
	return nil
}

// staticSource is a Source returning a fixed map, so a case can state its layer
// inline rather than through the filesystem or the environment.
type staticSource struct {
	m   map[string]any
	err error
}

// Load returns the fixed layer, or the fixed failure.
func (s staticSource) Load() (map[string]any, error) { return s.m, s.err }

// TestLoad pins the loader's three failure classes and the layering order.
//
// The source-failure normalisation is the one worth spelling out. Source is a
// public interface, so a third-party implementation can return any error —
// including one carrying a dotted-quad code from a completely different domain.
// Normalising at the loader boundary is what makes Load's typed contract hold
// whoever wrote the source, and an error already carrying the sentinel's code
// passes through untouched rather than being double-wrapped.
func TestLoad(t *testing.T) {
	t.Parallel()
	foreign := errors.New("a source nobody in this repo wrote")

	type tc struct {
		name     string
		sources  []coreconfig.Source
		want     appConf
		wantCode errs.Code
	}
	tests := []tc{
		{
			name:    "a single layer",
			sources: []coreconfig.Source{staticSource{m: map[string]any{"port": 8080, "host": "a"}}},
			want:    appConf{Port: 8080, Host: "a"},
		},
		{
			//: later sources win key by key, which is what makes an env layer
			//: over a file layer useful.
			name: "a later layer overrides an earlier one",
			sources: []coreconfig.Source{
				staticSource{m: map[string]any{"port": 1, "host": "file"}},
				staticSource{m: map[string]any{"port": 8080}},
			},
			want: appConf{Port: 8080, Host: "file"},
		},
		{
			name:    "no sources at all",
			sources: nil,
			//: nothing set the port, so validation refuses the result.
			wantCode: coreconfig.CodeConfigValidationFailed,
		},
		{
			name:     "a validation failure",
			sources:  []coreconfig.Source{staticSource{m: map[string]any{"host": "a"}}},
			wantCode: coreconfig.CodeConfigValidationFailed,
		},
		{
			name:     "a source that fails with a foreign error",
			sources:  []coreconfig.Source{staticSource{err: foreign}},
			wantCode: coreconfig.CodeConfigSourceFailed,
		},
		{
			//: an in-tree source already emits the sentinel; it must not be
			//: wrapped again, or the trail grows a layer per loader.
			name:     "a source that fails with the sentinel",
			sources:  []coreconfig.Source{staticSource{err: coreconfig.ConfigSourceFailed}},
			wantCode: coreconfig.CodeConfigSourceFailed,
		},
		{
			name:     "a layer whose type does not fit the target",
			sources:  []coreconfig.Source{staticSource{m: map[string]any{"port": "not a number"}}},
			wantCode: coreconfig.CodeConfigDecodeFailed,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got appConf

		err := cfg.Load(&got, c.sources...)

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("Load(%s) = %v, want code %v", c.name, err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("Load(%s) = %v, want nil", c.name, err)
		}
		if got != c.want {
			t.Errorf("Load(%s) = %+v, want %+v", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestLoadDoesNotAliasSourceMaps pins that merging never writes into a layer a
// Source handed over. A Source may reasonably return a map it keeps — a cached
// parse, a package-level default — and a loader that mutated it would corrupt
// every later Load in the process.
func TestLoadDoesNotAliasSourceMaps(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the nested layer the first source owns and hands over.
		owned map[string]any
		//: what a later layer merges onto the same key.
		later map[string]any
	}
	tests := []tc{
		{"an overridden nested key", map[string]any{"port": 1}, map[string]any{"port": 8080}},
		{"a new nested key", map[string]any{"port": 1}, map[string]any{"host": "b"}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the source keeps its own reference, as a caching source would.
		owned := map[string]any{"nested": c.owned}
		before := maps.Clone(c.owned)

		type nestedConf struct {
			Nested appConf `json:"nested"`
		}
		var target nestedConf
		//: the target has no Validate, so the load must succeed outright —
		//: anything else would mean the aliasing assertion below never ran.
		if err := cfg.Load(&target,
			staticSource{m: owned},
			staticSource{m: map[string]any{"nested": c.later}},
		); err != nil {
			t.Fatalf("Load = %v, want nil", err)
		}

		for k, want := range before {
			if c.owned[k] != want {
				t.Errorf("the source's own map changed: %q = %#v, want %#v", k, c.owned[k], want)
			}
		}
		if len(c.owned) != len(before) {
			t.Errorf("the source's own map grew to %d keys, want %d", len(c.owned), len(before))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
