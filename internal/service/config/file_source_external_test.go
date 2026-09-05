// Package config_test — the file Source as a caller configures it.
package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	cfg "github.com/kitsunium/sdk/internal/service/config"
)

// TestFileSource pins that every way of getting a configuration file wrong is
// one typed failure, which is what lets a caller distinguish "this layer could
// not be read" from "this layer said something invalid".
//
// The unregistered-format case is the one that surprises people: the codec
// registry is populated by blank imports, so a binary that names "yaml" without
// importing the codec gets a source failure at startup rather than a nil map.
func TestFileSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	junk := filepath.Join(dir, "junk.json")
	empty := filepath.Join(dir, "empty.json")
	write(t, good, []byte(`{"port":8080,"host":"kitsune"}`))
	write(t, junk, []byte("this is not json at all\n"))
	write(t, empty, nil)

	type tc struct {
		name    string
		format  codec.Format
		path    string
		want    map[string]any
		wantErr bool
	}
	tests := []tc{
		{
			name:   "a well-formed document",
			format: "json",
			path:   good,
			want:   map[string]any{"port": float64(8080), "host": "kitsune"},
		},
		{name: "a file that does not exist", format: "json", path: filepath.Join(dir, "absent.json"), wantErr: true},
		{name: "a directory where a file belongs", format: "json", path: dir, wantErr: true},
		{name: "a file that is not the declared format", format: "json", path: junk, wantErr: true},
		{name: "an empty file", format: "json", path: empty, wantErr: true},
		{name: "a format nobody registered", format: "yaml-not-imported", path: good, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := cfg.FileSource(c.format, c.path).Load()

		if c.wantErr {
			//: every failure on this path is one the caller handles the same
			//: way — drop the layer or refuse to start — so it carries one code.
			if !errs.HasCode(err, coreconfig.CodeConfigSourceFailed) {
				t.Fatalf("Load(%s) = %v, want CONFIG_SOURCE_FAILED", c.name, err)
			}
			//: a refused layer must hand back nothing, or the loader would
			//: merge a half-parsed map.
			if got != nil {
				t.Errorf("Load(%s) returned %v beside the error", c.name, got)
			}
			return
		}
		if err != nil {
			t.Fatalf("Load(%s) = %v, want nil", c.name, err)
		}
		for key, want := range c.want {
			if got[key] != want {
				t.Errorf("%q = %#v, want %#v", key, got[key], want)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// write puts one fixture on disk.
func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
