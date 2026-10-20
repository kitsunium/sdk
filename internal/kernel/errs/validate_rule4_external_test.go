// Package errs_test — a structural failure obeys rule 4 like every other error.
package errs_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// rule4Code is a valid application code the structural cases vary one argument
// away from (64.1.1.1).
const rule4Code errs.Code = 0x40_01_01_01

// TestAStructuralFailureKeepsItsPublicFixedAndItsDetailPrivate pins rule 4 on
// the errors NewRuntime returns for bad arguments (#469): the Public is a fixed
// literal of at most 120 runes with no newline that never echoes the caller's
// argument, and the Private — empty before — carries the detail, the caller's
// value clipped so a runaway argument cannot bloat a log line.
func TestAStructuralFailureKeepsItsPublicFixedAndItsDetailPrivate(t *testing.T) {
	t.Parallel()
	hugeReason := "BAD-" + strings.Repeat("X", 200)
	type tc struct {
		name                           string
		code                           errs.Code
		reason, public, private        string
		wantReason                     string
		wantPrivateHas, wantPrivateNot string
	}
	tests := []tc{
		{"a zero code", 0, "OK", "ok", "ok", "INVALID_CODE", "got 0", ""},
		{"a code above int32", 0x80_00_00_01, "OK", "ok", "ok", "INVALID_CODE", "0x80000001", ""},
		{"a layer-0 code", 0x01_00_01_01, "OK", "ok", "ok", "INVALID_CODE", "1.0.1.1", ""},
		//: the issue's scenario: a 204-character reason.
		{"a huge reason", rule4Code, hugeReason, "ok", "ok", "INVALID_REASON", "BAD-XXXX", strings.Repeat("X", 100)},
		{"a lowercase reason", rule4Code, "user_missing", "ok", "ok", "INVALID_REASON", `"user_missing"`, ""},
		{"a reason with a newline", rule4Code, "BAD\nLINE", "ok", "ok", "INVALID_REASON", `BAD\nLINE`, ""},
		{"an empty public", rule4Code, "OK", "", "ok", "INVALID_PUBLIC", "must not be empty", ""},
		{"a public of 121 runes", rule4Code, "OK", strings.Repeat("é", 121), "ok", "INVALID_PUBLIC", "got 121", "é"},
		{"a public with a newline", rule4Code, "OK", "two\nlines", "ok", "INVALID_PUBLIC", "newline", "two"},
		{"an empty private", rule4Code, "OK", "ok", "", "INVALID_PRIVATE", "must not be empty", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := errs.NewRuntime(c.code, c.reason, c.public, c.private)
		if err.Reason() != c.wantReason {
			t.Fatalf("Reason = %q, want %q", err.Reason(), c.wantReason)
		}
		public := err.Public()
		if n := utf8.RuneCountInString(public); n == 0 || n > 120 {
			t.Errorf("Public is %d runes, want 1..120: %q", n, public)
		}
		if strings.ContainsAny(public, "\r\n") {
			t.Errorf("Public holds a newline: %q", public)
		}
		//: the caller's argument never reaches the wire.
		for _, arg := range []string{c.reason, c.public, c.private} {
			if len(arg) > 3 && strings.Contains(public, arg) {
				t.Errorf("Public %q echoes the caller's argument %q", public, arg)
			}
		}
		private := err.Private()
		if !strings.Contains(private, c.wantPrivateHas) {
			t.Errorf("Private = %q, want it to name %q", private, c.wantPrivateHas)
		}
		if c.wantPrivateNot != "" && strings.Contains(private, c.wantPrivateNot) {
			t.Errorf("Private = %q holds %q: the echo is not bounded", private, c.wantPrivateNot)
		}
		//: the rendering is the bracket header and the Public, nothing else.
		if rendered := err.Error(); !strings.HasSuffix(rendered, "] "+public) || strings.Contains(rendered, private) {
			t.Errorf("Error() = %q, want the header and the Public only", rendered)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTheEchoIsClippedOnARuneBoundary pins the bound on the reason a Private
// quotes: at most 64 runes and a mark, never a split rune, and a reason at the
// bound quoted whole.
func TestTheEchoIsClippedOnARuneBoundary(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		reason  string
		wantCut bool
	}
	tests := []tc{
		{"64 runes are quoted whole", strings.Repeat("é", 64), false},
		{"65 runes are clipped", strings.Repeat("é", 65), true},
		{"a long multibyte reason is clipped", strings.Repeat("日本", 100), true},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			private := errs.NewRuntime(rule4Code, c.reason, "ok", "ok").Private()
			if !utf8.ValidString(private) {
				t.Fatalf("Private is not valid UTF-8: %q", private)
			}
			if cut := strings.Contains(private, "…"); cut != c.wantCut {
				t.Errorf("clipped = %v, want %v: %q", cut, c.wantCut, private)
			}
			if !c.wantCut && !strings.Contains(private, c.reason) {
				t.Errorf("Private = %q, want the whole reason", private)
			}
		})
	}
}

// TestDefinePanicsWithTheDetail pins what a bad Define reports at init: the
// meta-code header AND the detail — Public no longer carries the offending
// value, so the panic message must, or the developer is told only "a reason is
// malformed" with nothing to grep for.
func TestDefinePanicsWithTheDetail(t *testing.T) {
	t.Parallel()
	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "INVALID_REASON") || !strings.Contains(msg, `"lower_case"`) {
			t.Errorf("panic = %q, want the meta-code and the offending reason", msg)
		}
	}()
	errs.Define(rule4Code, "lower_case", "ok", "ok")
	t.Fatal("Define accepted a lowercase reason")
}

// TestAWrapFallbackKeepsTheDetailPrivate pins Wrap's fallback for bad params:
// its Private names the failed rule's detail, and its Public stays fixed.
func TestAWrapFallbackKeepsTheDetailPrivate(t *testing.T) {
	t.Parallel()
	err := errs.Wrap(nil, errs.WrapParams{Code: rule4Code, Reason: "lower_case", Public: "ok", Private: "ok"})
	if err.Code() != errs.CodeInvalidWrapParams {
		t.Fatalf("Code = %s, want INVALID_WRAP_PARAMS", err.Code())
	}
	if strings.Contains(err.Public(), "lower_case") {
		t.Errorf("Public %q echoes the caller's reason", err.Public())
	}
	if !strings.Contains(err.Private(), `"lower_case"`) {
		t.Errorf("Private = %q, want the offending reason", err.Private())
	}
}
