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
// on a directory the caller owns. A row whose grant cannot be applied says WHY
// through t.Skip rather than passing quietly.

// everyone is the string-form SID `icacls` is handed, so the test and the
// production rule agree on the identifier by construction rather than by a
// name that could resolve differently under a non-English locale.
const everyone string = "*S-1-1-0"

// grant applies an icacls permission string to dir, and FAILS rather than
// skips when it cannot.
//
// A skip would be the comfortable choice and it is the wrong one. This lane is
// the only gate `dacl_windows.go` has — the Linux Bazel gate compiles neither
// the check nor this file — and `go test` runs here WITHOUT -v, so it buffers
// a passing package's output and discards it. A skipped row and a passed row
// are therefore the same green tick, which makes a test that quietly stopped
// running indistinguishable from one that never existed. That is ADR 0082
// §D5's own argument, applied to a table whose refusing rows are the entire
// claim of ADR 0084.
//
// icacls ships with every supported Windows and needs no privilege to edit an
// ACL on a directory the caller owns, so a failure here is a real change in
// the runner image and is worth someone's attention rather than a shrug.
func grant(t *testing.T, dir, permission string) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "icacls", dir, "/grant", everyone+":"+permission).CombinedOutput()
	//: the command's own output is the diagnosis; an exit status says nothing
	//: about why an ACL edit was refused.
	if err != nil {
		t.Fatalf("icacls could not grant %q on %s — this lane is the only gate the DACL check has, so this is a failure and not a skip: %s (%v)", permission, dir, out, err)
	}
}

// TestTheWindowsDirectoryRuleIsAPlantingGrantToEveryone pins the whole rule,
// accepting rows included.
//
// The first row is first on purpose and is the load-bearing one. If the
// runner's temporary directory carried an Everyone-write entry, this rule
// would refuse every lock directory the suite builds and the failure would
// read as a bug in the lock domain rather than as a wrong assumption about the
// runner. So the assumption is asserted rather than relied on.
//
// The read-only row is the one a careless rule loses: a check that refused any
// directory naming Everyone at all would pass the refusing row and quietly
// refuse a great many safe directories. A world-READABLE lock directory is not
// a world-writable one, exactly as 0755 is not 0777.
func TestTheWindowsDirectoryRuleIsAPlantingGrantToEveryone(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// grant is the icacls permission string applied to Everyone, or empty
		// for a directory whose ACL is left exactly as created.
		grant string
		// refuse says whether NewFileLocker must reject the directory.
		refuse bool
	}
	tests := []tc{
		{"as created, no Everyone entry at all", "", false},
		{"Everyone may write", "(OI)(CI)W", true},
		{"Everyone may only read", "(OI)(CI)R", false},
		{"Everyone has full control", "(OI)(CI)F", true},
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
		//: (OI)(CI) makes the entry inheritable AND leaves it applying to this
		//: directory — (IO) would make it inherit-only, which the rule skips
		//: on purpose and which would make the refusing rows assert nothing.
		if c.grant != "" {
			grant(t, locks, c.grant)
		}
		locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: locks})
		//: the refusing half: any account can replace this directory's lock
		//: files, so the next process locks a different inode.
		if c.refuse {
			//: both halves, because a constructor returning a usable locker
			//: AND an error is a different bug from one returning the wrong
			//: code.
			if locker != nil {
				t.Fatalf("a directory granting Everyone %q was accepted", c.grant)
			}
			//: and the code, because an operator acts on
			//: LOCK_DIRECTORY_UNSAFE by changing an ACL and on anything else
			//: by filing a bug against this package.
			if !errs.HasCode(err, svclock.CodeLockDirectoryUnsafe) {
				t.Fatalf("NewFileLocker on a directory granting Everyone %q = %v, want LOCK_DIRECTORY_UNSAFE", c.grant, err)
			}
			//: verdict pinned.
			return
		}
		//: the accepting half, which is the one a rule that matched on the
		//: identifier alone would lose.
		if err != nil || locker == nil {
			t.Fatalf("NewFileLocker on a directory granting Everyone %q = %v, want a locker", c.grant, err)
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

// TestADirectoryCreatedUnderAnEveryoneWritableParentIsRefused pins the hole
// that `prepareDir` had: it returned as soon as os.MkdirAll succeeded.
//
// On Unix that is sound — a directory this process created has the mode it
// asked for. On Windows it is false: a new directory INHERITS its parent's
// access control list and lockDirMode means nothing there, so the FIRST
// construction used a directory every account could write and only a later one
// would have noticed.
func TestADirectoryCreatedUnderAnEveryoneWritableParentIsRefused(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "state")
	//: the mode is irrelevant on Windows and is passed for the signature; the
	//: ACL below is what this test is about.
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatalf("creating the parent = %v", err)
	}
	//: (OI)(CI) without (IO), so the grant applies to the parent AND is
	//: inherited by the directory NewFileLocker is about to create.
	grant(t, parent, "(OI)(CI)W")
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

// TestAnIndirectionAboveTheLockFileIsRefusedWhenEveryoneCanWriteItsContainer
// closes the platform asymmetry ADR 0083 shipped with.
//
// Until the DACL could be read, `plantable` answered no on Windows and the
// chain rule refused nothing here. It now answers the same question the POSIX
// rule answers, in this platform's own vocabulary, so a junction planted in a
// directory any account can write is refused exactly as a symbolic link in a
// world-writable directory is.
//
// A junction rather than a symbolic link because `mklink /J` needs no
// privilege, so this row runs on an ordinary runner.
func TestAnIndirectionAboveTheLockFileIsRefusedWhenEveryoneCanWriteItsContainer(t *testing.T) {
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
	//: the command's own output is the diagnosis.
	if linkErr != nil {
		t.Skipf("cannot create a junction on this runner: %s (%v)", out, linkErr)
	}
	//: the grant is applied AFTER the junction is planted, so the junction's
	//: own creation does not depend on it — the planter in the real attack
	//: needs the write, this test only needs the resulting ACL.
	grant(t, container, "(OI)(CI)W")
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
