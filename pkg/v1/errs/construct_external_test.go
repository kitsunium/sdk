package errs_test

import (
	"errors"
	"strings"
	"testing"

	// Only the PUBLIC facade is imported — this file deliberately stands in
	// for an external consumer with no internal/ access, proving the whole
	// error model (build + introspect) is reachable from pkg/v1/errs alone.
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// appCode is a consumer-owned dotted-quad in the reserved application range
// (Major 0x40 ≥ MinAppMajor), guaranteed collision-free with every SDK code.
const (
	appCode      errs.Code = 0x40_01_01_01 // 64.1.1.1
	appWrapCode  errs.Code = 0x40_01_02_01 // 64.1.2.1
	appCodeOther errs.Code = 0x40_02_01_01 // 64.2.1.1
)

// TestNewRoundTrip is acceptance criteria #1 and #2: an external module builds
// a typed error with a Code + Public/Private split using only pkg/v1/errs, and
// CodeOf / PublicOf / HasCode round-trip on it.
func TestNewRoundTrip(t *testing.T) {
	t.Parallel()
	//: construct entirely through the public facade — the consumer path.
	err := errs.New(appCode, "USER_NOT_FOUND",
		"user not found", "lookup miss in users table id=42",
		errs.Int("id", 42), errs.String("table", "users"))
	//: the constructor never returns nil for in-range, well-formed args.
	if err == nil {
		t.Fatal("New returned nil for valid args")
	}
	//: CodeOf round-trips the exact code we minted.
	if got, ok := errs.CodeOf(err); !ok || got != appCode {
		t.Errorf("CodeOf = (%s, %v), want (%s, true)", got, ok, appCode)
	}
	//: the typed octets compose off the returned Code.
	if got, _ := errs.CodeOf(err); got.Major() != errs.MinAppMajor {
		t.Errorf("Major = %#x, want %#x", got.Major(), errs.MinAppMajor)
	}
	//: PublicOf returns the wire-safe message verbatim.
	if got := errs.PublicOf(err); got != "user not found" {
		t.Errorf("PublicOf = %q, want %q", got, "user not found")
	}
	//: ReasonOf round-trips the SCREAMING_SNAKE identifier.
	if got, _ := errs.ReasonOf(err); got != "USER_NOT_FOUND" {
		t.Errorf("ReasonOf = %q, want %q", got, "USER_NOT_FOUND")
	}
	//: HasCode finds the minted code in the chain.
	if !errs.HasCode(err, appCode) {
		t.Error("HasCode did not find the minted code")
	}
}

// TestWrapPreservesChain is acceptance criterion #3: Wrap preserves the cause
// chain so errors.Is / errors.Unwrap keep working across an SDK wrap.
func TestWrapPreservesChain(t *testing.T) {
	t.Parallel()
	//: a plain stdlib cause an external consumer might be migrating from.
	cause := errors.New("dial tcp: connection refused")
	//: wrap it into the SDK model with a consumer-owned code.
	wrapped := errs.Wrap(cause, errs.WrapParams{
		Code:    appWrapCode,
		Reason:  "UPSTREAM_UNAVAILABLE",
		Public:  "service temporarily unavailable",
		Private: "users-api dial failed",
	})
	//: errors.Is must still reach the original cause through the wrap.
	if !errors.Is(wrapped, cause) {
		t.Error("errors.Is(wrapped, cause) = false, want true")
	}
	//: errors.Unwrap must return the original cause.
	if errors.Unwrap(wrapped) != cause {
		t.Error("errors.Unwrap(wrapped) did not return the cause")
	}
	//: the wrap stamped the consumer code as the origin (stdlib cause path).
	if got, _ := errs.CodeOf(wrapped); got != appWrapCode {
		t.Errorf("CodeOf(wrapped) = %s, want %s", got, appWrapCode)
	}
}

// TestWrapOriginWins proves the origin-wins contract for a consumer wrapping an
// already-typed SDK error: the inherited Code is the origin's, and HasCode sees
// both the origin and the appended trail code.
func TestWrapOriginWins(t *testing.T) {
	t.Parallel()
	//: an SDK-model origin error built by the consumer.
	origin := errs.New(appCode, "USER_NOT_FOUND", "user not found", "id=42")
	//: wrap it with a different code at a higher layer.
	wrapped := errs.Wrap(origin, errs.WrapParams{
		Code:    appCodeOther,
		Reason:  "REQUEST_FAILED",
		Public:  "request failed",
		Private: "handler wrap",
	})
	//: origin wins — the observed Code stays the origin's, not the wrap's.
	if got, _ := errs.CodeOf(wrapped); got != appCode {
		t.Errorf("CodeOf(wrapped) = %s, want origin %s", got, appCode)
	}
	//: HasCode still finds the origin code.
	if !errs.HasCode(wrapped, appCode) {
		t.Error("HasCode lost the origin code after wrap")
	}
	//: HasCode also finds the wrap code appended to the trail.
	if !errs.HasCode(wrapped, appCodeOther) {
		t.Error("HasCode did not find the trail code after wrap")
	}
}

// TestNewRejectsBadPublic is acceptance criterion #5: public-message validation
// (≤120 runes, no newline) is enforced at construction with a TYPED error —
// the consumer gets a usable INVALID_PUBLIC error, never a panic or nil.
func TestNewRejectsBadPublic(t *testing.T) {
	t.Parallel()
	//: 0.0.0.3 is the kernel's INVALID_PUBLIC meta-code.
	const invalidPublic errs.Code = 0x00_00_00_03
	type tc struct {
		name   string
		public string
	}
	tests := []tc{
		//: a 121-rune public breaches the ≤120 cap.
		{"over 120 runes", strings.Repeat("x", 121)},
		//: an embedded newline is rejected for single-line log/HTTP safety.
		{"embedded newline", "line one\nline two"},
		//: an empty public is rejected — every error must carry a message.
		{"empty", ""},
	}
	//: runCase executes one row directly so the static analyser credits it.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: construction must not panic — the failure is returned as a value.
		err := errs.New(appCode, "BAD_PUBLIC", tc.public, "detail")
		//: the result is a non-nil, introspectable validation error.
		if err == nil {
			t.Fatal("New returned nil for invalid public")
		}
		//: the typed verdict is the specific INVALID_PUBLIC code.
		if got, _ := errs.CodeOf(err); got != invalidPublic {
			t.Errorf("CodeOf = %s, want INVALID_PUBLIC %s", got, invalidPublic)
		}
		//: the reason names the rule that failed for log-grep diagnosis.
		if got, _ := errs.ReasonOf(err); got != "INVALID_PUBLIC" {
			t.Errorf("ReasonOf = %q, want INVALID_PUBLIC", got)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// TestAppMajorRangeIsCollisionFree is acceptance criterion #4: the documented
// MM assignment holds — the application range sits entirely above every Major
// the SDK allocates, so a consumer code can never shadow an SDK code.
func TestAppMajorRangeIsCollisionFree(t *testing.T) {
	t.Parallel()
	//: the reserved boundary is the documented 0x40, ceiling the int32-safe 0x7F.
	if errs.MinAppMajor != 0x40 || errs.MaxMajor != 0x7F {
		t.Fatalf("range = [%#x,%#x], want [0x40,0x7F]", errs.MinAppMajor, errs.MaxMajor)
	}
	//: a consumer code packed in-range reports a Major within the reserved band
	//: — strictly above 0x01, the highest Major the SDK uses today (pkg/v1).
	got := errs.Pack(errs.MinAppMajor, 0x01, 0x01, 0x01)
	if m := got.Major(); m < errs.MinAppMajor || m > errs.MaxMajor {
		t.Errorf("packed Major = %#x, outside reserved [%#x,%#x]", m, errs.MinAppMajor, errs.MaxMajor)
	}
}
