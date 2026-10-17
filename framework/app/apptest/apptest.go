package apptest

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"runtime"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
	"github.com/kitsunium/sdk/pkg/v1/proc"
)

// secretsDir is where kit keeps the secrets that follow the data, in the
// data directory: never searched for words, since that is what it keeps.
const secretsDir string = ".secrets"

// Disk is a data directory of a test's own, and the option that runs an app
// on it.
type Disk struct {
	// AppOption keeps an app's data in Dir.
	app.AppOption
	// Dir is the data directory, removed once the test and its subtests end.
	Dir string
}

// NewDisk returns a new data directory for t. The secrets kit makes — its
// data-key and its index key, what the product generates — are kept beside
// the data, as production keeps them unless KIT_SECRETS says otherwise, so
// an app started again on the same Disk opens what the first sealed.
//
// It skips t where the platform keeps no data directory: the SDK's disk
// file system is Linux's, macOS's and the BSDs', and refuses Windows.
func NewDisk(t testing.TB) *Disk {
	t.Helper()
	dir := t.TempDir()
	fsys, err := vfs.NewOS(dir)
	if closer, ok := fsys.(io.Closer); ok {
		if closeErr := closer.Close(); closeErr != nil {
			t.Fatalf("the data directory's probe cannot be closed: %v", closeErr)
		}
	}
	if errors.Is(err, proc.UnsupportedPlatform) {
		t.Skipf("%s keeps no data directory: the SDK's disk file system is Linux's, macOS's and the BSDs'", runtime.GOOS)
	}
	if err != nil {
		t.Fatalf("the data directory cannot be opened: %v", err)
	}
	return &Disk{AppOption: app.DataDir(dir), Dir: dir}
}

// Holding returns the files of the data directory that hold one of words,
// byte for byte — slash-separated, relative to Dir, sorted —, the secrets
// kit keeps there left out: what a test asserts is empty once kit seals
// what the words were written into. An empty word matches nothing.
func (d *Disk) Holding(t testing.TB, words ...string) []string {
	t.Helper()
	root, err := os.OpenRoot(d.Dir)
	if err != nil {
		t.Fatalf("the data directory cannot be opened: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("the data directory cannot be closed: %v", closeErr)
		}
	}()
	scan := &scanner{fsys: root.FS()}
	for _, w := range words {
		if w != "" {
			scan.needles = append(scan.needles, []byte(w))
		}
	}
	err = fs.WalkDir(scan.fsys, ".", scan.visit)
	if err != nil {
		t.Fatalf("the data directory cannot be read: %v", err)
	}
	slices.Sort(scan.found)
	return scan.found
}

// scanner is Holding's walk over the data directory: the words it looks
// for, as bytes, and the files found holding one.
type scanner struct {
	fsys    fs.FS
	needles [][]byte
	found   []string
}

// visit is the walk's step over one entry: kit's secrets are skipped, a
// directory is walked into, and a file holding one of the needles is found.
func (s *scanner) visit(name string, entry fs.DirEntry, walkErr error) error {
	switch {
	case walkErr != nil:
		return walkErr
	case entry.IsDir() && name == secretsDir:
		return fs.SkipDir
	case entry.IsDir():
		return nil
	}
	raw, err := fs.ReadFile(s.fsys, name)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(s.needles, func(needle []byte) bool { return bytes.Contains(raw, needle) }) {
		s.found = append(s.found, path.Clean(name))
	}
	return nil
}
