//go:build windows

package lock_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// This file carries the SAME build constraint as dacl_windows.go. The lane
// that executes it is the `windows` job of .github/workflows/e2e-cross.yml;
// the Linux Bazel gate compiles neither the check nor this file, so this lane
// is the only gate either has.
//
// Every row drives the real Windows access-control model through `icacls`,
// which ships with the operating system and needs no privilege to edit an ACL
// on a directory the caller owns. A row whose grant cannot be applied is a
// FAILURE and never a skip — see [grant].

// everyone and builtinUsers are the string-form SIDs `icacls` is handed, so
// the tests and the production rule agree on the identifier by construction
// rather than by a name that could resolve differently under a non-English
// locale.
const (
	everyone     string = "*S-1-1-0"
	builtinUsers string = "*S-1-5-32-545"
)

// grant applies an icacls permission string to dir for one identifier, and
// FAILS rather than skips when it cannot.
//
// A skip would be the comfortable choice and it is the wrong one. This lane is
// the only gate `dacl_windows.go` has — the Linux Bazel gate compiles neither
// the check nor this file — and `go test` runs here WITHOUT -v, so it buffers
// a passing package's output and discards it. A skipped row and a passed row
// are therefore the same green tick, which makes a test that quietly stopped
// running indistinguishable from one that never existed. That is ADR 0082
// §D5's own argument, applied to a table whose refusing rows are the entire
// claim of ADR 0084 and ADR 0085.
//
// icacls ships with every supported Windows and needs no privilege to edit an
// ACL on a directory the caller owns, so a failure here is a real change in
// the runner image and is worth someone's attention rather than a shrug.
func grant(t *testing.T, dir, sid, permission string) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "icacls", dir, "/grant", sid+":"+permission).CombinedOutput()
	//: the command's own output is the diagnosis; an exit status says nothing
	//: about why an ACL edit was refused.
	if err != nil {
		t.Fatalf("icacls could not grant %q to %s on %s — this lane is the only gate the DACL check has, so this is a failure and not a skip: %s (%v)", permission, sid, dir, out, err)
	}
}

