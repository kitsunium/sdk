package console_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	_ "github.com/kitsunium/sdk/internal/service/writer/console"
)

func TestConsoleRegistered(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"importing the package registers the console writer"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: the blank import must have self-registered the factory.
		if !writer.Name("console").Known() {
			t.Errorf("console writer not registered after import")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestConsoleOpen(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		cfg     writer.Config
		wantErr bool
	}
	tests := []tc{
		{"stdout default", writer.ConsoleConfig{}, false},
		{"stderr stream", writer.ConsoleConfig{Stream: writer.ConsoleStderr}, false},
		{"per-writer error floor", writer.ConsoleConfig{MinLevel: level.Error}, false},
		{"wrong config type rejected", writer.FileConfig{Path: "/x"}, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := writer.Open("console", c.cfg)
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
