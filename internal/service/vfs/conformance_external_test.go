package vfs_test

import (
	"errors"
	"io/fs"
	"testing"

	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcvfs "github.com/kitsunium/sdk/internal/service/vfs"
)

// factory builds one filesystem under test.
type factory struct {
	name string
	make func(t *testing.T) corevfs.FullFS
}

// implementations is the whole argument for having two of them. A memory
// filesystem is only usable as a test double if it refuses what the real one
// refuses, so every case below runs against BOTH and the table has no
// per-implementation exceptions. Where a difference is genuine — symbolic
// links, durability, modification times — it lives in its own named test with
// the reason attached, not in a branch here.
var implementations = []factory{
	{name: "disk", make: func(t *testing.T) corevfs.FullFS {
		t.Helper()
		filesystem, err := svcvfs.NewOS(t.TempDir())
		if err != nil {
			t.Fatalf("NewOS = %v, want a filesystem", err)
		}
		return filesystem
	}},
	{name: "memory", make: func(t *testing.T) corevfs.FullFS {
		t.Helper()
		return svcvfs.NewMem()
	}},
}

// eachImplementation runs body against every filesystem.
func eachImplementation(t *testing.T, body func(t *testing.T, filesystem corevfs.FullFS)) {
	t.Helper()
	for _, impl := range implementations {
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			body(t, impl.make(t))
		})
	}
}

// TestAWrittenFileReadsBackThroughIoFS is the baseline: what went in comes out,
// and it comes out through the STANDARD LIBRARY's readers rather than through
// a method this SDK invented.
func TestAWrittenFileReadsBackThroughIoFS(t *testing.T) {
	t.Parallel()
	eachImplementation(t, func(t *testing.T, filesystem corevfs.FullFS) {
		payload := []byte("hello, filesystem")
		if err := filesystem.WriteFile("greeting.txt", payload, 0o644); err != nil {
			t.Fatalf("WriteFile = %v, want nil", err)
		}
		got, readErr := fs.ReadFile(filesystem, "greeting.txt")
		if readErr != nil {
			t.Fatalf("fs.ReadFile = %v, want nil", readErr)
		}
		if string(got) != string(payload) {
			t.Fatalf("content = %q, want %q", got, payload)
		}
	})
}

// TestTheStdlibWalkersWorkUnchanged is the executable form of the claim that
// this SDK does not ship a Walk or a Glob. Both functions come from io/fs and
// are handed the port directly.
func TestTheStdlibWalkersWorkUnchanged(t *testing.T) {
	t.Parallel()
	eachImplementation(t, func(t *testing.T, filesystem corevfs.FullFS) {
		if err := filesystem.MkdirAll("assets/css", 0o755); err != nil {
			t.Fatalf("MkdirAll = %v, want nil", err)
		}
		for _, name := range []string{"index.html", "assets/css/site.css", "assets/css/print.css"} {
			if err := filesystem.WriteFile(name, []byte("x"), 0o644); err != nil {
				t.Fatalf("WriteFile(%q) = %v, want nil", name, err)
			}
		}
		var walked []string
		walkErr := fs.WalkDir(filesystem, ".", func(p string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			walked = append(walked, p)
			return nil
		})
		if walkErr != nil {
			t.Fatalf("fs.WalkDir = %v, want nil", walkErr)
		}
		want := []string{".", "assets", "assets/css", "assets/css/print.css", "assets/css/site.css", "index.html"}
		if len(walked) != len(want) {
			t.Fatalf("fs.WalkDir visited %v, want %v", walked, want)
		}
		for i := range want {
			if walked[i] != want[i] {
				t.Fatalf("fs.WalkDir visited %v, want %v", walked, want)
			}
		}
		matches, globErr := fs.Glob(filesystem, "assets/css/*.css")
		if globErr != nil {
			t.Fatalf("fs.Glob = %v, want nil", globErr)
		}
		if len(matches) != 2 {
			t.Fatalf("fs.Glob = %v, want two stylesheets", matches)
		}
	})
}

// TestAnAbsentFileStillAnswersErrorsIsNotExist pins the interoperability rule
// that makes this domain adoptable: wrapping the cause rather than replacing
// it, so `errors.Is(err, fs.ErrNotExist)` — the sentence every Go program that
// touches files already contains — keeps working alongside the dotted-quad
// code.
func TestAnAbsentFileStillAnswersErrorsIsNotExist(t *testing.T) {
	t.Parallel()
	eachImplementation(t, func(t *testing.T, filesystem corevfs.FullFS) {
		_, openErr := filesystem.Open("nowhere.txt")
		if !errors.Is(openErr, fs.ErrNotExist) {
			t.Errorf("Open = %v, want an error matching fs.ErrNotExist", openErr)
		}
		if !kerrs.HasCode(openErr, corevfs.CodeReadFailed) {
			t.Errorf("Open = %v, want READ_FAILED as well", openErr)
		}
	})
}