// TestTheWindowsDirectoryRuleIsTheRightToReplaceSomebodyElsesEntry pins the
// whole of checkDir's Windows rule, accepting rows included.
//
// The first row is first on purpose and is the load-bearing one. If the
// runner's temporary directory carried a broad write entry, this rule would
// refuse every lock directory the suite builds and the failure would read as a
// bug in the lock domain rather than as a wrong assumption about the runner.
// So the assumption is asserted rather than relied on.
//
// The read-only row is the one a careless rule loses: a check that refused any
// directory naming Everyone at all would pass the refusing row and quietly
// refuse a great many safe directories. A world-READABLE lock directory is not
// a world-writable one, exactly as 0755 is not 0777.
//
// The two rows that separate CREATING an entry from REPLACING somebody else's
// are ADR 0085's subject. Windows spells those as different bits, so it does
// have the 0777|sticky shape ADR 0084 §Consequences said it lacked — and
// %ProgramData% is a directory Windows itself ships in it.
func TestTheWindowsDirectoryRuleIsTheRightToReplaceSomebodyElsesEntry(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// sid is the identifier the grant is addressed to.
		sid string
		// grant is the icacls permission string, or empty for a directory
		// whose ACL is left exactly as created.
		grant string
		// refuse says whether NewFileLocker must reject the directory.
		refuse bool
	}
	tests := []tc{
		{"as created, no broad entry at all", everyone, "", false},
		{"Everyone may write, and files created here inherit it", everyone, "(OI)(CI)W", true},
		{"Everyone may only read", everyone, "(OI)(CI)R", false},
		{"Everyone has full control", everyone, "(OI)(CI)F", true},
		{"Everyone may delete an entry it does not own", everyone, "(CI)(DC)", true},
		{"Everyone may rewrite the ACL", everyone, "(CI)(WDAC)", true},
		{"Everyone may add entries but may not replace one", everyone, "(CI)(WD,AD)", false},
		{"Everyone may write every FILE created here, and nothing on the directory", everyone, "(OI)(IO)(W)", true},
		{"BUILTIN\\Users may write, and files created here inherit it", builtinUsers, "(OI)(CI)W", true},
		{"BUILTIN\\Users may add entries but may not replace one", builtinUsers, "(CI)(WD,AD)", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		locks := filepath.Join(t.TempDir(), "locks")
		//: a child of t.TempDir() rather than the temporary directory itself,
		//: so its own cleanup never has to remove a directory this test made
		//: hostile.
		if err := os.Mkdir(locks, 0o700); err != nil {
			t.Fatalf("creating the lock directory = %v", err)
		}
		//: (CI) leaves the entry applying to this directory AND inherited by
		//: subdirectories; (OI) adds the FILES created in it, which is a
		//: separate question and a separate half of the rule. (IO) makes an
		//: entry inherit-only, which grants nothing on the directory itself.
		if c.grant != "" {
			grant(t, locks, c.sid, c.grant)
		}
		locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: locks})
		//: the refusing half: somebody who is not the holder can replace this
		//: directory's lock file, or rewrite the fencing ledger inside it.
		if c.refuse {
			//: both halves, because a constructor returning a usable locker
			//: AND an error is a different bug from one returning the wrong
			//: code.
			if locker != nil {
				t.Fatalf("a directory granting %s %q was accepted", c.sid, c.grant)
			}
			//: and the code, because an operator acts on
			//: LOCK_DIRECTORY_UNSAFE by changing an ACL and on anything else
			//: by filing a bug against this package.
			if !errs.HasCode(err, svclock.CodeLockDirectoryUnsafe) {
				t.Fatalf("NewFileLocker on a directory granting %s %q = %v, want LOCK_DIRECTORY_UNSAFE", c.sid, c.grant, err)
			}
			//: verdict pinned.
			return
		}
		//: the accepting half, which is the one a rule that matched on the
		//: identifier alone would lose.
		if err != nil || locker == nil {
			t.Fatalf("NewFileLocker on a directory granting %s %q = %v, want a locker", c.sid, c.grant, err)
		}
	}
	//: one subtest per row, each on its own directory.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestADirectoryCreatedUnderAWritableParentIsRefused pins the hole that
// prepareDir had: it returned as soon as os.MkdirAll succeeded.
//
// On Unix that is sound — a directory this process created has the mode it
// asked for. On Windows it is false: a new directory INHERITS its parent's
// access control list and lockDirMode means nothing there, so the FIRST
// construction used a directory every account could write and only a later one
// would have noticed.
func TestADirectoryCreatedUnderAWritableParentIsRefused(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "state")
	//: the mode is irrelevant on Windows and is passed for the signature; the
	//: ACL below is what this test is about.
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("creating the parent = %v", err)
	}
	//: (OI)(CI) without (IO), so the grant applies to the parent AND is
	//: inherited by the directory NewFileLocker is about to create AND by the
	//: lock files created inside it.
	grant(t, parent, everyone, "(OI)(CI)W")
	locks := filepath.Join(parent, "locks")
	//: deliberately absent, so prepareDir takes its create branch.
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: locks})
	//: accepted here would mean the create branch still returns before the
	//: check, which is the defect this test exists for.
	if locker != nil {
		t.Fatalf("a freshly created directory under an Everyone-writable parent was accepted")
	}
	//: the same code the existing-directory path reports, because it is the
	//: same fault found one branch earlier.
	if !errs.HasCode(err, svclock.CodeLockDirectoryUnsafe) {
		t.Fatalf("NewFileLocker on a fresh directory under an Everyone-writable parent = %v, want LOCK_DIRECTORY_UNSAFE", err)
	}
}

