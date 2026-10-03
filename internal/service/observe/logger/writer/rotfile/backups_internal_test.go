package rotfile

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_rotatingSink_backupName(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		compress bool
		slot     int
		want     string
	}
	tests := []tc{
		{"plain slot", false, 2, "/base.2"},
		{"compressed slot", true, 1, "/base.1.gz"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := &rotatingSink{cfg: Config{Path: "/base", Compress: c.compress}}
		//: the slot name must match the on-disk artefact for that slot.
		if got := s.backupName(c.slot); got != c.want {
			t.Errorf("%s: backupName(%d)=%q want %q", c.name, c.slot, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_highestSlot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	type tc struct {
		name       string
		maxBackups int
		seed       []int
		want       int
	}
	tests := []tc{
		{"finite cap returns cap-1", 3, nil, 2},
		{"unbounded empty returns zero", 0, nil, 0},
		{"unbounded probes to gap", 0, []int{1, 2}, 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		base := filepath.Join(dir, c.name+".log")
		s := &rotatingSink{cfg: Config{Path: base, MaxBackups: c.maxBackups}}
		//: seed the requested backup slots on disk for the probe.
		for _, n := range c.seed {
			if werr := os.WriteFile(s.backupName(n), []byte("x"), 0o600); werr != nil {
				t.Fatalf("%s: seed slot %d: %v", c.name, n, werr)
			}
		}
		//: highestSlot must report the top index to shift.
		if got := s.highestSlot(); got != c.want {
			t.Errorf("%s: highestSlot()=%d want %d", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_shiftExisting(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"shiftExisting moves each backup down by one"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		base := filepath.Join(t.TempDir(), "e.log")
		s := &rotatingSink{cfg: Config{Path: base, MaxBackups: 3}}
		//: seed .1 and .2 so the shift has work to do.
		for _, n := range []int{1, 2} {
			if werr := os.WriteFile(s.backupName(n), []byte{byte(n)}, 0o600); werr != nil {
				t.Fatalf("seed %d: %v", n, werr)
			}
		}
		//: shift must succeed.
		if serr := s.shiftExisting(); serr != nil {
			t.Fatalf("shiftExisting: %v", serr)
		}
		//: old .2 is now .3.
		if _, st := os.Stat(s.backupName(3)); st != nil {
			t.Errorf("expected .3 after shift: %v", st)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_promoteActive(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		compress bool
		wantName string
	}
	tests := []tc{
		{"plain promote renames to .1", false, ".1"},
		{"compress promote gzips to .1.gz", true, ".1.gz"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		base := filepath.Join(t.TempDir(), "p.log")
		//: seed the active file so promote has content to move.
		if werr := os.WriteFile(base, []byte("payload"), 0o600); werr != nil {
			t.Fatalf("%s: seed: %v", c.name, werr)
		}
		s := &rotatingSink{cfg: Config{Path: base, Compress: c.compress}}
		//: promote must succeed.
		if perr := s.promoteActive(); perr != nil {
			t.Fatalf("%s: promoteActive: %v", c.name, perr)
		}
		//: the expected sibling must exist at 0600.
		assertPerm0600(t, c.name, base+c.wantName)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_gzipActive(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"gzipActive writes a 0600 .gz and drops the original"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		base := filepath.Join(t.TempDir(), "g.log")
		//: seed the active file with content to compress.
		if werr := os.WriteFile(base, []byte("compress me"), 0o600); werr != nil {
			t.Fatalf("seed: %v", werr)
		}
		s := &rotatingSink{cfg: Config{Path: base, Compress: true}}
		//: gzipActive must succeed.
		if gerr := s.gzipActive(); gerr != nil {
			t.Fatalf("gzipActive: %v", gerr)
		}
		//: the gz sibling must be 0600.
		assertPerm0600(t, "gzipActive", base+".1.gz")
		//: the uncompressed original must be removed.
		if _, st := os.Stat(base); !os.IsNotExist(st) {
			t.Errorf("expected original removed, stat err=%v", st)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_shiftExisting_renameFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a frozen directory fails the backup rename"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: root bypasses the read-only directory bit, so the negative path is moot.
		if rootBypassesPerms() {
			return
		}
		dir := t.TempDir()
		base := filepath.Join(dir, "se.log")
		//: MaxBackups=3 so highestSlot returns 2 and the shift renames .1 -> .2.
		s := &rotatingSink{cfg: Config{Path: base, MaxBackups: 3}}
		//: seed .1 so shiftExisting has a slot to move into .2.
		if werr := os.WriteFile(s.backupName(1), []byte("x"), 0o600); werr != nil {
			t.Fatalf("seed .1: %v", werr)
		}
		//: freeze the directory so the .1 -> .2 rename fails (restored in cleanup).
		freezeDirReadOnly(t, dir)
		//: the rename EACCES surfaces under the rotate sentinel.
		if serr := s.shiftExisting(); !errs.HasCode(serr, CodeRotFileRotateFailed) {
			t.Errorf("shiftExisting err=%v want rotate-failed", serr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_promoteActive_renameFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a frozen directory fails the active-to-.1 rename"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: root bypasses the read-only directory bit, so the negative path is moot.
		if rootBypassesPerms() {
			return
		}
		dir := t.TempDir()
		base := filepath.Join(dir, "pa.log")
		//: seed the active file so promote has content to rename.
		if werr := os.WriteFile(base, []byte("payload"), 0o600); werr != nil {
			t.Fatalf("seed active: %v", werr)
		}
		//: Compress=false so promoteActive takes the plain os.Rename branch.
		s := &rotatingSink{cfg: Config{Path: base}}
		//: freeze the directory so renaming active -> .1 fails (restored in cleanup).
		freezeDirReadOnly(t, dir)
		//: the rename EACCES surfaces under the rotate sentinel.
		if perr := s.promoteActive(); !errs.HasCode(perr, CodeRotFileRotateFailed) {
			t.Errorf("promoteActive err=%v want rotate-failed", perr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_gzipActive_createFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a frozen directory fails the .gz create"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: root bypasses the read-only directory bit, so the negative path is moot.
		if rootBypassesPerms() {
			return
		}
		dir := t.TempDir()
		base := filepath.Join(dir, "gc.log")
		//: seed the active file so gzipActive reaches its OpenFile for the .gz.
		if werr := os.WriteFile(base, []byte("compress me"), 0o600); werr != nil {
			t.Fatalf("seed active: %v", werr)
		}
		s := &rotatingSink{cfg: Config{Path: base, Compress: true}}
		//: freeze the directory so creating .1.gz fails (restored in cleanup).
		freezeDirReadOnly(t, dir)
		//: the OpenFile EACCES on the .gz sibling surfaces under the rotate sentinel.
		if gerr := s.gzipActive(); !errs.HasCode(gerr, CodeRotFileRotateFailed) {
			t.Errorf("gzipActive err=%v want rotate-failed", gerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_gzipActive_compressIntoFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a missing source fails compressInto inside gzipActive"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		dir := t.TempDir()
		base := filepath.Join(dir, "gci.log")
		//: seed then remove the active file so the .gz create succeeds but the
		//: subsequent compressInto os.Open(cfg.Path) fails with ENOENT.
		if werr := os.WriteFile(base, []byte("gone soon"), 0o600); werr != nil {
			t.Fatalf("seed active: %v", werr)
		}
		s := &rotatingSink{cfg: Config{Path: base, Compress: true}}
		//: drop the source before gzipActive so compressInto cannot open it.
		if rerr := os.Remove(base); rerr != nil {
			t.Fatalf("remove active: %v", rerr)
		}
		//: the ENOENT on the source open surfaces under the rotate sentinel.
		if gerr := s.gzipActive(); !errs.HasCode(gerr, CodeRotFileRotateFailed) {
			t.Errorf("gzipActive err=%v want rotate-failed", gerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_compressInto_openFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a missing source fails the streaming open"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: a path whose parent does not exist makes os.Open fail with ENOENT.
		missing := filepath.Join(t.TempDir(), "nosuchdir", "src")
		//: the open failure surfaces under the rotate sentinel.
		if cerr := compressInto(io.Discard, missing); !errs.HasCode(cerr, CodeRotFileRotateFailed) {
			t.Errorf("compressInto err=%v want rotate-failed", cerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_compressInto(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		payload string
	}
	tests := []tc{{"round-trips the source bytes through gzip", "the quick brown fox"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		src := filepath.Join(t.TempDir(), "src")
		//: seed the source file for the streaming copy.
		if werr := os.WriteFile(src, []byte(c.payload), 0o600); werr != nil {
			t.Fatalf("%s: seed: %v", c.name, werr)
		}
		var buf bytes.Buffer
		//: compressInto must stream without error.
		if cerr := compressInto(&buf, src); cerr != nil {
			t.Fatalf("%s: compressInto: %v", c.name, cerr)
		}
		//: the gzip output must decompress back to the source bytes.
		zr, zerr := gzip.NewReader(&buf)
		if zerr != nil {
			t.Fatalf("%s: gzip reader: %v", c.name, zerr)
		}
		got, rerr := io.ReadAll(zr)
		if rerr != nil {
			t.Fatalf("%s: read: %v", c.name, rerr)
		}
		//: the decompressed bytes must equal the original payload.
		if string(got) != c.payload {
			t.Errorf("%s: got %q want %q", c.name, got, c.payload)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
