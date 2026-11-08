//go:build (linux && !mips && !mipsle && !mips64 && !mips64le) || darwin || dragonfly || freebsd || netbsd || openbsd

// Package exec — the safe PID-file walk (#729), on every kernel it runs on:
// the same walk over Linux's O_PATH descriptors and the BSDs' and darwin's
// os.Root directories.
package exec

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// walkRoot makes a directory in the test's temporary tree, mode 0755 whatever
// the umask, for a walk to start at as if it were "/": its own stat decides
// its trust, so the trust of what lies below it is tested on any machine,
// whatever the ancestors of the temporary tree.
func walkRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	mustMkdir(t, root, 0o755)
	return root
}

// mustMkdir makes dir with exactly mode.
func mustMkdir(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatalf("chmod %s: %v", dir, err)
	}
}

// mustWrite writes content to file with exactly mode.
func mustWrite(t *testing.T, file, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	if err := os.Chmod(file, mode); err != nil {
		t.Fatalf("chmod %s: %v", file, err)
	}
}

// mustSymlink makes link point at target.
func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}

// TestPIDWalkTrust pins the walk's verdicts (#729): what is read, what is
// refused, and when the file is trustworthy — nobody but root and this
// process's user could have made what the walk went through. Every fixture
// lies under the test's own directory, used as the walk's root.
func TestPIDWalkTrust(t *testing.T) {
	t.Parallel()
	root := walkRoot(t)
	join := func(p ...string) string { return filepath.Join(append([]string{root}, p...)...) }
	mustMkdir(t, join("ok"), 0o755)
	mustWrite(t, join("ok", "x.pid"), "123\n", 0o644)
	mustWrite(t, join("ok", "gw.pid"), "123\n", 0o664)
	mustWrite(t, join("ok", "ow.pid"), "123\n", 0o646)
	mustMkdir(t, join("gw"), 0o775)
	mustWrite(t, join("gw", "x.pid"), "123\n", 0o644)
	mustMkdir(t, join("ow"), 0o777)
	mustWrite(t, join("ow", "x.pid"), "123\n", 0o644)
	mustMkdir(t, join("sticky"), os.ModeSticky|0o777)
	mustWrite(t, join("sticky", "x.pid"), "123\n", 0o644)
	mustMkdir(t, join("ok", "dir.pid"), 0o755)
	if err := syscall.Mkfifo(join("ok", "fifo.pid"), 0o644); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	//: a link at the end, to a witness the walk must never read.
	mustWrite(t, join("ok", "witness"), "123\n", 0o644)
	mustSymlink(t, "witness", join("ok", "link.pid"))
	//: links on the way.
	mustSymlink(t, "ok", join("lnk"))
	mustSymlink(t, "../ok", join("gw", "lnk"))
	mustSymlink(t, "/ok", join("abs"))
	mustSymlink(t, "loop2", join("loop1"))
	mustSymlink(t, "loop1", join("loop2"))

	type tc struct {
		path        string
		wantTrusted bool
		//: the refusal's why, empty when the file is read.
		wantWhy string
	}
	tests := []tc{
		{path: "ok/x.pid", wantTrusted: true},
		{path: "ok/gw.pid", wantTrusted: false},
		{path: "ok/ow.pid", wantTrusted: false},
		{path: "gw/x.pid", wantTrusted: false},
		{path: "ow/x.pid", wantTrusted: false},
		{path: "sticky/x.pid", wantTrusted: true},
		{path: "lnk/x.pid", wantTrusted: true},
		{path: "gw/lnk/x.pid", wantTrusted: false},
		{path: "abs/x.pid", wantTrusted: true},
		{path: "ok/../ok/x.pid", wantTrusted: true},
		{path: "ok/link.pid", wantWhy: "a symbolic link"},
		{path: "ok/fifo.pid", wantWhy: "not a regular file"},
		{path: "ok/dir.pid", wantWhy: "not a regular file"},
		{path: "ok/missing.pid", wantWhy: "cannot be opened"},
		{path: "ok/x.pid/y", wantWhy: "walk refused at x.pid"},
		{path: "loop1/x.pid", wantWhy: "too many symbolic links"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		w, err := newPIDWalk(root)
		if err != nil {
			t.Fatalf("newPIDWalk: %v", err)
		}
		defer w.close()
		f, trusted, err := w.walk(splitPath(c.path), c.path)
		if c.wantWhy != "" {
			if f != nil {
				_ = f.Close()
			}
			if !errs.HasCode(err, coreproc.CodePIDFileRefused) {
				t.Fatalf("walk(%s) = %v, want PID_FILE_REFUSED", c.path, err)
			}
			if got := fieldOf(err, "why"); got != c.wantWhy {
				t.Fatalf("walk(%s) refused because %q, want %q", c.path, got, c.wantWhy)
			}
			return
		}
		if err != nil {
			t.Fatalf("walk(%s) = %v, want the file", c.path, err)
		}
		defer func() { _ = f.Close() }()
		if trusted != c.wantTrusted {
			t.Errorf("walk(%s) trusted = %v, want %v", c.path, trusted, c.wantTrusted)
		}
	}
	for _, c := range tests {
		t.Run(c.path, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	t.Run("missing is fs.ErrNotExist", func(t *testing.T) {
		t.Parallel()
		w, err := newPIDWalk(root)
		if err != nil {
			t.Fatalf("newPIDWalk: %v", err)
		}
		defer w.close()
		_, _, err = w.walk(splitPath("ok/missing.pid"), "ok/missing.pid")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("walk(missing) = %v, want errors.Is fs.ErrNotExist", err)
		}
	})
}

// fieldOf returns the string field name of err's *errs.Error, empty when
// absent.
func fieldOf(err error, name string) string {
	var e *errs.Error
	if !errors.As(err, &e) {
		return ""
	}
	return fieldValue(e, name)
}

// TestParsePID pins what a PID file may hold: one decimal PID, blanks around
// it, nothing else.
func TestParsePID(t *testing.T) {
	t.Parallel()
	type tc struct {
		content string
		want    int
	}
	tests := []tc{
		{"123\n", 123},
		{" 42 \t\n", 42},
		{"2147483647", 2147483647},
		{"", 0},
		{"\n", 0},
		{"abc", 0},
		{"-5", 0},
		{"+5", 0},
		{"0", 0},
		{"1 2", 0},
		{"12abc", 0},
		{"2147483648", 0},
		{strings.Repeat(" ", 63) + "7", 7},
		{strings.Repeat(" ", 64) + "7", 0},
	}
	for _, c := range tests {
		got, err := parsePID([]byte(c.content), "x.pid")
		if c.want == 0 {
			if !errs.HasCode(err, coreproc.CodePIDFileRefused) {
				t.Errorf("parsePID(%q) = %d, %v, want PID_FILE_REFUSED", c.content, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("parsePID(%q) = %d, %v, want %d", c.content, got, err, c.want)
		}
	}
}

// startOSSleeper starts a child of the test that sleeps, killed and reaped at
// the test's end if still running.
func startOSSleeper(t *testing.T) *os.Process {
	t.Helper()
	p, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "exec sleep 30"}, &os.ProcAttr{})
	if err != nil {
		t.Fatalf("starting the sleeper: %v", err)
	}
	t.Cleanup(func() {
		_ = p.Kill()
		_, _ = p.Wait()
	})
	return p
}

// TestPIDWalkClosesEverything pins that a walk, read or refused, leaves no
// descriptor behind.
func TestPIDWalkClosesEverything(t *testing.T) {
	root := walkRoot(t)
	mustMkdir(t, filepath.Join(root, "a"), 0o755)
	mustMkdir(t, filepath.Join(root, "a", "b"), 0o755)
	mustWrite(t, filepath.Join(root, "a", "b", "x.pid"), strconv.Itoa(os.Getpid()), 0o644)
	before := openFDCount(t)
	for range 50 {
		for _, p := range []string{"a/b/x.pid", "a/b/missing", "a/x.pid/b"} {
			w, err := newPIDWalk(root)
			if err != nil {
				t.Fatalf("newPIDWalk: %v", err)
			}
			if f, _, wErr := w.walk(splitPath(p), p); wErr == nil {
				_ = f.Close()
			}
			w.close()
		}
	}
	if after := openFDCount(t); after > before {
		t.Errorf("descriptors before %d, after 150 walks %d", before, after)
	}
}
