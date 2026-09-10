package vfs_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcvfs "github.com/kitsunium/sdk/internal/service/vfs"
)

// nativePlatforms is the set of GOOS on which NewOS is expected to succeed. It
// is written out rather than derived from the build tag on purpose: a table
// that mirrors the constraint is a second opinion, and it fails when someone
// widens the constraint without widening the platform matrix in the docs.
var nativePlatforms = map[string]bool{
	"linux": true, "darwin": true, "freebsd": true,
	"openbsd": true, "netbsd": true, "dragonfly": true,
}

// TestTheConstructorRefusesRatherThanPretendingOffItsPlatforms pins ADR 0018's
// contract for this domain. On a GOOS without a flushable directory handle and
// without POSIX permission semantics, NewOS returns the shared typed sentinel
// at CONSTRUCTION — where the program is wired — instead of a filesystem that
// would report success for a durability claim it cannot keep.
//
// On a native platform the same test asserts the opposite, so the case never
// degenerates into "whatever this machine does is correct".
func TestTheConstructorRefusesRatherThanPretendingOffItsPlatforms(t *testing.T) {
	t.Parallel()
	filesystem, err := svcvfs.NewOS(t.TempDir())
	if nativePlatforms[runtime.GOOS] {
		if err != nil {
			t.Fatalf("NewOS on %s = %v, want a filesystem", runtime.GOOS, err)
		}
		if filesystem == nil {
			t.Fatal("NewOS returned no filesystem and no error")
		}
		return
	}
	if !errors.Is(err, coreproc.UnsupportedPlatform) {
		t.Fatalf("NewOS on %s = %v, want UnsupportedPlatform", runtime.GOOS, err)
	}
	if filesystem != nil {
		t.Fatal("NewOS refused and still returned a filesystem")
	}
}

// TestARootThatIsNotThereIsRefusedAtConstruction pins the other half: a
// filesystem whose root does not exist will fail every call it is ever given,
// and the useful place to learn that is where it is built.
func TestARootThatIsNotThereIsRefusedAtConstruction(t *testing.T) {
	t.Parallel()
	if !nativePlatforms[runtime.GOOS] {
		t.Skipf("NewOS refuses %s before it looks at the path", runtime.GOOS)
	}
	_, err := svcvfs.NewOS(filepath.Join(t.TempDir(), "absent"))
	if !kerrs.HasCode(err, svcvfs.CodeRootUnavailable) {
		t.Fatalf("NewOS = %v, want ROOT_UNAVAILABLE", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("NewOS = %v, want the cause to survive as fs.ErrNotExist", err)
	}
}

// TestASymbolicLinkOutOfTheTreeCannotBeReadOrWritten is the escape test, and
// it covers both halves of the guarantee with a link the test plants by hand
// using the real operating system — not through the port, which has no way to
// create one.
//
//   - READING through it is refused by os.Root, in the KERNEL. That is the
//     strong half: it holds against a link planted at any moment, because the
//     resolution itself is confined.
//   - WRITING through it is refused by this package, before the open. os.Root
//     would have followed it and kept the result inside the root, so nothing
//     would have escaped — but the bytes would have landed on the link's
//     target rather than on the name the caller gave.
func TestASymbolicLinkOutOfTheTreeCannotBeReadOrWritten(t *testing.T) {
	t.Parallel()
	if !nativePlatforms[runtime.GOOS] {
		t.Skipf("%s has no NewOS to test", runtime.GOOS)
	}
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	root := filepath.Join(base, "root")
	for _, dir := range []string{outside, root} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("preparing %q: %v", dir, err)
		}
	}
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("the bytes that must not move"), 0o600); err != nil {
		t.Fatalf("preparing the secret: %v", err)
	}
	if err := os.Symlink("../outside/secret", filepath.Join(root, "escape")); err != nil {
		t.Skipf("this platform will not create a symbolic link: %v", err)
	}

	filesystem, newErr := svcvfs.NewOS(root)
	if newErr != nil {
		t.Fatalf("NewOS = %v, want a filesystem", newErr)
	}

	//: reading through the link never reaches the target.
	if _, readErr := fs.ReadFile(filesystem, "escape"); readErr == nil {
		t.Fatal("reading through an escaping symbolic link succeeded")
	}

	//: writing through it is refused with the security verdict, by name.
	writeErr := filesystem.WriteFile("escape", []byte("overwritten"), 0o644)
	if !kerrs.HasCode(writeErr, corevfs.CodePathEscaped) {
		t.Fatalf("WriteFile through a link = %v, want PATH_ESCAPED", writeErr)
	}
	publishErr := filesystem.WriteAtomic("escape", []byte("overwritten"), 0o644)
	if !kerrs.HasCode(publishErr, corevfs.CodePathEscaped) {
		t.Fatalf("WriteAtomic through a link = %v, want PATH_ESCAPED", publishErr)
	}

	//: and the file outside the tree is byte-for-byte what it was.
	survived, readErr := os.ReadFile(filepath.Clean(secret))
	if readErr != nil {
		t.Fatalf("re-reading the secret: %v", readErr)
	}
	if string(survived) != "the bytes that must not move" {
		t.Fatalf("the file outside the root was modified: %q", survived)
	}
}

