package kit

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The source endpoint reads a regular file of its root and never a link at
// the file's name: what the graph names is served while it is a regular
// file, and refused once a link takes its name, though the link stays
// inside the root.

// sourceTree is a directory holding a regular file, a.go, and beside it a
// file no graph names, .env, which a link could lead to.
func sourceTree(t *testing.T) (string, *os.Root) {
	t.Helper()
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("TOKEN=hunter2\n"), 0o600))
	root, err := os.OpenRoot(dir)
	must(t, err)
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	return dir, root
}

// linkOrSkip puts a symbolic link to target at name, or skips the test on
// a system that lets the test make none — Windows without the right to.
func linkOrSkip(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Skipf("no symbolic link here: %v", err)
	}
}

// A source is a regular file within the bound: a directory, a missing file
// and a file past the bound are refused.
func TestASourceIsARegularFileWithinTheBound(t *testing.T) {
	dir, root := sourceTree(t)
	sub, err := os.MkdirTemp(dir, "sub*.go")
	must(t, err)
	must(t, os.WriteFile(filepath.Join(dir, "big.go"), make([]byte, sourceMaxBytes+1), 0o600))
	if raw, err := readSource(root, "a.go"); err != nil || string(raw) != "package a\n" {
		t.Errorf("the regular file: %q, %v", raw, err)
	}
	for _, name := range []string{filepath.Base(sub), "missing.go", "big.go"} {
		if raw, err := readSource(root, name); err == nil {
			t.Errorf("%s was read: %d bytes", name, len(raw))
		}
	}
}

// A link at the file's name is refused, though it stays inside the root,
// and so is one swapped in between the look at the name and the open: what
// is read is the regular file looked at, never what a link leads to.
func TestASourceIsNeverALink(t *testing.T) {
	dir, root := sourceTree(t)
	linkOrSkip(t, ".env", filepath.Join(dir, "link.go"))
	if raw, err := readSource(root, "link.go"); err == nil {
		t.Errorf("a link at the name was read: %q", raw)
	}
	seen, err := root.Lstat("a.go")
	must(t, err)
	must(t, os.Remove(filepath.Join(dir, "a.go")))
	linkOrSkip(t, ".env", filepath.Join(dir, "a.go"))
	if raw, err := readSeen(root, "a.go", seen); err == nil {
		t.Errorf("a link swapped in at the name was read: %q", raw)
	}
}

// The endpoint serves the file the graph names while it is a regular file,
// and refuses it once a link takes its name, saying nothing of what the
// link leads to. The module and its service are declared in the root
// package of toolsModule, as they say, from a file of the test's directory:
// that directory is the Go module's root, and .env lies there.
func TestTheSourceEndpointServesNoLink(t *testing.T) {
	tools, _ := goModuleOf(toolsPos(t).pkg())
	dir := t.TempDir()
	file := filepath.Join(dir, "packages.go")
	must(t, os.WriteFile(file, []byte("// Package clock\npackage clock\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, ".env"), []byte("TOKEN=hunter2\n"), 0o600))
	desk := NewService("desk", "A module's service.")
	m := NewModule("tools", "A module of another Go module.", desk)
	m.decl = posAt(file, 1, "", tools.path)
	desk.decl = m.decl
	app := NewApp("x").With(m, InMemory(), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	must(t, app.Start(t.Context()))
	t.Cleanup(func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
	src := app.Graph().ModuleOf("tools").Source
	servesOnlyWhatTheGraphNames(t, app, src.GoModule, src.File)

	must(t, os.Remove(file))
	linkOrSkip(t, ".env", file)
	q := url.Values{"module": {src.GoModule}, "file": {src.File}, "line": {"1"}}
	resp, err := http.Get(app.URL() + "/_kit/api/source?" + q.Encode())
	must(t, err)
	defer resp.Body.Close()
	body, bodyErr := io.ReadAll(resp.Body)
	if bodyErr != nil {
		t.Fatal(bodyErr)
	}
	if resp.StatusCode != http.StatusNotFound || strings.Contains(string(body), "hunter2") {
		t.Errorf("a link at the file's name: %d %s", resp.StatusCode, body)
	}
}
