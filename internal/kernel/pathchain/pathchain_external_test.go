package pathchain_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/pathchain"
)

// resolved renders path the way [pathchain.Resolve] will report it: with every
// indirection ALREADY followed.
//
// It exists because of a measurement rather than a theory. The first run of
// this file on the `macos-arm64` job of e2e-cross failed four rows, all of them
// this shape:
//
//	the last described step = "/private/var/folders/…/001"
//	                    want "/var/folders/…/001"
//
// macOS ships /var as a symbolic link to /private/var, so every t.TempDir() on
// that kernel sits under one. The code was right and the assertions were
// Linux-only: StepValue.Path is documented to reflect the indirections already
// followed, because a caller deciding who could have replaced a component has
// to ask about the directory it actually lives in.
//
// That failure is also the best evidence this package has for the rule its
// first consumer applies. A blanket "refuse any link above the lock file"
// would refuse every lock directory on macOS — and the accepting rows of
// internal/service/lock's chain table passed on that same run, because the
// directory holding /var is / and nobody but root can write it.
func resolved(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s) = %v", path, err)
	}
	return real
}

// plant creates a symbolic link at link pointing at target, or skips the test
// when this platform will not let an unprivileged account create one.
//
// Windows needs SeCreateSymbolicLinkPrivilege, which an account holds only
// with Developer Mode on or an elevated token. A skip names the reason; a
// silently absent assertion would not.
func plant(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create a symbolic link on this platform: %v", err)
	}
}

// TestResolveDescribesEveryComponent pins the shape of the answer: one step
// per component, in order, each naming where it actually lives.
func TestResolveDescribesEveryComponent(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	deep := filepath.Join(base, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatalf("building the tree = %v", err)
	}
	steps, err := pathchain.Resolve(deep)
	if err != nil {
		t.Fatalf("Resolve(%s) = %v", deep, err)
	}
	//: the last three steps are the three components built above, and their
	//: Paths are where those components actually live — which on macOS is not
	//: where the test typed them, because /var is a symbolic link there.
	real := resolved(t, base)
	want := []string{filepath.Join(real, "a"), filepath.Join(real, "a", "b"), filepath.Join(real, "a", "b", "c")}
	if len(steps) < len(want) {
		t.Fatalf("Resolve returned %d steps, want at least %d", len(steps), len(want))
	}
	tail := steps[len(steps)-len(want):]
	for i, expected := range want {
		//: a step describing the wrong path makes every verdict built on it
		//: meaningless, so this is asserted before anything else.
		if tail[i].Path != expected {
			t.Fatalf("step %d path = %q, want %q", i, tail[i].Path, expected)
		}
		//: every component here is a plain directory.
		if tail[i].Indirect {
			t.Fatalf("step %d (%s) was reported as an indirection", i, tail[i].Path)
		}
	}
	//: the container of the last component is the directory above it, so its
	//: mode is that directory's and not the component's own.
	if !tail[len(tail)-1].Container.IsDir() {
		t.Fatalf("the last step's container = %v, want a directory", tail[len(tail)-1].Container)
	}
}

// TestResolveReportsAnIndirectionAndWhereItWent is the exposure's own shape: a
// component that is a link, described rather than traversed silently.
func TestResolveReportsAnIndirectionAndWhereItWent(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	target := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(filepath.Join(target, "locks"), 0o700); err != nil {
		t.Fatalf("building the target = %v", err)
	}
	pub := filepath.Join(base, "pub")
	if err := os.Mkdir(pub, 0o700); err != nil {
		t.Fatalf("building the container = %v", err)
	}
	link := filepath.Join(pub, "myapp")
	plant(t, target, link)

	steps, err := pathchain.Resolve(filepath.Join(link, "locks"))
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	//: the component lives where the walk found it, which is not where the
	//: test typed it on a kernel whose /var is a symbolic link.
	real := filepath.Join(resolved(t, pub), "myapp")
	var found *pathchain.StepValue
	//: locate the planted component among the steps rather than assuming its
	//: index, which depends on how deep t.TempDir() happens to be.
	for i := range steps {
		if steps[i].Path == real {
			found = &steps[i]
		}
	}
	if found == nil {
		t.Fatalf("no step described %s; got %d steps", real, len(steps))
	}
	//: the component is an indirection.
	if !found.Indirect {
		t.Fatalf("step %s Indirect = false, want true", found.Path)
	}
	//: and it says where it goes — the target EXACTLY as the filesystem stores
	//: it, unresolved, which is what os.Readlink returns and what an operator
	//: reading a refusal needs to compare against the link they can see.
	if found.Target != target {
		t.Fatalf("step %s Target = %q, want %q", found.Path, found.Target, target)
	}
	//: resolution CONTINUED at the target, so the step after the link lives
	//: there and not under the link's own name.
	last := steps[len(steps)-1]
	wantLast := filepath.Join(resolved(t, target), "locks")
	if last.Path != wantLast {
		t.Fatalf("the last step = %q, want %q", last.Path, wantLast)
	}
}

