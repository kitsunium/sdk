// Package static_test — a 404 is the name's, a 500 is the tree's, and a
// wrapping handler sees which.
package static_test

import (
	"cmp"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/kitsunium/sdk/internal/service/net/static"
)

// errDiskFailure stands for the tree failing: a disk, a remote store, a full
// descriptor table — anything that is not the name's fault.
var errDiskFailure = errors.New("the storage behind the tree failed")

// failingFS is a tree that fails on purpose: Open of openFails fails with
// openErr (errDiskFailure by default), and statFails opens but cannot be
// stat'ed.
type failingFS struct {
	fstest.MapFS
	openFails string
	openErr   error
	statFails string
}

// Open fails as configured, and otherwise opens from the map.
func (f failingFS) Open(name string) (fs.File, error) {
	//: the configured lookup failure.
	if name == f.openFails {
		return nil, &fs.PathError{Op: "open", Path: name, Err: cmp.Or(f.openErr, errDiskFailure)}
	}
	file, err := f.MapFS.Open(name)
	//: the map's own answer.
	if err != nil {
		return nil, err
	}
	//: a file that opens and cannot say what it is.
	if name == f.statFails {
		return statFailingFile{File: file}, nil
	}
	return file, nil
}

// statFailingFile is an open file whose Stat fails.
type statFailingFile struct{ fs.File }

// Stat fails.
func (statFailingFile) Stat() (fs.FileInfo, error) { return nil, errDiskFailure }

// statusRecorder is what a framework wraps a handler with to see the status
// it answered — the reason every status must go through WriteHeader.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the first status written.
func (s *statusRecorder) WriteHeader(code int) {
	//: the first one is the status.
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

// Write records the implicit 200 of a body written without a status.
func (s *statusRecorder) Write(p []byte) (int, error) {
	//: an implicit 200.
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(p)
}

// wrappedStatus serves one GET through handler behind a status recorder and
// returns what the recorder saw.
func wrappedStatus(handler http.Handler, target string) int {
	recorder := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder.status
}

// TestAFailingTreeIsA500AWrapperSees pins the observable failure: a tree that
// holds a name and cannot open it, stat it, or open the index or the shell it
// stands for is a 500, and the wrapping recorder sees the 500 — which a
// framework turns into a failed span.
func TestAFailingTreeIsA500AWrapperSees(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		tree   failingFS
		spa    bool
		target string
	}
	tests := []tc{
		{"a file the storage fails to open", failingFS{MapFS: site(), openFails: "app.js"}, false, "/app.js"},
		{"a file the tree may not read", failingFS{MapFS: site(), openFails: "app.js", openErr: fs.ErrPermission}, false, "/app.js"},
		{"a file that cannot be stat'ed", failingFS{MapFS: site(), statFails: "app.js"}, false, "/app.js"},
		{"a directory's index that fails", failingFS{MapFS: site(), openFails: "guide/index.html"}, false, "/guide/"},
		{"the shell of a fallback that fails", failingFS{MapFS: site(), openFails: "index.html"}, true, "/settings"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		handler := build(t, c.tree, static.Config{SinglePageApp: c.spa})
		//: the server's failure, seen from outside the handler.
		if got := wrappedStatus(handler, c.target); got != http.StatusInternalServerError {
			t.Errorf("GET %s: the wrapper saw %d, want 500", c.target, got)
		}
	}
	//: one subtest per case.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestARefusedNameIsA404 pins the other half through the portable refusals: a
// file system that says a name does not exist, or is not a name it can hold,
// is answered 404 — the client chose the name, so the client's request is
// what failed.
func TestARefusedNameIsA404(t *testing.T) {
	t.Parallel()
	//: each portable refusal of a name.
	for _, refusal := range []error{fs.ErrNotExist, fs.ErrInvalid} {
		handler := build(t, failingFS{MapFS: site(), openFails: "odd.js", openErr: refusal}, static.Config{})
		//: the name's fault.
		if got := wrappedStatus(handler, "/odd.js"); got != http.StatusNotFound {
			t.Errorf("a lookup refused with %v: the wrapper saw %d, want 404", refusal, got)
		}
	}
}

// TestNamesTheOperatingSystemRefusesAre404s asks a real directory, through
// os.DirFS, for every kind of name a client can send that the operating
// system refuses — a file used as a directory, a component past the length a
// file system holds, a character Windows forbids, a device name, a NUL, a byte
// that is not UTF-8 — and pins that each is a 404 on this platform: otherwise
// any client could make the server report a failure at will.
func TestNamesTheOperatingSystemRefusesAre404s(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	//: one real file, for the name that uses it as a directory.
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<title>disk</title>"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	handler := build(t, os.DirFS(root), static.Config{})
	targets := map[string]string{
		"a file used as a directory":      "/index.html/x",
		"a component past NAME_MAX":       "/" + strings.Repeat("a", 300) + ".html",
		"a star":                          "/a%2Ab.html",
		"a colon":                         "/a%3Ab.html",
		"a question mark":                 "/a%3Fb.html",
		"a pipe":                          "/a%7Cb.html",
		"a device name":                   "/con",
		"a device name with an extension": "/nul.txt",
		"a NUL":                           "/a%00b.html",
		"a byte that is not UTF-8":        "/%FF.html",
		"a backslash":                     "/a%5Cb.html",
	}
	//: each name the operating system refuses.
	for name, target := range targets {
		//: the client's name, the client's 404.
		if got := wrappedStatus(handler, target); got != http.StatusNotFound {
			t.Errorf("%s (%s): the wrapper saw %d, want 404", name, target, got)
		}
	}
	//: and the file itself is served, so the tree is not simply unreadable.
	if got := wrappedStatus(handler, "/index.html"); got != http.StatusOK {
		t.Errorf("GET /index.html = %d, want 200", got)
	}
}

// TestAnUnreadableFileIsTheTreesFailure pins, on a real file system, that a
// permission refused on an entry the tree holds is a 500: the client asked
// for a file that exists, and the server cannot read what it was told to
// serve.
func TestAnUnreadableFileIsTheTreesFailure(t *testing.T) {
	t.Parallel()
	//: Windows grants read access through ACLs a mode does not change.
	if runtime.GOOS == "windows" {
		t.Skip("a Unix mode cannot make a file unreadable on Windows; the refusal is pinned portably by TestAFailingTreeIsA500AWrapperSees")
	}
	//: the superuser reads through any mode.
	if os.Geteuid() == 0 {
		t.Skip("running as root, which reads a mode-000 file; the refusal is pinned portably by TestAFailingTreeIsA500AWrapperSees")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked.js")
	//: a file the process then may not read.
	if err := os.WriteFile(locked, []byte("x"), 0o000); err != nil {
		t.Fatalf("write: %v", err)
	}
	handler := build(t, os.DirFS(root), static.Config{})
	//: the tree's failure, not the client's.
	if got := wrappedStatus(handler, "/locked.js"); got != http.StatusInternalServerError {
		t.Errorf("GET /locked.js: the wrapper saw %d, want 500", got)
	}
}
