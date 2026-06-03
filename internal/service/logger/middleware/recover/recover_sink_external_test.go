package recover_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	recoversink "github.com/kitsunium/sdk/internal/service/logger/middleware/recover"
	filesink "github.com/kitsunium/sdk/internal/service/logger/sink/file"
)

// panickingSink panics on every method to exercise the recovery branches.
type panickingSink struct{}

func (panickingSink) Write(_ context.Context, _ corelogger.RecordEvent, _ []byte) (int, error) {
	panic("write boom")
}
func (panickingSink) Flush(_ context.Context) error { panic("flush boom") }
func (panickingSink) Close() error                  { panic("close boom") }

// quietSink never panics — used to assert the happy path.
type quietSink struct{}

func (quietSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}
func (quietSink) Flush(_ context.Context) error { return nil }
func (quietSink) Close() error                  { return nil }

// erringSink returns a downstream stdlib sentinel from every method WITHOUT
// panicking. It proves the recover wrapper is transparent on the normal
// error path — a non-panic error must pass through untouched, not be
// re-wrapped under the Panicked sentinel.
type erringSink struct{}

func (erringSink) Write(_ context.Context, _ corelogger.RecordEvent, _ []byte) (int, error) {
	//: a deterministic stdlib sentinel so errors.Is can pin identity downstream.
	return 0, io.ErrClosedPipe
}
func (erringSink) Flush(_ context.Context) error { return io.ErrClosedPipe }
func (erringSink) Close() error                  { return io.ErrClosedPipe }

// nilPanicSink panics with a literal nil on every method. Go 1.21+ converts
// panic(nil) into a *runtime.PanicNilError, so the recover block's rv != nil
// guard MUST still fire — proving the nil-panic is caught, not swallowed.
type nilPanicSink struct{}

//nolint:govet // intentional panic(nil) to exercise the PanicNilError path.
func (nilPanicSink) Write(_ context.Context, _ corelogger.RecordEvent, _ []byte) (int, error) {
	//: panic(nil) materialises as *runtime.PanicNilError under Go 1.21+.
	panic(nil)
}

//nolint:govet // intentional panic(nil) to exercise the PanicNilError path.
func (nilPanicSink) Flush(_ context.Context) error { panic(nil) }

//nolint:govet // intentional panic(nil) to exercise the PanicNilError path.
func (nilPanicSink) Close() error { panic(nil) }

func TestNew(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		nilDown  bool
		wantCode errs.Code
	}{
		{"non-nil downstream succeeds", false, 0},
		{"nil downstream yields DownstreamNil", true, recoversink.CodeRecoverDownstreamNil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var down corelogger.Sink
			if !tc.nilDown {
				down = quietSink{}
			}
			s, err := recoversink.New(down)
			if tc.wantCode == 0 {
				if err != nil {
					t.Errorf("New err = %v, want nil", err)
				}
				if s == nil {
					t.Error("New returned nil sink on happy path")
				}
				return
			}
			if !errs.HasCode(err, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", err, tc.wantCode)
			}
		})
	}
}

func TestRecover_Write(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		panic    bool
		wantCode errs.Code
	}{
		{"happy path delegates to downstream", false, 0},
		{"panic surfaces as Panicked", true, recoversink.CodeRecoverPanicked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var down corelogger.Sink = quietSink{}
			if tc.panic {
				down = panickingSink{}
			}
			s, err := recoversink.New(down)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			_, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			if tc.wantCode == 0 {
				if werr != nil {
					t.Errorf("Write err = %v, want nil", werr)
				}
				return
			}
			if !errs.HasCode(werr, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", werr, tc.wantCode)
			}
		})
	}
}

func TestRecover_Flush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		panic    bool
		wantCode errs.Code
	}{
		{"happy path delegates to downstream", false, 0},
		{"panic surfaces as Panicked", true, recoversink.CodeRecoverPanicked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var down corelogger.Sink = quietSink{}
			if tc.panic {
				down = panickingSink{}
			}
			s, err := recoversink.New(down)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			ferr := s.Flush(t.Context())
			if tc.wantCode == 0 {
				if ferr != nil {
					t.Errorf("Flush err = %v, want nil", ferr)
				}
				return
			}
			if !errs.HasCode(ferr, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", ferr, tc.wantCode)
			}
		})
	}
}

func TestRecover_Close(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		panic    bool
		wantCode errs.Code
	}{
		{"happy path delegates to downstream", false, 0},
		{"panic surfaces as Panicked", true, recoversink.CodeRecoverPanicked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var down corelogger.Sink = quietSink{}
			if tc.panic {
				down = panickingSink{}
			}
			s, err := recoversink.New(down)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			cerr := s.Close()
			if tc.wantCode == 0 {
				if cerr != nil {
					t.Errorf("Close err = %v, want nil", cerr)
				}
				return
			}
			if !errs.HasCode(cerr, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", cerr, tc.wantCode)
			}
		})
	}
}

