package vfs

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// The publication sequence, one constant per mechanic that can fail.
//
// stepNone is the zero value and sabotages NOTHING, which is what makes it the
// control: a harness that quietly broke publication on its own would make every
// sabotage result meaningless, so the same harness has to be able to publish
// successfully. TestTheTemporaryLivesBesideItsTarget runs on it.
const (
	stepNone step = iota
	stepCreate
	stepWrite
	stepSync
	stepClose
	stepRename
	stepSyncDir
)

// errInjected stands in for whatever the operating system would have reported:
// a full disk, an I/O error on the device, a descriptor closed underneath us.
// Which one it is does not matter — what matters is that publication stopped
// partway, and that the destination did not move.
var errInjected = errors.New("injected failure")

// brittleTemp must remain substitutable for the real handle publish() drives;
// a drift in tempFile that this double did not follow would otherwise surface
// as an unrelated compile error inside brittleOps.
var _ tempFile = (*brittleTemp)(nil)

// step names one mechanic of the publication sequence.
type step int

// brittleTemp wraps a REAL file handle and fails at one chosen step.
//
// The handle is real on purpose. A pure double would prove that publish calls
// its ops in the right order; it would not prove that the bytes on disk are
// where the contract says they are. Here the temporary genuinely exists, is
// genuinely half-written on the write case, and its absence afterwards is
// genuinely observed by listing the directory.
type brittleTemp struct {
	file   *os.File
	failAt step
}

// Write writes a PREFIX of the payload and then fails, when the write step is
// the chosen one. Half a file on disk is exactly what a process killed
// mid-publication leaves, and it is the state the cleanup has to survive.
func (b *brittleTemp) Write(payload []byte) (n int, err error) {
	if b.failAt != stepWrite {
		return b.file.Write(payload)
	}
	half := len(payload) / 2
	written, writeErr := b.file.Write(payload[:half])
	//: a genuine failure to lay down the first half is reported as itself; the
	//: injected one only stands in when the real write actually succeeded.
	return written, cmp.Or(writeErr, errInjected)
}

// Sync flushes, or fails.
func (b *brittleTemp) Sync() error {
	if b.failAt == stepSync {
		return errInjected
	}
	return b.file.Sync()
}

// Close always releases the real descriptor — a test that leaked one would
// eventually fail for a reason unrelated to what it is checking — and then
// reports the injected failure if this is the chosen step.
func (b *brittleTemp) Close() error {
	closeErr := b.file.Close()
	if b.failAt == stepClose {
		return errInjected
	}
	return closeErr
}

// brittleOps returns the production mechanics with one of them sabotaged.
func brittleOps(disk *osFS, failAt step, failRemove bool) atomicOps {
	native := disk.nativeOps()
	return atomicOps{
		create: func(name string, perm fs.FileMode) (tempFile, error) {
			if failAt == stepCreate {
				return nil, errInjected
			}
			file, openErr := disk.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
			if openErr != nil {
				return nil, openErr
			}
			return &brittleTemp{file: file, failAt: failAt}, nil
		},
		rename: func(from, to string) error {
			if failAt == stepRename {
				return errInjected
			}
			return native.rename(from, to)
		},
		remove: func(name string) error {
			if failRemove {
				return errInjected
			}
			return native.remove(name)
		},
		syncDir: func(dir string) error {
			if failAt == stepSyncDir {
				return errInjected
			}
			return native.syncDir(dir)
		},
	}
}

// newDiskFS builds a disk filesystem over a fresh temporary directory and
// hands back both the concrete value and the real path, so a test can inspect
// the tree from OUTSIDE the abstraction it is checking.
func newDiskFS(t *testing.T) (disk *osFS, dir string) {
	t.Helper()
	dir = t.TempDir()
	filesystem, newErr := NewOS(dir)
	if newErr != nil {
		t.Fatalf("NewOS(%q) = %v, want a filesystem", dir, newErr)
	}
	concrete, ok := filesystem.(*osFS)
	if !ok {
		t.Fatalf("NewOS returned %T, want *osFS", filesystem)
	}
	return concrete, dir
}

// hashOf is the evidence. Comparing content by hash rather than by equality
// means the assertion says "these bytes are the same bytes", and says it in a
// failure message a reader can act on.
func hashOf(t *testing.T, file string) string {
	t.Helper()
	data, readErr := os.ReadFile(filepath.Clean(file))
	if errors.Is(readErr, fs.ErrNotExist) {
		return "<absent>"
	}
	if readErr != nil {
		t.Fatalf("reading %q: %v", file, readErr)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// leftoverTemps lists any temporary this package created and failed to remove.
func leftoverTemps(t *testing.T, dir string) []string {
	t.Helper()
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatalf("listing %q: %v", dir, readErr)
	}
	var orphans []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), tempPrefix) && strings.HasSuffix(entry.Name(), tempSuffix) {
			orphans = append(orphans, entry.Name())
		}
	}
	return orphans
}

