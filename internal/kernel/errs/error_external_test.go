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
	return errs.Define(3101, "WRITER_NIL",
		"Log handler requires a non-nil writer",
		"service/logger.NewTextHandler called with nil io.Writer")
}

func TestDefine(t *testing.T) {
	t.Parallel()
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
			wantCode:    3101,
			wantReason:  "WRITER_NIL",
			wantPublic:  "Log handler requires a non-nil writer",
			wantLayer:   3,
			wantHTTP:    500,
			wantExit:    70,
			wantErrText: "[3101 WRITER_NIL] Log handler requires a non-nil writer",
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
		{"lowercase reason", func() { errs.Define(3100, "bad", "y", "z") }, "INVALID_REASON"},
		{"empty public", func() { errs.Define(3100, "GOOD_EMPTY", "", "z") }, "INVALID_PUBLIC"},
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
	tests := []struct {
		name       string
		code       int
		reason     string
		public     string
		wantCode   int
		wantReason string
	}{
		{"alias matches Define", 1100, "LEVEL_TEST_NEW", "Level test public", 1100, "LEVEL_TEST_NEW"},
	}
	for _, tc := range tests {
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
			e := errs.Define(3990, "WITH_HTTP_OPT", "Override http test public", "priv",
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
			e := errs.Define(3991, "WITH_EXIT_OPT", "Override exit test public", "priv",
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
				wrapped := errs.Wrap(context.Canceled, errs.WrapParams{
					Code: 3110, Reason: "CTX_CANCELLED",
					Public: "Operation aborted due to cancellation", Private: "wrapped context.Canceled",
				})
				if !errors.Is(wrapped, context.Canceled) {
					tt.Error("errors.Is(wrapped, context.Canceled) = false")
				}
				if wrapped.Code() != 3110 {
					tt.Errorf("Code = %d", wrapped.Code())
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(tt *testing.T) { tc.run(tt) })
	}
}

func TestWrapPanics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"stdlib wrap with zero code panics"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("expected panic")
				}
			}()
			errs.Wrap(fs.ErrNotExist, errs.WrapParams{Code: 0, Reason: "X", Public: "y", Private: "z"})
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
			if s := newSampleSentinel(t); s.Code() != 3101 {
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
			base := errs.Wrap(nil, errs.WrapParams{
				Code: 3100, Reason: "MUT_TEST", Public: "Mut test public", Private: "debug",
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
	tests := []struct {
		name   string
		code   int
		reason string
		want   int
	}{
		{"kernel layer", 1100, "LAYER_TEST_KERNEL", 1},
		{"service layer", 3101, "LAYER_TEST_SERVICE", 3},
		{"pkg layer", 4101, "LAYER_TEST_PKG", 4},
	}
	for _, tc := range tests {
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
				Code: 3110, Reason: "CTX_CANCELLED_UW", Public: "Operation aborted due to cancellation",
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
				Code: 3110, Reason: "CTX_CANCELLED_SRC", Public: "Operation aborted due to cancellation",
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
