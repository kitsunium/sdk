package s3

import (
	"errors"
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The rows of internal/service/data/vfs's conformance table, run over the S3
// engine. Until the importable vfstest suite exists (kitsunium/sdk#985), the
// rows are restated here, one function each, and run twice: over the
// in-memory bucket below, in every lane, and over LocalStack under the
// `localstack` tag (localstack_test.go).

// newFS func builds one filesystem under test.
type fsFactory func(t *testing.T) corevfs.FullFS

// fakeFS builds the filesystem over a fresh in-memory bucket, under a prefix
// so every key is checked to carry it.
func fakeFS(t *testing.T) corevfs.FullFS {
	t.Helper()
	return newFS(Config{Bucket: "b", Prefix: "root", MaxKeys: 2, Timeout: time.Minute}, newFakeBucket())
}

// conformanceRows is every row, by name.
var conformanceRows = []struct {
	name string
	run  func(t *testing.T, build fsFactory)
}{
	{"a written file reads back through io/fs", rowWrittenFileReadsBack},
	{"the stdlib walkers work unchanged", rowStdlibWalkers},
	{"an absent file still answers errors.Is NotExist", rowAbsentIsNotExist},
	{"every escape and every zero mode is refused identically", rowRefusals},
	{"a missing parent is not invented silently", rowMissingParent},
	{"Remove is precise and RemoveAll idempotent", rowRemove},
	{"a publication replaces content wholesale", rowPublicationReplaces},
	{"the mode applies on creation and not on rewrite", rowModeOnCreation},
	{"a publication applies its mode every time", rowPublicationMode},
	{"the stdlib shortcuts are implemented", rowShortcuts},
	{"testing/fstest.TestFS accepts the tree", rowTestFS},
	{"SyncDir answers as the memory engine", rowSyncDir},
}

// runConformance runs every row over make.
func runConformance(t *testing.T, build fsFactory) {
	t.Helper()
	for _, row := range conformanceRows {
		t.Run(row.name, func(t *testing.T) { row.run(t, build) })
	}
}

func TestTheFakeBucketPassesTheVfsConformanceRows(t *testing.T) {
	t.Parallel()
	runConformance(t, fakeFS)
}

func mustWrite(t *testing.T, filesystem corevfs.FullFS, name, content string) {
	t.Helper()
	if err := filesystem.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) = %v, want nil", name, err)
	}
}

func rowWrittenFileReadsBack(t *testing.T, build fsFactory) {
	filesystem := build(t)
	mustWrite(t, filesystem, "greeting.txt", "hello, filesystem")
	got, err := fs.ReadFile(filesystem, "greeting.txt")
	if err != nil || string(got) != "hello, filesystem" {
		t.Fatalf("fs.ReadFile = (%q, %v), want the bytes written", got, err)
	}
}

func rowStdlibWalkers(t *testing.T, build fsFactory) {
	filesystem := build(t)
	if err := filesystem.MkdirAll("assets/css", 0o755); err != nil {
		t.Fatalf("MkdirAll = %v, want nil", err)
	}
	for _, name := range []string{"index.html", "assets/css/site.css", "assets/css/print.css"} {
		mustWrite(t, filesystem, name, "x")
	}
	var walked []string
	walkErr := fs.WalkDir(filesystem, ".", func(p string, _ fs.DirEntry, err error) error {
		walked = append(walked, p)
		return err
	})
	want := []string{".", "assets", "assets/css", "assets/css/print.css", "assets/css/site.css", "index.html"}
	if walkErr != nil || !slices.Equal(walked, want) {
		t.Fatalf("fs.WalkDir = (%v, %v), want (%v, nil)", walked, walkErr, want)
	}
	matches, globErr := fs.Glob(filesystem, "assets/css/*.css")
	if globErr != nil || len(matches) != 2 {
		t.Fatalf("fs.Glob = (%v, %v), want two stylesheets", matches, globErr)
	}
}

func rowAbsentIsNotExist(t *testing.T, build fsFactory) {
	filesystem := build(t)
	_, openErr := filesystem.Open("nowhere.txt")
	if !errors.Is(openErr, fs.ErrNotExist) || !errs.HasCode(openErr, corevfs.CodeReadFailed) {
		t.Errorf("Open = %v, want READ_FAILED matching fs.ErrNotExist", openErr)
	}
	_, statErr := fs.Stat(filesystem, "no/such/prefix")
	if !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("Stat(prefix) = %v, want fs.ErrNotExist", statErr)
	}
}

