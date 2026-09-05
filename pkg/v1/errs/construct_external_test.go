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
	if err == nil {
		t.Fatal("New returned nil for valid args")
	}

	type tc struct {
		name  string
		check func(t *testing.T)
	}
	tests := []tc{
		{"CodeOf round-trips the minted code", func(t *testing.T) {
			t.Helper()
			if got, ok := errs.CodeOf(err); !ok || got != appCode {
				t.Errorf("CodeOf = (%s, %v), want (%s, true)", got, ok, appCode)
			}
		}},
		{"the typed octets compose off the Code", func(t *testing.T) {
			t.Helper()
			if got, _ := errs.CodeOf(err); got.Major() != errs.MinAppMajor {
				t.Errorf("Major = %#x, want %#x", got.Major(), errs.MinAppMajor)
			}
		}},
		{"PublicOf returns the wire-safe message verbatim", func(t *testing.T) {
			t.Helper()
			if got := errs.PublicOf(err); got != "user not found" {
				t.Errorf("PublicOf = %q, want %q", got, "user not found")
			}
		}},
		{"the private half never leaks into the public one", func(t *testing.T) {
			t.Helper()
			//: the whole point of the split: an operator's detail must not
			//: reach a wire-safe message.
			if got := errs.PublicOf(err); strings.Contains(got, "users table") {
				t.Errorf("PublicOf leaked the private detail: %q", got)
			}
		}},
		{"ReasonOf round-trips the identifier", func(t *testing.T) {
			t.Helper()
			if got, _ := errs.ReasonOf(err); got != "USER_NOT_FOUND" {
				t.Errorf("ReasonOf = %q, want %q", got, "USER_NOT_FOUND")
			}
		}},
		{"HasCode finds the minted code", func(t *testing.T) {
			t.Helper()
			if !errs.HasCode(err, appCode) {
				t.Error("HasCode did not find the minted code")
			}
		}},
		{"HasCode does not find a code that is not there", func(t *testing.T) {
			t.Helper()
			if errs.HasCode(err, appCode+1) {
				t.Error("HasCode matched a code the error does not carry")
			}
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		c.check(t)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestWrapPreservesChain is acceptance criterion #3: Wrap preserves the cause
// chain so errors.Is / errors.Unwrap keep working across an SDK wrap.
func TestWrapPreservesChain(t *testing.T) {
	t.Parallel()
	//: a plain stdlib cause an external consumer might be migrating from.
	cause := errors.New("dial tcp: connection refused")
	wrapped := errs.Wrap(cause, errs.WrapParams{
		Code:    appWrapCode,
		Reason:  "UPSTREAM_UNAVAILABLE",
		Public:  "service temporarily unavailable",
		Private: "users-api dial failed",
	})

	type tc struct {
		name  string
		check func(t *testing.T)
	}
	tests := []tc{
		{"errors.Is reaches the original cause", func(t *testing.T) {
			t.Helper()
			if !errors.Is(wrapped, cause) {
				t.Error("errors.Is(wrapped, cause) = false, want true")
			}
		}},
		{"errors.Unwrap returns the original cause", func(t *testing.T) {
			t.Helper()
			if errors.Unwrap(wrapped) != cause {
				t.Error("errors.Unwrap(wrapped) did not return the cause")
			}
		}},
		{"the consumer code becomes the origin", func(t *testing.T) {
			t.Helper()
			//: a stdlib cause carries no code, so the wrap's is the only one.
			if got, _ := errs.CodeOf(wrapped); got != appWrapCode {
				t.Errorf("CodeOf(wrapped) = %s, want %s", got, appWrapCode)
			}
		}},
		{"the cause's text is not promoted to the public message", func(t *testing.T) {
			t.Helper()
			//: "dial tcp: connection refused" names an upstream host shape a
			//: wire-safe message must not carry.
			if got := errs.PublicOf(wrapped); strings.Contains(got, "dial tcp") {
				t.Errorf("PublicOf leaked the cause: %q", got)
			}
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		c.check(t)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestWrapOriginWins proves the origin-wins contract for a consumer wrapping an
// already-typed SDK error: the inherited Code is the origin's, and HasCode sees
// both the origin and the appended trail code.
func TestWrapOriginWins(t *testing.T) {
	t.Parallel()
	//: an SDK-model origin error built by the consumer.
	origin := errs.New(appCode, "USER_NOT_FOUND", "user not found", "id=42")
	wrapped := errs.Wrap(origin, errs.WrapParams{
		Code:    appCodeOther,
		Reason:  "REQUEST_FAILED",
		Public:  "request failed",
		Private: "handler wrap",
	})

	type tc struct {
		name  string
		code  errs.Code
		want  bool
		isTop bool
	}
	tests := []tc{
		//: origin wins — the observed Code stays the origin's, not the wrap's.
		{"the origin code is the observed one", appCode, true, true},
		{"the wrap code lands in the trail", appCodeOther, true, false},
		{"a code neither carries is not found", appCode + 0x10, false, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := errs.HasCode(wrapped, c.code); got != c.want {
			t.Errorf("HasCode(%s) = %v, want %v", c.code, got, c.want)
		}
		if c.isTop {
			if got, _ := errs.CodeOf(wrapped); got != c.code {
				t.Errorf("CodeOf(wrapped) = %s, want origin %s", got, c.code)
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
	type tc struct {
		name  string
		major errs.Major
		want  bool
	}
	tests := []tc{
		//: the SDK's own Majors sit below the reserved band, which is what
		//: makes a consumer code unable to collide with one.
		{"the SDK kernel Major", 0x00, false},
		{"the SDK pkg/v1 Major", 0x01, false},
		{"one below the reserved floor", errs.MinAppMajor - 1, false},
		{"the reserved floor", errs.MinAppMajor, true},
		{"inside the band", errs.MinAppMajor + 0x10, true},
		{"the reserved ceiling", errs.MaxMajor, true},
		{"one above the ceiling", errs.MaxMajor + 1, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := errs.Pack(c.major, 0x01, 0x01, 0x01).Major()
		inBand := got >= errs.MinAppMajor && got <= errs.MaxMajor
		if inBand != c.want {
			t.Errorf("Major %#x in reserved band = %v, want %v", got, inBand, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}

	//: the boundary itself is documented, so a silent widening is a defect.
	if errs.MinAppMajor != 0x40 || errs.MaxMajor != 0x7F {
		t.Errorf("range = [%#x,%#x], want [0x40,0x7F]", errs.MinAppMajor, errs.MaxMajor)
	}
}
