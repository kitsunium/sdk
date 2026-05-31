package rotfile

import (
	"os"
	"path/filepath"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

func Test_rotatingSink_maybeRotate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		maxBytes   int64
		size       int64
		incoming   int64
		wantRotate bool
	}
	tests := []tc{
		{"disabled cap never rotates", 0, 100, 100, false},
		{"empty file never rotates", 10, 0, 50, false},
		{"under threshold keeps file", 10, 4, 5, false},
		{"over threshold rotates", 10, 8, 8, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "m.log")
		s := newSink(t, Config{Path: path, MaxBytes: c.maxBytes})
		//: drive the pre-state and rotation decision, then close, all inline so
		//: no deferred cleanup races the descriptor swap rotate may perform.
		s.mu.Lock()
		s.size = c.size
		rerr := s.maybeRotate(c.incoming)
		closeErr := s.f.Close()
		s.mu.Unlock()
		//: maybeRotate must succeed regardless of whether it rotated.
		if rerr != nil {
			t.Fatalf("%s: maybeRotate: %v", c.name, rerr)
		}
		//: the inline close must succeed.
		if closeErr != nil {
			t.Fatalf("%s: close: %v", c.name, closeErr)
		}
		_, statErr := os.Stat(path + ".1")
		rotated := statErr == nil
		//: a backup exists iff the case expected a rotation.
		if rotated != c.wantRotate {
			t.Errorf("%s: rotated=%v want %v", c.name, rotated, c.wantRotate)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_rotate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"rotate renames active to .1 and reopens a fresh Path"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "r.log")
		s := newSink(t, Config{Path: path, MaxBytes: 5})
		//: seed content so the rotated backup is non-empty.
		if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("12345678")); werr != nil {
			t.Fatalf("seed: %v", werr)
		}
		//: drive the rotation cycle and close the reopened file, all inline.
		s.mu.Lock()
		rerr := s.rotate()
		closeErr := s.f.Close()
		s.mu.Unlock()
		//: rotate must complete cleanly.
		if rerr != nil {
			t.Fatalf("rotate: %v", rerr)
		}
		//: the inline close of the reopened file must succeed.
		if closeErr != nil {
			t.Fatalf("close reopened: %v", closeErr)
		}
		//: the rotated backup must exist.
		if _, serr := os.Stat(path + ".1"); serr != nil {
			t.Errorf("backup missing: %v", serr)
		}
		//: the reopened active file must exist.
		if _, serr := os.Stat(path); serr != nil {
			t.Errorf("active missing after rotate: %v", serr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_shiftBackups(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"shiftBackups promotes the active file into the .1 slot"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		base := filepath.Join(t.TempDir(), "s.log")
		//: seed the active file directly so shiftBackups has content to move.
		if werr := os.WriteFile(base, []byte("abcd"), 0o600); werr != nil {
			t.Fatalf("seed: %v", werr)
		}
		//: a bare sink struct is enough — shiftBackups never touches s.f.
		s := &rotatingSink{cfg: Config{Path: base, MaxBytes: 5}}
		//: the shift must succeed.
		if serr := s.shiftBackups(); serr != nil {
			t.Fatalf("shiftBackups: %v", serr)
		}
		//: the active file became the .1 backup.
		if _, st := os.Stat(base + ".1"); st != nil {
			t.Errorf("expected .1 backup: %v", st)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
