package errs_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func newSampleSentinel(tb testing.TB) *errs.Error {
	tb.Helper()
	//: 0.3.1.1 = service/logger WriterNil (ADR 0005).
	return errs.Define(0x00_03_01_01, "WRITER_NIL",
		"Log handler requires a non-nil writer",
		"service/logger.NewTextHandler called with nil io.Writer")
}

func TestDefine(t *testing.T) {
	t.Parallel()
	//: the int returns cast from uint32(0x00_03_01_01) == 197889.
	//: Layer() returns the byte Layer octet as int.
	tests := []struct {
		name        string
		wantCode    int
		wantReason  string
		wantPublic  string
		wantLayer   int
		wantHTTP    int
		wantExit    int
		wantErrText string
	}{
		{
			name:        "writer-nil sentinel",
			wantCode:    int(uint32(0x00_03_01_01)),
			wantReason:  "WRITER_NIL",
			wantPublic:  "Log handler requires a non-nil writer",
			wantLayer:   3,
			wantHTTP:    500,
			wantExit:    70,
			wantErrText: "[0.3.1.1 WRITER_NIL] Log handler requires a non-nil writer",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSampleSentinel(t)
			if s.Code() != tc.wantCode {
				t.Errorf("Code() = %d", s.Code())
			}
			if s.Reason() != tc.wantReason {
				t.Errorf("Reason() = %q", s.Reason())
			}
			if s.Public() != tc.wantPublic {
				t.Errorf("Public() = %q", s.Public())
			}
			if s.Layer() != tc.wantLayer {
				t.Errorf("Layer() = %d", s.Layer())
			}
			if s.HTTPStatus() != tc.wantHTTP {
				t.Errorf("HTTPStatus() = %d", s.HTTPStatus())
			}
			if s.ExitCode() != tc.wantExit {
				t.Errorf("ExitCode() = %d", s.ExitCode())
			}
			if s.Error() != tc.wantErrText {
				t.Errorf("Error() = %q", s.Error())
			}
		})
	}
}

func TestDefinePanics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		call   func()
		substr string
	}{
		{"zero code", func() { errs.Define(0, "X", "y", "z") }, "INVALID_CODE"},
		//: 0x00_01_01_00 = 0.1.1.0 — valid non-meta layer, used to exercise the
		//: non-code validation paths.
		{"lowercase reason", func() { errs.Define(0x00_01_01_00, "bad", "y", "z") }, "INVALID_REASON"},
		{"empty public", func() { errs.Define(0x00_01_01_00, "GOOD_EMPTY", "", "z") }, "INVALID_PUBLIC"},
		{"layer 0 non-meta rejected", func() { errs.Define(0x00_00_01_00, "L0", "y", "z") }, "INVALID_CODE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("expected panic with %q", tc.substr)
				}
				msg, ok := r.(string)
				if !ok || !strings.Contains(msg, tc.substr) {
					t.Errorf("panic = %v, want contains %q", r, tc.substr)
				}
			}()
			tc.call()
		})
	}
}

func TestNewError(t *testing.T) {
	t.Parallel()
	//: 0x00_02_00_01 = 0.2.0.1 — valid layer-2 code for the NewError alias test.
	tests := []struct {
		name       string
		code       errs.Code
		reason     string
		public     string
		wantCode   int
		wantReason string
	}{
		{"alias matches Define", 0x00_02_00_01, "LEVEL_TEST_NEW", "Level test public", int(uint32(0x00_02_00_01)), "LEVEL_TEST_NEW"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := errs.NewError(tc.code, tc.reason, tc.public, "level test private")
			if got.Code() != tc.wantCode || got.Reason() != tc.wantReason {
				t.Errorf("got %d %q, want %d %q", got.Code(), got.Reason(), tc.wantCode, tc.wantReason)
			}
		})
	}
}

func TestWithHTTPStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		override int
		want     int
	}{
		{"override applied", 503, 503},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: 0x00_03_0F_96 = 0.3.15.150 — test code in an unused dotted-quad slot.
			e := errs.Define(0x00_03_0F_96, "WITH_HTTP_OPT", "Override http test public", "priv",
				errs.WithHTTPStatus(tc.override))
			if e.HTTPStatus() != tc.want {
				t.Errorf("HTTPStatus = %d, want %d", e.HTTPStatus(), tc.want)
			}
		})
	}
}

func TestWithExitCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		override int
		want     int
	}{
		{"override applied", 74, 74},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: 0x00_03_0F_97 = 0.3.15.151 — test code in an unused dotted-quad slot.
			e := errs.Define(0x00_03_0F_97, "WITH_EXIT_OPT", "Override exit test public", "priv",
				errs.WithExitCode(tc.override))
			if e.ExitCode() != tc.want {
				t.Errorf("ExitCode = %d, want %d", e.ExitCode(), tc.want)
			}
		})
	}
}

func TestWrap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(tt *testing.T)
	}{
		{
			name: "sdk cause → origin wins",
			run: func(tt *testing.T) {
				s := newSampleSentinel(tt)
				wrapped := errs.Wrap(s, errs.WrapParams{
					Code: 9999, Reason: "RELABEL_ATTEMPT",
					Public: "This public is ignored because origin wins", Private: "ignored",
				}, errs.String("extra", "v"))
				if wrapped.Code() != s.Code() {
					tt.Errorf("Code = %d, want inherit", wrapped.Code())
				}
				if wrapped.Reason() != s.Reason() {
					tt.Errorf("Reason = %q", wrapped.Reason())
				}
				if len(wrapped.Fields()) != 1 {
					tt.Errorf("expected 1 extra field, got %d", len(wrapped.Fields()))
				}
			},
		},
		{
			name: "stdlib cause → params apply, errors.Is preserved",
			run: func(tt *testing.T) {
				//: 0x00_03_01_0A = 0.3.1.10 (CtxCancelled dotted-quad).
				wrapped := errs.Wrap(context.Canceled, errs.WrapParams{
					Code: 0x00_03_01_0A, Reason: "CTX_CANCELLED",
					Public: "Operation aborted due to cancellation", Private: "wrapped context.Canceled",
				})
				if !errors.Is(wrapped, context.Canceled) {
					tt.Error("errors.Is(wrapped, context.Canceled) = false")
				}
				if wrapped.Code() != int(uint32(0x00_03_01_0A)) {
					tt.Errorf("Code = %d", wrapped.Code())
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(tt *testing.T) { tc.run(tt) })
	}
}

func TestWrap_RuntimeSafeOnBadParams(t *testing.T) {
	t.Parallel()
	//: v5 HIGH fix — runtime Wrap with invalid params MUST NOT panic; it
	//: returns an *Error with CodeInvalidWrapParams and preserves the cause.
	tests := []struct {
		name string
	}{
		{"stdlib wrap with zero code returns typed fallback (no panic)"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var got *errs.Error
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("expected NO panic, got: %v", r)
					}
				}()
				e := errs.Wrap(fs.ErrNotExist, errs.WrapParams{Code: 0, Reason: "X", Public: "y", Private: "z"})
				got = e
			}()
			if got == nil {
				t.Fatal("expected non-nil *Error")
			}
			if got.CodeValue() != errs.Code(errs.CodeInvalidWrapParams) {
				t.Errorf("expected CodeInvalidWrapParams, got %s", got.CodeValue())
			}
			if !errors.Is(got, fs.ErrNotExist) {
				t.Error("errors.Is should still reach fs.ErrNotExist through the wrap")
			}
		})
	}
}

func TestError_Code(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"sentinel code matches Define"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if s := newSampleSentinel(t); s.Code() != int(uint32(0x00_03_01_01)) {
				t.Errorf("Code = %d", s.Code())
			}
		})
	}
}

func TestError_Reason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"sentinel reason matches Define"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if s := newSampleSentinel(t); s.Reason() != "WRITER_NIL" {
				t.Errorf("Reason = %q", s.Reason())
			}
		})
	}
}

func TestError_Public(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"sentinel public matches Define"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if s := newSampleSentinel(t); s.Public() != "Log handler requires a non-nil writer" {
				t.Errorf("Public = %q", s.Public())
			}
		})
	}
}

