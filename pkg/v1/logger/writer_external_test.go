package logger_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	corewriter "github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/pkg/v1/logger"

	// Registers the console + file writers so NewMulti can resolve them.
	_ "github.com/kitsunium/sdk/pkg/v1/logger/writer"
)

// probeCloseCount counts Close calls on probe sinks so the leak test can assert
// NewMulti released an already-opened sink when a later spec failed to resolve.
var probeCloseCount atomic.Int64

// probeSink is a no-op Sink whose Close bumps probeCloseCount.
type probeSink struct{}

func (probeSink) Write(context.Context, corelogger.RecordEvent, []byte) (int, error) {
	//: the leak test never emits — only construction + rollback matter.
	return 0, nil
}

func (probeSink) Flush(context.Context) error { return nil }

func (probeSink) Close() error {
	//: record that this opened sink was released on rollback.
	probeCloseCount.Add(1)
	return nil
}

// probeFactory registers under a unique Name and hands back a probeSink.
type probeFactory struct{}

func (probeFactory) Name() corewriter.Name { return "probe-leak" }

func (probeFactory) Open(corewriter.Config) (corelogger.Sink, error) {
	//: a fresh probe sink per Open so the rollback close is observable.
	return probeSink{}, nil
}

// Register the probe writer once at package load (mirrors the real writers).
var _ = corewriter.Register(probeFactory{})

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

func TestDefaultMulti(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		path   func(dir string) string
		wantOK bool
	}
	tests := []tc{
		{
			name:   "console and file build a logger that writes to the file",
			path:   func(dir string) string { return filepath.Join(dir, "default-multi.log") },
			wantOK: true,
		},
		{
			//: an unwritable directory makes the file writer fail to open, which
			//: rolls the already-opened console sink back and surfaces an error.
			name:   "unopenable file path is rejected",
			path:   func(dir string) string { return filepath.Join(dir, "missing-subdir", "x.log") },
			wantOK: false,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := c.path(t.TempDir())
		lg, err := logger.DefaultMulti(path)
		//: the failure arm must surface a non-nil error and no logger.
		if !c.wantOK {
			if err == nil {
				t.Errorf("%s: expected an error, got nil", c.name)
			}
			return
		}
		//: the happy arm must build a logger.
		if err != nil || lg == nil {
			t.Fatalf("%s: err=%v lg=%v want nil+logger", c.name, err, lg)
		}
		//: the file branch must actually receive an emitted record.
		logger.Info(t.Context(), lg, "default-multi-marker")
		data, rErr := os.ReadFile(path)
		content := string(data)
		if rErr != nil || !strings.Contains(content, "default-multi-marker") {
			t.Errorf("%s: file %s err=%v content=%q want the marker", c.name, path, rErr, content)
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

func TestNewMultiClosesOpenedSinksOnError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a later unresolved writer closes the sinks already opened"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		before := probeCloseCount.Load()
		//: the first spec opens a probe sink; the second (unknown) fails to
		//: resolve, so NewMulti must close the opened probe sink before returning.
		_, err := logger.NewMulti(
			logger.LevelInfo,
			logger.WriterSpec{Name: "probe-leak", Config: nil},
			logger.WriterSpec{Name: "ghost-zzz", Config: nil},
		)
		//: the unresolved second writer must surface an error.
		if err == nil {
			t.Fatal("NewMulti: want error from the unknown second writer")
		}
		//: exactly the one opened probe sink must have been closed (no leak).
		if got := probeCloseCount.Load() - before; got != 1 {
			t.Errorf("probe Close calls=%d want 1 (opened sink leaked on error)", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			//: parallel-safe: probe-leak is opened only here, so the atomic
			//: before/after delta is unaffected by other concurrent tests.
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
