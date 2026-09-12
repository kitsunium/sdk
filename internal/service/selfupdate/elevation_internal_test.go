// Package updater provides self-update functionality for ktn-linter binary.
// White-box tests for the privilege-escalation opt-in: `sudo -n mv` must not
// run unless someone authorised it, and the refusal must reach the caller
// through finalizeReplacement rather than being swallowed.
//
// None of these tests can be parallel: they set process environment
// variables, and t.Setenv panics under t.Parallel (see the project CLAUDE.md,
// development rule 10).
package selfupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreupd "github.com/kitsunium/sdk/internal/core/selfupdate"
)

// Test_truthyEnv pins the closed set of values that mean "yes".
//
// The default branch is the load-bearing one: an opt-in that reads an
// unrecognised value as permission is not an opt-in, so a typo must refuse
// rather than escalate.
func Test_truthyEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "one", value: "1", want: true},
		{name: "true", value: "true", want: true},
		{name: "uppercase TRUE", value: "TRUE", want: true},
		{name: "yes", value: "yes", want: true},
		{name: "y", value: "y", want: true},
		{name: "on", value: "on", want: true},
		{name: "padded", value: "  true  ", want: true},
		{name: "unset", value: "", want: false},
		{name: "zero", value: "0", want: false},
		{name: "false", value: "false", want: false},
		{name: "no", value: "no", want: false},
		{name: "typo", value: "ture", want: false},
		{name: "arbitrary", value: "please", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: Any drift here changes who is allowed to become root.
			if got := truthyEnv(tc.value); got != tc.want {
				t.Errorf("truthyEnv(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// Test_guardedSudoMove is the direct proof that no `sudo` process is created
// without authorisation.
//
// Observability is the point. The assertion is not only on the returned
// sentinel but on the FILESYSTEM: `sudo -n mv` would consume the staged file
// and overwrite the target, so both staying exactly as they were is what shows
// the command never ran — on a machine where sudo may well be NOPASSWD and a
// sentinel-only assertion would pass either way.
//
// The declined rows carry the other half of the contract. An opt-in that reads
// an unrecognised value as permission is not an opt-in, so "0", "false", "no"
// and a typo must all refuse, and refuse the same way as an unset variable.
//
// Not parallel: every row calls t.Setenv, which panics under t.Parallel
// (project CLAUDE.md, development rule 10).
func Test_guardedSudoMove(t *testing.T) {
	tests := []struct {
		name  string
		value string
		why   string
	}{
		{
			name:  "unset refuses",
			value: "",
			why:   "the defect being closed: a plain `ktn-linter upgrade` must never escalate on its own",
		},
		{name: "explicit zero refuses", value: "0", why: "an explicit no is a no"},
		{name: "explicit false refuses", value: "false", why: "an explicit no is a no"},
		{name: "explicit no refuses", value: "no", why: "an explicit no is a no"},
		{
			name:  "a typo refuses rather than escalating",
			value: "ture",
			why:   "an opt-in that reads an unrecognised value as permission is not an opt-in",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			staged := filepath.Join(tmpDir, "staged")
			target := filepath.Join(tmpDir, "installed")
			//: Seed both sides so a move would be visible in either direction.
			if err := os.WriteFile(staged, []byte("replacement"), 0o600); err != nil {
				t.Fatalf("seed staged file: %v", err)
			}
			if err := os.WriteFile(target, []byte("original"), 0o755); err != nil {
				t.Fatalf("seed target file: %v", err)
			}
			//: Set explicitly rather than trusting the ambient environment — a
			//: CI image that exports the variable would otherwise make this
			//: pass by running a real sudo.
			t.Setenv(testSource.SudoOptInEnv(), tc.value)

			err := testSource.guardedSudoMove(staged, target)
			//: The refusal must be classifiable: license_gate.go branches on it
			//: to tell the user the one thing that would unblock them.
			if !errors.Is(err, coreupd.ElevationNotAuthorised) {
				t.Fatalf("testSource.guardedSudoMove() error = %v, want errors.Is coreupd.ElevationNotAuthorised (%s)", err, tc.why)
			}
			//: And it must name the way forward.
			if !strings.Contains(err.Error(), testSource.SudoOptInEnv()) {
				t.Errorf("testSource.guardedSudoMove() error = %q, want it to name %s", err.Error(), testSource.SudoOptInEnv())
			}

			//: Nothing moved: the staged file is still staged...
			if _, statErr := os.Stat(staged); statErr != nil {
				t.Errorf("staged file disappeared — a move ran for %q: %v", tc.value, statErr)
			}
			//: ...and the target is byte-identical. This is the assertion that
			//: distinguishes "refused" from "escalated and happened to fail".
			content, readErr := os.ReadFile(target)
			if readErr != nil {
				t.Fatalf("read target: %v", readErr)
			}
			if string(content) != "original" {
				t.Errorf("target = %q, want %q — an escalated move ran for %q", content, "original", tc.value)
			}
		})
	}
}

// TestService_finalizeReplacement_refusesEscalationByDefault proves the
// refusal reaches the caller through the real replacement path.
//
// The Service is built by NewUpdaterWithDeps — i.e. with whatever `elevate`
// the production constructors install, not an injected stub — so this test
// fails if that default is ever changed back to the unguarded sudoMove.
//
// The rows are the spellings of "no". Unset is the ordinary case, but an
// operator who wrote 0 or false made a DELIBERATE refusal, and a guard that
// only recognised the unset case would escalate for exactly the people who
// thought about it and said no.
//
// Not parallel: every row calls t.Setenv, which panics under t.Parallel
// (project CLAUDE.md, development rule 10).
func TestService_finalizeReplacement_refusesEscalationByDefault(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "unset", value: ""},
		{name: "explicit zero", value: "0"},
		{name: "explicit false", value: "false"},
		{name: "a typo is not consent", value: "ture"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			staged := filepath.Join(tmpDir, "staged")
			if err := os.WriteFile(staged, []byte("replacement"), 0o600); err != nil {
				t.Fatalf("seed staged file: %v", err)
			}
			t.Setenv(testSource.SudoOptInEnv(), tc.value)

			removed := false
			fs := &mockFileSystem{
				chmodFunc: func(string, os.FileMode) error { return nil },
				//: A root-owned install directory is exactly what makes the
				//: plain rename fail this way, and what used to trigger the
				//: escalation.
				renameFunc: func(string, string) error { return os.ErrPermission },
				removeFunc: func(string) error { removed = true; return nil },
			}
			svc := NewUpdaterWithDeps("v1.0.0", testSource, &mockHTTPClient{}, fs, &mockCopier{})

			err := svc.finalizeReplacement(staged, filepath.Join(tmpDir, "installed"))
			//: The permission failure must surface with the escalation refusal
			//: underneath it, not with a completed root-privileged move.
			if !errors.Is(err, coreupd.ElevationNotAuthorised) {
				t.Fatalf("finalizeReplacement() error = %v, want errors.Is coreupd.ElevationNotAuthorised", err)
			}
			//: The original refusal is still reported alongside it.
			if !errors.Is(err, os.ErrPermission) {
				t.Errorf("finalizeReplacement() error = %v, want it to still carry os.ErrPermission", err)
			}
			//: The staging file is cleaned up rather than left behind.
			if !removed {
				t.Error("finalizeReplacement() left the staged file behind after refusing")
			}
		})
	}
}

