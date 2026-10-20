package rotfile

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	corerotfile "github.com/kitsunium/sdk/internal/core/observe/logger/writer/rotfile"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// plantSymlink plants a symbolic link at link pointing at target. Every Unix
// CI runs on can; Windows can only with the privilege its runners hold, so a
// refusal there skips the case, naming why, and anywhere else fails it.
func plantSymlink(t *testing.T, target, link string) {
	t.Helper()
	lerr := os.Symlink(target, link)
	//: planted.
	if lerr == nil {
		return
	}
	//: Windows creates a link only with SeCreateSymbolicLinkPrivilege or
	//: developer mode; without it there is no link to refuse.
	if runtime.GOOS == "windows" {
		t.Skipf("this account cannot create a symbolic link on Windows: %v", lerr)
	}
	t.Fatalf("plant symlink %s -> %s: %v", link, target, lerr)
}

// gunzipFile decompresses the gzip file at path, failing the test when it is
// not one.
func gunzipFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("read %s: %v", path, rerr)
	}
	zr, zerr := gzip.NewReader(bytes.NewReader(raw))
	if zerr != nil {
		t.Fatalf("gzip reader %s: %v", path, zerr)
	}
	got, derr := io.ReadAll(zr)
	if derr != nil {
		t.Fatalf("gunzip %s: %v", path, derr)
	}
	return got
}

// dirEntries lists dir's entry names, sorted.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatalf("read dir: %v", rerr)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// Test_rotatingSink_gzipActive_replacesAPlantedLink pins #463's first half
// (CWE-59): a link at Path.1.gz — planted in the window after shiftExisting
// moved the previous one away — is replaced by the archive, never written
// through. The live target keeps its bytes, a dangling target is never
// created, Path.1.gz is a regular file holding the gzip of the active file,
// and no temporary sibling is left behind.
func Test_rotatingSink_gzipActive_replacesAPlantedLink(t *testing.T) {
	t.Parallel()
	for name, dangling := range map[string]bool{"a live target": false, "a dangling target": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			base := filepath.Join(dir, "g.log")
			victim := filepath.Join(dir, "victim.conf")
			if !dangling {
				if werr := os.WriteFile(victim, []byte("precious=1\n"), 0o600); werr != nil {
					t.Fatalf("seed victim: %v", werr)
				}
			}
			if werr := os.WriteFile(base, []byte("the active file\n"), 0o600); werr != nil {
				t.Fatalf("seed active: %v", werr)
			}
			plantSymlink(t, victim, base+".1.gz")
			s := &rotatingSink{cfg: Config{Path: base, Compress: true}}
			if gerr := s.gzipActive(); gerr != nil {
				t.Fatalf("gzipActive: %v", gerr)
			}
			got, rerr := os.ReadFile(victim)
			switch {
			case dangling && !os.IsNotExist(rerr):
				t.Fatalf("the dangling link's target was created: %q, %v", got, rerr)
			case !dangling && string(got) != "precious=1\n":
				t.Fatalf("the archive was written through the link: victim holds %q, %v", got, rerr)
			}
			fi, lerr := os.Lstat(base + ".1.gz")
			if lerr != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
				t.Fatalf("Path.1.gz is not a regular file: %v, %v", fi, lerr)
			}
			if archived := gunzipFile(t, base+".1.gz"); string(archived) != "the active file\n" {
				t.Fatalf("the archive holds %q", archived)
			}
			assertPerm0600(t, "gzipActive over a link", base+".1.gz")
			want := []string{"g.log.1.gz"}
			if !dangling {
				want = append(want, "victim.conf")
			}
			if names := dirEntries(t, dir); !slices.Equal(names, want) {
				t.Fatalf("the directory holds %v, want %v", names, want)
			}
		})
	}
}

// Test_rotatingSink_gzipActive_leavesNoPartialArchive pins that a failed
// compression publishes nothing: no Path.1.gz, and no temporary sibling.
func Test_rotatingSink_gzipActive_leavesNoPartialArchive(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := filepath.Join(dir, "g.log")
	s := &rotatingSink{cfg: Config{Path: base, Compress: true}}
	//: no active file: compressInto cannot open its source.
	if gerr := s.gzipActive(); !errs.HasCode(gerr, corerotfile.CodeRotFileRotateFailed) {
		t.Fatalf("gzipActive err=%v want rotate-failed", gerr)
	}
	if names := dirEntries(t, dir); len(names) != 0 {
		t.Fatalf("a failed compression left %v", names)
	}
}

// writeLine writes one record through s, as a producer does.
func writeLine(s *rotatingSink, line string) error {
	_, err := s.Write(context.Background(), corelogger.RecordEvent{}, []byte(line))
	return err
}

