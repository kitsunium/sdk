package console_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/sink/console"
)

// errBoom is the sentinel cause used by the origin-wins tests; a distinct
// identity lets errors.Is prove the cause survives the errs.Wrap chain.
var errBoom = errors.New("boom-sentinel")

// newCancelledCtx builds a context already in the requested cancelled state so
// each subtest exercises a single ctx.Err() identity without inline plumbing.
func newCancelledCtx(t *testing.T, deadline bool) context.Context {
	t.Helper()
	if deadline {
		//: a zero-length timeout fires DeadlineExceeded before the first write.
		ctx, cancel := context.WithTimeout(t.Context(), 0)
		t.Cleanup(cancel)
		return ctx
	}
	//: explicit cancel surfaces context.Canceled as ctx.Err().
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

func TestConsoleSink_Write_OriginWins_CtxCancelled(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		deadline bool
		wantErr  error
	}{
		{"cancelled context exposes context.Canceled", false, context.Canceled},
		{"expired timeout exposes context.DeadlineExceeded", true, context.DeadlineExceeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := console.New(io.Discard)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			ctx := newCancelledCtx(t, tc.deadline)
			_, werr := s.Write(ctx, corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			//: errs.Wrap stored ctx.Err() as Source, so stdlib Is must still walk to the cause.
			if !errors.Is(werr, tc.wantErr) {
				t.Errorf("errors.Is(%v, %v) = false", werr, tc.wantErr)
			}
			//: the SDK reason is layered on top of the stdlib cause, not in place of it.
			if !errs.HasCode(werr, console.CodeCtxCancelled) {
				t.Errorf("HasCode(%v, CtxCancelled) = false", werr)
			}
		})
	}
}

func TestConsoleSink_Write_WriteFailed_OriginWins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		give error
	}{
		{"writer sentinel reaches through the wrap chain", errBoom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := console.New(failingWriter{err: tc.give})
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			_, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			//: WriteFailed is the wrapping reason the consumer routes on.
			if !errs.HasCode(werr, console.CodeWriteFailed) {
				t.Errorf("HasCode(%v, WriteFailed) = false", werr)
			}
			//: origin wins: the underlying writer error stays reachable via Unwrap.
			if !errors.Is(werr, tc.give) {
				t.Errorf("errors.Is(%v, %v) = false", werr, tc.give)
			}
		})
	}
}

// fieldValue returns the FieldValue carrying key from err, or ("",false) when
// absent — keeps each diagnostic assertion a single readable expression.
func fieldValue(err error, key string) (errs.FieldValue, bool) {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key {
			return f, true
		}
	}
	return errs.FieldValue{}, false
}

func TestConsoleSink_Write_WriteFailed_DiagnosticFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload []byte
		lvl     level.Level
	}{
		{"warn write attaches bytes and level fields", []byte("hello"), level.Warn},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := console.New(failingWriter{err: errBoom})
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			_, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: tc.lvl}, tc.payload)
			//: errs.Int("bytes", len(p)) is attached in production but otherwise untested.
			if f, ok := fieldValue(werr, "bytes"); !ok || f.StringValue() != "5" {
				t.Errorf("bytes field = %q (ok=%v), want %q", f.StringValue(), ok, "5")
			}
			//: errs.Int("level", int(r.Level)) must round-trip the record's level.
			want := errs.Int("level", int(tc.lvl)).StringValue()
			if f, ok := fieldValue(werr, "level"); !ok || f.StringValue() != want {
				t.Errorf("level field = %q (ok=%v), want %q", f.StringValue(), ok, want)
			}
		})
	}
}

func TestConsoleSink_Write_WriteFailed_ExitCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		cause    error
		wantExit int
	}{
		//: stdlib cause → Wrap takes the new-from-stdlib path, where ExitCode comes
		//: from WrapParams.ExitCode (unset here → default 70), NOT the sentinel's 74.
		{"stdlib cause yields default EX_SOFTWARE", errBoom, 70},
		//: *errs.Error cause → origin wins, so the WriteFailed sentinel's
		//: WithExitCode(74) is inherited and EX_IOERR reaches the CLI consumer.
		{"errs cause inherits sentinel EX_IOERR", console.WriteFailed, 74},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := console.New(failingWriter{err: tc.cause})
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			_, werr := s.Write(t.Context(), corelogger.RecordEvent{Level: level.Info}, []byte("x"))
			//: pins the path-dependent exit code so a future Wrap change fails loudly.
			if got := errs.ExitCodeOf(werr); got != tc.wantExit {
				t.Errorf("ExitCodeOf(%v) = %d, want %d", werr, got, tc.wantExit)
			}
		})
	}
}

func TestConsoleSink_Flush_CancelledContext_Identity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		deadline bool
		wantErr  error
	}{
		{"cancelled context surfaces context.Canceled raw", false, context.Canceled},
		{"expired timeout surfaces context.DeadlineExceeded raw", true, context.DeadlineExceeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := console.New(io.Discard)
			if err != nil {
				t.Fatalf("New err = %v", err)
			}
			ferr := s.Flush(newCancelledCtx(t, tc.deadline))
			//: Flush returns raw ctx.Err(), so the stdlib identity must match directly.
			if !errors.Is(ferr, tc.wantErr) {
				t.Errorf("errors.Is(%v, %v) = false", ferr, tc.wantErr)
			}
			//: intentional asymmetry — Flush does NOT errs.Wrap the way Write does.
			if errs.HasCode(ferr, console.CodeCtxCancelled) {
				t.Errorf("Flush unexpectedly wrapped CtxCancelled: %v", ferr)
			}
		})
	}
}

func TestNew_TypedNilWriter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"typed-nil writer passes the interface-nil guard"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: a typed-nil *bytes.Buffer is a non-nil io.Writer interface value, so
			//: the w == nil guard does NOT trip — Write on this sink would panic.
			var b *bytes.Buffer
			s, err := console.New(b)
			if err != nil {
				t.Errorf("New(typed-nil) err = %v, want nil", err)
			}
			//: pin the accepted edge case so a future guard change fails loudly here.
			if s == nil {
				t.Error("New(typed-nil) returned nil sink")
			}
		})
	}
}
