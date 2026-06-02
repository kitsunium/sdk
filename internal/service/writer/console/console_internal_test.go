package console

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// : compile-time proof the factory satisfies the registry port AND the optional
// : config Decoder extension (kept in the test file per
// : KTN-IFACE-ASSERT-PLACEMENT).
var (
	_ writer.Factory = (*consoleFactory)(nil)
	_ writer.Decoder = (*consoleFactory)(nil)
)

func Test_consoleFactory_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want writer.Name
	}
	tests := []tc{{"reports the canonical key", "console"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the factory must report the key it registered under.
		if got := (&consoleFactory{}).Name(); got != c.want {
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

func Test_consoleFactory_Open(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		cfg     writer.Config
		wantErr bool
	}
	tests := []tc{
		{"valid console config builds a sink", writer.ConsoleConfig{Stream: writer.ConsoleStderr}, false},
		{"wrong config type rejected", writer.FileConfig{Path: "/x"}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := (&consoleFactory{}).Open(c.cfg)
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

func Test_pickStream(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		stream writer.ConsoleStream
	}
	tests := []tc{
		{"stdout (zero value)", writer.ConsoleStdout},
		{"stderr", writer.ConsoleStderr},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: both stream selectors must yield a usable sink (covers both arms).
		if got := pickStream(c.stream); got == nil {
			t.Errorf("%s: pickStream returned nil", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_consoleFactory_Decode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		raw        map[string]any
		wantErr    bool
		wantStream writer.ConsoleStream
		wantLevel  level.Level
	}
	tests := []tc{
		{"empty map yields stdout/info", map[string]any{}, false, writer.ConsoleStdout, level.Info},
		{"target stdout explicit", map[string]any{"target": "stdout"}, false, writer.ConsoleStdout, level.Info},
		{"target stderr", map[string]any{"target": "stderr"}, false, writer.ConsoleStderr, level.Info},
		{"empty target string defaults stdout", map[string]any{"target": ""}, false, writer.ConsoleStdout, level.Info},
		{"min_level warn", map[string]any{"min_level": "warn"}, false, writer.ConsoleStdout, level.Warn},
		{"both keys", map[string]any{"target": "stderr", "min_level": "error"}, false, writer.ConsoleStderr, level.Error},
		{"unknown target rejected", map[string]any{"target": "syslog"}, true, 0, 0},
		{"non-string target rejected", map[string]any{"target": 7}, true, 0, 0},
		{"unknown level rejected", map[string]any{"min_level": "verbose"}, true, 0, 0},
		{"non-string level rejected", map[string]any{"min_level": true}, true, 0, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cfg, err := (&consoleFactory{}).Decode(c.raw)
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
		//: happy arm — typed ConsoleConfig with the mapped fields.
		got, ok := cfg.(writer.ConsoleConfig)
		//: the decoder must return the factory's own concrete Config type.
		if err != nil || !ok {
			t.Fatalf("%s: err=%v cfg=%T want ConsoleConfig", c.name, err, cfg)
		}
		//: the stream selector and floor must reflect the decoded options.
		if got.Stream != c.wantStream || got.MinLevel != c.wantLevel {
			t.Errorf("%s: got {%v,%v} want {%v,%v}", c.name, got.Stream, got.MinLevel, c.wantStream, c.wantLevel)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_decodeStream(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		raw     map[string]any
		wantErr bool
		want    writer.ConsoleStream
	}
	tests := []tc{
		{"absent key keeps stdout", map[string]any{}, false, writer.ConsoleStdout},
		{"stdout name", map[string]any{"target": "stdout"}, false, writer.ConsoleStdout},
		{"stderr name", map[string]any{"target": "stderr"}, false, writer.ConsoleStderr},
		{"empty name defaults stdout", map[string]any{"target": ""}, false, writer.ConsoleStdout},
		{"unknown name rejected", map[string]any{"target": "udp"}, true, 0},
		{"non-string rejected", map[string]any{"target": 1}, true, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: dst starts at the zero value (stdout) like the real Decode path.
		var dst writer.ConsoleStream
		err := decodeStream(c.raw, &dst)
		//: failure arm — non-nil error, dst untouched is irrelevant.
		if c.wantErr {
			//: a malformed shape must surface the shared sentinel.
			if err == nil || !errs.HasCode(err, writer.CodeWriterConfigInvalid) {
				t.Errorf("%s: err=%v want WriterConfigInvalid", c.name, err)
			}
			return
		}
		//: happy arm — nil error and the mapped stream.
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
		{"warn name", map[string]any{"min_level": "warn"}, false, level.Warn},
		{"unknown name rejected", map[string]any{"min_level": "trace"}, true, 0},
		{"non-string rejected", map[string]any{"min_level": 4}, true, 0},
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