// TestAnIndirectionAboveTheLockFileIsRefusedWhenAnybodyCanWriteItsContainer
// closes the platform asymmetry ADR 0083 shipped with.
//
// Until the DACL could be read, plantable answered no on Windows and the
// chain rule refused nothing here. It now answers the same question the POSIX
// rule answers, in this platform's own vocabulary, so a junction planted in a
// directory any account can create an entry in is refused exactly as a
// symbolic link in a world-writable directory is.
//
// A junction rather than a symbolic link because `mklink /J` needs no
// privilege, so this row runs on an ordinary runner.
func TestAnIndirectionAboveTheLockFileIsRefusedWhenAnybodyCanWriteItsContainer(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	target := filepath.Join(base, "target")
	//: the tree the junction points at, built before the junction so mklink
	//: has a directory to target.
	if err := os.MkdirAll(filepath.Join(target, "locks"), 0o700); err != nil {
		t.Fatalf("building the redirect target = %v", err)
	}
	container := filepath.Join(base, "pub")
	//: the directory the junction is planted IN — the one whose ACL decides
	//: the verdict.
	if err := os.Mkdir(container, 0o700); err != nil {
		t.Fatalf("building the container = %v", err)
	}
	planted := filepath.Join(container, "app")
	out, linkErr := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", planted, target).CombinedOutput()
	//: a FAILURE and not a skip. `mklink /J` needs no privilege at all —
	//: unlike a symbolic link, which is why nofollow_windows_test.go pairs the
	//: two and this row has no pair — so a failure here is a change in the
	//: runner image, and skipping would leave the refusing half of the chain
	//: rule silently absent behind the same green tick a pass wears.
	if linkErr != nil {
		t.Fatalf("mklink /J failed on this runner — this is the refusing half of the Windows chain rule, so it is a failure and not a skip: %s (%v)", out, linkErr)
	}
	//: the grant is applied AFTER the junction is planted, so the junction's
	//: own creation does not depend on it — the planter in the real attack
	//: needs the right, this test only needs the resulting ACL.
	grant(t, container, everyone, "(OI)(CI)W")
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: filepath.Join(planted, "locks")})
	//: both halves, because they fail apart.
	if locker != nil {
		t.Fatalf("a junction planted in an Everyone-writable directory was accepted — every lock lands wherever it points")
	}
	//: the code matters as much as the refusal: an operator acts on
	//: LOCK_PATH_REDIRECTED by looking at the path, and on anything else by
	//: filing a bug against this package.
	if !errs.HasCode(err, svclock.CodeLockPathRedirected) {
		t.Fatalf("NewFileLocker under a junction in an Everyone-writable container = %v, want LOCK_PATH_REDIRECTED", err)
	}
	//: and nothing was created inside the redirect target: the audit runs
	//: before MkdirAll, which would have followed the junction.
	entries, readErr := os.ReadDir(filepath.Join(target, "locks"))
	//: the exact count is zero, which is what keeps "the audit runs before
	//: MkdirAll" an invariant rather than a habit.
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("the redirect target holds %d entries (%v), want 0", len(entries), readErr)
	}
}

