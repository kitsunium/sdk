package session_test

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coresession "github.com/kitsunium/sdk/internal/core/session"
	svcsession "github.com/kitsunium/sdk/internal/service/session"
)

// fileFixture is a file store plus the directory behind it, so a test can look
// at what actually landed on disk.
type fileFixture struct {
	store coresession.Store
	dir   string
	key   corecrypto.Key
}

// newFileFixture builds a file store in a directory it creates, and skips the
// test where the file store honestly refuses to exist.
func newFileFixture(t *testing.T, clk clock.Timed) fileFixture {
	t.Helper()
	dir := storeDir(t)
	key := testKey(t)
	store, err := svcsession.NewFileStore(svcsession.FileConfig{
		IdleTimeout: idleWindow, AbsoluteTimeout: absoluteCeiling, Clock: clk,
		Dir: dir, Key: key,
	})
	if errs.HasReason(err, "UNSUPPORTED_PLATFORM") {
		t.Skip("file store has no native mechanic on this platform")
	}
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return fileFixture{store: store, dir: dir, key: key}
}

// records lists the record files in the fixture's directory.
func (f fileFixture) records(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(f.dir, "*.session"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	return matches
}

// TestTheStoreNarrowsTheDirectoryItCreates pins the first of the file store's
// guarantees. It is asserted against the filesystem rather than against the
// constructor's return, because a mode that was requested and not granted is
// exactly the failure this check exists for.
func TestTheStoreNarrowsTheDirectoryItCreates(t *testing.T) {
	t.Parallel()
	fixture := newFileFixture(t, clock.NewManualClock(origin))
	info, err := os.Stat(fixture.dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("store directory mode = %o, want 700", info.Mode().Perm())
	}
}

// TestAPreExistingPermissiveDirectoryIsRefused pins the other half: a directory
// the operator made is not narrowed behind their back. It might be shared with
// another service, and a silent chmod would be the SDK changing a decision that
// was not its to change — so it is refused instead.
func TestAPreExistingPermissiveDirectoryIsRefused(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "operator-owned")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	//: defeat any default ACL so the mode really is 0755.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	_, err := svcsession.NewFileStore(svcsession.FileConfig{
		IdleTimeout: idleWindow, AbsoluteTimeout: absoluteCeiling,
		Dir: dir, Key: testKey(t),
	})
	if errs.HasReason(err, "UNSUPPORTED_PLATFORM") {
		t.Skip("file store has no native mechanic on this platform")
	}
	if !errs.HasCode(err, svcsession.CodeDirectoryUnsafe) {
		t.Fatalf("NewFileStore(0755 dir) = %v, want CodeDirectoryUnsafe", err)
	}
	//: and it stayed 0755 — the refusal did not quietly repair it.
	info, statErr := os.Stat(dir)
	if statErr != nil {
		t.Fatalf("Stat: %v", statErr)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("the refused directory was chmod'ed to %o", info.Mode().Perm())
	}
}

// TestEveryRecordIsOwnerOnly pins the mode of the files themselves, on disk,
// after a real write.
func TestEveryRecordIsOwnerOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newFileFixture(t, clock.NewManualClock(origin))
	for range 3 {
		if _, err := fixture.store.New(ctx); err != nil {
			t.Fatalf("New: %v", err)
		}
	}
	files := fixture.records(t)
	if len(files) != 3 {
		t.Fatalf("found %d record files, want 3", len(files))
	}
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(path), info.Mode().Perm())
		}
	}
}