func TestRecoverSentinels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		code errs.Code
	}{
		{"Panicked carries 0.3.21.1", recoversink.Panicked, recoversink.CodeRecoverPanicked},
		{"DownstreamNil carries 0.3.21.2", recoversink.DownstreamNil, recoversink.CodeRecoverDownstreamNil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if !errs.HasCode(tc.err, tc.code) {
				t.Errorf("HasCode(%v, %d) = false", tc.err, tc.code)
			}
		})
	}
}

// fieldByKey returns the StringValue of the first FieldValue whose Key equals
// key, and whether such a field was present. Used to assert panic metadata
// without depending on field ordering.
func fieldByKey(fields []errs.FieldValue, key string) (rendered string, found bool) {
	for _, f := range fields {
		//: first match wins — the recover wrapper attaches each key once.
		if f.Key() == key {
			return f.StringValue(), true
		}
	}
	return "", false
}

// TestRecover_Write_ByteCount pins the byte-count contract on both paths: the
// happy path forwards the downstream count verbatim, while a recovered panic
// surfaces n == 0 alongside the Panicked sentinel (named returns are zeroed
// when the deferred recover overwrites err).
func TestRecover_Write_ByteCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		panicky  bool
		payload  string
		wantN    int
		wantCode errs.Code
	}{
		{"happy path forwards downstream byte count", false, "hello", 5, 0},
		{"panic yields zero bytes and Panicked", true, "hello", 0, recoversink.CodeRecoverPanicked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var down corelogger.Sink = quietSink{}
			if tc.panicky {
				down = panickingSink{}
			}
			s, err := recoversink.New(down)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			n, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte(tc.payload))
			if n != tc.wantN {
				t.Errorf("Write n = %d, want %d", n, tc.wantN)
			}
			//: happy path returns nil; panic path returns the typed sentinel.
			if tc.wantCode == 0 {
				if werr != nil {
					t.Errorf("Write err = %v, want nil", werr)
				}
				return
			}
			if !errs.HasCode(werr, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", werr, tc.wantCode)
			}
		})
	}
}

// TestRecover_Flush_ByteCount asserts Flush carries no byte count — only an
// error — so the happy path returns nil and the panic path returns Panicked.
// (Flush / Close have no n return; byte-count assertions do not apply.)
func TestRecover_Flush_ByteCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		panicky  bool
		wantCode errs.Code
	}{
		{"happy path returns nil", false, 0},
		{"panic surfaces Panicked", true, recoversink.CodeRecoverPanicked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var down corelogger.Sink = quietSink{}
			if tc.panicky {
				down = panickingSink{}
			}
			s, err := recoversink.New(down)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			ferr := s.Flush(t.Context())
			if tc.wantCode == 0 {
				if ferr != nil {
					t.Errorf("Flush err = %v, want nil", ferr)
				}
				return
			}
			if !errs.HasCode(ferr, tc.wantCode) {
				t.Errorf("HasCode(%v, %d) = false", ferr, tc.wantCode)
			}
		})
	}
}

// TestRecover_Write_DownstreamError proves transparency on the normal error
// path: a downstream that RETURNS (never panics) a stdlib sentinel must reach
// the caller untouched. The recover wrapper only intercepts panics, so
// errors.Is against the original cause MUST still hold across Write/Flush/Close.
func TestRecover_Write_DownstreamError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: op invokes one Sink method and returns only its error component.
		op func(context.Context, corelogger.Sink) error
	}{
		{"Write passes a downstream error through", func(ctx context.Context, s corelogger.Sink) error {
			_, werr := s.Write(ctx, corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			return werr
		}},
		{"Flush passes a downstream error through", func(ctx context.Context, s corelogger.Sink) error {
			return s.Flush(ctx)
		}},
		{"Close passes a downstream error through", func(_ context.Context, s corelogger.Sink) error {
			return s.Close()
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := recoversink.New(erringSink{})
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			got := tc.op(t.Context(), s)
			//: identity must survive — the wrapper must not replace a non-panic error.
			if !errors.Is(got, io.ErrClosedPipe) {
				t.Errorf("err = %v, want errors.Is(io.ErrClosedPipe)", got)
			}
			//: and it must NOT have been relabelled as a recovered panic.
			if errs.HasCode(got, recoversink.CodeRecoverPanicked) {
				t.Errorf("non-panic error %v was wrongly tagged Panicked", got)
			}
		})
	}
}

