package file_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	filesink "github.com/kitsunium/sdk/internal/service/logger/sink/file"
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

// rec builds a RecordEvent carrying only the level the gate inspects.
func rec(l level.Level) corelogger.RecordEvent {
	//: the levelgate reads r.Level; nothing else influences the gate decision.
	return corelogger.RecordEvent{Level: l}
}

// TestFileOpen_WriteFlushRoundTrip drives the production Write/Flush/Close path
// end-to-end against a real on-disk file and reads the bytes back. This is the
// real-I/O E2E: the factory's Open returns a sink wrapping the hardened file
// sink, and the assertion is on the bytes the OS actually persisted.
func TestFileOpen_WriteFlushRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		min    level.Level
		writes []struct {
			lvl     level.Level
			payload []byte
		}
		wantPresent [][]byte
		wantAbsent  [][]byte
	}
	tests := []tc{
		{
			name: "no floor persists the written payload",
			min:  level.Info,
			writes: []struct {
				lvl     level.Level
				payload []byte
			}{{level.Info, []byte("hello\n")}},
			//: with no floor the single Info payload must reach the file verbatim.
			wantPresent: [][]byte{[]byte("hello\n")},
		},
		{
			name: "warn floor drops info keeps warn",
			min:  level.Warn,
			writes: []struct {
				lvl     level.Level
				payload []byte
			}{
				{level.Info, []byte("dropped-info\n")},
				{level.Warn, []byte("kept-warn\n")},
			},
			//: the gate must let the at-floor Warn record through to disk.
			wantPresent: [][]byte{[]byte("kept-warn\n")},
			//: the below-floor Info record must never reach the file sink.
			wantAbsent: [][]byte{[]byte("dropped-info\n")},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "round-trip.log")
		sink, err := writer.Open("file", writer.FileConfig{Path: path, MinLevel: c.min})
		//: a usable sink is the precondition for the round-trip assertion.
		if err != nil || sink == nil {
			t.Fatalf("%s: Open err=%v sink=%v want nil+sink", c.name, err, sink)
		}
		//: tracks whether the explicit Close already ran so cleanup is a no-op then.
		closed := false
		//: release the descriptor even if an early assertion aborts the body.
		t.Cleanup(func() {
			//: skip the cleanup close once the happy path closed explicitly.
			if closed {
				return
			}
			//: surface a cleanup close failure rather than discarding it.
			if cerr := sink.Close(); cerr != nil {
				t.Errorf("%s: cleanup close failed: %v", c.name, cerr)
			}
		})
		ctx := t.Context()
		//: feed each record through the real production Write seam.
		for _, w := range c.writes {
			if _, werr := sink.Write(ctx, rec(w.lvl), w.payload); werr != nil {
				t.Fatalf("%s: Write err=%v want nil", c.name, werr)
			}
		}
		//: Flush forces fsync so the bytes are durable before the read-back.
		if ferr := sink.Flush(ctx); ferr != nil {
			t.Fatalf("%s: Flush err=%v want nil", c.name, ferr)
		}
		//: Close so the read-back observes a fully released descriptor.
		if cerr := sink.Close(); cerr != nil {
			t.Fatalf("%s: Close err=%v want nil", c.name, cerr)
		}
		//: mark closed so the deferred cleanup does not double-close.
		closed = true
		got, rerr := os.ReadFile(path)
		//: the file must be readable once Close has returned.
		if rerr != nil {
			t.Fatalf("%s: ReadFile err=%v want nil", c.name, rerr)
		}
		//: every passing payload must be present in the persisted bytes.
		for _, want := range c.wantPresent {
			if !bytes.Contains(got, want) {
				t.Errorf("%s: file=%q missing %q", c.name, got, want)
			}
		}
		//: every dropped payload must be absent from the persisted bytes.
		for _, absent := range c.wantAbsent {
			if bytes.Contains(got, absent) {
				t.Errorf("%s: file=%q unexpectedly contains %q", c.name, got, absent)
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

// TestFileOpen_ErrorCodes pins the factory's Open error-forwarding contract:
// origin wins, so an empty path surfaces the file sink's own CodePathEmpty and a
// wrong config type surfaces the shared CodeWriterConfigInvalid.
func TestFileOpen_ErrorCodes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		cfg      writer.Config
		wantCode errs.Code
	}
	tests := []tc{
		//: empty path defers to the sink, so the sink's PathEmpty wins (origin).
		{"empty path carries CodePathEmpty", writer.FileConfig{Path: ""}, filesink.CodePathEmpty},
		//: a mismatched config type is rejected before delegation with the shared code.
		{"wrong config type carries CodeWriterConfigInvalid", writer.ConsoleConfig{}, writer.CodeWriterConfigInvalid},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sink, err := writer.Open("file", c.cfg)
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