// TestNoIdentifierIsEverOnDisk is the guarantee that makes a stolen backup
// useless. Every byte the store wrote — filenames included — is searched for
// the identifier and for the subject.
func TestNoIdentifierIsEverOnDisk(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newFileFixture(t, clock.NewManualClock(origin))
	anon, err := fixture.store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bound, err := fixture.store.Regenerate(ctx, anon.ID(), "alice@example.test")
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if saveErr := fixture.store.Save(ctx, bound.Set("cart", "2 items")); saveErr != nil {
		t.Fatalf("Save: %v", saveErr)
	}
	secrets := []string{bound.ID().Reveal(), "alice@example.test", "cart", "2 items"}
	walkErr := filepath.WalkDir(fixture.dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		//: the filename is the digest, and a digest is not a cookie.
		for _, secret := range secrets {
			if strings.Contains(path, secret) {
				t.Errorf("%q appears in a filename", secret)
			}
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		//: and the contents are an AEAD box, so neither the identifier nor the
		//: subject nor the payload is readable in it.
		for _, secret := range secrets {
			if bytes.Contains(raw, []byte(secret)) {
				t.Errorf("%q appears in %s", secret, filepath.Base(path))
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("WalkDir: %v", walkErr)
	}
	//: and the digest IS the filename, which is what makes lookup possible
	//: without the identifier ever being written down.
	if _, statErr := os.Stat(filepath.Join(fixture.dir, bound.ID().Digest()+".session")); statErr != nil {
		t.Errorf("the record is not filed under its digest: %v", statErr)
	}
}

// TestARecordIsBoundToItsOwnFilename pins the anti-substitution guarantee. An
// attacker with write access to the directory but no key still must not be able
// to make one session's record answer for another's identifier — which is what
// swapping two files would achieve if the seal did not bind the filename.
func TestARecordIsBoundToItsOwnFilename(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newFileFixture(t, clock.NewManualClock(origin))
	first, err := fixture.store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	second, err := fixture.store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	//: the admin's record, moved onto the visitor's filename.
	elevated, err := fixture.store.Regenerate(ctx, second.ID(), "admin")
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	victimPath := filepath.Join(fixture.dir, first.ID().Digest()+".session")
	adminPath := filepath.Join(fixture.dir, elevated.ID().Digest()+".session")
	adminBytes, err := os.ReadFile(adminPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if writeErr := os.WriteFile(victimPath, adminBytes, 0o600); writeErr != nil {
		t.Fatalf("WriteFile: %v", writeErr)
	}
	//: the identifier the attacker already holds now names a file whose
	//: contents say "admin" — and it does not open.
	_, loadErr := fixture.store.Load(ctx, first.ID())
	if !errs.HasCode(loadErr, svcsession.CodeRecordCorrupt) {
		t.Fatalf("Load after a record swap = %v, want CodeRecordCorrupt", loadErr)
	}
}

// TestATamperedOrForeignRecordIsRefused pins that a record which was not
// written by this store, under this key, is not read back.
func TestATamperedOrForeignRecordIsRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		corrupt func(raw []byte) []byte
	}{
		{"a flipped bit", func(raw []byte) []byte {
			out := bytes.Clone(raw)
			out[len(out)/2] ^= 0x01
			return out
		}},
		{"a truncated box", func(raw []byte) []byte { return raw[:len(raw)/2] }},
		{"an empty file", func([]byte) []byte { return nil }},
		{"noise", func(raw []byte) []byte { return bytes.Repeat([]byte{0xAB}, len(raw)) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			fixture := newFileFixture(t, clock.NewManualClock(origin))
			fresh, err := fixture.store.New(ctx)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			path := filepath.Join(fixture.dir, fresh.ID().Digest()+".session")
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("ReadFile: %v", readErr)
			}
			if writeErr := os.WriteFile(path, tc.corrupt(raw), 0o600); writeErr != nil {
				t.Fatalf("WriteFile: %v", writeErr)
			}
			_, loadErr := fixture.store.Load(ctx, fresh.ID())
			//: one verdict for every cause: distinguishing them would tell
			//: whoever arranged this which half of the attempt already worked.
			if !errs.HasCode(loadErr, svcsession.CodeRecordCorrupt) {
				t.Errorf("Load = %v, want CodeRecordCorrupt", loadErr)
			}
		})
	}
}

// TestSessionsSurviveAReopen pins the one thing the memory store cannot do, and
// pins that reopening the store's own directory is not mistaken for the
// operator-owned case the constructor refuses.
func TestSessionsSurviveAReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	manual := clock.NewManualClock(origin)
	fixture := newFileFixture(t, manual)
	bound, err := fixture.store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	bound, err = fixture.store.Regenerate(ctx, bound.ID(), "alice")
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if saveErr := fixture.store.Save(ctx, bound.Set("locale", "fr")); saveErr != nil {
		t.Fatalf("Save: %v", saveErr)
	}
	//: the process ends — and the file store is the one that says so through
	//: io.Closer, which is a capability reached by assertion, not a Store method.
	closer, ok := fixture.store.(io.Closer)
	if !ok {
		t.Fatal("the file store does not implement io.Closer")
	}
	if closeErr := closer.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	//: a new process opens the same directory with the same key.
	reopened, err := svcsession.NewFileStore(svcsession.FileConfig{
		IdleTimeout: idleWindow, AbsoluteTimeout: absoluteCeiling, Clock: manual,
		Dir: fixture.dir, Key: fixture.key,
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	loaded, loadErr := reopened.Load(ctx, bound.ID())
	if loadErr != nil {
		t.Fatalf("Load after reopen: %v", loadErr)
	}
	if loaded.Subject() != "alice" {
		t.Errorf("Subject after reopen = %q, want alice", loaded.Subject())
	}
	if value, _ := loaded.Get("locale"); value != "fr" {
		t.Errorf("locale after reopen = %q, want fr", value)
	}
}

// TestAnotherKeyReadsNothing pins that the records are the store key's to read.
// Rotating the key therefore logs everyone out, which is a documented
// consequence rather than a surprise.
func TestAnotherKeyReadsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	manual := clock.NewManualClock(origin)
	fixture := newFileFixture(t, manual)
	fresh, err := fixture.store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	otherRaw := make([]byte, corecrypto.KeyLen)
	for i := range otherRaw {
		otherRaw[i] = byte(255 - i)
	}
	otherKey, keyErr := corecrypto.NewKey(otherRaw)
	if keyErr != nil {
		t.Fatalf("NewKey: %v", keyErr)
	}
	intruder, err := svcsession.NewFileStore(svcsession.FileConfig{
		IdleTimeout: idleWindow, AbsoluteTimeout: absoluteCeiling, Clock: manual,
		Dir: fixture.dir, Key: otherKey,
	})
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	//: the identifier is right; the key is not.
	if _, loadErr := intruder.Load(ctx, fresh.ID()); !errs.HasCode(loadErr, svcsession.CodeRecordCorrupt) {
		t.Errorf("Load under another key = %v, want CodeRecordCorrupt", loadErr)
	}
}

// TestAFailedPublishLeavesThePreviousRecord is the atomic-publication invariant
// checked FOR REAL rather than assumed because the code looks right: the write
// is made to fail, and the record that was there before is read back byte for
// byte afterwards.
func TestAFailedPublishLeavesThePreviousRecord(t *testing.T) {
	t.Parallel()
	//: root ignores the write bit, so the failure cannot be provoked this way.
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory does not refuse a write")
	}
	ctx := context.Background()
	fixture := newFileFixture(t, clock.NewManualClock(origin))
	fresh, err := fixture.store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if saveErr := fixture.store.Save(ctx, fresh.Set("state", "good")); saveErr != nil {
		t.Fatalf("Save: %v", saveErr)
	}
	path := filepath.Join(fixture.dir, fresh.ID().Digest()+".session")
	before, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	//: the directory becomes unwritable mid-flight — a full disk, a quota, a
	//: read-only remount all look like this from here.
	if chmodErr := os.Chmod(fixture.dir, 0o500); chmodErr != nil {
		t.Fatalf("Chmod: %v", chmodErr)
	}
	saveErr := fixture.store.Save(ctx, fresh.Set("state", "clobbered"))
	if !errs.HasCode(saveErr, coresession.CodeStoreUnavailable) {
		t.Fatalf("Save into a read-only directory = %v, want CodeStoreUnavailable", saveErr)
	}
	if chmodErr := os.Chmod(fixture.dir, 0o700); chmodErr != nil {
		t.Fatalf("Chmod: %v", chmodErr)
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	//: byte for byte. A failed write that had truncated the file, or published
	//: an empty one, would differ here.
	if !bytes.Equal(before, after) {
		t.Error("the failed write changed the record on disk")
	}
	loaded, loadErr := fixture.store.Load(ctx, fresh.ID())
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if value, _ := loaded.Get("state"); value != "good" {
		t.Errorf("state = %q, want the pre-failure value", value)
	}
	//: and no temporary file was left behind.
	if leftovers := orphans(t, fixture.dir); len(leftovers) != 0 {
		t.Errorf("a failed publish left %d temporary files behind", len(leftovers))
	}
}

// TestARenameOntoADirectoryLeavesNothingBehind puts a non-empty directory
// where the record file belongs and pins that Save then fails, reports it, and
// leaves no orphan behind.
//
// It does NOT reach the rename, despite the name it was given: Save reads the
// record first, and reading a directory fails ("is a directory", op=read)
// before publish ever creates a temporary — so its "no orphan" is true of a
// publication that never started. Removing publish's orphan cleanup leaves it
// green. The rename failure itself, over a temporary that really was written,
// synced and closed, is TestAFailedRenameLeavesNoOrphan in
// dirsync_internal_test.go, which calls publish directly.
func TestARenameOntoADirectoryLeavesNothingBehind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newFileFixture(t, clock.NewManualClock(origin))
	fresh, err := fixture.store.New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	path := filepath.Join(fixture.dir, fresh.ID().Digest()+".session")
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("ReadFile: %v", readErr)
	}
	if removeErr := os.Remove(path); removeErr != nil {
		t.Fatalf("Remove: %v", removeErr)
	}
	//: a NON-EMPTY directory in the record's place. The record's own bytes go
	//: inside it, so Save's read step still finds nothing and the failure is
	//: the publication, not the lookup.
	if mkErr := os.Mkdir(path, 0o700); mkErr != nil {
		t.Fatalf("Mkdir: %v", mkErr)
	}
	if writeErr := os.WriteFile(filepath.Join(path, "occupied"), raw, 0o600); writeErr != nil {
		t.Fatalf("WriteFile: %v", writeErr)
	}
	if saveErr := fixture.store.Save(ctx, fresh.Set("state", "doomed")); saveErr == nil {
		t.Fatal("Save succeeded against a directory in the record's place")
	}
	if leftovers := orphans(t, fixture.dir); len(leftovers) != 0 {
		t.Errorf("a failed publish left %d temporary files behind", len(leftovers))
	}
}

