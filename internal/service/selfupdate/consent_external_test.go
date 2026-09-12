// Package updater_test black-box tests the consent gate: the decision that a
// run which did not ask to upgrade must not silently download and replace this
// binary.
//
// These live outside the package because they are the contract cmd/ depends on
// — AuthoriseUnattendedUpgrade, the two Explain* writers and StdinIsTerminal
// are what license_gate.go calls, and asserting them from inside would let a
// test reach past the surface a caller actually has.
package selfupdate_test

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	selfupdate "github.com/kitsunium/sdk/internal/service/selfupdate"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// TestAuthoriseUnattendedUpgrade covers the composed decision, including the
// case that matters most: nobody at the keyboard and no standing
// authorisation means NO, without asking a question no one can answer.
func TestAuthoriseUnattendedUpgrade(t *testing.T) {
	tests := []struct {
		name        string
		env         string
		interactive bool
		input       string
		want        bool
		//: wantPrompt asserts whether a question was printed at all.
		wantPrompt bool
	}{
		{
			name: "unattended with no authorisation refuses silently",
			//: The defect being closed: this is a `ktn-linter run .` in CI.
			env: "", interactive: false, want: false, wantPrompt: false,
		},
		{
			name: "unattended with authorisation proceeds without asking",
			env:  "1", interactive: false, want: true, wantPrompt: false,
		},
		{
			name: "explicit refusal is honoured even at a terminal",
			env:  "0", interactive: true, input: "y\n", want: false, wantPrompt: false,
		},
		{
			name: "terminal with no authorisation asks and takes yes",
			env:  "", interactive: true, input: "y\n", want: true, wantPrompt: true,
		},
		{
			name: "terminal with no authorisation asks and takes no",
			env:  "", interactive: true, input: "\n", want: false, wantPrompt: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			//: Set explicitly rather than trusting the ambient environment.
			t.Setenv(testSource.AutoUpgradeEnv(), tc.env)

			var out bytes.Buffer
			got := testSource.AuthoriseUnattendedUpgrade(&out, strings.NewReader(tc.input), tc.interactive)
			if got != tc.want {
				t.Errorf("testSource.AuthoriseUnattendedUpgrade() = %v, want %v", got, tc.want)
			}
			//: An unattended run must not stall on a prompt, and an explicit
			//: standing answer must not be second-guessed with one.
			asked := strings.Contains(out.String(), "[y/N]")
			if asked != tc.wantPrompt {
				t.Errorf("prompted = %v, want %v (output %q)", asked, tc.wantPrompt, out.String())
			}
		})
	}
}

// TestExplainUpgradeRefusal pins that a refusal is never a dead end: all
// three ways forward are named, including the second opt-in that only bites
// once the first has been granted.
//
// The rows are the three distinct escapes, not three spellings of one: a
// manual command for the operator at the keyboard, the standing authorisation
// for an unattended run, and the privilege opt-in that only becomes relevant
// after the first has been granted. Dropping any one of them strands a
// different audience.
func TestExplainUpgradeRefusal(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	testSource.ExplainUpgradeRefusal(&out)
	text := out.String()

	tests := []struct {
		name string
		want string
		why  string
	}{
		{
			name: "the manual command",
			want: testSource.Product + " upgrade",
			why:  "the operator at the keyboard needs a command to type",
		},
		{
			name: "the standing authorisation",
			want: testSource.AutoUpgradeEnv(),
			why:  "an unattended run has nobody to answer a prompt",
		},
		{
			name: "the privilege opt-in",
			want: testSource.SudoOptInEnv(),
			why:  "it only bites after the first opt-in is granted, so it is the escape people miss",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			//: A refusal that does not name the way forward strands the user.
			if !strings.Contains(text, tt.want) {
				t.Errorf("testSource.ExplainUpgradeRefusal() = %q, want it to mention %q (%s)", text, tt.want, tt.why)
			}
		})
	}
}

