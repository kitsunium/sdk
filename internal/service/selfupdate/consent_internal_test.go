// Package updater provides self-update functionality for ktn-linter binary.
// White-box tests for the consent gate: a run that did not ask to upgrade
// must not silently download and replace this binary.
//
// The env-reading tests cannot be parallel (t.Setenv panics under
// t.Parallel); the pure ones are.
package selfupdate

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Test_consentFromEnv pins the three-way answer the variable can give:
// authorised, refused, or nothing recorded — and only the last falls through
// to a prompt.
func Test_consentFromEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		value       string
		wantGranted bool
		wantDecided bool
	}{
		{name: "unset falls through", value: "", wantDecided: false},
		{name: "whitespace falls through", value: "   ", wantDecided: false},
		{name: "one authorises", value: "1", wantGranted: true, wantDecided: true},
		{name: "true authorises", value: "true", wantGranted: true, wantDecided: true},
		{name: "yes authorises", value: "yes", wantGranted: true, wantDecided: true},
		{name: "zero refuses", value: "0", wantGranted: false, wantDecided: true},
		{name: "false refuses", value: "false", wantGranted: false, wantDecided: true},
		//: A typo must be a refusal, never permission — and it must be
		//: DECIDED, so it does not silently fall through to a prompt the
		//: operator thought they had answered.
		{name: "typo refuses and decides", value: "ture", wantGranted: false, wantDecided: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			granted, decided := consentFromEnv(tc.value)
			if granted != tc.wantGranted {
				t.Errorf("consentFromEnv(%q) granted = %v, want %v", tc.value, granted, tc.wantGranted)
			}
			if decided != tc.wantDecided {
				t.Errorf("consentFromEnv(%q) decided = %v, want %v", tc.value, decided, tc.wantDecided)
			}
		})
	}
}

// Test_askUpgradeConsent pins the prompt contract: default-no, an explicit
// yes required, and the question actually printed so the pause is explained.
func Test_askUpgradeConsent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		//: nilReader stands in for a caller with no stdin at all.
		nilReader bool
		want      bool
	}{
		{name: "y accepts", input: "y\n", want: true},
		{name: "yes accepts", input: "yes\n", want: true},
		{name: "uppercase Y accepts", input: "Y\n", want: true},
		{name: "yes without newline accepts", input: "yes", want: true},
		{name: "bare enter declines", input: "\n", want: false},
		{name: "n declines", input: "n\n", want: false},
		{name: "anything else declines", input: "maybe\n", want: false},
		{name: "eof declines", input: "", want: false},
		{name: "no reader declines", nilReader: true, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			//: A nil io.Reader is a distinct case from an empty one.
			if tc.nilReader {
				if got := askUpgradeConsent(&out, nil); got != tc.want {
					t.Errorf("askUpgradeConsent(nil) = %v, want %v", got, tc.want)
				}
				return
			}

			got := askUpgradeConsent(&out, strings.NewReader(tc.input))
			if got != tc.want {
				t.Errorf("askUpgradeConsent(%q) = %v, want %v", tc.input, got, tc.want)
			}
			//: The user must have been told what they are answering, and
			//: that the default is no.
			if !strings.Contains(out.String(), "[y/N]") {
				t.Errorf("prompt = %q, want it to show the [y/N] default", out.String())
			}
		})
	}
}

// Test_terminalLike pins the mode test StdinIsTerminal delegates to, which is
// the whole reason a prompt is not offered to a pipe or a redirect.
func Test_terminalLike(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode os.FileMode
		want bool
	}{
		{name: "character device is a terminal", mode: os.ModeCharDevice | os.ModeDevice, want: true},
		{name: "pipe is not", mode: os.ModeNamedPipe, want: false},
		{name: "regular file is not", mode: 0o644, want: false},
		{name: "directory is not", mode: os.ModeDir | 0o755, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := terminalLike(tc.mode); got != tc.want {
				t.Errorf("terminalLike(%v) = %v, want %v", tc.mode, got, tc.want)
			}
		})
	}
}

// Test_isNullDevice pins the exclusion the mode test cannot make.
//
// /dev/null IS a character device — os.Stat reports ModeCharDevice on it — so
// terminalLike answers true for it, and a caller redirecting stdin from it was
// told a human was present. The consent path then wrote a prompt to a reader
// that answers EOF forever, instead of refusing immediately as unattended.
func Test_isNullDevice(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		open   func(t *testing.T) *os.File
		want   bool
		reason string
	}{
		{
			name: "the null device is one",
			open: func(t *testing.T) *os.File {
				t.Helper()
				f, err := os.Open(nullDevicePath)
				//: A platform without /dev/null has nothing to assert here.
				if err != nil {
					t.Skipf("%s unavailable: %v", nullDevicePath, err)
				}
				t.Cleanup(func() { _ = f.Close() })

				//: The handle the case stats.
				return f
			},
			want:   true,
			reason: "it passes the mode test and answers every prompt with EOF",
		},
		{
			name: "a regular file is not",
			open: func(t *testing.T) *os.File {
				t.Helper()
				f, err := os.CreateTemp(t.TempDir(), "stdin-*")
				if err != nil {
					t.Fatalf("temp: %v", err)
				}
				t.Cleanup(func() { _ = f.Close() })

				//: An ordinary file, excluded by the mode test anyway.
				return f
			},
			want:   false,
			reason: "a redirect from a file is already excluded by mode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			info, err := tt.open(t).Stat()
			//: A handle that cannot be stat'd tells the case nothing.
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			//: Identity, not name — a symlink or bind mount must still match.
			if got := isNullDevice(info); got != tt.want {
				t.Errorf("isNullDevice() = %t, want %t (%s)", got, tt.want, tt.reason)
			}
		})
	}
}

// Test_terminalLikeDoesNotExcludeTheNullDevice pins WHY the identity check has
// to exist: the mode test alone cannot make this distinction, and the comment
// that claimed it could was wrong.
func Test_terminalLikeDoesNotExcludeTheNullDevice(t *testing.T) {
	t.Parallel()

	tests := []struct{ name string }{{name: "the null device is a character device"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			info, err := os.Stat(nullDevicePath)
			//: A platform without /dev/null has nothing to assert here.
			if err != nil {
				t.Skipf("%s unavailable: %v", nullDevicePath, err)
			}
			//: This is the finding, asserted rather than described: the mode
			//: test says "terminal" for something that is not one.
			if !terminalLike(info.Mode()) {
				t.Error("terminalLike(/dev/null) = false; the identity check would then be dead code")
			}
		})
	}
}
