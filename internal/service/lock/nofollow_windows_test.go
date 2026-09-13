//go:build windows

package lock_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
	svclock "github.com/kitsunium/sdk/internal/service/lock"
)

// This file carries the SAME build constraint as nofollow_windows.go. The lane
// that executes it is the `windows` job of .github/workflows/e2e-cross.yml;
// the Linux Bazel gate compiles neither the backend nor this file.
//
// # One of these two rows may not run, and the test refuses to be silent
//
// Creating a SYMBOLIC LINK on Windows needs SeCreateSymbolicLinkPrivilege,
// which an unprivileged account does not hold unless Developer Mode is on.
// Creating a JUNCTION needs nothing but write access to the parent directory.
// So the table below plants both, and they exercise the two halves of the
// backend's refusal separately:
//
//   - the SYMBOLIC LINK is a FILE reparse point, so the open SUCCEEDS with
//     FILE_FLAG_OPEN_REPARSE_POINT and refuseReparseHandle is what refuses it;
//   - the JUNCTION is a DIRECTORY reparse point, so the open fails outright
//     (read-write on a directory needs FILE_FLAG_BACKUP_SEMANTICS) and
//     classifyOpenFailure is what names it.
//
// A row that cannot be planted says WHY through t.Skip rather than passing
// quietly, and the parent test FAILS if both are lost — which is the
// difference between a test that skipped and a test that is not there.
//
// What a green e2e-cross lane therefore proves is "at least one of the two
// reached a kernel", and NOT which one. The lane runs `go test` without -v, and
// go test buffers a passing package's output and discards it — measured, for
// t.Log AND for a raw fmt.Println, so there is no spelling of the count that
// survives. Reading which row ran means running this file with -v. That is
// stated here rather than left as a plausible-sounding claim about the log.

// reparseLedger is what the symbolic-link row points at: content readFence
// ACCEPTS as a counter, so the planter would be choosing the victim's fencing
// token. The same worst case as the Unix table's decimal row.
const reparseLedger string = "48213\n"

// reparseCase is one reparse point planted at the lock path.
type reparseCase struct {
	// name says what was planted; it is also the subtest's name.
	name string
	// plant creates the indirection at lockPath and reports why it could not,
	// if it could not. target is a directory that already exists, because a
	// junction has no other kind of target.
	plant func(t *testing.T, target, lockPath string) (why string, planted bool)
}

// plantSymbolicLink points the lock path at a FILE inside target, so the open
// succeeds on the link and the handle check is what refuses it.
func plantSymbolicLink(t *testing.T, target, lockPath string) (why string, planted bool) {
	t.Helper()
	file := filepath.Join(target, "elsewhere.lock")
	if err := os.WriteFile(file, []byte(reparseLedger), 0o600); err != nil {
		t.Fatalf("writing the redirect target = %v", err)
	}
	if err := os.Symlink(file, lockPath); err != nil {
		//: ERROR_PRIVILEGE_NOT_HELD (1314) is the expected refusal on an
		//: account without SeCreateSymbolicLinkPrivilege.
		return err.Error(), false
	}
	return "", true
}

// plantJunction points the lock path at target itself, so the open fails and
// classifyOpenFailure is what names it.
//
// mklink is a cmd builtin and /J needs no privilege at all, which is the whole
// reason this row sits beside one that does.
func plantJunction(t *testing.T, target, lockPath string) (why string, planted bool) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", lockPath, target).CombinedOutput()
	//: the command's own output is the diagnosis; an exit status says nothing
	//: about why.
	if err != nil {
		return string(out), false
	}
	return "", true
}

// reparseCases is the Unix probe's Windows twin, reduced to a table.
func reparseCases() []reparseCase {
	return []reparseCase{
		{"symbolic link", plantSymbolicLink},
		{"junction", plantJunction},
	}
}

// assertNothingReachedThrough pins that the refusal was a refusal: the
// acquisition wrote nothing through the link.
//
// before is how many entries the PLANT itself left in target — one for the
// symbolic-link row, none for the junction row — so the assertion is about
// what the acquisition added and nothing else.
func assertNothingReachedThrough(t *testing.T, what, target string, before int) {
	t.Helper()
	entries, readErr := os.ReadDir(target)
	if readErr != nil {
		t.Fatalf("reading the redirect target = %v", readErr)
	}
	if len(entries) > before {
		t.Fatalf("the redirect target gained %d entries — the lock reached through the %s", len(entries)-before, what)
	}
}

// newPlantedLocker builds a locker over a fresh lock directory and returns it
// with the redirect target directory beside it.
func newPlantedLocker(t *testing.T) (locker corelock.Locker, dir, target string) {
	t.Helper()
	base := t.TempDir()
	target = filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("creating the redirect target directory = %v", err)
	}
	dir = filepath.Join(base, "locks")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("creating the lock directory = %v", err)
	}
	built, err := svclock.NewFileLocker(svclock.FileConfig{Dir: dir})
	//: Windows accepts every directory by mode (ADR 0081 §D5), so this never
	//: refuses — and if it ever did, every row would be asserting nothing.
	if err != nil {
		t.Fatalf("NewFileLocker = %v, want a locker", err)
	}
	return built, dir, target
}