// TestExplainUpgradeFailure pins that every refusal class gets an explanation
// naming the correct response, and specifically that an authenticity refusal
// is NOT presented as something to retry.
//
// This is the output a user actually sees: exit.Code only writes to the
// --debug log, so before this existed `ktn-linter upgrade` against a forged
// release printed nothing and exited non-zero.
func TestExplainUpgradeFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		//: wantContains are substrings the explanation must carry.
		wantContains []string
		//: wantAbsent are substrings it must NOT carry — chiefly "retry",
		//: which is wrong advice for a supply-chain refusal.
		wantAbsent []string
	}{
		{
			name:         "forged signature says do not install by hand",
			err:          fmt.Errorf("upgrading: %w", coreupd.SignatureInvalid),
			wantContains: []string{"not signed by the vendor key", "do NOT install it by hand"},
			wantAbsent:   []string{"retry"},
		},
		{
			name:         "unsigned release says do not install by hand",
			err:          fmt.Errorf("upgrading: %w", coreupd.SignatureMissing),
			wantContains: []string{"not signed by the vendor key"},
			wantAbsent:   []string{"retry"},
		},
		{
			name:         "unanchored build says rebuild from source",
			err:          fmt.Errorf("upgrading: %w", coreupd.NoVendorKey),
			wantContains: []string{"built from source", "rebuild from source"},
			wantAbsent:   []string{"retry"},
		},
		{
			name:         "privilege refusal names the opt-in",
			err:          fmt.Errorf("replacing binary: %w", coreupd.ElevationNotAuthorised),
			wantContains: []string{testSource.SudoOptInEnv(), "owner of that path"},
		},
		{
			name:         "digest mismatch names both possible causes",
			err:          fmt.Errorf("upgrading: %w", coreupd.ChecksumMismatch),
			wantContains: []string{"corrupted download", "changed after it was signed"},
			wantAbsent:   []string{"retry"},
		},
		{
			name:         "everything else is worth retrying",
			err:          fmt.Errorf("upgrading: %w", coreupd.DownloadFailed),
			wantContains: []string{"retry once the problem is resolved"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			testSource.ExplainUpgradeFailure(&out, "upgrade failed", tc.err)
			text := out.String()

			//: The action and the underlying error are always named.
			if !strings.Contains(text, "upgrade failed") || !strings.Contains(text, tc.err.Error()) {
				t.Errorf("testSource.ExplainUpgradeFailure() = %q, want it to name the action and the error", text)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(text, want) {
					t.Errorf("testSource.ExplainUpgradeFailure() = %q, want substring %q", text, want)
				}
			}
			//: Telling someone to retry a forged release is the one piece of
			//: advice that actively makes things worse.
			for _, absent := range tc.wantAbsent {
				if strings.Contains(text, absent) {
					t.Errorf("testSource.ExplainUpgradeFailure() = %q, must not suggest %q", text, absent)
				}
			}
		})
	}
}

// TestStdinIsTerminal exercises the composed accessor.
//
// It asserts AGREEMENT with the mode of the real stdin rather than a fixed
// answer: a test binary's stdin is a pipe under CI and inherits the
// developer's tty when run by hand, so a hardcoded expectation would be a test
// that passes in one place and fails in the other for no reason anyone caused.
// What has to hold either way is that the accessor reports what the mode
// actually says.
//
// The character-device test is RESTATED here rather than taken from the
// unexported terminalLike. That is deliberate and not merely a consequence of
// the package boundary: delegating would make this test agree with the
// implementation by construction, including when the implementation is wrong.
//
// The stability row pins something the agreement row cannot. The accessor
// re-stats os.Stdin on every call, so a flapping answer would make the consent
// decision non-deterministic — the same run could prompt or not — and an
// agreement check performed once would never notice.
func TestStdinIsTerminal(t *testing.T) {
	t.Parallel()

	info, err := os.Stdin.Stat()

	tests := []struct {
		name  string
		check func(t *testing.T)
	}{
		{
			name: "the accessor reports what the mode says",
			check: func(t *testing.T) {
				t.Helper()
				//: A stat we cannot read is the accessor's own "assume
				//: unattended" case, and the only answer available then.
				if err != nil {
					if selfupdate.StdinIsTerminal() {
						t.Error("StdinIsTerminal() = true with an unreadable stdin, want false")
					}
					return
				}
				//: Same mode, same answer — the accessor must not invent one.
				want := info.Mode()&os.ModeCharDevice != 0
				if got := selfupdate.StdinIsTerminal(); got != want {
					t.Errorf("StdinIsTerminal() = %v, want %v for mode %v", got, want, info.Mode())
				}
			},
		},
		{
			name: "the answer does not flap between calls",
			check: func(t *testing.T) {
				t.Helper()
				first := selfupdate.StdinIsTerminal()
				//: Several calls, because the accessor re-stats every time and
				//: a decision that changes mid-run is worse than a wrong one.
				for attempt := range 3 {
					if got := selfupdate.StdinIsTerminal(); got != first {
						t.Fatalf("StdinIsTerminal() = %v on attempt %d, was %v — the consent decision is not deterministic", got, attempt, first)
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.check(t)
		})
	}
}
