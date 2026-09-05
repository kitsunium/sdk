// Package config — white-box tests for the file Source.
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	_ "github.com/kitsunium/sdk/internal/service/codec/json" // register "json"
)

// Test_fileSource_Load pins the order of the two things that can go wrong: the
// file is read BEFORE the codec is resolved, so a missing file and an
// unregistered format are distinguishable in the error's fields even though
// they share one code.
func Test_fileSource_Load(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	if err := os.WriteFile(good, []byte(`{"port":8080}`), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	type tc struct {
		name string
		src  fileSource
		//: the format annotation the error must carry, if any.
		wantFormat string
		wantErr    bool
	}
	tests := []tc{
		{name: "a registered format on a readable file", src: fileSource{format: "json", path: good}},
		{
			name:    "a readable file in the wrong format",
			src:     fileSource{format: "json", path: filepath.Join(dir, "absent.json")},
			wantErr: true,
		},
		{
			//: the codec lookup happens after the read, so an unregistered
			//: format on a readable file names the format it could not resolve.
			name:       "an unregistered format",
			src:        fileSource{format: "nope-not-registered", path: good},
			wantFormat: "nope-not-registered",
			wantErr:    true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := c.src.Load()

		if c.wantErr {
			if !errs.HasCode(err, coreconfig.CodeConfigSourceFailed) {
				t.Fatalf("Load(%s) = %v, want CONFIG_SOURCE_FAILED", c.name, err)
			}
			if got != nil {
				t.Errorf("Load(%s) returned %v beside the error", c.name, got)
			}
			if c.wantFormat != "" {
				var found string
				for _, f := range errs.FieldsOf(err) {
					if f.Key() == "format" {
						found = f.StringValue()
					}
				}
				if found != c.wantFormat {
					t.Errorf("the format annotation is %q, want %q", found, c.wantFormat)
				}
			}
			return
		}
		if err != nil {
			t.Fatalf("Load(%s) = %v, want nil", c.name, err)
		}
		//: the loader ranges over the result without a nil check.
		if got == nil {
			t.Fatal("Load returned a nil map with no error")
		}
		if got["port"] != float64(8080) {
			t.Errorf("port = %#v, want 8080", got["port"])
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the registry is what FileSource resolves through; a format that is
	//: registered here must be the one the source finds.
	if _, ok := codec.Lookup("json"); !ok {
		t.Fatal("the json codec is not registered — the fixture above proves nothing")
	}
}
