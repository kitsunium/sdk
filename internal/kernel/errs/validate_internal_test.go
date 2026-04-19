package errs

import (
	"errors"
	"strings"
	"testing"
)

func Test_validateDefineArgs(t *testing.T) {
	t.Parallel()
	longPublic := strings.Repeat("x", maxPublicRunes+1)
	type tc struct {
		name            string
		code            int
		reason          string
		public          string
		private         string
		wantErrSentinel error
	}
	tests := []tc{
		{"all valid", 3101, "WRITER_NIL", "writer is nil", "ctx/logger nil writer", nil},
		{"zero code", 0, "X", "y", "z", errInvalidCode},
		{"too small code", 999, "X", "y", "z", errInvalidCode},
		{"empty reason", 1100, "", "y", "z", errInvalidReason},
		{"lowercase reason", 1100, "bad_reason", "y", "z", errInvalidReason},
		{"digit leading reason", 1100, "1BAD", "y", "z", errInvalidReason},
		{"empty public", 1100, "GOOD", "", "z", errInvalidPublic},
		{"too long public", 1100, "GOOD", longPublic, "z", errInvalidPublic},
		{"public with newline", 1100, "GOOD", "a\nb", "z", errInvalidPublic},
		{"empty private", 1100, "GOOD", "pub", "", errInvalidPrivate},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := validateDefineArgs(c.code, c.reason, c.public, c.private)
		if c.wantErrSentinel == nil {
			if got != nil {
				t.Errorf("expected nil, got %v", got)
			}
			return
		}
		if !errors.Is(got, c.wantErrSentinel) {
			t.Errorf("errors.Is(%v, %v) = false", got, c.wantErrSentinel)
		}
	}
	for _, c := range tests {
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
		code    int
		wantErr bool
	}
	tests := []tc{
		{"ok", 3101, false},
		{"zero", 0, true},
		{"below min", 999, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := validateCode(c.code)
		if (got != nil) != c.wantErr {
			t.Errorf("validateCode(%d) err=%v, wantErr=%v", c.code, got, c.wantErr)
		}
	}
	for _, c := range tests {
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