// Test_rotatingSink_aFailedReopenNeverKillsTheSink pins #463's second half:
// when the reopen after a rotation is refused, the sink holds no descriptor
// rather than the closed one, so every later Write retries the open — failing
// typed while the obstacle stands, succeeding once it is removed — and
// Flush, Rotate and Close neither panic nor fail on the missing descriptor.
// The obstacle is a directory at Path, which every platform refuses to open
// for writing, and a link, which the hardening refuses where one can be made.
func Test_rotatingSink_aFailedReopenNeverKillsTheSink(t *testing.T) {
	t.Parallel()
	obstacles := map[string]func(t *testing.T, path string){
		"a directory": func(t *testing.T, path string) {
			if merr := os.Mkdir(path, 0o700); merr != nil {
				t.Fatalf("mkdir: %v", merr)
			}
		},
		"a link": func(t *testing.T, path string) {
			plantSymlink(t, filepath.Join(filepath.Dir(path), "elsewhere"), path)
		},
	}
	for name, plant := range obstacles {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "app.log")
			s := newSink(t, Config{Path: path, MaxBytes: 16, MaxBackups: 2})
			if werr := writeLine(s, "before\n"); werr != nil {
				t.Fatalf("first write: %v", werr)
			}
			//: the state rotate leaves when the reopen is refused: the active
			//: file closed and renamed away, something else at Path.
			s.mu.Lock()
			if cerr := s.f.Close(); cerr != nil {
				t.Fatalf("close: %v", cerr)
			}
			if rerr := os.Rename(path, path+".1"); rerr != nil {
				t.Fatalf("rename: %v", rerr)
			}
			plant(t, path)
			s.reopenAfterFailure()
			held := s.f
			rotErr := s.rotate()
			s.mu.Unlock()
			if held != nil {
				t.Fatalf("a refused reopen kept a descriptor")
			}
			if !errs.HasCode(rotErr, corerotfile.CodeRotFileOpenFailed) {
				t.Fatalf("rotate() with no descriptor = %v, want ROT_FILE_OPEN_FAILED", rotErr)
			}
			for i := range 2 {
				if werr := writeLine(s, "while refused\n"); !errs.HasCode(werr, corerotfile.CodeRotFileOpenFailed) {
					t.Fatalf("write %d while the obstacle stands = %v, want ROT_FILE_OPEN_FAILED", i, werr)
				}
			}
			if ferr := s.Flush(context.Background()); ferr != nil {
				t.Fatalf("Flush() with no descriptor = %v", ferr)
			}
			if rerr := s.Rotate(); rerr != nil {
				t.Fatalf("Rotate() with no descriptor = %v", rerr)
			}
			//: the operator removes the obstacle.
			if rerr := os.Remove(path); rerr != nil {
				t.Fatalf("remove obstacle: %v", rerr)
			}
			for i := range 3 {
				if werr := writeLine(s, "after repair\n"); werr != nil {
					t.Fatalf("write %d after the repair = %v", i, werr)
				}
			}
			if ferr := s.Flush(context.Background()); ferr != nil {
				t.Fatalf("Flush() after the repair = %v", ferr)
			}
			if cerr := s.Close(); cerr != nil {
				t.Fatalf("Close() = %v", cerr)
			}
			got, rerr := os.ReadFile(path)
			if rerr != nil || !strings.Contains(string(got), "after repair\n") {
				t.Fatalf("the reopened file holds %q, %v", got, rerr)
			}
		})
	}
}

// Test_rotatingSink_closeWithNoDescriptor pins that a sink whose reopen was
// refused closes cleanly: there is nothing to release.
func Test_rotatingSink_closeWithNoDescriptor(t *testing.T) {
	t.Parallel()
	s := &rotatingSink{cfg: Config{Path: filepath.Join(t.TempDir(), "n.log")}}
	if cerr := s.Close(); cerr != nil {
		t.Fatalf("Close() = %v", cerr)
	}
	if ferr := s.Flush(context.Background()); ferr != nil {
		t.Fatalf("Flush() = %v", ferr)
	}
	//: a closed sink never reopens its file for a late write.
	if werr := writeLine(s, "after close\n"); !errs.HasCode(werr, corerotfile.CodeRotFileWriteFailed) {
		t.Fatalf("a write after Close = %v, want ROT_FILE_WRITE_FAILED", werr)
	}
	if _, serr := os.Lstat(s.cfg.Path); !os.IsNotExist(serr) {
		t.Fatalf("a write after Close created the file: %v", serr)
	}
}