// TestEveryEscapeAndEveryZeroModeIsRefusedIdentically is the conformance table
// proper: one set of inputs, one set of verdicts, two implementations.
func TestEveryEscapeAndEveryZeroModeIsRefusedIdentically(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call func(filesystem corevfs.FullFS) error
		code kerrs.Code
	}{
		{"a climbing write", func(f corevfs.FullFS) error {
			return f.WriteFile("../escape.txt", []byte("x"), 0o644)
		}, corevfs.CodeInvalidPath},
		{"a climbing publication", func(f corevfs.FullFS) error {
			return f.WriteAtomic("../escape.txt", []byte("x"), 0o644)
		}, corevfs.CodeInvalidPath},
		{"an absolute write", func(f corevfs.FullFS) error {
			return f.WriteFile("/etc/passwd", []byte("x"), 0o644)
		}, corevfs.CodeInvalidPath},
		{"a climb hidden in the middle", func(f corevfs.FullFS) error {
			return f.WriteFile("assets/../../escape.txt", []byte("x"), 0o644)
		}, corevfs.CodeInvalidPath},
		{"removing the root", func(f corevfs.FullFS) error {
			return f.RemoveAll(".")
		}, corevfs.CodeInvalidPath},
		{"removing the root precisely", func(f corevfs.FullFS) error {
			return f.Remove(".")
		}, corevfs.CodeInvalidPath},
		{"a zero mode on a write", func(f corevfs.FullFS) error {
			return f.WriteFile("file.txt", []byte("x"), 0)
		}, corevfs.CodeInvalidPermission},
		{"a zero mode on a publication", func(f corevfs.FullFS) error {
			return f.WriteAtomic("file.txt", []byte("x"), 0)
		}, corevfs.CodeInvalidPermission},
		{"a zero mode on a directory", func(f corevfs.FullFS) error {
			return f.MkdirAll("dir", 0)
		}, corevfs.CodeInvalidPermission},
		{"a setuid mode", func(f corevfs.FullFS) error {
			return f.WriteFile("file.txt", []byte("x"), 0o755|fs.ModeSetuid)
		}, corevfs.CodeInvalidPermission},
		{"writing over a directory", func(f corevfs.FullFS) error {
			if err := f.MkdirAll("occupied", 0o755); err != nil {
				return err
			}
			return f.WriteFile("occupied", []byte("x"), 0o644)
		}, corevfs.CodeNotRegularFile},
		{"publishing over a directory", func(f corevfs.FullFS) error {
			if err := f.MkdirAll("occupied", 0o755); err != nil {
				return err
			}
			return f.WriteAtomic("occupied", []byte("x"), 0o644)
		}, corevfs.CodeNotRegularFile},
		{"creating a directory under a file", func(f corevfs.FullFS) error {
			if err := f.WriteFile("blocker", []byte("x"), 0o644); err != nil {
				return err
			}
			return f.MkdirAll("blocker/child", 0o755)
		}, corevfs.CodeNotRegularFile},
		{"writing under a file", func(f corevfs.FullFS) error {
			if err := f.WriteFile("blocker", []byte("x"), 0o644); err != nil {
				return err
			}
			return f.WriteFile("blocker/child.txt", []byte("x"), 0o644)
		}, corevfs.CodeNotRegularFile},
		{"publishing under a file", func(f corevfs.FullFS) error {
			if err := f.WriteFile("blocker", []byte("x"), 0o644); err != nil {
				return err
			}
			return f.WriteAtomic("blocker/child.txt", []byte("x"), 0o644)
		}, corevfs.CodeNotRegularFile},
		{"removing a populated directory", func(f corevfs.FullFS) error {
			if err := f.MkdirAll("full", 0o755); err != nil {
				return err
			}
			if err := f.WriteFile("full/child.txt", []byte("x"), 0o644); err != nil {
				return err
			}
			return f.Remove("full")
		}, corevfs.CodeDirectoryNotEmpty},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eachImplementation(t, func(t *testing.T, filesystem corevfs.FullFS) {
				if err := tc.call(filesystem); !kerrs.HasCode(err, tc.code) {
					t.Fatalf("got %v, want code %s", err, tc.code)
				}
			})
		})
	}
}

// TestAMissingParentIsNotInventedSilently pins the port's most opinionated
// choice. A typo in a path is far more common than a genuinely absent
// directory, and creating the tree on the caller's behalf is how a file ends
// up somewhere nobody looks — so it is ENOENT, and it is ENOENT identically in
// both implementations.
func TestAMissingParentIsNotInventedSilently(t *testing.T) {
	t.Parallel()
	eachImplementation(t, func(t *testing.T, filesystem corevfs.FullFS) {
		err := filesystem.WriteFile("never/created/file.txt", []byte("x"), 0o644)
		if !kerrs.HasCode(err, corevfs.CodeWriteFailed) {
			t.Fatalf("WriteFile = %v, want WRITE_FAILED", err)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("WriteFile = %v, want an error matching fs.ErrNotExist", err)
		}
	})
}

