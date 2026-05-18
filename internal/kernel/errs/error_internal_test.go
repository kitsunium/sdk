package errs

import (
	"errors"
	"testing"
)

// Test_newValidationError verifies the bootstrap constructor that the
// validate.go pipeline uses to surface structural failures without
// recursing back into Define.
func Test_newValidationError(t *testing.T) {
	t.Parallel()
	//: 0x00_00_00_0A = 0.0.0.10 — an arbitrary meta-slot for the test.
	tests := []struct {
		name    string
		code    Code
		reason  string
		public  string
		wantStr string
	}{
		{
			name:    "fields plumb through verbatim",
			code:    CodeInvalidCode,
			reason:  "INVALID_CODE",
			public:  "bootstrap public",
			wantStr: "[0.0.0.1 INVALID_CODE] bootstrap public",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			built := newValidationError(tc.code, tc.reason, tc.public)
			if built.CodeValue() != tc.code {
				t.Errorf("CodeValue = %s, want %s", built.CodeValue(), tc.code)
			}
			if built.Reason() != tc.reason {
				t.Errorf("Reason = %q, want %q", built.Reason(), tc.reason)
			}
			if built.Public() != tc.public {
				t.Errorf("Public = %q, want %q", built.Public(), tc.public)
			}
			//: bootstrap path leaves Private empty by construction.
			if built.Private() != "" {
				t.Errorf("Private = %q, want empty", built.Private())
			}
			if got := built.Error(); got != tc.wantStr {
				t.Errorf("Error = %q, want %q", got, tc.wantStr)
			}
		})
	}
}

// Test_newFromStdlibCause covers the runtime-safe path Wrap takes when
// the cause is NOT an *Error. Two main legs: success and the
// CodeInvalidWrapParams fallback for bad params.
func Test_newFromStdlibCause(t *testing.T) {
	t.Parallel()
	stdErr := errors.New("stdlib cause")
	tests := []struct {
		name     string
		cause    error
		params   WrapParams
		wantCode Code
	}{
		{
			name:  "valid params propagate through",
			cause: stdErr,
			//: 0x00_03_0F_A0 = 0.3.15.160 — internal-test slot.
			params: WrapParams{
				Code: 0x00_03_0F_A0, Reason: "INTERNAL_OK",
				Public: "Internal ok public", Private: "Internal ok private",
			},
			wantCode: 0x00_03_0F_A0,
		},
		{
			name:  "zero code yields CodeInvalidWrapParams fallback",
			cause: stdErr,
			params: WrapParams{
				Code: 0, Reason: "BAD", Public: "bad public", Private: "bad private",
			},
			wantCode: CodeInvalidWrapParams,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			built := newFromStdlibCause(tc.cause, tc.params, nil)
			if built.CodeValue() != tc.wantCode {
				t.Errorf("CodeValue = %s, want %s", built.CodeValue(), tc.wantCode)
			}
			//: source must always thread through, regardless of the validation leg.
			if built.Source() != tc.cause {
				t.Errorf("Source = %v, want %v", built.Source(), tc.cause)
			}
		})
	}
}

// Test_newFromStdlibCause_FieldsCopy verifies the defensive Clone on
// the fields parameter — mutating the caller's slice after Wrap must
// not affect the returned *Error.
func Test_newFromStdlibCause_FieldsCopy(t *testing.T) {
	t.Parallel()
	//: 0x00_03_0F_A1 = 0.3.15.161 — fields-copy test slot.
	tests := []struct {
		name string
	}{
		{"caller slice mutation does not affect Error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			original := []FieldValue{String("k", "v")}
			built := newFromStdlibCause(nil, WrapParams{
				Code: 0x00_03_0F_A1, Reason: "INTERNAL_FIELDS",
				Public: "Internal fields public", Private: "Internal fields private",
			}, original)
			original[0] = String("mutated", "mutated")
			again := built.Fields()
			if len(again) != 1 {
				t.Fatalf("defensive Clone failed: got len=%d, want 1", len(again))
			}
			if again[0].Key() != "k" {
				t.Errorf("defensive Clone failed: got Key=%q, want %q", again[0].Key(), "k")
			}
		})
	}
}

// Test_wrapSDKCause exercises the helper extracted from Wrap that builds
// the origin-wins *Error when the cause already carries an *Error layer.
func Test_wrapSDKCause(t *testing.T) {
	t.Parallel()
	//: 0x00_03_0F_A2 = 0.3.15.162 — origin slot for the helper test.
	//: 0x00_03_0F_A3 = 0.3.15.163 — wrap-site slot.
	tests := []struct {
		name         string
		originCode   Code
		wrapSiteCode Code
		extraField   FieldValue
	}{
		{
			name:         "origin code wins; trail records wrap site",
			originCode:   0x00_03_0F_A2,
			wrapSiteCode: 0x00_03_0F_A3,
			extraField:   String("extra", "value"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			origin := Define(tc.originCode, "INTERNAL_ORIGIN",
				"Internal origin public", "Internal origin private")
			built := wrapSDKCause(origin, origin, WrapParams{
				Code: tc.wrapSiteCode, Reason: "INTERNAL_WRAP_SITE",
				Public: "wrap site public", Private: "wrap site private",
			}, []FieldValue{tc.extraField})
			if built.CodeValue() != tc.originCode {
				t.Errorf("CodeValue = %s, want %s", built.CodeValue(), tc.originCode)
			}
			trail := built.Trail()
			if len(trail) != 1 || trail[0] != tc.wrapSiteCode {
				t.Errorf("Trail = %v, want [%s]", trail, tc.wrapSiteCode)
			}
			if len(built.Fields()) != 1 {
				t.Fatalf("Fields len = %d, want 1", len(built.Fields()))
			}
		})
	}
}

