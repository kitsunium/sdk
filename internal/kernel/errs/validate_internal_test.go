package errs

import (
	"strings"
	"testing"
)

func Test_validateDefineArgs(t *testing.T) {
	t.Parallel()
	longPublic := strings.Repeat("x", maxPublicRunes+1)
	//: valid dotted-quad codes under ADR 0005:
	//:   0x00_03_01_01 = 0.3.1.1 (service/logger CodeWriterNil equivalent)
	//:   0x00_02_02_01 = 0.2.2.1 (core/codec CodeDuplicateRegistration equivalent)
	type tc struct {
		name       string
		code       Code
		reason     string
		public     string
		private    string
		wantReason string // reason of the returned *Error on failure; "" = expect nil
	}
	tests := []tc{
		{"all valid", 0x00_03_01_01, "WRITER_NIL", "writer is nil", "ctx/logger nil writer", ""},
		{"zero code", 0, "X", "y", "z", "INVALID_CODE"},
		{"layer 0 non-meta rejected", 0x00_00_01_01, "X", "y", "z", "INVALID_CODE"},
		{"layer 0 meta ok", CodeInvalidCode, "INVALID_CODE", "y", "z", ""},
		{"empty reason", 0x00_02_02_01, "", "y", "z", "INVALID_REASON"},
		{"lowercase reason", 0x00_02_02_01, "bad_reason", "y", "z", "INVALID_REASON"},
		{"digit leading reason", 0x00_02_02_01, "1BAD", "y", "z", "INVALID_REASON"},
		{"empty public", 0x00_02_02_01, "GOOD", "", "z", "INVALID_PUBLIC"},
		{"too long public", 0x00_02_02_01, "GOOD", longPublic, "z", "INVALID_PUBLIC"},
		{"public with newline", 0x00_02_02_01, "GOOD", "a\nb", "z", "INVALID_PUBLIC"},
		//: v5 FIX — private failure now cites INVALID_PRIVATE (not INVALID_PUBLIC).
		{"empty private cites INVALID_PRIVATE", 0x00_02_02_01, "GOOD", "pub", "", "INVALID_PRIVATE"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := validateDefineArgs(c.code, c.reason, c.public, c.private)
		if c.wantReason == "" {
			if got != nil {
				t.Errorf("expected nil, got %v", got)
			}
			return
		}
		if got == nil {
			t.Fatalf("expected failure with reason %s, got nil", c.wantReason)
		}
		if got.Reason() != c.wantReason {
			t.Errorf("want reason %q, got %q (full error: %v)", c.wantReason, got.Reason(), got)
		}
	}
	for _, c := range tests {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_validateCode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		code    Code
		wantErr bool
	}
	tests := []tc{
		{"ok layer 1", 0x00_01_01_01, false},
		{"ok layer 3", 0x00_03_02_01, false},
		{"ok pkg v1", 0x01_01_00_01, false},
		{"zero", 0, true},
		{"layer 0 non-meta", 0x00_00_05_01, true},
		{"layer 0 meta", CodeInvalidCode, false},
		{"int32 overflow", 0x80_00_00_01, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := validateCode(c.code)
		if (got != nil) != c.wantErr {
			t.Errorf("validateCode(%#08x) err=%v, wantErr=%v", uint32(c.code), got, c.wantErr)
		}
	}
	for _, c := range tests {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_validateReason(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		reason  string
		wantErr bool
	}{
		{"ok", "FOO_BAR", false},
		{"empty", "", true},
		{"lower", "foo", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := validateReason(tc.reason)
			if (got != nil) != tc.wantErr {
				t.Errorf("validateReason(%q) err=%v", tc.reason, got)
			}
		})
	}
}

func Test_validatePublic(t *testing.T) {
	t.Parallel()
	tooLong := strings.Repeat("x", maxPublicRunes+1)
	type tc struct {
		name    string
		public  string
		wantErr bool
	}
	tests := []tc{
		{"ok", "a public message", false},
		{"empty", "", true},
		{"too long", tooLong, true},
		{"newline", "a\nb", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := validatePublic(c.public)
		if (got != nil) != c.wantErr {
			t.Errorf("validatePublic(%q) err=%v", c.public, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_validatePrivate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		private string
		wantErr bool
	}{
		{"ok", "priv", false},
		{"empty", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := validatePrivate(tc.private)
			if (got != nil) != tc.wantErr {
				t.Errorf("validatePrivate(%q) err=%v", tc.private, got)
			}
		})
	}
}

func Test_isScreamingSnake(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		want bool
	}
	tests := []tc{
		{"empty", "", false},
		{"single upper", "A", true},
		{"upper snake", "HELLO_WORLD", true},
		{"with digit", "V2_READY", true},
		{"leading digit", "1ABC", false},
		{"lowercase", "abc", false},
		{"mixed case", "AbC", false},
		{"dash", "A-B", false},
		{"space", "A B", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := isScreamingSnake(c.in)
		if got != c.want {
			t.Errorf("isScreamingSnake(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_containsNewline(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		want bool
	}
	tests := []tc{
		{"empty", "", false},
		{"plain", "hello", false},
		{"with LF", "a\nb", true},
		{"with CR", "a\rb", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := containsNewline(c.in)
		if got != c.want {
			t.Errorf("containsNewline(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_isValidReasonRune(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		idx  int
		r    rune
		want bool
	}{
		{"leading upper", 0, 'A', true},
		{"leading digit", 0, '5', false},
		{"leading lower", 0, 'a', false},
		{"leading underscore", 0, '_', false},
		{"tail upper", 3, 'Z', true},
		{"tail digit", 3, '9', true},
		{"tail underscore", 3, '_', true},
		{"tail dash", 3, '-', false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isValidReasonRune(tc.idx, tc.r); got != tc.want {
				t.Errorf("isValidReasonRune(%d, %q) = %v, want %v", tc.idx, tc.r, got, tc.want)
			}
		})
	}
}

func Test_isUpperLetter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		r    rune
		want bool
	}{
		{"A", 'A', true},
		{"Z", 'Z', true},
		{"digit", '5', false},
		{"lower", 'a', false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isUpperLetter(tc.r); got != tc.want {
				t.Errorf("isUpperLetter(%q) = %v", tc.r, got)
			}
		})
	}
}

func Test_isDigit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		r    rune
		want bool
	}{
		{"0", '0', true},
		{"9", '9', true},
		{"A", 'A', false},
		{"underscore", '_', false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isDigit(tc.r); got != tc.want {
				t.Errorf("isDigit(%q) = %v", tc.r, got)
			}
		})
	}
}
