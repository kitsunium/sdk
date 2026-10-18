package app_test

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The names at rest in the data directory (ADR 0180 step 5): dev's default
// directory .kit/data is moved to .app/data by rename(2), the privacy
// service's kit.privacy to app.privacy in it, every file byte for byte; the
// tool's own directory keeps what is the tool's; a second start moves
// nothing; a present name already taken refuses the start and nothing
// moves; a move the system refuses leaves the former directory where it
// was, and the next start moves it.

// restNote is a thing the dev product keeps, in a store of its own.
type restNote struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// devRest is a product in dev with no data directory named: its data in
// dev's default, under the current directory.
func devRest() *app.App {
	svc := app.NewService("desk", "Notes, in dev's default data directory.")
	svc.Store("notes", func(n restNote) string { return n.ID })
	return app.NewApp("desk", svc).With(app.Env(app.EnvDev), app.Listen("127.0.0.1:0"), app.Analyze(false), app.Logs(io.Discard))
}

// formerDevTree is what a former release kept in dev, and the tool's own
// file beside it, written under the current directory.
var formerDevTree = map[string]string{
	".kit/data/desk/legacy.txt":         "a file the former release kept",
	".kit/data/kit.privacy/marker.json": `{"kept":"by the former release"}`,
	".kit/tool.state":                   "the tool's own",
}

// writeTree writes files under the current directory.
func writeTree(t *testing.T, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// filesUnder are the files under root, by their path, with their bytes.
func filesUnder(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		out[filepath.ToSlash(path)] = string(b)
		return err
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return out
}

func TestDevsDataDirectoryMovesToItsPresentName(t *testing.T) {
	needsFileStore(t)
	t.Chdir(t.TempDir())
	writeTree(t, formerDevTree)
	for round := range 2 {
		a := devRest()
		run(t, a)
		if err := a.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
		for name, want := range map[string]string{
			".app/data/desk/legacy.txt":         formerDevTree[".kit/data/desk/legacy.txt"],
			".app/data/app.privacy/marker.json": formerDevTree[".kit/data/kit.privacy/marker.json"],
			".kit/tool.state":                   formerDevTree[".kit/tool.state"],
		} {
			if got, err := os.ReadFile(name); err != nil || !bytes.Equal(got, []byte(want)) {
				t.Errorf("round %d, %s: %q %v, want %q", round, name, got, err, want)
			}
		}
		if _, err := os.Stat(".kit/data"); !os.IsNotExist(err) {
			t.Errorf("round %d: .kit/data is still there: %v", round, err)
		}
	}
}

// Outside dev's default — a directory named, or production —, nothing is
// moved from .kit/data: the directory is the operator's.
func TestANamedDataDirectoryIsNotMovedInto(t *testing.T) {
	needsFileStore(t)
	t.Chdir(t.TempDir())
	writeTree(t, formerDevTree)
	a := devRest().With(app.DataDir("elsewhere"))
	run(t, a)
	if got := filesUnder(t, ".kit"); len(got) != len(formerDevTree) {
		t.Errorf("the former tree under a named directory: %v", got)
	}
}

func TestADataDirectoryMoveNeverReplaces(t *testing.T) {
	needsFileStore(t)
	for _, taken := range []string{".app/data/desk/present.txt", ".kit/data/app.privacy/present.json"} {
		t.Run(taken, func(t *testing.T) {
			t.Chdir(t.TempDir())
			writeTree(t, formerDevTree)
			writeTree(t, map[string]string{taken: "already there"})
			before := filesUnder(t, ".")
			err := devRest().Start(t.Context())
			if !errs.HasCode(err, app.CodeRenameTarget) {
				t.Fatalf("the start with %s taken: %v", taken, err)
			}
			if got := filesUnder(t, "."); len(got) != len(before) {
				t.Errorf("the tree changed: %v, want %v", got, before)
			} else {
				for name, body := range before {
					if got[name] != body {
						t.Errorf("%s: %q, want %q", name, got[name], body)
					}
				}
			}
		})
	}
}

func TestADataDirectoryMoveTheSystemRefusesIsResumed(t *testing.T) {
	needsFileStore(t)
	if os.Geteuid() == 0 {
		t.Skip("root renames in a directory it may not write")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	writeTree(t, formerDevTree)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	err := devRest().Start(t.Context())
	if cerr := os.Chmod(dir, 0o700); cerr != nil {
		t.Fatal(cerr)
	}
	if !errs.HasCode(err, app.CodeRenameFailed) {
		t.Fatalf("the start whose move the system refused: %v", err)
	}
	if got := filesUnder(t, ".kit"); len(got) != len(formerDevTree) {
		t.Errorf("the former tree after the refusal: %v", got)
	}
	run(t, devRest())
	if got, err := os.ReadFile(".app/data/desk/legacy.txt"); err != nil || string(got) != formerDevTree[".kit/data/desk/legacy.txt"] {
		t.Errorf("resumed: %q %v", got, err)
	}
}