// TestSweepLeavesAForeignFileAlone pins that Sweep deletes only files that are
// records by NAME, not every file it cannot read.
//
// A sweep removes an unreadable record — it can never be loaded again — so
// what counts as a record file decides what a sweep may delete. The test used
// to be a suffix and a 64-character stem, while ID.Digest only ever produces
// 64 LOWERCASE HEX characters: any other file of that shape in the directory,
// being unreadable as a session, was swept. The directory is the store's own
// 0700 one, which makes this rare rather than impossible. A genuinely dead
// record is swept beside them, so the test cannot pass by a sweep that
// deletes nothing.
//
// Mutation: reducing isDigest to the length check — recognition by suffix and
// length alone, as before — failed with `Sweep removed 3, want 1 — the dead
// record and nothing else` and `a foreign file was swept: "zzzz…zzzz.session"`.
// Reverting recordDigest alone does NOT fail it, and should not: recordPath
// applies the same test, so the unlink is refused anyway.
func TestSweepLeavesAForeignFileAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	manual := clock.NewManualClock(origin)
	fixture := newFileFixture(t, manual)
	if _, err := fixture.store.New(ctx); err != nil {
		t.Fatalf("New: %v", err)
	}
	manual.Advance(idleWindow + time.Minute)
	//: right suffix, right length, not a digest: not hex, and hex in the one
	//: case Digest never writes.
	foreign := []string{
		strings.Repeat("z", 64) + ".session",
		strings.Repeat("AB", 32) + ".session",
	}
	for _, name := range foreign {
		if err := os.WriteFile(filepath.Join(fixture.dir, name), []byte("not a session"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	sweeper, ok := fixture.store.(coresession.Sweeper)
	if !ok {
		t.Fatal("the file store does not implement Sweeper")
	}
	removed, err := sweeper.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if removed != 1 {
		t.Errorf("Sweep removed %d, want 1 — the dead record and nothing else", removed)
	}
	for _, name := range foreign {
		if content, readErr := os.ReadFile(filepath.Join(fixture.dir, name)); readErr != nil || string(content) != "not a session" {
			t.Errorf("a foreign file was swept: %q (%v)", name, readErr)
		}
	}
	if left := fixture.records(t); len(left) != len(foreign) {
		t.Errorf("%d *.session files remain, want only the %d foreign ones", len(left), len(foreign))
	}
}

// orphans lists the temporary files the publication path may have left behind.
func orphans(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	return matches
}

// TestACancelledContextIsRefusedBeforeTheLock pins that the file store honours
// its context, and that the refusal is the retryable StoreUnavailable rather
// than a raw context error — every error out of this SDK is typed.
func TestACancelledContextIsRefusedBeforeTheLock(t *testing.T) {
	t.Parallel()
	fixture := newFileFixture(t, clock.NewManualClock(origin))
	fresh, err := fixture.store.New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, loadErr := fixture.store.Load(cancelled, fresh.ID()); !errs.HasCode(loadErr, coresession.CodeStoreUnavailable) {
		t.Errorf("Load(cancelled) = %v, want CodeStoreUnavailable", loadErr)
	}
	if destroyErr := fixture.store.Destroy(cancelled, fresh.ID()); !errs.HasCode(destroyErr, coresession.CodeStoreUnavailable) {
		t.Errorf("Destroy(cancelled) = %v, want CodeStoreUnavailable", destroyErr)
	}
	//: the memory store does NOT honour it, because it never blocks: nothing a
	//: cancellation could interrupt exists there, and inventing a failure it
	//: cannot have would make the two stores disagree for no reason.
	memory := newMemory(t, clock.NewManualClock(origin))
	held, err := memory.New(context.Background())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, loadErr := memory.Load(cancelled, held.ID()); loadErr != nil {
		t.Errorf("memory Load(cancelled) = %v, want nil", loadErr)
	}
}
