package file

import (
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	filesink "github.com/kitsunium/sdk/internal/service/logger/sink/file"
)

// : compile-time proof the factory satisfies the registry port AND the optional
// : config Decoder extension (kept in the test file per
// : KTN-IFACE-ASSERT-PLACEMENT).
var (
	_ writer.Factory = (*fileFactory)(nil)
	_ writer.Decoder = (*fileFactory)(nil)
)

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

func Test_fileFactory_Decode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		raw       map[string]any
		wantErr   bool
		wantPath  string
		wantLevel level.Level
	}
	tests := []tc{
		{"path only yields info floor", map[string]any{"path": "/tmp/a.log"}, false, "/tmp/a.log", level.Info},
		{"path with warn floor", map[string]any{"path": "/tmp/b.log", "min_level": "warn"}, false, "/tmp/b.log", level.Warn},
		{"missing path rejected", map[string]any{"min_level": "info"}, true, "", 0},
		{"empty path rejected", map[string]any{"path": ""}, true, "", 0},
		{"non-string path rejected", map[string]any{"path": 9}, true, "", 0},
		{"unknown level rejected", map[string]any{"path": "/tmp/c.log", "min_level": "loud"}, true, "", 0},
		{"non-string level rejected", map[string]any{"path": "/tmp/d.log", "min_level": 3}, true, "", 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cfg, err := (&fileFactory{}).Decode(c.raw)
		//: failure arm — error + nil config, sentinel must still match.
		if c.wantErr {
			//: a malformed shape must surface a non-nil error and no config.
			if err == nil || cfg != nil {
				t.Fatalf("%s: err=%v cfg=%v want error+nil", c.name, err, cfg)
			}
			//: origin-wins keeps the shared sentinel identifiable.
			if !errs.HasCode(err, writer.CodeWriterConfigInvalid) {
				t.Errorf("%s: err=%v want WriterConfigInvalid code", c.name, err)
			}
			return
		}
		//: happy arm — typed FileConfig with the mapped fields.
		got, ok := cfg.(writer.FileConfig)
		//: the decoder must return the factory's own concrete Config type.
		if err != nil || !ok {
			t.Fatalf("%s: err=%v cfg=%T want FileConfig", c.name, err, cfg)
		}
		//: the path and floor must reflect the decoded options.
		if got.Path != c.wantPath || got.MinLevel != c.wantLevel {
			t.Errorf("%s: got {%q,%v} want {%q,%v}", c.name, got.Path, got.MinLevel, c.wantPath, c.wantLevel)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_fileFactory_Open_ErrorCodes is the white-box complement to the external
// error-code test: it asserts the exact origin code each Open failure forwards,
// proving origin-wins (sink's PathEmpty) versus the shared config-type sentinel.
func Test_fileFactory_Open_ErrorCodes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		cfg      writer.Config
		wantCode errs.Code
	}
	tests := []tc{
		//: empty path defers to the sink, so its PathEmpty code wins (origin).
		{"empty path carries CodePathEmpty", writer.FileConfig{Path: ""}, filesink.CodePathEmpty},
		//: a wrong concrete type is rejected before delegation with the shared code.
		{"wrong config type carries CodeWriterConfigInvalid", writer.ConsoleConfig{}, writer.CodeWriterConfigInvalid},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := (&fileFactory{}).Open(c.cfg)
		//: the failure arm must surface a nil sink and a typed error.
		if err == nil || sink != nil {
			t.Fatalf("%s: err=%v sink=%v want error+nil", c.name, err, sink)
		}
		//: the forwarded error must carry the expected origin code unchanged.
		if !errs.HasCode(err, c.wantCode) {
			t.Errorf("%s: err=%v want code %v", c.name, err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_decodeMinLevel_AllValidNames round-trips every canonical name ParseLevel
// recognises onto its Level constant, replacing the weaker absent-key default
// check with a meaningful name→Level mapping for the full vocabulary.
func Test_decodeMinLevel_AllValidNames(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		raw  string
		want level.Level
	}
	tests := []tc{
		//: the lowest canonical name must map to the Debug floor.
		{"debug name", "debug", level.Debug},
		//: an explicit info key must still resolve to the Info floor.
		{"info name", "info", level.Info},
		//: warn is the mid floor used by the default-active file writer docs.
		{"warn name", "warn", level.Warn},
		//: error is the most severe canonical floor.
		{"error name", "error", level.Error},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: dst starts at the zero (inherit) value like the real Decode path.
		var dst level.Level
		err := decodeMinLevel(map[string]any{"min_level": c.raw}, &dst)
		//: a canonical name must decode cleanly and land on its Level constant.
		if err != nil || dst != c.want {
			t.Errorf("%s: err=%v dst=%v want %v", c.name, err, dst, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_decodePath(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     map[string]any
		wantErr bool
		want    string
	}
	tests := []tc{
		{"valid path", map[string]any{"path": "/tmp/x.log"}, false, "/tmp/x.log"},
		{"absent key rejected", map[string]any{}, true, ""},
		{"empty string rejected", map[string]any{"path": ""}, true, ""},
		{"non-string rejected", map[string]any{"path": 5}, true, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: dst starts empty like the real Decode path.
		var dst string
		err := decodePath(c.raw, &dst)
		//: failure arm — non-nil error mapping to the shared sentinel.
		if c.wantErr {
			//: a malformed shape must surface the shared sentinel.
			if err == nil || !errs.HasCode(err, writer.CodeWriterConfigInvalid) {
				t.Errorf("%s: err=%v want WriterConfigInvalid", c.name, err)
			}
			return
		}
		//: happy arm — nil error and the mapped path.
		if err != nil || dst != c.want {
			t.Errorf("%s: err=%v dst=%q want %q", c.name, err, dst, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_decodeMinLevel(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     map[string]any
		wantErr bool
		want    level.Level
	}
	tests := []tc{
		{"absent key inherits zero", map[string]any{}, false, level.Info},
		{"error name", map[string]any{"min_level": "error"}, false, level.Error},
		{"unknown name rejected", map[string]any{"min_level": "chatty"}, true, 0},
		{"non-string rejected", map[string]any{"min_level": 2}, true, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: dst starts at the zero value (inherit) like the real Decode path.
		var dst level.Level
		err := decodeMinLevel(c.raw, &dst)
		//: failure arm — non-nil error mapping to the shared sentinel.
		if c.wantErr {
			//: a malformed shape must surface the shared sentinel.
			if err == nil || !errs.HasCode(err, writer.CodeWriterConfigInvalid) {
				t.Errorf("%s: err=%v want WriterConfigInvalid", c.name, err)
			}
			return
		}
		//: happy arm — nil error and the mapped floor.
		if err != nil || dst != c.want {
			t.Errorf("%s: err=%v dst=%v want %v", c.name, err, dst, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_configInvalid(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"sentinel carries the shared code and no value"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		err := configInvalid()
		//: the helper must surface the shared config-invalid sentinel.
		if !errs.HasCode(err, writer.CodeWriterConfigInvalid) {
			t.Errorf("configInvalid()=%v want WriterConfigInvalid code", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