// TestProgramDataIsAcceptedAsALockDirectoryAndRefusedAsAContainer is ADR
// 0085's centre, on a directory whose access control list Windows itself
// wrote rather than one this test built.
//
// %ProgramData% grants BUILTIN\Users a container-inherited entry carrying
// FILE_ADD_FILE and FILE_ADD_SUBDIRECTORY and NOT FILE_DELETE_CHILD — measured
// on windows-latest as S-1-5-32-545 with mask 0x116, inherited by every
// subdirectory. So every local interactive account can put an entry in a lock
// directory there, and none of them can replace one that a holder already
// owns. That is precisely what 0777|sticky means on Unix, which ADR 0052's
// table has accepted since it was written, and it is the shape ADR 0084
// §Consequences said Windows had no ACL for.
//
// The two rules therefore disagree about the same identifier on the same
// directory, and both halves are asserted here:
//
//   - the lock directory is ACCEPTED, because nobody can replace the lock file
//     a holder created;
//   - a junction planted beside it is REFUSED, because anybody can create a
//     component at a name nobody has taken.
//
// If %ProgramData% ever stops having that shape, the first half fails and
// says which way it moved — which is the measurement, not a flake.
func TestProgramDataIsAcceptedAsALockDirectoryAndRefusedAsAContainer(t *testing.T) {
	t.Parallel()
	programData := os.Getenv("ProgramData")
	//: a FAILURE and not a skip: %ProgramData% is set on every supported
	//: Windows, and this is the only row that measures the shipped shape.
	if programData == "" {
		t.Fatalf("ProgramData is unset on this runner — this test is the only measurement of the shape ADR 0085 rests on, so it is a failure and not a skip")
	}
	root := filepath.Join(programData, "kitsunium-sdk-lock-"+t.Name())
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	target := filepath.Join(root, "target")
	//: the lock directory NewFileLocker is asked for, and the tree a junction
	//: will point at.
	if err := os.MkdirAll(filepath.Join(target, "locks"), 0o700); err != nil {
		t.Fatalf("building a tree under ProgramData = %v", err)
	}
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: filepath.Join(target, "locks")})
	//: the accepting half. A refusal here is the breaking change ADR 0085
	//: exists to avoid, and it would name ProgramData in its message.
	if err != nil || locker == nil {
		t.Fatalf("NewFileLocker on a lock directory under ProgramData = %v, want a locker — every machine-wide Windows deployment puts one there", err)
	}
	planted := filepath.Join(root, "app")
	out, linkErr := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", planted, target).CombinedOutput()
	//: a FAILURE and not a skip, for the reason above: `mklink /J` needs no
	//: privilege, and this is the half that proves BUILTIN\Users is read as
	//: "anybody" on a directory Windows configured.
	if linkErr != nil {
		t.Fatalf("mklink /J failed under ProgramData — this is the half that proves BUILTIN\\Users is in the table, so it is a failure and not a skip: %s (%v)", out, linkErr)
	}
	redirected, redirectErr := svclock.NewFileLocker(svclock.FileConfig{Dir: filepath.Join(planted, "locks")})
	//: the refusing half, and it is the one that proves BUILTIN\Users is read
	//: as "anybody": the container is root, which inherited ProgramData's
	//: create-but-not-replace entry, and anybody holding it could have put
	//: that junction there.
	if redirected != nil {
		t.Fatalf("a junction planted under ProgramData was accepted — every local account can create one there")
	}
	//: the code, because the two refusals have different remedies.
	if !errs.HasCode(redirectErr, svclock.CodeLockPathRedirected) {
		t.Fatalf("NewFileLocker under a junction planted in ProgramData = %v, want LOCK_PATH_REDIRECTED", redirectErr)
	}
}

// TestTheSystemTemporaryDirectoryIsRefused is the breaking change ADR 0084
// §Consequences announced and did not deliver.
//
// That record says C:\Windows\Temp-shaped directories are "the realistic
// case" of what now fails. They were not failing: measured on
// windows-latest, the entry that makes that directory dangerous names
// BUILTIN\Users and not Everyone — S-1-5-32-545 with mask 0x1f01ff, full
// control, applying to the directory itself — and BUILTIN\Users was excluded
// from the table. So the one directory the ADR named as the realistic breakage
// was accepted by the rule that was supposed to refuse it.
//
// A red lane here means the runner image changed the ACL of its own system
// temporary directory, which is worth someone reading rather than a skip.
func TestTheSystemTemporaryDirectoryIsRefused(t *testing.T) {
	t.Parallel()
	systemRoot := os.Getenv("SystemRoot")
	//: a FAILURE and not a skip, for the same reason as above.
	if systemRoot == "" {
		t.Fatalf("SystemRoot is unset on this runner — this test is the only measurement of the breakage ADR 0084 announced, so it is a failure and not a skip")
	}
	systemTemp := filepath.Join(systemRoot, "Temp")
	//: the directory has to exist for the verdict to mean anything.
	if _, statErr := os.Stat(systemTemp); statErr != nil {
		t.Fatalf("%s is absent on this runner: %v", systemTemp, statErr)
	}
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: systemTemp})
	//: accepted here is the gap: every local interactive account holds full
	//: control on that directory, so any of them can unlink a holder's lock
	//: file and hand the next process a different inode.
	if locker != nil {
		t.Fatalf("%s was accepted as a lock directory — BUILTIN\\Users holds full control on it, which is the exposure ADR 0084 §Consequences said was refused", systemTemp)
	}
	//: the code, because the remedy is an ACL and not a retry.
	if !errs.HasCode(err, svclock.CodeLockDirectoryUnsafe) {
		t.Fatalf("NewFileLocker on %s = %v, want LOCK_DIRECTORY_UNSAFE", systemTemp, err)
	}
}