// TestRemoveIsPreciseAndRemoveAllIsIdempotent pins the asymmetry both
// implementations inherit from os.Remove and os.RemoveAll — a difference
// people rely on without noticing, and one a double must not smooth over.
func TestRemoveIsPreciseAndRemoveAllIsIdempotent(t *testing.T) {
	t.Parallel()
	eachImplementation(t, func(t *testing.T, filesystem corevfs.FullFS) {
		if err := filesystem.Remove("absent.txt"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Remove(absent) = %v, want an error matching fs.ErrNotExist", err)
		}
		if err := filesystem.RemoveAll("absent.txt"); err != nil {
			t.Errorf("RemoveAll(absent) = %v, want nil", err)
		}
		if err := filesystem.MkdirAll("tree/deep", 0o755); err != nil {
			t.Fatalf("MkdirAll = %v, want nil", err)
		}
		if err := filesystem.WriteFile("tree/deep/leaf.txt", []byte("x"), 0o644); err != nil {
			t.Fatalf("WriteFile = %v, want nil", err)
		}
		if err := filesystem.RemoveAll("tree"); err != nil {
			t.Fatalf("RemoveAll(tree) = %v, want nil", err)
		}
		if _, statErr := fs.Stat(filesystem, "tree/deep/leaf.txt"); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("a descendant survived RemoveAll: %v", statErr)
		}
		if _, statErr := fs.Stat(filesystem, "tree"); !errors.Is(statErr, fs.ErrNotExist) {
			t.Errorf("the directory survived RemoveAll: %v", statErr)
		}
	})
}

// TestPublicationReplacesContentWholesale is the success path of the headline
// feature, on both implementations. The failure path — the part that needs
// proving — is disk-only and lives in publish_internal_test.go, because a
// memory filesystem has no half-written state to leave behind.
func TestPublicationReplacesContentWholesale(t *testing.T) {
	t.Parallel()
	eachImplementation(t, func(t *testing.T, filesystem corevfs.FullFS) {
		const name = "index.html"
		if err := filesystem.WriteAtomic(name, []byte("first"), 0o644); err != nil {
			t.Fatalf("WriteAtomic = %v, want nil", err)
		}
		if err := filesystem.WriteAtomic(name, []byte("second"), 0o644); err != nil {
			t.Fatalf("WriteAtomic (replace) = %v, want nil", err)
		}
		got, readErr := fs.ReadFile(filesystem, name)
		if readErr != nil {
			t.Fatalf("fs.ReadFile = %v, want nil", readErr)
		}
		if string(got) != "second" {
			t.Fatalf("content = %q, want %q", got, "second")
		}
	})
}

// TestModeAppliesOnCreationAndNotOnRewrite pins os.WriteFile's rule in both
// implementations: a rewrite must not silently re-open a file someone
// deliberately narrowed.
func TestModeAppliesOnCreationAndNotOnRewrite(t *testing.T) {
	t.Parallel()
	eachImplementation(t, func(t *testing.T, filesystem corevfs.FullFS) {
		const name = "secret.txt"
		if err := filesystem.WriteFile(name, []byte("one"), 0o600); err != nil {
			t.Fatalf("WriteFile = %v, want nil", err)
		}
		if err := filesystem.WriteFile(name, []byte("two"), 0o666); err != nil {
			t.Fatalf("WriteFile (rewrite) = %v, want nil", err)
		}
		info, statErr := fs.Stat(filesystem, name)
		if statErr != nil {
			t.Fatalf("fs.Stat = %v, want nil", statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600 — a rewrite must not widen a file", info.Mode().Perm())
		}
	})
}

// TestBothFilesystemsImplementTheStdlibShortcuts checks that fs.Stat,
// fs.ReadDir and fs.ReadFile take their FAST path rather than falling back
// through Open. The fallbacks are correct, so this is not about behaviour: it
// is about the two implementations offering the same optional interfaces, so a
// consumer that type-asserts for one does not get a different answer depending
// on which filesystem it was handed.
func TestBothFilesystemsImplementTheStdlibShortcuts(t *testing.T) {
	t.Parallel()
	eachImplementation(t, func(t *testing.T, filesystem corevfs.FullFS) {
		if _, ok := filesystem.(fs.StatFS); !ok {
			t.Error("does not implement fs.StatFS")
		}
		if _, ok := filesystem.(fs.ReadDirFS); !ok {
			t.Error("does not implement fs.ReadDirFS")
		}
		if _, ok := filesystem.(fs.ReadFileFS); !ok {
			t.Error("does not implement fs.ReadFileFS")
		}
	})
}
