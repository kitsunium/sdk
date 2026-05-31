package transform_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestCodeDuplicateRegistration_Value pins the dotted-quad value so an
// accidental renumber is caught by the suite, not only by the AST audit. The
// codec sibling pins 0.2.2.1; transform's own block owns 0.2.5.5.
func TestCodeDuplicateRegistration_Value(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  errs.Code
		want errs.Code
	}
	tests := []tc{
		{"duplicate registration code is 0.2.5.5", transform.CodeDuplicateRegistration, 0x00_02_05_05},
	}
	runCase := func(t *testing.T, got, want errs.Code) {
		t.Helper()
		//: the constant must stay pinned to its ADR-0014 slot.
		if got != want {
			t.Errorf("CodeDuplicateRegistration = %#x, want %#x", got, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.got, c.want)
		})
	}
}

// TestCodeUnknownCompressor_Value pins UnknownCompressor's slot so the renumber
// that introduced the distinct duplicate-registration code cannot silently
// collide it back onto 0.2.5.1.
func TestCodeUnknownCompressor_Value(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		got  errs.Code
		want errs.Code
	}
	tests := []tc{
		{"unknown compressor code is 0.2.5.1", transform.CodeUnknownCompressor, 0x00_02_05_01},
	}
	runCase := func(t *testing.T, got, want errs.Code) {
		t.Helper()
		//: UnknownCompressor must stay distinct from DuplicateRegistration.
		if got != want {
			t.Errorf("CodeUnknownCompressor = %#x, want %#x", got, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.got, c.want)
		})
	}
}

// TestDuplicateRegistration verifies the sentinel carries the DUPLICATE_REGISTRATION
// reason paired with code 0.2.5.5, and renders that pairing in its bracket header
// — the invariant the fix protects (ADR 0005 §Semantics: one canonical
// (code,reason) per code, keyed by the log-parser regex).
func TestDuplicateRegistration(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		wantCode   errs.Code
		wantReason string
		wantHeader string
	}
	tests := []tc{
		{
			"sentinel pairs 0.2.5.5 with DUPLICATE_REGISTRATION",
			0x00_02_05_05,
			"DUPLICATE_REGISTRATION",
			"[0.2.5.5 DUPLICATE_REGISTRATION]",
		},
	}
	runCase := func(t *testing.T, wantCode errs.Code, wantReason, wantHeader string) {
		t.Helper()
		//: the sentinel's code octets must match the reason word in the header.
		if got, ok := errs.CodeOf(transform.DuplicateRegistration); !ok || got != wantCode {
			t.Errorf("CodeOf(DuplicateRegistration) = %#x ok=%v, want %#x", got, ok, wantCode)
		}
		//: reason must be the SCREAMING_SNAKE word that appears in the header.
		if got, ok := errs.ReasonOf(transform.DuplicateRegistration); !ok || got != wantReason {
			t.Errorf("ReasonOf(DuplicateRegistration) = %q ok=%v, want %q", got, ok, wantReason)
		}
		//: Error() must render the canonical "[<code> <REASON>]" bracket header.
		if got := transform.DuplicateRegistration.Error(); !strings.Contains(got, wantHeader) {
			t.Errorf("DuplicateRegistration.Error() = %q, want it to contain %q", got, wantHeader)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.wantCode, c.wantReason, c.wantHeader)
		})
	}
}

// TestRegisterPanicHeader is the regression guard for the confirmed defect: both
// boot-time panic paths (nil scheme + distinct-scheme conflict) MUST render the
// DUPLICATE_REGISTRATION reason alongside code 0.2.5.5 — never 0.2.5.1, whose
// reason is UNKNOWN_COMPRESSOR. The recovered panic string is asserted to carry
// the matching header and to NOT carry the contradictory word.
func TestRegisterPanicHeader(t *testing.T) {
	type tc struct {
		name string
		run  func()
	}
	tests := []tc{
		{"nil scheme panics with duplicate-registration header", func() { transform.Register(nil) }},
		{
			"distinct scheme under taken name panics with duplicate-registration header",
			func() {
				transform.Register(&fakeCompressor{name: "hdr-dup"})
				transform.Register(&fakeCompressor{name: "hdr-dup"})
			},
		},
	}
	runCase := func(t *testing.T, name string, run func()) {
		t.Helper()
		//: clean slate so the conflict row panics on its own second register.
		transform.ResetForTest()
		defer func() {
			//: a missing panic means the guard regressed entirely.
			r := recover()
			if r == nil {
				t.Fatalf("%s: expected panic, got none", name)
			}
			//: panic value is the sentinel's Error() string.
			msg, ok := r.(string)
			if !ok {
				t.Fatalf("%s: panic value type = %T, want string", name, r)
			}
			//: the canonical pairing must be present.
			if !strings.Contains(msg, "[0.2.5.5 DUPLICATE_REGISTRATION]") {
				t.Errorf("%s: panic %q missing header [0.2.5.5 DUPLICATE_REGISTRATION]", name, msg)
			}
			//: the impossible pairing (code 0.2.5.1 = UNKNOWN_COMPRESSOR) must be gone.
			if strings.Contains(msg, "0.2.5.1") {
				t.Errorf("%s: panic %q still references the contradictory code 0.2.5.1", name, msg)
			}
		}()
		run()
	}
	for _, c := range tests {
		//: sequential — each row mutates the process-wide registry.
		t.Run(c.name, func(t *testing.T) { runCase(t, c.name, c.run) })
	}
}