func rowRefusals(t *testing.T, build fsFactory) {
	mkdir := func(f corevfs.FullFS, name string) error { return f.MkdirAll(name, 0o755) }
	write := func(f corevfs.FullFS, name string) error { return f.WriteFile(name, []byte("x"), 0o644) }
	tests := []struct {
		call func(f corevfs.FullFS) error
		name string
		code errs.Code
	}{
		{func(f corevfs.FullFS) error { return write(f, "../escape.txt") }, "a climbing write", corevfs.CodeInvalidPath},
		{func(f corevfs.FullFS) error { return f.WriteAtomic("../escape.txt", nil, 0o644) }, "a climbing publication", corevfs.CodeInvalidPath},
		{func(f corevfs.FullFS) error { return write(f, "/etc/passwd") }, "an absolute write", corevfs.CodeInvalidPath},
		{func(f corevfs.FullFS) error { return write(f, "assets/../../escape.txt") }, "a hidden climb", corevfs.CodeInvalidPath},
		{func(f corevfs.FullFS) error { return f.RemoveAll(".") }, "removing the root", corevfs.CodeInvalidPath},
		{func(f corevfs.FullFS) error { return f.Remove(".") }, "removing the root precisely", corevfs.CodeInvalidPath},
		{func(f corevfs.FullFS) error { return f.WriteFile("file.txt", nil, 0) }, "a zero mode", corevfs.CodeInvalidPermission},
		{func(f corevfs.FullFS) error { return f.WriteAtomic("file.txt", nil, 0) }, "a zero publication mode", corevfs.CodeInvalidPermission},
		{func(f corevfs.FullFS) error { return f.MkdirAll("dir", 0) }, "a zero directory mode", corevfs.CodeInvalidPermission},
		{func(f corevfs.FullFS) error { return f.WriteFile("f", nil, 0o755|fs.ModeSetuid) }, "a setuid mode", corevfs.CodeInvalidPermission},
		{func(f corevfs.FullFS) error {
			return errors.Join(mkdir(f, "occupied"), write(f, "occupied"))
		}, "writing over a directory", corevfs.CodeNotRegularFile},
		{func(f corevfs.FullFS) error {
			return errors.Join(mkdir(f, "occupied"), f.WriteAtomic("occupied", nil, 0o644))
		}, "publishing over a directory", corevfs.CodeNotRegularFile},
		{func(f corevfs.FullFS) error {
			return errors.Join(write(f, "blocker"), mkdir(f, "blocker/child"))
		}, "a directory under a file", corevfs.CodeNotRegularFile},
		{func(f corevfs.FullFS) error {
			return errors.Join(write(f, "blocker"), write(f, "blocker/child.txt"))
		}, "writing under a file", corevfs.CodeNotRegularFile},
		{func(f corevfs.FullFS) error {
			return errors.Join(write(f, "blocker"), f.WriteAtomic("blocker/child.txt", nil, 0o644))
		}, "publishing under a file", corevfs.CodeNotRegularFile},
		{func(f corevfs.FullFS) error {
			return errors.Join(mkdir(f, "full"), write(f, "full/child.txt"), f.Remove("full"))
		}, "removing a populated directory", corevfs.CodeDirectoryNotEmpty},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(build(t)); !errs.HasCode(err, tc.code) {
				t.Fatalf("got %v, want code %s", err, tc.code)
			}
		})
	}
}

func rowMissingParent(t *testing.T, build fsFactory) {
	err := build(t).WriteFile("never/created/file.txt", []byte("x"), 0o644)
	if !errs.HasCode(err, corevfs.CodeWriteFailed) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("WriteFile = %v, want WRITE_FAILED matching fs.ErrNotExist", err)
	}
}

func rowRemove(t *testing.T, build fsFactory) {
	filesystem := build(t)
	if err := filesystem.Remove("absent.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Remove(absent) = %v, want fs.ErrNotExist", err)
	}
	if err := filesystem.RemoveAll("absent.txt"); err != nil {
		t.Errorf("RemoveAll(absent) = %v, want nil", err)
	}
	if err := filesystem.MkdirAll("tree/deep", 0o755); err != nil {
		t.Fatalf("MkdirAll = %v, want nil", err)
	}
	for _, name := range []string{"tree/deep/leaf.txt", "tree/a.txt", "tree/b.txt", "tree/c.txt", "treetop.txt"} {
		mustWrite(t, filesystem, name, "x")
	}
	if err := filesystem.RemoveAll("tree"); err != nil {
		t.Fatalf("RemoveAll(tree) = %v, want nil", err)
	}
	for _, name := range []string{"tree/deep/leaf.txt", "tree/deep", "tree"} {
		if _, err := fs.Stat(filesystem, name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s survived RemoveAll: %v", name, err)
		}
	}
	if _, err := fs.Stat(filesystem, "treetop.txt"); err != nil {
		t.Errorf("RemoveAll(tree) removed its neighbour treetop.txt: %v", err)
	}
	if err := filesystem.MkdirAll("empty", 0o755); err != nil {
		t.Fatalf("MkdirAll(empty) = %v, want nil", err)
	}
	if err := filesystem.Remove("empty"); err != nil {
		t.Fatalf("Remove(empty dir) = %v, want nil", err)
	}
	if _, err := fs.Stat(filesystem, "empty"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an empty directory survived Remove: %v", err)
	}
}

