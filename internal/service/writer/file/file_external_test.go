package file_test

import (
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	_ "github.com/kitsunium/sdk/internal/service/writer/file"
)

func TestFileRegistered(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"importing the package registers the file writer"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: the blank import must have self-registered the factory.
		if !writer.Name("file").Known() {
			t.Errorf("file writer not registered after import")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestFileOpen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	type tc struct {
		name    string
		cfg     writer.Config
		wantErr bool
	}
	tests := []tc{
		{"valid path opens", writer.FileConfig{Path: filepath.Join(dir, "a.log")}, false},
		{"per-writer floor opens", writer.FileConfig{Path: filepath.Join(dir, "b.log"), MinLevel: level.Warn}, false},
		{"empty path rejected", writer.FileConfig{Path: ""}, true},
		{"wrong config type rejected", writer.ConsoleConfig{}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := writer.Open("file", c.cfg)
		//: clean up any descriptor the happy path opened.
		if sink != nil {
			t.Cleanup(func() {
				//: surface a close failure rather than discarding it.
				if cerr := sink.Close(); cerr != nil {
					t.Errorf("%s: cleanup close failed: %v", c.name, cerr)
				}
			})
		}
		//: the failure arm must surface an error and a nil sink.
		if c.wantErr {
			if err == nil || sink != nil {
				t.Errorf("%s: err=%v sink=%v want error+nil", c.name, err, sink)
			}
			return
		}
		//: the happy arm must build a usable sink.
		if err != nil || sink == nil {
			t.Errorf("%s: err=%v sink=%v want nil+sink", c.name, err, sink)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
