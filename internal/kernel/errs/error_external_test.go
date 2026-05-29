package errs_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// newSampleSentinel builds the shared sentinel used across multiple tests.
// Centralised so any future code-range change touches one place.
func newSampleSentinel(tb testing.TB) *errs.Error {
	tb.Helper()
	//: 0.3.1.1 = service/logger WriterNil (ADR 0005).
	return errs.Define(0x00_03_01_01, "WRITER_NIL",
		"Log handler requires a non-nil writer",
		"service/logger.NewTextHandler called with nil io.Writer")
}

func TestDefine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		wantCode    errs.Code
		wantReason  string
		wantPublic  string
		wantLayer   errs.Layer
		wantHTTP    int
		wantExit    int
		wantErrText string
	}{
		{
			name:        "writer-nil sentinel",
			wantCode:    0x00_03_01_01,
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
			t.Parallel()
			//: rebuild the sentinel under the subtest so each parallel case is independent.
			sentinel := newSampleSentinel(t)
			if sentinel.Code() != tc.wantCode {
				t.Errorf("Code() = %s", sentinel.Code())
			}
			if sentinel.Reason() != tc.wantReason {
				t.Errorf("Reason() = %q", sentinel.Reason())
			}
			if sentinel.Public() != tc.wantPublic {
				t.Errorf("Public() = %q", sentinel.Public())
			}
			if sentinel.Code().Layer() != tc.wantLayer {
				t.Errorf("Code().Layer() = %d", sentinel.Code().Layer())
			}
			if sentinel.HTTPStatus() != tc.wantHTTP {
				t.Errorf("HTTPStatus() = %d", sentinel.HTTPStatus())
			}
			if sentinel.ExitCode() != tc.wantExit {
				t.Errorf("ExitCode() = %d", sentinel.ExitCode())
			}
			if sentinel.Error() != tc.wantErrText {
				t.Errorf("Error() = %q", sentinel.Error())
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
			t.Parallel()
			defer func() {
				rec := recover()
				if rec == nil {
					t.Fatalf("expected panic with %q", tc.substr)
				}
				msg, ok := rec.(string)
				if !ok || !strings.Contains(msg, tc.substr) {
					t.Errorf("panic = %v, want contains %q", rec, tc.substr)
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
		wantCode   errs.Code
		wantReason string
	}{
		{"alias matches Define", 0x00_02_00_01, "LEVEL_TEST_NEW", "Level test public", 0x00_02_00_01, "LEVEL_TEST_NEW"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := errs.NewError(tc.code, tc.reason, tc.public, "level test private")
			if got.Code() != tc.wantCode || got.Reason() != tc.wantReason {
				t.Errorf("got %s %q, want %s %q", got.Code(), got.Reason(), tc.wantCode, tc.wantReason)
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
			defined := errs.Define(0x00_03_0F_96, "WITH_HTTP_OPT", "Override http test public", "priv",
				errs.WithHTTPStatus(tc.override))
			if defined.HTTPStatus() != tc.want {
				t.Errorf("HTTPStatus = %d, want %d", defined.HTTPStatus(), tc.want)
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
			defined := errs.Define(0x00_03_0F_97, "WITH_EXIT_OPT", "Override exit test public", "priv",
				errs.WithExitCode(tc.override))
			if defined.ExitCode() != tc.want {
				t.Errorf("ExitCode = %d, want %d", defined.ExitCode(), tc.want)
			}
		})
	}
}

func TestWrap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "sdk cause → origin wins",
			run: func(t *testing.T) {
				sentinel := newSampleSentinel(t)
				wrapped := errs.Wrap(sentinel, errs.WrapParams{
					Code: 9999, Reason: "RELABEL_ATTEMPT",
					Public: "This public is ignored because origin wins", Private: "ignored",
				}, errs.String("extra", "v"))
				if wrapped.Code() != sentinel.Code() {
					t.Errorf("Code = %d, want inherit", wrapped.Code())
				}
				if wrapped.Reason() != sentinel.Reason() {
					t.Errorf("Reason = %q", wrapped.Reason())
				}
				if len(wrapped.Fields()) != 1 {
					t.Errorf("expected 1 extra field, got %d", len(wrapped.Fields()))
				}
			},
		},
		{
			name: "stdlib cause → params apply, errors.Is preserved",
			run: func(t *testing.T) {
				//: 0x00_03_01_0A = 0.3.1.10 (CtxCancelled dotted-quad).
				wrapped := errs.Wrap(context.Canceled, errs.WrapParams{
					Code: 0x00_03_01_0A, Reason: "CTX_CANCELLED",
					Public: "Operation aborted due to cancellation", Private: "wrapped context.Canceled",
				})
				if !errors.Is(wrapped, context.Canceled) {
					t.Error("errors.Is(wrapped, context.Canceled) = false")
				}
				if wrapped.Code() != 0x00_03_01_0A {
					t.Errorf("Code = %s", wrapped.Code())
				}
			},
		},
		{
			name: "stdlib cause → WrapParams.ExitCode overrides the default 70",
			run: func(t *testing.T) {
				//: a stdlib cause has no *Error to inherit an exit override from,
				//: so WrapParams.ExitCode is how a wrap carries sysexits-style codes.
				wrapped := errs.Wrap(context.Canceled, errs.WrapParams{
					Code: 0x00_03_01_0A, Reason: "CTX_CANCELLED",
					Public: "Operation aborted due to cancellation", Private: "wrapped context.Canceled",
					ExitCode: 74,
				})
				if wrapped.ExitCode() != 74 {
					t.Errorf("ExitCode = %d, want 74", wrapped.ExitCode())
				}
			},
		},
		{
			name: "stdlib cause → zero ExitCode keeps the default 70",
			run: func(t *testing.T) {
				//: the zero value must not disturb the documented default.
				wrapped := errs.Wrap(context.Canceled, errs.WrapParams{
					Code: 0x00_03_01_0A, Reason: "CTX_CANCELLED",
					Public: "Operation aborted due to cancellation", Private: "wrapped context.Canceled",
				})
				if wrapped.ExitCode() != 70 {
					t.Errorf("ExitCode = %d, want 70 (default)", wrapped.ExitCode())
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
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
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got *errs.Error
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						t.Fatalf("expected NO panic, got: %v", rec)
					}
				}()
				wrapped := errs.Wrap(fs.ErrNotExist, errs.WrapParams{Code: 0, Reason: "X", Public: "y", Private: "z"})
				got = wrapped
			}()
			if got == nil {
				t.Fatal("expected non-nil *Error")
			}
			if got.Code() != errs.CodeInvalidWrapParams {
				t.Errorf("expected CodeInvalidWrapParams, got %s", got.Code())
			}
			if !errors.Is(got, fs.ErrNotExist) {
				t.Error("errors.Is should still reach fs.ErrNotExist through the wrap")
			}
		})
	}
}

