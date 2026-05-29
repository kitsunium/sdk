package console

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/writer"
)

// : compile-time proof the factory satisfies the registry port (kept in the
// : test file per KTN-IFACE-ASSERT-PLACEMENT).
var _ writer.Factory = (*consoleFactory)(nil)

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