// TestResolveStopsWhereThePathStopsExisting pins the decision that a missing
// component is an answer and not a fault.
func TestResolveStopsWhereThePathStopsExisting(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	steps, err := pathchain.Resolve(filepath.Join(base, "not", "there", "at", "all"))
	//: the caller audits a directory it is about to create, so "the last four
	//: components do not exist yet" is the ordinary case.
	if err != nil {
		t.Fatalf("Resolve of a missing tail = %v, want nil", err)
	}
	//: and what DOES exist is still described.
	if len(steps) == 0 {
		t.Fatalf("Resolve of a missing tail returned no steps at all")
	}
	real := resolved(t, base)
	if steps[len(steps)-1].Path != real {
		t.Fatalf("the last described step = %q, want %q", steps[len(steps)-1].Path, real)
	}
}

// TestResolveRefusesAnIndirectionLoop pins the bound, and pins that it is
// reported as the same errno a kernel reports for the same condition.
func TestResolveRefusesAnIndirectionLoop(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	first, second := filepath.Join(base, "a"), filepath.Join(base, "b")
	plant(t, second, first)
	plant(t, first, second)
	_, err := pathchain.Resolve(first)
	//: without the bound this call does not return at all, so reaching this
	//: line is half the assertion.
	if err == nil {
		t.Fatalf("Resolve of a loop = nil, want the kernel's own refusal")
	}
	//: and the refusal is the kernel's, on the path the caller gave — which
	//: is what makes the errno right on every platform instead of on the one
	//: this was written on.
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Path != first {
		t.Fatalf("Resolve of a loop = %v, want a *os.PathError naming %s", err, first)
	}
}

// TestResolveFollowsARelativeTargetFromTheLinksOwnDirectory pins the half of
// symbolic-link resolution that is easy to get wrong: a relative target is
// relative to the LINK, not to the process's working directory.
func TestResolveFollowsARelativeTargetFromTheLinksOwnDirectory(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "real"), 0o700); err != nil {
		t.Fatalf("building the target = %v", err)
	}
	if err := os.Mkdir(filepath.Join(base, "pub"), 0o700); err != nil {
		t.Fatalf("building the container = %v", err)
	}
	plant(t, filepath.Join("..", "real"), filepath.Join(base, "pub", "rel"))
	steps, err := pathchain.Resolve(filepath.Join(base, "pub", "rel"))
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	last := steps[len(steps)-1]
	want := filepath.Join(resolved(t, base), "real")
	//: the walk ascended out of pub and came back down into real, which only
	//: works if ".." is a movement through the handles the walk still holds.
	if last.Path != want {
		t.Fatalf("the last step = %q, want %q", last.Path, want)
	}
}

// TestResolveReportsAFileInTheMiddleOfAPath pins that a component which is not
// a directory ends the walk with the filesystem's own error rather than
// silently resolving the rest against the wrong directory.
func TestResolveReportsAFileInTheMiddleOfAPath(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	regular := filepath.Join(base, "file")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatalf("building the file = %v", err)
	}
	//: a file as the LAST component is legitimate — it is what a lock file is.
	if _, err := pathchain.Resolve(regular); err != nil {
		t.Fatalf("Resolve of a trailing file = %v, want nil", err)
	}
	//: a file in the MIDDLE is not, and the components after it would be
	//: described against the directory above rather than refused.
	if _, err := pathchain.Resolve(filepath.Join(regular, "below")); err == nil {
		t.Fatalf("Resolve through a file = nil, want a failure")
	}
}