// TestError_Code covers the typed accessor on *Error.
func TestError_Code(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want errs.Code
	}{
		{"sentinel CodeValue matches Define", 0x00_03_01_01},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if sentinel := newSampleSentinel(t); sentinel.Code() != tc.want {
				t.Errorf("CodeValue = %s, want %s", sentinel.Code(), tc.want)
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
			t.Parallel()
			if sentinel := newSampleSentinel(t); sentinel.Reason() != "WRITER_NIL" {
				t.Errorf("Reason = %q", sentinel.Reason())
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
			t.Parallel()
			if sentinel := newSampleSentinel(t); sentinel.Public() != "Log handler requires a non-nil writer" {
				t.Errorf("Public = %q", sentinel.Public())
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
			t.Parallel()
			if sentinel := newSampleSentinel(t); sentinel.Private() != "service/logger.NewTextHandler called with nil io.Writer" {
				t.Errorf("Private = %q", sentinel.Private())
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

// TestError_Trail exercises the Trail accessor on both a Define-only
// sentinel (expected empty trail) and a wrapped *Error (expected single
// trail entry for the wrap site).
func TestError_Trail(t *testing.T) {
	t.Parallel()
	//: 0x00_03_0F_70 = 0.3.15.112 — origin slot for the trail test.
	//: 0x00_03_0F_71 = 0.3.15.113 — wrap-site slot.
	tests := []struct {
		name      string
		buildErr  func() *errs.Error
		wantTrail []errs.Code
	}{
		{
			name: "Define has empty trail",
			buildErr: func() *errs.Error {
				return errs.Define(0x00_03_0F_70, "TRAIL_ORIGIN", "Trail origin public", "debug")
			},
			wantTrail: nil,
		},
		{
			name: "Wrap appends wrap-site to trail",
			buildErr: func() *errs.Error {
				origin := errs.Define(0x00_03_0F_70, "TRAIL_ORIGIN", "Trail origin public", "debug")
				return errs.Wrap(origin, errs.WrapParams{
					Code: 0x00_03_0F_71, Reason: "TRAIL_WRAP_SITE",
					Public: "Wrap site public", Private: "debug",
				})
			},
			wantTrail: []errs.Code{0x00_03_0F_71},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotTrail := tc.buildErr().Trail()
			if len(gotTrail) != len(tc.wantTrail) {
				t.Fatalf("len(Trail) = %d, want %d", len(gotTrail), len(tc.wantTrail))
			}
			for idx, want := range tc.wantTrail {
				if gotTrail[idx] != want {
					t.Errorf("Trail[%d] = %s, want %s", idx, gotTrail[idx], want)
				}
			}
		})
	}
}

// TestError_TrailTruncated verifies the flag is false on a fresh sentinel
// and stays false on a single-level wrap.
func TestError_TrailTruncated(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		buildErr func() *errs.Error
		want     bool
	}{
		{
			name: "Define is not truncated",
			buildErr: func() *errs.Error {
				return errs.Define(0x00_03_0F_72, "TRUNC_BASE", "Trunc base public", "debug")
			},
			want: false,
		},
		{
			name: "single Wrap is not truncated",
			buildErr: func() *errs.Error {
				origin := errs.Define(0x00_03_0F_72, "TRUNC_BASE", "Trunc base public", "debug")
				return errs.Wrap(origin, errs.WrapParams{
					Code: 0x00_03_0F_73, Reason: "TRUNC_WRAP",
					Public: "Trunc wrap public", Private: "debug",
				})
			},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.buildErr().TrailTruncated(); got != tc.want {
				t.Errorf("TrailTruncated = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestError_Code_Layer(t *testing.T) {
	t.Parallel()
	//: Layer extracts the second octet of the dotted-quad Code. Examples:
	//: 0x00_01_01_00 = 0.1.1.0 (kernel) → Layer 1
	//: 0x00_03_01_01 = 0.3.1.1 (service) → Layer 3
	//: 0x01_01_00_01 = 1.1.0.1 (pkg v1 under major=1) → Layer 1
	tests := []struct {
		name   string
		code   errs.Code
		reason string
		want   errs.Layer
	}{
		{"kernel layer", 0x00_01_01_00, "LAYER_TEST_KERNEL", 1},
		{"service layer", 0x00_03_01_01, "LAYER_TEST_SERVICE", 3},
		{"pkg v1 layer", 0x01_01_00_01, "LAYER_TEST_PKG", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			built := errs.Define(tc.code, tc.reason, "Layer test public", "priv")
			if built.Code().Layer() != tc.want {
				t.Errorf("Code().Layer() = %d, want %d", built.Code().Layer(), tc.want)
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
			t.Parallel()
			sentinel := newSampleSentinel(t)
			wrapped := errs.Wrap(sentinel, errs.WrapParams{}, errs.String("secret_key", "secret_value"))
			got := wrapped.Error()
			if strings.Contains(got, sentinel.Private()) {
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
			t.Parallel()
			if sentinel := newSampleSentinel(t); sentinel.HTTPStatus() != 500 {
				t.Errorf("HTTPStatus = %d", sentinel.HTTPStatus())
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
			t.Parallel()
			if sentinel := newSampleSentinel(t); sentinel.ExitCode() != 70 {
				t.Errorf("ExitCode = %d", sentinel.ExitCode())
			}
		})
	}
}

// TestError_Is_MatchesByCode locks in the contract that a wrap result
// and the package-level sentinel built by Define share the
// same (Code, Reason) and MUST satisfy errors.Is even though they are
// different pointers. The original (*Error).Is fell through to pointer
// equality, which silently broke every "errors.Is(err, SomeSentinel)"
// call site at the wrap boundary.
//
// Table-driven so future cases (e.g. trail-match cases) can be appended
// without forking the test.
func TestError_Is_MatchesByCode(t *testing.T) {
	t.Parallel()
	//: build a package-level-style sentinel once; reused across cases.
	sentinel := newSampleSentinel(t)
	//: WrapParams are ignored for *errs.Error causes (origin wins) so we
	//: wrap a stdlib error to exercise the wrap path that inflates Code.
	matching := errs.Wrap(errors.New("stdlib cause"), errs.WrapParams{
		Code:    sentinel.Code(),
		Reason:  sentinel.Reason(),
		Public:  sentinel.Public(),
		Private: sentinel.Private(),
	})
	//: a sentinel with a different Code must NOT match.
	other := errs.Define(errs.Code(0x01_01_00_0F), "OTHER",
		"other sentinel",
		"test: different Code must not collide")
	tests := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{"Code+Reason match implies Is true", matching, sentinel, true},
		{"different Codes do not match", matching, other, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := errors.Is(tc.err, tc.target); got != tc.want {
				t.Errorf("errors.Is = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestError_Is exercises the three matching modes of (*Error).Is in one
// table; the matchesByCode test above only covers the *Error → *Error path,
// so this table backstops the pointer-equality and prefix-matcher legs.
func TestError_Is(t *testing.T) {
	t.Parallel()
	//: 0x00_03_0F_80 = 0.3.15.128 — Is-test slot (unused production code).
	sentinel := errs.Define(0x00_03_0F_80, "IS_BASE",
		"Is base public", "Is base private")
	//: pointer-equality leg uses an unrelated sentinel built fresh.
	separate := errs.Define(0x00_03_0F_81, "IS_OTHER",
		"Is other public", "Is other private")
	//: prefix matcher targeting all codes in layer 3, package 0x0F.
	prefix := errs.NewPrefixMatcher(0x00_03_0F_00, errs.MaskByPackage)
	tests := []struct {
		name   string
		target error
		want   bool
	}{
		{"prefix matcher hit", prefix, true},
		{"pointer equality with self", sentinel, true},
		{"pointer equality miss against other sentinel", separate, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := errors.Is(sentinel, tc.target); got != tc.want {
				t.Errorf("errors.Is = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestError_Error_RendersTrail covers the non-empty-trail formatting arm of
// Error(): wrapping an *Error with a non-zero wrap Code appends a trail entry,
// so the rendered header carries the origin code, the " <- " separator, the
// wrap-site code, then the inherited Reason and public message.
func TestError_Error_RendersTrail(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{
		{"single wrap renders one <- separated trail code"},
	}
	//: runCase executes one row directly so the static analyser credits
	//: the branch; a wrap on a non-zero Code must produce the " <- " trail.
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: 0x00_03_0F_70 = origin slot; 0x00_03_0F_71 = wrap-site slot.
		origin := errs.Define(0x00_03_0F_70, "ORIGIN_TRAIL",
			"origin public", "origin private")
		wrapped := errs.Wrap(origin, errs.WrapParams{Code: 0x00_03_0F_71})
		got := wrapped.Error()
		//: a populated trail must render the ASCII wrap separator.
		if !strings.Contains(got, " <- ") {
			t.Errorf("Error() = %q, want ' <- ' trail separator", got)
		}
		//: origin-wins — Reason and public come from the wrapped cause.
		if !strings.Contains(got, "ORIGIN_TRAIL") || !strings.Contains(got, "origin public") {
			t.Errorf("Error() = %q, want origin reason + public", got)
		}
		//: Private must never leak into the rendered form.
		if strings.Contains(got, "origin private") {
			t.Errorf("Error() leaked Private: %q", got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestError_Error_RendersTruncated drives the trail past its cap so the
// monotonic truncation marker " (truncated)" is rendered before the Reason.
func TestError_Error_RendersTruncated(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		wraps int
	}
	tests := []tc{
		{"forty wraps overflow the trail cap", 40},
	}
	//: runCase executes one row directly so the static analyser credits
	//: the branch; overflowing the trail cap must flip trailTruncated.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: 0x00_03_0F_72 = origin slot for the truncation walk.
		var err error = errs.Define(0x00_03_0F_72, "TRUNC_ORIGIN",
			"trunc public", "trunc private")
		//: each wrap adds one distinct non-zero trail code; well past the
		//: documented cap of 16 this must flip trailTruncated.
		for i := range tc.wraps {
			//: 0x00_07_01_xx — distinct non-zero wrap codes in layer 7.
			err = errs.Wrap(err, errs.WrapParams{Code: errs.Code(0x00_07_01_00 + i + 1)})
		}
		got := err.Error()
		//: overflow must surface the verbatim marker operators grep for.
		if !strings.Contains(got, "(truncated)") {
			t.Errorf("Error() = %q, want '(truncated)' marker", got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestWrap_TypedNilErrorCause covers the typed-nil guard in Wrap: a
// (*errs.Error)(nil) cause must collapse to the stdlib path with a nil source
// rather than dereferencing the nil receiver.
func TestWrap_TypedNilErrorCause(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{
		{"typed-nil *Error cause yields a params-driven error with nil source"},
	}
	//: runCase executes one row directly so the static analyser credits
	//: the branch; a typed-nil *Error cause must collapse to the params path.
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: a typed-nil *Error is the classic interface-holding-nil trap.
		var cause *errs.Error
		//: 0x00_03_0F_73 = wrap slot for the typed-nil path.
		wrapped := errs.Wrap(cause, errs.WrapParams{
			Code: 0x00_03_0F_73, Reason: "TYPED_NIL_WRAP",
			Public: "typed nil collapses to stdlib path", Private: "debug",
		})
		//: params apply because no *Error was inherited from the cause.
		if wrapped.Code() != 0x00_03_0F_73 {
			t.Errorf("Code = %s, want params code", wrapped.Code())
		}
		//: the collapsed path carries no wrapped cause.
		if wrapped.Source() != nil {
			t.Errorf("Source = %v, want nil", wrapped.Source())
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestError_Is_MatchesTrailPrefix covers the trail-walk arm of matchesPrefix:
// the origin code falls outside the matcher's subnet but a wrap-site trail
// code falls inside it, so errors.Is must still report a hit.
func TestError_Is_MatchesTrailPrefix(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{
		{"prefix matches a trail entry the origin misses"},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the trail-walk arm of matchesPrefix must catch a wrap-site
	//: code whose subnet the origin misses.
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: origin sits in layer 3; the matcher targets layer 7.
		origin := errs.Define(0x00_03_0F_74, "PREFIX_TRAIL_ORIGIN",
			"prefix trail public", "prefix trail private")
		//: wrap with a layer-7 code so only the trail entry can match.
		wrapped := errs.Wrap(origin, errs.WrapParams{Code: 0x00_07_01_01})
		//: subnet over major+layer == 0.7 — origin (layer 3) misses it.
		matcher := errs.NewPrefixMatcher(0x00_07_00_00, errs.MaskByLayer)
		if !errors.Is(wrapped, matcher) {
			t.Errorf("errors.Is(wrapped, layer-7 matcher) = false, want true via trail")
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestHasCode_MultiUnwrap covers the multi-error Unwrap arm of HasCode: an
// errors.Join aggregate exposes Unwrap() []error, and HasCode must recurse
// through every branch to find a matching *Error code.
func TestHasCode_MultiUnwrap(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		code errs.Code
		want bool
	}
	tests := []tc{
		{"code present in a joined branch", 0x00_03_0F_75, true},
		{"code absent from every branch", 0x00_03_0F_76, false},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; HasCode must recurse through every Unwrap() []error branch.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: 0x00_03_0F_75 = the sdk branch buried inside the join.
		sdk := errs.Define(0x00_03_0F_75, "JOINED_SDK",
			"joined public", "joined private")
		//: errors.Join exposes Unwrap() []error, forcing the multi walk.
		joined := errors.Join(errors.New("plain branch"), sdk)
		if got := errs.HasCode(joined, tc.code); got != tc.want {
			t.Errorf("HasCode(joined, %s) = %v, want %v", tc.code, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