// TestService_finalizeReplacement_escalatesWhenAuthorised is the other half:
// with the opt-in set, the elevation hook IS reached. It uses an injected
// elevate so the test never shells out to a real sudo — what is being pinned
// is that authorisation unblocks the path, not what sudo then does.
//
// The failing row is the one worth having. "The hook was reached" and "the
// replacement succeeded" are different claims, and a finalizeReplacement that
// called elevate and then swallowed its error would satisfy the first while
// reporting a successful upgrade that never happened — the worst outcome of
// the three, because the user is told their binary was replaced.
func TestService_finalizeReplacement_escalatesWhenAuthorised(t *testing.T) {
	//: Unlike its siblings in this file, this test injects `elevate` and never
	//: touches the environment, so it is the one that CAN run in parallel.
	t.Parallel()

	elevateFailure := errors.New("sudo: a password is required")

	tests := []struct {
		name        string
		elevateErr  error
		wantErr     error
		wantElevate bool
	}{
		{
			name:        "a successful escalation completes the replacement",
			elevateErr:  nil,
			wantErr:     nil,
			wantElevate: true,
		},
		{
			name:        "a failed escalation is reported, not swallowed",
			elevateErr:  elevateFailure,
			wantErr:     elevateFailure,
			wantElevate: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()
			staged := filepath.Join(tmpDir, "staged")
			target := filepath.Join(tmpDir, "installed")
			if err := os.WriteFile(staged, []byte("replacement"), 0o600); err != nil {
				t.Fatalf("seed staged file: %v", err)
			}

			elevated := false
			svc := NewUpdaterWithDeps("v1.0.0", testSource, &mockHTTPClient{}, &mockFileSystem{
				chmodFunc:  func(string, os.FileMode) error { return nil },
				renameFunc: func(string, string) error { return os.ErrPermission },
				removeFunc: func(string) error { return nil },
			}, &mockCopier{})
			//: The injected hook stands in for guardedSudoMove; the env var is
			//: what the real one consults, and the real one is covered above.
			svc.elevate = func(string, string) error {
				elevated = true
				return tc.elevateErr
			}

			err := svc.finalizeReplacement(staged, target)
			//: A swallowed elevation error would report a replacement that
			//: never happened.
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("finalizeReplacement() error = %v, want errors.Is %v", err, tc.wantErr)
			}
			if elevated != tc.wantElevate {
				t.Errorf("elevation hook reached = %v, want %v", elevated, tc.wantElevate)
			}
		})
	}
}

// TestSudoMove exercises the real, non-mocked escalation path so it isn't
// only covered through the injection seam exercised above. -n guarantees
// this never blocks on a password prompt, and pointing it at paths that
// don't exist makes the outcome deterministic (non-nil) regardless of
// whether this sandbox has sudo installed or a passwordless rule at all: no
// sudo binary, no cached credential, and a successful-but-doomed `mv` on a
// missing source all fail the same way.
func TestSudoMove(t *testing.T) {
	t.Parallel()

	err := sudoMove("/nonexistent/ktn-linter-sudomove-src", "/nonexistent/ktn-linter-sudomove-dst")
	if err == nil {
		t.Fatal("sudoMove() with nonexistent paths: error = nil, want non-nil")
	}
}