// TestALinkInsideTheTreeIsRefusedToo covers the case people forget. This link
// points at a perfectly ordinary file INSIDE the root, so os.Root would follow
// it and confine nothing, and no escape happens at all — but a caller that
// asked to write `config.json` would have written `passwords.json`. That is
// the symlink plant, and confinement does not address it.
func TestALinkInsideTheTreeIsRefusedToo(t *testing.T) {
	t.Parallel()
	if !nativePlatforms[runtime.GOOS] {
		t.Skipf("%s has no NewOS to test", runtime.GOOS)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "real.txt"), []byte("original"), 0o600); err != nil {
		t.Fatalf("preparing: %v", err)
	}
	if err := os.Symlink("real.txt", filepath.Join(root, "alias.txt")); err != nil {
		t.Skipf("this platform will not create a symbolic link: %v", err)
	}
	filesystem, newErr := svcvfs.NewOS(root)
	if newErr != nil {
		t.Fatalf("NewOS = %v, want a filesystem", newErr)
	}
	if err := filesystem.WriteFile("alias.txt", []byte("hijacked"), 0o644); !kerrs.HasCode(err, corevfs.CodePathEscaped) {
		t.Fatalf("WriteFile through an internal link = %v, want PATH_ESCAPED", err)
	}
	survived, readErr := os.ReadFile(filepath.Join(root, "real.txt"))
	if readErr != nil {
		t.Fatalf("re-reading: %v", readErr)
	}
	if string(survived) != "original" {
		t.Fatalf("the link's target was rewritten: %q", survived)
	}
}

// TestARealKernelRefusalLeavesThePreviousBytesIntact is the injection-free
// companion to publish_internal_test.go.
//
// The failure here is the kernel's own: the parent directory is stripped of
// its write bit, so creating the temporary genuinely fails with EACCES. No
// double, no substituted mechanic — and the destination must still hash to
// what it hashed before.
//
// It SKIPS under euid 0, because root bypasses the permission check and the
// call would succeed. That skip is not an exclusion in rule 12's sense: the
// same guarantee is covered unconditionally by the injected-failure table,
// which runs on every lane. This test adds a second, independent witness where
// the environment allows one.
func TestARealKernelRefusalLeavesThePreviousBytesIntact(t *testing.T) {
	t.Parallel()
	if !nativePlatforms[runtime.GOOS] {
		t.Skipf("%s has no NewOS to test", runtime.GOOS)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: the write bit is not enforced, so no failure can be provoked here")
	}
	root := t.TempDir()
	filesystem, newErr := svcvfs.NewOS(root)
	if newErr != nil {
		t.Fatalf("NewOS = %v, want a filesystem", newErr)
	}
	if err := filesystem.MkdirAll("published", 0o755); err != nil {
		t.Fatalf("MkdirAll = %v, want nil", err)
	}
	const target = "published/index.html"
	previous := []byte("<h1>still being served</h1>")
	if err := filesystem.WriteAtomic(target, previous, 0o644); err != nil {
		t.Fatalf("seeding = %v, want nil", err)
	}

	sealed := filepath.Join(root, "published")
	if err := os.Chmod(sealed, 0o555); err != nil {
		t.Fatalf("sealing the directory: %v", err)
	}
	t.Cleanup(func() {
		//: restored so t.TempDir's own cleanup can descend into it; a failure
		//: here would otherwise surface as an unrelated teardown error.
		if chmodErr := os.Chmod(sealed, 0o755); chmodErr != nil {
			t.Errorf("restoring %q: %v", sealed, chmodErr)
		}
	})

	publishErr := filesystem.WriteAtomic(target, []byte("<h1>the replacement</h1>"), 0o644)
	if !kerrs.HasCode(publishErr, corevfs.CodePublishFailed) {
		t.Fatalf("WriteAtomic into a read-only directory = %v, want PUBLISH_FAILED", publishErr)
	}
	if !errors.Is(publishErr, fs.ErrPermission) {
		t.Errorf("the kernel's cause did not survive: %v", publishErr)
	}
	survived, readErr := os.ReadFile(filepath.Join(root, target))
	if readErr != nil {
		t.Fatalf("re-reading the target: %v", readErr)
	}
	if string(survived) != string(previous) {
		t.Fatalf("content = %q, want the previous bytes %q", survived, previous)
	}
}

// TestTheDiskFilesystemPublishesAtomicallyUnderConcurrentReaders is the
// property WriteFile does not have, observed rather than asserted: while one
// goroutine republishes a file repeatedly, a reader must only ever see a
// COMPLETE version — never a prefix, never an empty file.
func TestTheDiskFilesystemPublishesAtomicallyUnderConcurrentReaders(t *testing.T) {
	t.Parallel()
	if !nativePlatforms[runtime.GOOS] {
		t.Skipf("%s has no NewOS to test", runtime.GOOS)
	}
	filesystem, newErr := svcvfs.NewOS(t.TempDir())
	if newErr != nil {
		t.Fatalf("NewOS = %v, want a filesystem", newErr)
	}
	const name = "feed.json"
	versions := [][]byte{
		[]byte(`{"v":1,"payload":"` + strings.Repeat("a", 20000) + `"}`),
		[]byte(`{"v":2,"payload":"` + strings.Repeat("b", 20000) + `"}`),
	}
	if err := filesystem.WriteAtomic(name, versions[0], 0o644); err != nil {
		t.Fatalf("seeding = %v, want nil", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			if err := filesystem.WriteAtomic(name, versions[i%2], 0o644); err != nil {
				t.Errorf("WriteAtomic = %v, want nil", err)
				return
			}
		}
	}()
	for range 400 {
		got, readErr := fs.ReadFile(filesystem, name)
		if readErr != nil {
			t.Fatalf("fs.ReadFile = %v, want nil — a published name must always resolve", readErr)
		}
		if string(got) != string(versions[0]) && string(got) != string(versions[1]) {
			t.Fatalf("a reader saw %d bytes that are neither version — publication was not atomic", len(got))
		}
	}
	<-done
}