// TestTheFileLockerRefusesAnIndirectionAtTheLockPath is the Unix probe's
// Windows twin.
//
// The defect is the same one, and here it was READ rather than probed, because
// there was no Windows kernel on the machine this was written on: syscall.Open
// sets FILE_FLAG_OPEN_REPARSE_POINT for CREATE_NEW only — O_CREAT|O_EXCL — and
// this locker opens O_CREATE|O_RDWR, which is OPEN_ALWAYS (go1.27.0,
// src/syscall/syscall_windows.go, func Open). So CreateFileW followed a
// planted reparse point exactly as the Unix open followed a planted symlink,
// and the lock landed on the planter's file.
func TestTheFileLockerRefusesAnIndirectionAtTheLockPath(t *testing.T) {
	planted := 0
	runCase := func(t *testing.T, c reparseCase) {
		t.Helper()
		locker, dir, target := newPlantedLocker(t)
		//: the filename is the SHA-256 of the lock name: unforgeable, entirely
		//: predictable, and predictable is all the attack needs.
		why, ok := c.plant(t, target, lockFilePath(dir, victimName))
		if !ok {
			t.Skipf("this account cannot plant a %s, so the kernel half of this row did not run: %s", c.name, why)
		}
		planted++
		//: counted AFTER the plant, because the plant may have put a redirect
		//: target in place itself.
		before, readErr := os.ReadDir(target)
		if readErr != nil {
			t.Fatalf("reading the redirect target = %v", readErr)
		}
		lease, held, acquireErr := locker.TryAcquire(t.Context(), victimName)
		assertRefused(t, c.name, lease, held, acquireErr)
		assertNothingReachedThrough(t, c.name, target, len(before))
	}
	for _, c := range reparseCases() {
		//: deliberately NOT parallel: `planted` is written here and read below,
		//: and a non-parallel subtest has finished when t.Run returns.
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
	//: a test that skips on a runner nobody watches is indistinguishable from
	//: a test that was never written, and this file has a real chance of it:
	//: whether GitHub's windows-latest image grants
	//: SeCreateSymbolicLinkPrivilege is not something this repository controls.
	//: The junction row exists so the answer does not matter — and if BOTH
	//: become unplantable, that is a fact about the lane worth a red tick
	//: rather than a green one with two grey lines above it.
	if planted == 0 {
		t.Fatal("neither a symbolic link nor a junction could be planted on this host, so nothing verified that the lock refuses a reparse point — the Windows half of ADR 0081's closed Deferred item is UNPROVEN on this lane")
	}
	//: visible with -v and nowhere else; see this file's header for why there
	//: is no version of this line that a non-verbose lane would print.
	t.Logf("indirection rows that reached the kernel: %d of %d", planted, len(reparseCases()))
}

// TestTheReparseFlagIsAcceptedByTheToolchain is the canary for the one thing
// this backend borrows from the standard library rather than from kernel32.
//
// go1.27's syscall.Open forwards the high 12 bits of its flag word to
// CreateFileW's dwFlagsAndAttributes, and only those in its
// validFileFlagsMask — FILE_FLAG_OPEN_REPARSE_POINT is one of them. If that
// ever stops being true, syscall.Open returns ErrInvalid and EVERY acquisition
// on this platform fails at the open, before any lock is involved.
//
// The whole Windows suite would go red with it, which is why this is a canary
// rather than the coverage: it exists so the failure carries a name that
// points at the toolchain instead of at the locker.
func TestTheReparseFlagIsAcceptedByTheToolchain(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "plain.lock")
	//: the exact flag word the backend uses, on an ordinary file.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0o600)
	if err != nil {
		t.Fatalf("os.OpenFile with FILE_FLAG_OPEN_REPARSE_POINT = %v — the toolchain no longer forwards this flag, and every acquisition fails at the open", err)
	}
	if closeErr := file.Close(); closeErr != nil {
		t.Fatalf("closing = %v", closeErr)
	}
	//: and a plain file's handle must NOT carry the reparse bit, or the
	//: backend would refuse every lock file it ever created.
	reopened, err := os.OpenFile(path, os.O_RDWR|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0o600)
	if err != nil {
		t.Fatalf("reopening = %v", err)
	}
	defer func() {
		if closeErr := reopened.Close(); closeErr != nil {
			t.Errorf("closing the reopened file = %v", closeErr)
		}
	}()
	var info syscall.ByHandleFileInformation
	if infoErr := syscall.GetFileInformationByHandle(syscall.Handle(reopened.Fd()), &info); infoErr != nil {
		t.Fatalf("GetFileInformationByHandle = %v", infoErr)
	}
	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		t.Fatalf("a plain file reports attributes %#x with FILE_ATTRIBUTE_REPARSE_POINT set", info.FileAttributes)
	}
}