func TestError_Private(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"sentinel private matches Define"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if s := newSampleSentinel(t); s.Private() != "service/logger.NewTextHandler called with nil io.Writer" {
				t.Errorf("Private = %q", s.Private())
			}
		})
	}
}

func TestError_Fields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"mutating returned slice does not affect the Error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: 0x00_03_0F_64 = 0.3.15.100 — test slot (unused production code).
			base := errs.Wrap(nil, errs.WrapParams{
				Code: 0x00_03_0F_64, Reason: "MUT_TEST", Public: "Mut test public", Private: "debug",
			}, errs.String("k", "v"))
			got := base.Fields()
			if len(got) != 1 {
				t.Fatalf("expected 1 field, got %d", len(got))
			}
			got[0] = errs.String("other", "other")
			again := base.Fields()
			if again[0].Key() != "k" {
				t.Errorf("defensive copy failed: mutated to %s", again[0].Key())
			}
		})
	}
}

func TestError_Layer(t *testing.T) {
	t.Parallel()
	//: Layer extracts the second byte (byte 2). ADR 0005 dotted-quad codes:
	//:   0x00_01_01_00 = 0.1.1.0 (kernel)  → Layer 1
	//:   0x00_03_01_01 = 0.3.1.1 (service) → Layer 3
	//:   0x01_01_00_01 = 1.1.0.1 (pkg v1)  → Layer 1 (under v1 major)
	tests := []struct {
		name   string
		code   errs.Code
		reason string
		want   int
	}{
		{"kernel layer", 0x00_01_01_00, "LAYER_TEST_KERNEL", 1},
		{"service layer", 0x00_03_01_01, "LAYER_TEST_SERVICE", 3},
		{"pkg v1 layer", 0x01_01_00_01, "LAYER_TEST_PKG", 1},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := errs.Define(tc.code, tc.reason, "Layer test public", "priv")
			if e.Layer() != tc.want {
				t.Errorf("Layer = %d, want %d", e.Layer(), tc.want)
			}
		})
	}
}

func TestError_Error(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Error never contains Private or Fields"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSampleSentinel(t)
			wrapped := errs.Wrap(s, errs.WrapParams{}, errs.String("secret_key", "secret_value"))
			got := wrapped.Error()
			if strings.Contains(got, s.Private()) {
				t.Errorf("leaked Private: %q", got)
			}
			if strings.Contains(got, "secret_key") || strings.Contains(got, "secret_value") {
				t.Errorf("leaked Field: %q", got)
			}
		})
	}
}

func TestError_Unwrap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Unwrap returns wrapped cause"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wrapped := errs.Wrap(context.Canceled, errs.WrapParams{
				Code: 0x00_03_01_0A, Reason: "CTX_CANCELLED_UW", Public: "Operation aborted due to cancellation",
				Private: "debug",
			})
			if wrapped.Unwrap() != context.Canceled {
				t.Errorf("Unwrap = %v", wrapped.Unwrap())
			}
			var nilErr *errs.Error
			if nilErr.Unwrap() != nil {
				t.Errorf("nil Unwrap = %v", nilErr.Unwrap())
			}
		})
	}
}

func TestError_Source(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"Source returns wrapped cause"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wrapped := errs.Wrap(context.Canceled, errs.WrapParams{
				Code: 0x00_03_01_0A, Reason: "CTX_CANCELLED_SRC", Public: "Operation aborted due to cancellation",
				Private: "debug",
			})
			if wrapped.Source() != context.Canceled {
				t.Errorf("Source = %v", wrapped.Source())
			}
		})
	}
}

func TestError_HTTPStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"default 500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if s := newSampleSentinel(t); s.HTTPStatus() != 500 {
				t.Errorf("HTTPStatus = %d", s.HTTPStatus())
			}
		})
	}
}

func TestError_ExitCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"default 70"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if s := newSampleSentinel(t); s.ExitCode() != 70 {
				t.Errorf("ExitCode = %d", s.ExitCode())
			}
		})
	}
}