func rowPublicationReplaces(t *testing.T, build fsFactory) {
	filesystem := build(t)
	for _, content := range []string{"first", "second"} {
		if err := filesystem.WriteAtomic("index.html", []byte(content), 0o644); err != nil {
			t.Fatalf("WriteAtomic(%q) = %v, want nil", content, err)
		}
	}
	got, err := fs.ReadFile(filesystem, "index.html")
	if err != nil || string(got) != "second" {
		t.Fatalf("content = (%q, %v), want second", got, err)
	}
}

func rowModeOnCreation(t *testing.T, build fsFactory) {
	filesystem := build(t)
	if err := filesystem.WriteFile("secret.txt", []byte("one"), 0o600); err != nil {
		t.Fatalf("WriteFile = %v, want nil", err)
	}
	if err := filesystem.WriteFile("secret.txt", []byte("two"), 0o666); err != nil {
		t.Fatalf("WriteFile (rewrite) = %v, want nil", err)
	}
	info, err := fs.Stat(filesystem, "secret.txt")
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("Stat = (%v, %v), want mode 0600 — a rewrite must not widen a file", info, err)
	}
}

func rowPublicationMode(t *testing.T, build fsFactory) {
	filesystem := build(t)
	if err := filesystem.WriteAtomic("page.html", []byte("one"), 0o600); err != nil {
		t.Fatalf("WriteAtomic = %v, want nil", err)
	}
	if err := filesystem.WriteAtomic("page.html", []byte("two"), 0o644); err != nil {
		t.Fatalf("WriteAtomic (republish) = %v, want nil", err)
	}
	info, err := fs.Stat(filesystem, "page.html")
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("Stat = (%v, %v), want mode 0644 — a publication applies its mode", info, err)
	}
}

func rowShortcuts(t *testing.T, build fsFactory) {
	filesystem := build(t)
	if _, ok := filesystem.(fs.StatFS); !ok {
		t.Error("does not implement fs.StatFS")
	}
	if _, ok := filesystem.(fs.ReadDirFS); !ok {
		t.Error("does not implement fs.ReadDirFS")
	}
	if _, ok := filesystem.(fs.ReadFileFS); !ok {
		t.Error("does not implement fs.ReadFileFS")
	}
	if _, ok := filesystem.(fs.ReadLinkFS); !ok {
		t.Error("does not implement fs.ReadLinkFS")
	}
}

func rowTestFS(t *testing.T, build fsFactory) {
	filesystem := build(t)
	if err := filesystem.MkdirAll("a/b/c", 0o750); err != nil {
		t.Fatalf("MkdirAll = %v, want nil", err)
	}
	if err := filesystem.MkdirAll("empty", 0o700); err != nil {
		t.Fatalf("MkdirAll = %v, want nil", err)
	}
	for _, name := range []string{"top.txt", "a/one.txt", "a/b/two.txt", "a/b/c/three.txt", "a/b/c/four.txt"} {
		mustWrite(t, filesystem, name, "content of "+name)
	}
	expected := []string{"top.txt", "a", "a/one.txt", "a/b", "a/b/two.txt", "a/b/c", "a/b/c/three.txt", "a/b/c/four.txt", "empty"}
	if err := fstest.TestFS(filesystem, expected...); err != nil {
		t.Fatalf("fstest.TestFS = %v", err)
	}
}

// The S3 filesystem is a core/data/vfs DirSyncer, as both SDK engines are:
// a caller reaches SyncDir by the same assertion on any of them.
var _ corevfs.DirSyncer = (*s3FS)(nil)

func rowSyncDir(t *testing.T, build fsFactory) {
	filesystem := build(t)
	syncer, ok := filesystem.(corevfs.DirSyncer)
	if !ok {
		t.Fatal("does not implement corevfs.DirSyncer")
	}
	if err := filesystem.MkdirAll("dir", 0o755); err != nil {
		t.Fatalf("MkdirAll = %v, want nil", err)
	}
	mustWrite(t, filesystem, "dir/file.txt", "x")
	if err := syncer.SyncDir("."); err != nil {
		t.Errorf("SyncDir(.) = %v, want nil", err)
	}
	if err := syncer.SyncDir("dir"); err != nil {
		t.Errorf("SyncDir(dir) = %v, want nil", err)
	}
	if err := syncer.SyncDir("dir/file.txt"); !errs.HasCode(err, corevfs.CodeNotRegularFile) {
		t.Errorf("SyncDir(file) = %v, want NOT_REGULAR_FILE", err)
	}
	if err := syncer.SyncDir("absent"); !errs.HasCode(err, corevfs.CodeDirectorySyncFailed) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("SyncDir(absent) = %v, want DIRECTORY_SYNC_FAILED matching fs.ErrNotExist", err)
	}
}
