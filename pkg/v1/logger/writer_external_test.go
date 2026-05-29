package logger_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/logger"
	// Registers the console + file writers so NewMulti can resolve them.
	_ "github.com/kitsunium/sdk/pkg/v1/logger/writer"
)

func TestNewMulti(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		specs   func(dir string) []logger.WriterSpec
		wantErr error
		wantOK  bool
	}
	tests := []tc{
		{
			name: "console and file build a logger",
			specs: func(dir string) []logger.WriterSpec {
				return []logger.WriterSpec{
					{Name: "console", Config: logger.ConsoleConfig{Stream: logger.StreamStderr}},
					{Name: "file", Config: logger.FileConfig{Path: filepath.Join(dir, "m.log")}},
				}
			},
			wantOK: true,
		},
		{
			name:    "no specs is rejected",
			specs:   func(string) []logger.WriterSpec { return nil },
			wantErr: logger.WriterSpecInvalid,
		},
		{
			name: "unknown writer name is rejected",
			specs: func(string) []logger.WriterSpec {
				return []logger.WriterSpec{{Name: "ghost-writer", Config: logger.ConsoleConfig{}}}
			},
			wantOK: false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		lg, err := logger.NewMulti(logger.LevelInfo, c.specs(t.TempDir())...)
		//: a specific sentinel expectation must match via errors.Is.
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) {
				t.Errorf("%s: err=%v want %v", c.name, err, c.wantErr)
			}
			return
		}
		//: the happy arm must build a logger; the unknown-name arm must error.
		if c.wantOK {
			if err != nil || lg == nil {
				t.Errorf("%s: err=%v lg=%v want nil+logger", c.name, err, lg)
			}
			return
		}
		//: remaining arm — unresolved name yields a non-nil error.
		if err == nil {
			t.Errorf("%s: expected an error, got nil", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestNewMultiFanOut(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		branches int
	}
	tests := []tc{{"every file branch receives the same record", 2}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		//: build N distinct file writers sharing one logger.
		paths := make([]string, c.branches)
		specs := make([]logger.WriterSpec, c.branches)
		for i := range paths {
			paths[i] = filepath.Join(dir, fmt.Sprintf("branch-%d.log", i))
			specs[i] = logger.WriterSpec{Name: "file", Config: logger.FileConfig{Path: paths[i]}}
		}
		lg, err := logger.NewMulti(logger.LevelInfo, specs...)
		//: construction must succeed for valid file writers.
		if err != nil {
			t.Fatalf("%s: NewMulti: %v", c.name, err)
		}
		logger.Info(t.Context(), lg, "fan-out-marker")
		//: the fan-out is correct iff EVERY branch file carries the record.
		for _, p := range paths {
			data, rErr := os.ReadFile(p)
			content := string(data)
			if rErr != nil || !strings.Contains(content, "fan-out-marker") {
				t.Errorf("%s: file %s err=%v content=%q want the marker", c.name, p, rErr, content)
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

func TestNewCredentialValue(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"facade ctor produces a redacting value"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		cv := logger.NewCredentialValue("AKIA", "secret", "")
		//: the façade value must redact like the core type.
		if cv.String() != "<redacted>" {
			t.Errorf("String()=%q want <redacted>", cv.String())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