// TestAFailedPublicationLeavesThePreviousBytesExactlyWhereTheyWere is the test
// this whole domain exists to make possible.
//
// For every step of the publication sequence that can fail, it: hashes the
// destination, publishes new content with that step sabotaged, and then checks
// three things — the error is PUBLISH_FAILED, the destination hashes to the
// SAME value it did before, and no temporary was left in the directory.
//
// The write case is the interesting one: the temporary really is half-written
// when the failure arrives, which is the state a process killed mid-publish
// leaves behind. That is the case a `WriteFile` would have turned into a
// truncated destination.
func TestAFailedPublicationLeavesThePreviousBytesExactlyWhereTheyWere(t *testing.T) {
	t.Parallel()
	const target = "index.html"
	previous := []byte("<h1>the version everyone is currently reading</h1>")
	replacement := []byte(strings.Repeat("<p>a much longer replacement that must never land partially</p>", 64))

	tests := []struct {
		name   string
		failAt step
	}{
		{"the temporary cannot be created", stepCreate},
		{"the write stops halfway", stepWrite},
		{"the flush to the device fails", stepSync},
		{"the descriptor will not close", stepClose},
		{"the rename is refused", stepRename},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			disk, dir := newDiskFS(t)
			if seedErr := disk.WriteFile(target, previous, 0o644); seedErr != nil {
				t.Fatalf("seeding %q: %v", target, seedErr)
			}
			before := hashOf(t, filepath.Join(dir, target))

			disk.ops = brittleOps(disk, tc.failAt, false)
			publishErr := disk.WriteAtomic(target, replacement, 0o644)

			if !kerrs.HasCode(publishErr, corevfs.CodePublishFailed) {
				t.Fatalf("WriteAtomic = %v, want PUBLISH_FAILED", publishErr)
			}
			if !errors.Is(publishErr, errInjected) && tc.failAt != stepCreate {
				t.Errorf("the injected cause did not survive in the chain: %v", publishErr)
			}
			after := hashOf(t, filepath.Join(dir, target))
			if after != before {
				t.Errorf("destination hash moved: before=%s after=%s — a failed publication rewrote the file", before, after)
			}
			if orphans := leftoverTemps(t, dir); len(orphans) != 0 {
				t.Errorf("temporaries left behind: %v", orphans)
			}
		})
	}
}

// TestAFailedPublicationOverAnAbsentFileCreatesNothing is the same guarantee
// stated for the case where there is no previous content: a publication that
// fails must not leave a name that resolves to a half-written file, and must
// not leave a name at all.
func TestAFailedPublicationOverAnAbsentFileCreatesNothing(t *testing.T) {
	t.Parallel()
	disk, dir := newDiskFS(t)
	disk.ops = brittleOps(disk, stepWrite, false)

	const target = "fresh.json"
	if publishErr := disk.WriteAtomic(target, []byte(strings.Repeat("x", 4096)), 0o600); !kerrs.HasCode(publishErr, corevfs.CodePublishFailed) {
		t.Fatalf("WriteAtomic = %v, want PUBLISH_FAILED", publishErr)
	}
	if got := hashOf(t, filepath.Join(dir, target)); got != "<absent>" {
		t.Errorf("%q exists after a failed publication (hash %s), want nothing", target, got)
	}
	if orphans := leftoverTemps(t, dir); len(orphans) != 0 {
		t.Errorf("temporaries left behind: %v", orphans)
	}
}

// TestACleanupThatFailsDoesNotHideTheRealFailure pins the precedence in
// discard(). When the disk is full AND the orphan will not unlink, reporting
// the unlink is reporting the consequence; the caller needs the cause.
func TestACleanupThatFailsDoesNotHideTheRealFailure(t *testing.T) {
	t.Parallel()
	disk, _ := newDiskFS(t)
	disk.ops = brittleOps(disk, stepRename, true)

	publishErr := disk.WriteAtomic("page.html", []byte("body"), 0o644)
	if !kerrs.HasCode(publishErr, corevfs.CodePublishFailed) {
		t.Fatalf("WriteAtomic = %v, want PUBLISH_FAILED", publishErr)
	}
	if !errors.Is(publishErr, errInjected) {
		t.Fatalf("the rename failure was replaced by the cleanup failure: %v", publishErr)
	}
}

