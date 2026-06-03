package rotfile

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// : compile-time proof the rotating sink satisfies the Sink port.
var _ corelogger.Sink = (*rotatingSink)(nil)

// newSink builds a rotating sink at the given config. It does NOT register an
// auto-close: callers that mutate s.f (rotate/maybeRotate) close it inline so a
// deferred cleanup never races the descriptor swap under the race detector.
func newSink(t *testing.T, cfg Config) *rotatingSink {
	t.Helper()
	//: build through the real constructor so size-seeding + hardening run.
	base, err := newRotatingSink(&cfg)
	//: a clean open is the precondition for every white-box case.
	if err != nil {
		t.Fatalf("newRotatingSink(%+v): %v", cfg, err)
	}
	s, ok := base.(*rotatingSink)
	//: the concrete type is required to poke unexported fields/methods.
	if !ok {
		t.Fatalf("newRotatingSink returned %T, want *rotatingSink", base)
	}
	return s
}

func Test_openHardened(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		symlink bool
		wantErr bool
	}
	tests := []tc{
		{"opens a fresh path", false, false},
		{"refuses a symlink target", true, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "h.log")
		//: the symlink arm plants a link where the open would land.
		if c.symlink {
			if lerr := os.Symlink(filepath.Join(dir, "real"), path); lerr != nil {
				t.Fatalf("%s: symlink: %v", c.name, lerr)
			}
		}
		f, err := openHardened(path)
		//: release the descriptor when the open succeeded.
		if f != nil {
			closeFile(t, f)
		}
		//: failure arm — typed open error + nil descriptor.
		if c.wantErr {
			if !errs.HasCode(err, CodeRotFileOpenFailed) || f != nil {
				t.Errorf("%s: err=%v f=%v want open-failed+nil", c.name, err, f)
			}
			return
		}
		//: happy arm — nil error + usable descriptor at 0600.
		if err != nil || f == nil {
			t.Fatalf("%s: err=%v f=%v want nil+file", c.name, err, f)
		}
		//: the hardened open must apply the 0600 bitmask.
		assertPerm0600(t, c.name, path)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_refuseSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	//: a real file must pass the symlink guard.
	if werr := os.WriteFile(regular, []byte("x"), 0o600); werr != nil {
		t.Fatalf("seed regular: %v", werr)
	}
	link := filepath.Join(dir, "link")
	//: a symlink must be refused by the guard.
	if lerr := os.Symlink(regular, link); lerr != nil {
		t.Fatalf("symlink: %v", lerr)
	}
	type tc struct {
		name    string
		path    string
		wantErr bool
	}
	tests := []tc{
		{"regular file passes", regular, false},
		{"absent path passes", filepath.Join(dir, "missing"), false},
		{"symlink refused", link, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := refuseSymlink(c.path)
		//: the symlink arm must carry the open code.
		if c.wantErr {
			if !errs.HasCode(err, CodeRotFileOpenFailed) {
				t.Errorf("%s: err=%v want open-failed", c.name, err)
			}
			return
		}
		//: the pass arm must be a clean nil.
		if err != nil {
			t.Errorf("%s: err=%v want nil", c.name, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_newRotatingSink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	seeded := filepath.Join(dir, "seeded.log")
	//: pre-seed a file so the constructor's size counter starts non-zero.
	if werr := os.WriteFile(seeded, []byte("0123456789"), 0o600); werr != nil {
		t.Fatalf("seed: %v", werr)
	}
	type tc struct {
		name     string
		cfg      Config
		wantErr  bool
		wantSize int64
	}
	tests := []tc{
		{"fresh path seeds zero", Config{Path: filepath.Join(dir, "f.log")}, false, 0},
		{"existing file seeds size", Config{Path: seeded}, false, 10},
		{"empty path rejected", Config{Path: ""}, true, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		base, err := newRotatingSink(&c.cfg)
		//: failure arm — typed open error + nil sink.
		if c.wantErr {
			if !errs.HasCode(err, CodeRotFileOpenFailed) || base != nil {
				t.Errorf("%s: err=%v sink=%v want open-failed+nil", c.name, err, base)
			}
			return
		}
		//: happy arm — usable sink with the expected seeded size.
		if err != nil || base == nil {
			t.Fatalf("%s: err=%v sink=%v want nil+sink", c.name, err, base)
		}
		s := base.(*rotatingSink)
		//: close inline so no deferred cleanup races the descriptor.
		if cerr := s.Close(); cerr != nil {
			t.Errorf("%s: close: %v", c.name, cerr)
		}
		//: the seeded size must reflect any pre-existing content.
		if s.size != c.wantSize {
			t.Errorf("%s: size=%d want %d", c.name, s.size, c.wantSize)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_Write(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		cancelled bool
		wantErr   bool
	}
	tests := []tc{
		{"live context writes", false, false},
		{"cancelled context refused", true, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := newSink(t, Config{Path: filepath.Join(t.TempDir(), "w.log")})
		//: close once the case finishes asserting; closeQuiet tolerates the
		//: already-closed sentinel since a write may have left it closed.
		defer closeQuiet(t, s)
		ctx := t.Context()
		//: the cancelled arm pre-cancels its context to hit the guard.
		if c.cancelled {
			cctx, cancel := newCancelled(t)
			ctx = cctx
			cancel()
		}
		n, err := s.Write(ctx, corelogger.RecordEvent{}, []byte("hello"))
		//: failure arm — typed write error + zero count.
		if c.wantErr {
			if !errs.HasCode(err, CodeRotFileWriteFailed) || n != 0 {
				t.Errorf("%s: n=%d err=%v want 0+write-failed", c.name, n, err)
			}
			return
		}
		//: happy arm — full byte count + nil error.
		if err != nil || n != 5 {
			t.Errorf("%s: n=%d err=%v want 5+nil", c.name, n, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_Flush(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"flush syncs without error"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		s := newSink(t, Config{Path: filepath.Join(t.TempDir(), "fl.log")})
		//: close once the case finishes asserting.
		defer closeQuiet(t, s)
		//: a written-then-flushed sink must sync cleanly.
		if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("x")); werr != nil {
			t.Fatalf("write: %v", werr)
		}
		//: flush forwards fsync; on a healthy temp dir it must succeed.
		if ferr := s.Flush(t.Context()); ferr != nil {
			t.Errorf("flush: %v", ferr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"close releases the descriptor once"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		s := newSink(t, Config{Path: filepath.Join(t.TempDir(), "c.log")})
		//: the first close must succeed cleanly.
		if cerr := s.Close(); cerr != nil {
			t.Errorf("first close: %v", cerr)
		}
		//: a second close hits the kernel's already-closed error, wrapped typed.
		if cerr := s.Close(); !errs.HasCode(cerr, CodeRotFileWriteFailed) {
			t.Errorf("second close err=%v want write-failed", cerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_rotatingSink_reopenReappliesHardening proves the CWE-59 re-check: a
// symlink planted at Path after the active file is renamed aside must be
// refused by the rotation reopen, not silently followed. It manages the
// descriptor itself (no auto-cleanup) so the manual close + reopen never races.
func Test_rotatingSink_reopenReappliesHardening(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"reopen refuses a symlink planted at Path"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "reopen.log")
		s := newSink(t, Config{Path: path, MaxBytes: 5})
		//: seed the active file, then close + rename it aside (rotate's prefix).
		if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("12345678")); werr != nil {
			t.Fatalf("seed write: %v", werr)
		}
		//: close the active descriptor before renaming and reopening.
		if cerr := s.f.Close(); cerr != nil {
			t.Fatalf("close active: %v", cerr)
		}
		//: rename the active file into the .1 slot.
		if rerr := os.Rename(path, path+".1"); rerr != nil {
			t.Fatalf("rename active: %v", rerr)
		}
		//: plant a symlink exactly where openHardened will recreate Path.
		if lerr := os.Symlink(path+".1", path); lerr != nil {
			t.Fatalf("symlink: %v", lerr)
		}
		f, oerr := openHardened(path)
		//: the planted symlink must be refused on reopen (CWE-59).
		if f != nil {
			closeFile(t, f)
			t.Fatalf("openHardened followed a symlink on reopen")
		}
		//: the refusal must carry the documented open code.
		if !errs.HasCode(oerr, CodeRotFileOpenFailed) {
			t.Errorf("reopen err=%v want open-failed", oerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_Close_joinsDaemon(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"interval daemon is spawned and joined on close"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		s := newSink(t, Config{
			Path: filepath.Join(t.TempDir(), "d.log"), MaxBytes: 1 << 30,
			RotateEvery: time.Hour, Clock: &fakeClock{now: time.Unix(0, 0)},
		})
		//: a positive RotateEvery must spawn the ticker daemon.
		if s.daemon == nil {
			t.Fatalf("RotateEvery>0 spawned no daemon")
		}
		//: Close joins the daemon (Stop blocks on the join) before returning.
		if cerr := s.Close(); cerr != nil {
			t.Fatalf("close: %v", cerr)
		}
		//: a joined daemon has a closed Done channel — deterministic, no leak.
		select {
		//: closed channel proves the ticker goroutine returned.
		case <-s.daemon.Done():
		//: still open means Close did not join the goroutine.
		default:
			t.Errorf("daemon not joined after Close (Done still open)")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_Write_tickErr(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"stashed tick error surfaces via OnError once then clears"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: capture the one-shot OnError surfacing without gating the write.
		var seen error
		s := newSink(t, Config{Path: filepath.Join(t.TempDir(), "te.log"), OnError: func(err error) { seen = err }})
		//: close once the case finishes asserting.
		defer closeQuiet(t, s)
		//: stash a typed rotate failure exactly as a failing tick would.
		s.mu.Lock()
		s.tickErr = RotFileRotateFailed
		s.mu.Unlock()
		//: the first Write surfaces the stash via OnError, NOT its return value —
		//: the triggering record is still written (F6-tickdrop: no dropped line).
		if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("x")); werr != nil {
			t.Fatalf("first Write err=%v want nil (record must still be written)", werr)
		}
		//: the failure reached the operator hook exactly once with the right code.
		if !errs.HasCode(seen, CodeRotFileRotateFailed) {
			t.Fatalf("OnError saw %v want rotate-failed", seen)
		}
		//: the second Write proceeds normally — the error is not latched.
		if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("y")); werr != nil {
			t.Errorf("second Write err=%v want nil", werr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// : compile-time proof the factory satisfies the registry port (kept in a
// : white-box file per KTN-IFACE-ASSERT-PLACEMENT).
var _ writer.Factory = (*rotFileFactory)(nil)

func Test_openHardened_openFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"missing parent directory yields the open sentinel"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: a path whose parent does not exist makes os.OpenFile fail with ENOENT,
		//: exercising the wrap branch distinct from the symlink refusal.
		path := filepath.Join(t.TempDir(), "nosuchdir", "x.log")
		f, err := openHardened(path)
		//: the failed open must hand back the typed sentinel and no descriptor.
		if !errs.HasCode(err, CodeRotFileOpenFailed) || f != nil {
			//: release a surprise descriptor so the test never leaks one.
			if f != nil {
				closeFile(t, f)
			}
			t.Errorf("openHardened err=%v f=%v want open-failed+nil", err, f)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_Write_rotationFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a failing rotate surfaces under the rotate sentinel"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		s := newSink(t, Config{Path: filepath.Join(t.TempDir(), "rf.log"), MaxBytes: 4})
		//: seed size > 0 so the next write's projected size trips maybeRotate.
		if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("12345")); werr != nil {
			t.Fatalf("seed write: %v", werr)
		}
		//: close the descriptor behind the sink's back so rotate()'s first
		//: s.f.Close() fails with the kernel already-closed error.
		s.mu.Lock()
		closeErr := s.f.Close()
		s.mu.Unlock()
		//: the sabotage close must itself have succeeded for the next close to fail.
		if closeErr != nil {
			t.Fatalf("sabotage close: %v", closeErr)
		}
		//: the next write trips maybeRotate, whose rotate() close fails → rotate-failed.
		_, werr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("67"))
		//: a failing rotation is fatal to this write under the rotate sentinel.
		if !errs.HasCode(werr, CodeRotFileRotateFailed) {
			t.Errorf("Write err=%v want rotate-failed", werr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_Write_underlyingWriteFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a closed descriptor fails the direct write path"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		//: MaxBytes=0 disables rotation so Write reaches s.f.Write directly with
		//: no rotate() interposed — the failure must come from the underlying fd.
		s := newSink(t, Config{Path: filepath.Join(t.TempDir(), "uw.log")})
		//: close the descriptor behind the sink's back so s.f.Write errors.
		s.mu.Lock()
		s.size = 0
		closeErr := s.f.Close()
		s.mu.Unlock()
		//: the sabotage close must succeed so the later Write hits a closed fd.
		if closeErr != nil {
			t.Fatalf("sabotage close: %v", closeErr)
		}
		n, werr := s.Write(t.Context(), corelogger.RecordEvent{}, []byte("data"))
		//: a closed fd surfaces the write sentinel; the count may be zero.
		if !errs.HasCode(werr, CodeRotFileWriteFailed) || n != 0 {
			t.Errorf("Write n=%d err=%v want 0+write-failed", n, werr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_Flush_cancelledContext(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a cancelled context short-circuits flush with ctx.Err"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		s := newSink(t, Config{Path: filepath.Join(t.TempDir(), "fc.log")})
		//: close once the case finishes asserting.
		defer closeQuiet(t, s)
		//: pre-cancel so Flush returns ctx.Err() before touching fsync.
		ctx, cancel := newCancelled(t)
		cancel()
		ferr := s.Flush(ctx)
		//: the early guard returns the raw cancellation cause, not a wrapped code.
		if ferr == nil || ferr != ctx.Err() {
			t.Errorf("Flush err=%v want %v", ferr, ctx.Err())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_rotatingSink_Flush_syncFails(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a closed descriptor fails fsync under the write sentinel"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		s := newSink(t, Config{Path: filepath.Join(t.TempDir(), "fs.log")})
		//: close the descriptor behind the sink's back so Sync() errors.
		s.mu.Lock()
		closeErr := s.f.Close()
		s.mu.Unlock()
		//: the sabotage close must succeed so the later Flush hits a closed fd.
		if closeErr != nil {
			t.Fatalf("sabotage close: %v", closeErr)
		}
		//: a failing fsync surfaces the write sentinel (Sync shares it with Write).
		if ferr := s.Flush(t.Context()); !errs.HasCode(ferr, CodeRotFileWriteFailed) {
			t.Errorf("Flush err=%v want write-failed", ferr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