// TestRecover_Write_PanicFields asserts the structured metadata attached to a
// recovered panic. Write attaches both panic_type and level; Flush and Close
// attach panic_type but never level (they have no RecordEvent in scope).
func TestRecover_Write_PanicFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: invoke runs one panicking Sink method and returns the recovered error.
		invoke func(context.Context, corelogger.Sink) error
		//: wantLevel is true only for Write, the sole method holding a RecordEvent.
		wantLevel bool
	}{
		{"Write attaches panic_type and level", func(ctx context.Context, s corelogger.Sink) error {
			_, werr := s.Write(ctx, corelogger.RecordEvent{Level: level.Error}, []byte("x"))
			return werr
		}, true},
		{"Flush attaches panic_type but not level", func(ctx context.Context, s corelogger.Sink) error {
			return s.Flush(ctx)
		}, false},
		{"Close attaches panic_type but not level", func(_ context.Context, s corelogger.Sink) error {
			return s.Close()
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := recoversink.New(panickingSink{})
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			fields := errs.FieldsOf(tc.invoke(t.Context(), s))
			pt, ok := fieldByKey(fields, "panic_type")
			//: panic_type identifies the recovered value's Go type for triage.
			if !ok || pt == "" {
				t.Errorf("panic_type field = %q present=%v, want non-empty", pt, ok)
			}
			lv, ok := fieldByKey(fields, "level")
			//: only the Write path carries a RecordEvent, so only it attaches level.
			if ok != tc.wantLevel {
				t.Fatalf("level field present=%v, want %v", ok, tc.wantLevel)
			}
			//: when present, level must mirror RecordEvent.Level as a decimal int.
			if tc.wantLevel {
				if want := strconv.Itoa(int(level.Error)); lv != want {
					t.Errorf("level field = %q, want %q", lv, want)
				}
			}
		})
	}
}

// TestRecover_Write_NilPanic locks the panic(nil) edge: Go 1.21+ converts a
// literal nil panic into a *runtime.PanicNilError, so the recover block's
// rv != nil guard still fires and the panic is caught — never silently
// swallowed — across Write / Flush / Close.
func TestRecover_Write_NilPanic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		//: op invokes one Sink method and returns only its error component.
		op func(context.Context, corelogger.Sink) error
	}{
		{"Write catches panic(nil)", func(ctx context.Context, s corelogger.Sink) error {
			_, werr := s.Write(ctx, corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			return werr
		}},
		{"Flush catches panic(nil)", func(ctx context.Context, s corelogger.Sink) error {
			return s.Flush(ctx)
		}},
		{"Close catches panic(nil)", func(_ context.Context, s corelogger.Sink) error {
			return s.Close()
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := recoversink.New(nilPanicSink{})
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			got := tc.op(t.Context(), s)
			//: a nil panic must surface as Panicked, proving rv != nil still fired.
			if !errs.HasCode(got, recoversink.CodeRecoverPanicked) {
				t.Errorf("HasCode(%v, Panicked) = false — panic(nil) was swallowed", got)
			}
		})
	}
}

// TestRecover_Write_RealFileSink drives the production Write end-to-end through
// a REAL on-disk file sink (no mock): the recover wrapper must be transparent
// on the happy path, so the exact bytes handed to Write land on disk and the
// returned count matches the payload length.
func TestRecover_Write_RealFileSink(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload string
	}{
		{"single line round-trips through the wrapper", "end-to-end through recover\n"},
		{"empty payload writes zero bytes cleanly", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "recover-e2e.log")
			down, err := filesink.New(path)
			if err != nil {
				t.Fatalf("file sink New err = %v", err)
			}
			t.Cleanup(func() {
				//: release the descriptor deterministically; a close failure on an
				//: append-only temp file is itself a regression worth surfacing.
				if cerr := down.Close(); cerr != nil && !errors.Is(cerr, os.ErrClosed) {
					t.Errorf("file sink Close err = %v", cerr)
				}
			})
			s, err := recoversink.New(down)
			if err != nil {
				t.Fatalf("recover New err = %v", err)
			}
			payload := []byte(tc.payload)
			n, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, payload)
			if werr != nil {
				t.Fatalf("Write err = %v, want nil", werr)
			}
			//: a transparent wrapper forwards the terminal sink's byte count verbatim.
			if n != len(payload) {
				t.Errorf("Write n = %d, want %d", n, len(payload))
			}
			if ferr := s.Flush(t.Context()); ferr != nil {
				t.Fatalf("Flush err = %v, want nil", ferr)
			}
			got, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatalf("ReadFile err = %v", rerr)
			}
			//: the real file must contain exactly the bytes that entered Write.
			if string(got) != tc.payload {
				t.Errorf("file contents = %q, want %q", got, tc.payload)
			}
			//: Close end-to-end through the wrapper must release the real descriptor
			//: cleanly; the Cleanup close then tolerates the already-closed fd.
			if cerr := s.Close(); cerr != nil {
				t.Errorf("Close err = %v, want nil", cerr)
			}
		})
	}
}