// Test_errCodeMatches covers the inner helper hauled out of HasCode; it
// exists as its own table so the trail-walk and code-match legs each get a
// direct test rather than only being exercised via HasCode call sites.
func Test_errCodeMatches(t *testing.T) {
	t.Parallel()
	//: 0x00_03_0F_B0 = 0.3.15.176 — origin slot for the inner test.
	//: 0x00_03_0F_B1 = 0.3.15.177 — wrap-site slot recorded in the trail.
	origin := Define(0x00_03_0F_B0, "INNER_BASE",
		"Inner base public", "Inner base private")
	withTrail := Wrap(origin, WrapParams{
		Code: 0x00_03_0F_B1, Reason: "INNER_TRAIL_WRAP",
		Public: "inner trail public", Private: "inner trail private",
	})
	stdErr := errors.New("plain stdlib")
	tests := []struct {
		name string
		err  error
		code Code
		want bool
	}{
		{"origin code hit", origin, 0x00_03_0F_B0, true},
		{"trail entry hit", withTrail, 0x00_03_0F_B1, true},
		{"miss against unrelated code", origin, 0x00_03_0F_BF, false},
		{"non-Error returns false", stdErr, 0x00_03_0F_B0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := errCodeMatches(tc.err, tc.code); got != tc.want {
				t.Errorf("errCodeMatches = %v, want %v", got, tc.want)
			}
		})
	}
}

// Test_hasCodeInSingleUnwrap covers the single-Unwrap recursion helper.
func Test_hasCodeInSingleUnwrap(t *testing.T) {
	t.Parallel()
	//: 0x00_03_0F_C0 = 0.3.15.192 — single-Unwrap test slot.
	origin := Define(0x00_03_0F_C0, "SINGLE_UNWRAP",
		"Single unwrap public", "Single unwrap private")
	wrapped := Wrap(origin, WrapParams{
		Code: 0x00_03_0F_C1, Reason: "SINGLE_WRAP_SITE",
		Public: "single wrap public", Private: "single wrap private",
	})
	tests := []struct {
		name string
		err  error
		code Code
		want bool
	}{
		{"wrapped chain finds origin one level down", wrapped, 0x00_03_0F_C0, true},
		{"non-wrapper returns false", errors.New("plain"), 0x00_03_0F_C0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasCodeInSingleUnwrap(tc.err, tc.code); got != tc.want {
				t.Errorf("hasCodeInSingleUnwrap = %v, want %v", got, tc.want)
			}
		})
	}
}

// Test_hasCodeInMultiUnwrap covers the multi-Unwrap branch (errors.Join).
func Test_hasCodeInMultiUnwrap(t *testing.T) {
	t.Parallel()
	//: 0x00_03_0F_D0 = 0.3.15.208 — multi-Unwrap test slot.
	first := Define(0x00_03_0F_D0, "JOIN_FIRST",
		"Join first public", "Join first private")
	second := Define(0x00_03_0F_D1, "JOIN_SECOND",
		"Join second public", "Join second private")
	joined := errors.Join(first, second)
	tests := []struct {
		name string
		err  error
		code Code
		want bool
	}{
		{"join hits first branch", joined, 0x00_03_0F_D0, true},
		{"join hits second branch", joined, 0x00_03_0F_D1, true},
		{"non-multi wrapper returns false", first, 0x00_03_0F_D0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hasCodeInMultiUnwrap(tc.err, tc.code); got != tc.want {
				t.Errorf("hasCodeInMultiUnwrap = %v, want %v", got, tc.want)
			}
		})
	}
}

// Test_Error_matchesPrefix and Test_Error_matchesSentinel exercise the
// helpers extracted out of (*Error).Is so the cyclomatic-complexity budget
// stays low. They are package-private, so they live in this internal test.
func Test_Error_matchesPrefix(t *testing.T) {
	t.Parallel()
	//: 0x00_03_0F_E0 = 0.3.15.224 — prefix-helper origin slot.
	origin := Define(0x00_03_0F_E0, "PREFIX_BASE",
		"Prefix base public", "Prefix base private")
	tests := []struct {
		name   string
		prefix Code
		mask   Code
		want   bool
	}{
		{"package-level prefix match", 0x00_03_0F_00, MaskByPackage, true},
		{"layer-level prefix match", 0x00_03_00_00, MaskByLayer, true},
		{"layer-level miss", 0x00_02_00_00, MaskByLayer, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			matcher := NewPrefixMatcher(tc.prefix, tc.mask)
			if got := origin.matchesPrefix(matcher); got != tc.want {
				t.Errorf("matchesPrefix = %v, want %v", got, tc.want)
			}
		})
	}
}

// Test_Error_matchesSentinel covers the (Code, Reason) equivalence leg
// plus the pointer-equality fallback that (*Error).Is relies on.
func Test_Error_matchesSentinel(t *testing.T) {
	t.Parallel()
	//: 0x00_03_0F_E8 = 0.3.15.232 — sentinel-helper slot.
	left := Define(0x00_03_0F_E8, "SENT_LEFT",
		"Sentinel left public", "Sentinel left private")
	matching := &Error{code: left.CodeValue(), reason: left.Reason()}
	otherCode := &Error{code: 0x00_03_0F_E9, reason: "SENT_RIGHT"}
	tests := []struct {
		name   string
		target *Error
		want   bool
	}{
		{"matching Code+Reason", matching, true},
		{"different Code falls back to pointer equality miss", otherCode, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := left.matchesSentinel(tc.target, tc.target); got != tc.want {
				t.Errorf("matchesSentinel = %v, want %v", got, tc.want)
			}
		})
	}
}
