package vfs_test

import (
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/vfs"
)

// TestTheFacadePublishesAliasesAndNotCopies is the ADR 0039 check at the
// public edge. Every type here must be an ALIAS, so a value crossing between
// pkg/v1 and internal/* needs no conversion — and so widening one of them
// breaks the SDK's own build before it breaks a consumer's.
func TestTheFacadePublishesAliasesAndNotCopies(t *testing.T) {
	t.Parallel()
	if reflect.TypeFor[vfs.FS]() != reflect.TypeFor[fs.FS]() {
		t.Error("vfs.FS is not io/fs.FS — fs.WalkDir and fs.Glob stop applying to it")
	}
	const wantWritable int = 5
	if got := reflect.TypeFor[vfs.WritableFS]().NumMethod(); got != wantWritable {
		t.Errorf("WritableFS has %d methods, want %d — a published port must not widen", got, wantWritable)
	}
	const wantAtomic int = 1
	if got := reflect.TypeFor[vfs.AtomicWriter]().NumMethod(); got != wantAtomic {
		t.Errorf("AtomicWriter has %d methods, want %d", got, wantAtomic)
	}
	const wantFull int = 6
	if got := reflect.TypeFor[vfs.FullFS]().NumMethod(); got != wantFull {
		t.Errorf("FullFS has %d methods, want %d", got, wantFull)
	}
}

// TestTheQuickstartInThePackageDocActuallyRuns exercises the exact sequence
// the doc comment shows a consumer, against the memory filesystem so the test
// needs nothing from the host.
func TestTheQuickstartInThePackageDocActuallyRuns(t *testing.T) {
	t.Parallel()
	site := vfs.NewMem()
	if err := site.MkdirAll("posts", 0o755); err != nil {
		t.Fatalf("MkdirAll = %v, want nil", err)
	}
	for _, name := range []string{"posts/first.md", "posts/second.md"} {
		if err := site.WriteAtomic(name, []byte("# a post"), 0o644); err != nil {
			t.Fatalf("WriteAtomic(%q) = %v, want nil", name, err)
		}
	}
	pages, globErr := fs.Glob(site, "posts/*.md")
	if globErr != nil {
		t.Fatalf("fs.Glob = %v, want nil", globErr)
	}
	if len(pages) != 2 {
		t.Fatalf("fs.Glob = %v, want two posts", pages)
	}
}

// TestARefusalCarriesBothIdentities pins the interoperability promise at the
// public edge, and it is the reason a consumer can adopt this package one call
// at a time: the same error answers the SDK's sentinel AND the io/fs one it
// already had.
//
// The first half is not free. The service layer wraps the operating system's
// *fs.PathError as the CAUSE and restates the sentinel's code and reason
// inline, so `errors.Is(err, vfs.ReadFailed)` matches on code+reason while
// `errors.Is(err, fs.ErrNotExist)` still walks through to the cause. Break
// either and one of these two lines fails.
func TestARefusalCarriesBothIdentities(t *testing.T) {
	t.Parallel()
	filesystem := vfs.NewMem()
	_, openErr := filesystem.Open("absent.txt")
	if !errors.Is(openErr, vfs.ReadFailed) {
		t.Errorf("Open = %v, want it to match vfs.ReadFailed", openErr)
	}
	if !errors.Is(openErr, fs.ErrNotExist) {
		t.Errorf("Open = %v, want it to still match fs.ErrNotExist", openErr)
	}
	permErr := filesystem.WriteFile("x.txt", []byte("x"), 0)
	if !errors.Is(permErr, vfs.InvalidPermission) {
		t.Errorf("WriteFile with a zero mode = %v, want vfs.InvalidPermission", permErr)
	}
	if !errs.HasReason(permErr, "INVALID_PERMISSION") {
		t.Errorf("WriteFile with a zero mode = %v, want the INVALID_PERMISSION reason", permErr)
	}
}

// TestTheDiskConstructorIsReachableFromTheFacade closes the one gap a facade
// can have that its aliases cannot show: NewOS is the only symbol here that
// does real work rather than re-exporting a type, and a delegation wired to the
// wrong constructor — or to none — type-checks perfectly.
//
// It exercises the headline verb end to end at the public edge, so the package
// that consumers actually import is the one proven to publish a file.
func TestTheDiskConstructorIsReachableFromTheFacade(t *testing.T) {
	t.Parallel()
	site, newErr := vfs.NewOS(t.TempDir())
	if newErr != nil {
		//: ADR 0018 — the refusal is the correct answer off the native
		//: platforms, and it is itself checked by the service-layer suite.
		t.Skipf("NewOS is unavailable on %s: %v", runtime.GOOS, newErr)
	}
	page := []byte("<h1>published through pkg/v1</h1>")
	if err := site.WriteAtomic("index.html", page, 0o644); err != nil {
		t.Fatalf("WriteAtomic = %v, want nil", err)
	}
	got, readErr := fs.ReadFile(site, "index.html")
	if readErr != nil {
		t.Fatalf("fs.ReadFile = %v, want nil", readErr)
	}
	if string(got) != string(page) {
		t.Errorf("read back %q, want %q", got, page)
	}
}

// TestARootThatIsNotThereIsRefusedByTheFacade pins the other half of NewOS: a
// construction-time refusal, carrying BOTH the SDK sentinel and the io/fs
// answer a caller already knows how to ask for.
func TestARootThatIsNotThereIsRefusedByTheFacade(t *testing.T) {
	t.Parallel()
	absent := filepath.Join(t.TempDir(), "no-such-directory")
	filesystem, newErr := vfs.NewOS(absent)
	if filesystem != nil {
		t.Fatal("NewOS returned a filesystem for a root that does not exist")
	}
	if runtime.GOOS != "windows" && !errors.Is(newErr, vfs.RootUnavailable) {
		t.Errorf("NewOS = %v, want vfs.RootUnavailable", newErr)
	}
	if runtime.GOOS != "windows" && !errors.Is(newErr, fs.ErrNotExist) {
		t.Errorf("NewOS = %v, want the cause to still match fs.ErrNotExist", newErr)
	}
}