// TestADirectoryFlushThatFailsAfterTheRenameIsNotRolledBack pins the one
// asymmetry in the sequence, and it is the one people get wrong.
//
// Steps 1 to 4 failing mean nothing was published. Step 5 failing means the
// opposite: the rename already happened, every reader now sees the new bytes,
// and the only thing in doubt is whether the directory entry survives a power
// loss. Undoing it would be a second, non-atomic write to repair a durability
// problem — so the verdict is a different code and the content stays.
func TestADirectoryFlushThatFailsAfterTheRenameIsNotRolledBack(t *testing.T) {
	t.Parallel()
	disk, dir := newDiskFS(t)
	const target = "index.html"
	if seedErr := disk.WriteFile(target, []byte("old"), 0o644); seedErr != nil {
		t.Fatalf("seeding: %v", seedErr)
	}
	replacement := []byte("new")
	wantHash := sha256.Sum256(replacement)

	disk.ops = brittleOps(disk, stepSyncDir, false)
	publishErr := disk.WriteAtomic(target, replacement, 0o644)

	if !kerrs.HasCode(publishErr, CodeDirectorySyncFailed) {
		t.Fatalf("WriteAtomic = %v, want DIRECTORY_SYNC_FAILED", publishErr)
	}
	if kerrs.HasCode(publishErr, corevfs.CodePublishFailed) {
		t.Error("a post-rename flush failure reported PUBLISH_FAILED, which asserts the opposite of what happened")
	}
	if got := hashOf(t, filepath.Join(dir, target)); got != hex.EncodeToString(wantHash[:]) {
		t.Errorf("content hash = %s, want the NEW content — the rename must not be rolled back", got)
	}
	if orphans := leftoverTemps(t, dir); len(orphans) != 0 {
		t.Errorf("temporaries left behind: %v", orphans)
	}
}

// TestTheTemporaryLivesBesideItsTarget pins the same-filesystem invariant.
//
// A rename between two entries of ONE directory cannot cross a device, so
// EXDEV is unreachable rather than handled. That is a design property, not a
// runtime check, and the only way to keep it true is to assert where the
// temporary is created — because the day somebody "tidies" this into
// os.TempDir, the result is a cross-device rename in production and nothing at
// all in the tests.
func TestTheTemporaryLivesBesideItsTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target string
	}{
		{"a top-level file", "index.html"},
		{"a nested file", "assets/css/site.css"},
		{"a deeply nested file", "a/b/c/d/e.json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			disk, _ := newDiskFS(t)
			if mkErr := disk.MkdirAll(path.Dir(tc.target), 0o755); mkErr != nil && path.Dir(tc.target) != "." {
				t.Fatalf("MkdirAll: %v", mkErr)
			}
			var seen string
			//: stepNone — the SAME harness the sabotage cases use, with
			//: nothing sabotaged. The publish below must therefore succeed,
			//: which is what makes the harness trustworthy elsewhere.
			control := brittleOps(disk, stepNone, false)
			recording := control
			recording.create = func(name string, perm fs.FileMode) (tempFile, error) {
				seen = name
				//: the real create, only observed.
				return control.create(name, perm)
			}
			disk.ops = recording
			if publishErr := disk.WriteAtomic(tc.target, []byte("payload"), 0o644); publishErr != nil {
				t.Fatalf("WriteAtomic = %v, want nil", publishErr)
			}
			if got, want := path.Dir(seen), path.Dir(tc.target); got != want {
				t.Fatalf("temporary created in %q, want %q — a rename out of that directory could cross a device", got, want)
			}
		})
	}
}

// TestWrapHelpersRestateTheirSentinelExactly closes the one gap the restating
// pattern opens.
//
// failRead / failWrite / failPublish / failRoot repeat their sentinel's
// Reason, Public, Private and ExitCode inline, because that is how errs keeps
// the operating-system cause in the chain instead of collapsing it into a
// field. Repetition drifts. This compares the two, so an edit to errors.go
// that forgets the helper — or the reverse — fails here rather than shipping
// two errors that claim the same identity and do not have it.
func TestWrapHelpersRestateTheirSentinelExactly(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		produced error
		sentinel *kerrs.Error
	}{
		{"read", failRead(errInjected), corevfs.ReadFailed},
		{"write", failWrite(errInjected), corevfs.WriteFailed},
		{"publish", failPublish(errInjected), corevfs.PublishFailed},
		{"root", failRoot(errInjected), RootUnavailable},
		{"directory sync", syncPublished(failingSyncOps(), ".", "x"), DirectorySyncFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			produced, ok := tc.produced.(*kerrs.Error)
			if !ok {
				t.Fatalf("helper returned %T, want *errs.Error", tc.produced)
			}
			if produced.Code() != tc.sentinel.Code() {
				t.Errorf("code = %#x, want %#x", uint32(produced.Code()), uint32(tc.sentinel.Code()))
			}
			if produced.Reason() != tc.sentinel.Reason() {
				t.Errorf("reason = %q, want %q", produced.Reason(), tc.sentinel.Reason())
			}
			if produced.Public() != tc.sentinel.Public() {
				t.Errorf("public = %q, want %q", produced.Public(), tc.sentinel.Public())
			}
			if produced.Private() != tc.sentinel.Private() {
				t.Errorf("private = %q, want %q", produced.Private(), tc.sentinel.Private())
			}
			if produced.ExitCode() != tc.sentinel.ExitCode() {
				t.Errorf("exit = %d, want %d", produced.ExitCode(), tc.sentinel.ExitCode())
			}
			if !errors.Is(tc.produced, errInjected) {
				t.Error("the cause did not survive in the chain — errors.Is(err, fs.ErrNotExist) would stop answering")
			}
		})
	}
}

// failingSyncOps is the minimum atomicOps needed to reach the post-rename
// branch of syncPublished.
func failingSyncOps() atomicOps {
	return atomicOps{syncDir: func(_ string) error { return errInjected }}
}
