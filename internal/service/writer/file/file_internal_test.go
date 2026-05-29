package file

import (
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
)

// : compile-time proof the factory satisfies the registry port (kept in the
// : test file per KTN-IFACE-ASSERT-PLACEMENT).
var _ writer.Factory = (*fileFactory)(nil)

func Test_fileFactory_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want writer.Name
	}
	tests := []tc{{"reports the canonical key", "file"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the factory must report the key it registered under.
		if got := (&fileFactory{}).Name(); got != c.want {
			t.Errorf("%s: Name()=%q want %q", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_fileFactory_Open(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	type tc struct {
		name    string
		cfg     writer.Config
		wantErr bool
	}
	tests := []tc{
		{"valid path builds a sink", writer.FileConfig{Path: filepath.Join(dir, "f.log")}, false},
		{"empty path rejected", writer.FileConfig{Path: ""}, true},
		{"wrong config type rejected", writer.ConsoleConfig{}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := (&fileFactory{}).Open(c.cfg)
		//: release any opened descriptor on the happy path.
		if sink != nil {
			t.Cleanup(func() {
				//: surface a close failure rather than discarding it.
				if cerr := sink.Close(); cerr != nil {
					t.Errorf("%s: cleanup close failed: %v", c.name, cerr)
				}
			})
		}
		//: failure arm — error + nil sink.
		if c.wantErr {
			if err == nil || sink != nil {
				t.Errorf("%s: err=%v sink=%v want error+nil", c.name, err, sink)
			}
			return
		}
		//: happy arm — nil error + usable sink.
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
