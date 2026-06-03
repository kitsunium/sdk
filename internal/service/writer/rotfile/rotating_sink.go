// Package rotfile — rotatingSink (the rotating Sink implementation) plus the
// open/reopen hardening shared with the rotation cycle. Its Config lives in the
// parent-prefixed sibling rotating_sink_config.go (KTN-STRUCT-ONEFILE).
package rotfile

import (
	"context"
	"os"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/worker"
)

// defaultFilePerm is the permission bitmask for the active file and every
// rotated sibling. 0600 restricts reads to the owning UID so diagnostic
// content is never world-readable; a gzip sibling is chmod'd to the same value
// rather than inheriting gzip's default.
const defaultFilePerm os.FileMode = 0o600

// rotatingSink is the unexported Sink that appends formatted records to Path
// and rotates the file when a write would exceed cfg.MaxBytes. It owns the
// descriptor directly so it can close + rename + reopen across a rotation.
type rotatingSink struct {
	// cfg is the immutable rotation policy (path, cap, backups, compress).
	cfg Config
	// mu serialises Write + rotation so the size accounting, rename shift, and
	// reopen are atomic against concurrent producers.
	mu sync.Mutex
	// f is the currently open active file at cfg.Path.
	f *os.File
	// size tracks bytes written to the active file since the last open, so the
	// threshold check needs no per-write stat syscall.
	size int64
	// clk is the time source for MaxAgeDays calendar pruning; never nil after
	// newRotatingSink resolves cfg.Clock (defaulting to clock.System).
	clk clock.Clock
	// daemon is the interval-rotation ticker, non-nil only when cfg.RotateEvery
	// is positive. Close joins it before locking mu so the ticker goroutine
	// (which itself locks mu via Rotate) cannot deadlock the join.
	daemon *worker.LoopDaemon
	// tickErr stashes a typed error from an interval-driven Rotate so the next
	// Write surfaces it exactly once (genuine propagation, never discarded);
	// guarded by mu.
	tickErr error
}

// openHardened opens path with O_NOFOLLOW (Linux) + 0600 after refusing a
// pre-existing symlink. It is the single hardening entry point so the checks
// re-run identically at construction AND on every reopen after a rotation
// (CWE-59) — the rotation cycle never reopens through a weaker path.
func openHardened(path string) (file *os.File, err error) {
	//: reject a pre-existing symlink before OpenFile can follow it.
	if serr := refuseSymlink(path); serr != nil {
		//: surface the hardening sentinel so HasCode introspection works.
		return nil, serr
	}
	//: open with O_NOFOLLOW on Linux so a symlink planted between the Lstat
	//: check and this call fails the open rather than silently redirecting.
	f, oerr := os.OpenFile(path, openFlags, defaultFilePerm)
	//: wrap os errors via errs.Wrap so errors.Is still catches the cause.
	if oerr != nil {
		//: surface the documented open sentinel for HasCode introspection.
		return nil, errs.Wrap(oerr, errs.WrapParams{
			Code:    CodeRotFileOpenFailed,
			Reason:  "ROT_FILE_OPEN_FAILED",
			Public:  "Rotating file sink could not open the destination file",
			Private: "service/writer/rotfile.openHardened: os.OpenFile returned an error",
		}, errs.String("path", path))
	}
	//: caller owns the returned descriptor.
	return f, nil
}

// refuseSymlink returns a typed error when path's final component is a symlink.
// os.Lstat does not traverse the final component, so a symlink is caught before
// OpenFile follows it; on Linux O_NOFOLLOW closes the residual TOCTOU window.
func refuseSymlink(path string) error {
	//: Lstat does NOT follow the final component; absent paths / stat errors
	//: fall through so OpenFile surfaces the real diagnostic uniformly.
	fi, lerr := os.Lstat(path)
	//: pass through when stat fails OR the target is a regular file.
	if lerr != nil || fi.Mode()&os.ModeSymlink == 0 {
		//: happy path — not a symlink, hand control back to OpenFile.
		return nil
	}
	//: path resolved to a symlink → reject by policy.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeRotFileOpenFailed,
		Reason:  "ROT_FILE_OPEN_FAILED",
		Public:  "Rotating file sink refuses to open a symlink",
		Private: "service/writer/rotfile.refuseSymlink: path is a symlink; refusing per hardening policy",
	}, errs.String("path", path))
}

// newRotatingSink validates cfg, opens the active file through the hardened
// path, and seeds the size counter from the existing file so a restart does not
// reset the rotation threshold. cfg is taken by pointer so the grown Config
// value (path + caps + clock, >64 bytes) is not copied onto the stack.
func newRotatingSink(cfg *Config) (sink corelogger.Sink, err error) {
	//: refuse an empty path so callers detect the misconfiguration immediately.
	if cfg.Path == "" {
		//: surface the open sentinel with an explicit private cause.
		return nil, errs.Wrap(nil, errs.WrapParams{
			Code:    CodeRotFileOpenFailed,
			Reason:  "ROT_FILE_OPEN_FAILED",
			Public:  "Rotating file sink requires a non-empty path",
			Private: "service/writer/rotfile.newRotatingSink: empty path",
		})
	}
	//: open + harden the active file (symlink refusal + O_NOFOLLOW + 0600).
	f, oerr := openHardened(cfg.Path)
	//: forward the typed open error unchanged.
	if oerr != nil {
		//: origin wins — RotFileOpenFailed already set the code/reason.
		return nil, oerr
	}
	//: seed size from the existing file; a stat error is non-fatal (start at 0).
	var size int64
	//: only consult the descriptor when the open succeeded.
	if fi, serr := f.Stat(); serr == nil {
		//: existing content counts toward the first rotation threshold.
		size = fi.Size()
	}
	//: resolve the clock once so age pruning never dereferences a nil source.
	clk := cfg.Clock
	//: a nil clock falls back to the shared system wall clock.
	if clk == nil {
		//: default keeps the zero-value Config working unchanged.
		clk = clock.System
	}
	//: build the ready sink owning the descriptor; store cfg by value so the
	//: sink is independent of the caller's Config after construction.
	s := &rotatingSink{cfg: *cfg, f: f, size: size, clk: clk}
	//: opt-in interval rotation: a strictly positive RotateEvery starts the
	//: ticker (worker.Every panics on non-positive, so the guard is mandatory);
	//: the zero value spawns no goroutine, keeping plain logging cost-free.
	if cfg.RotateEvery > 0 {
		//: the ticker forces a rotation each interval until Close joins it.
		s.daemon = worker.Every(cfg.RotateEvery, s.tickRotate)
	}
	//: hand back the ready sink.
	return s, nil
}

// Write appends p to the active file under the local mutex, rotating first when
// the write would push the file past MaxBytes.
func (s *rotatingSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: honour context cancellation: cancelled contexts skip the write entirely.
	if ctx != nil && ctx.Err() != nil {
		//: wrap ctx.Err() so consumers get both our reason and stdlib Is().
		return 0, errs.Wrap(ctx.Err(), errs.WrapParams{
			Code:    CodeRotFileWriteFailed,
			Reason:  "ROT_FILE_WRITE_FAILED",
			Public:  "Rotating file write failed",
			Private: "service/writer/rotfile.Write saw a cancelled context",
		}, errs.Int("level", int(r.Level)))
	}
	//: serialise the threshold check, optional rotation, and the write so the
	//: size accounting stays consistent against concurrent producers.
	s.mu.Lock()
	defer s.mu.Unlock()
	//: surface a stashed interval-rotation failure exactly once via the OnError
	//: hook: a failing "archive every 24h" policy must reach the operator, not
	//: vanish on the daemon goroutine. Clearing it after read makes this genuine
	//: one-shot propagation, not a latched error. Crucially we do NOT return
	//: here — the current record still falls through to the write below so a
	//: rotation I/O failure never costs a log line (the hook reports it instead
	//: of the swallowed handler-error return).
	if s.tickErr != nil {
		//: take and clear the pending error so the next Write proceeds normally.
		terr := s.tickErr
		s.tickErr = nil
		//: report the interval-rotation failure to the operator's hook when wired.
		if s.cfg.OnError != nil {
			//: hand the typed rotate error to the callback; it never gates the write.
			s.cfg.OnError(terr)
		}
	}
	//: rotate before the write when adding p would exceed the cap.
	if rerr := s.maybeRotate(int64(len(p))); rerr != nil {
		//: a rotation failure is fatal to this write — surface it.
		return 0, rerr
	}
	//: append to the (possibly freshly reopened) active file.
	written, werr := s.f.Write(p)
	//: account for the bytes that actually landed even on a short write.
	s.size += int64(written)
	//: wrap any *os.File error with the WriteFailed sentinel semantics.
	if werr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return written, errs.Wrap(werr, errs.WrapParams{
			Code:    CodeRotFileWriteFailed,
			Reason:  "ROT_FILE_WRITE_FAILED",
			Public:  "Rotating file write failed",
			Private: "service/writer/rotfile.Write underlying *os.File returned an error",
		}, errs.Int("bytes", len(p)), errs.Int("level", int(r.Level)))
	}
	//: happy path — return the byte count.
	return written, nil
}

// Flush calls fsync on the active file so kernel page cache contents reach
// durable storage.
func (s *rotatingSink) Flush(ctx context.Context) error {
	//: honour cancellation early — fsync is not cheap on slow disks.
	if ctx != nil && ctx.Err() != nil {
		//: V46: wrap ctx.Err() under the WriteFailed sentinel like Write does, so a
		//: cancelled Flush carries the dotted-quad code (errs.HasCode works) and
		//: errors.Is still catches the stdlib cancellation cause.
		return errs.Wrap(ctx.Err(), errs.WrapParams{
			Code:    CodeRotFileWriteFailed,
			Reason:  "ROT_FILE_WRITE_FAILED",
			Public:  "Rotating file flush failed",
			Private: "service/writer/rotfile.Flush saw a cancelled context",
		})
	}
	//: serialise the sync against in-flight writes via the same mutex.
	s.mu.Lock()
	serr := s.f.Sync()
	s.mu.Unlock()
	//: wrap any sync error with the WriteFailed sentinel semantics.
	if serr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return errs.Wrap(serr, errs.WrapParams{
			Code:    CodeRotFileWriteFailed,
			Reason:  "ROT_FILE_WRITE_FAILED",
			Public:  "Rotating file flush failed",
			Private: "service/writer/rotfile.Flush underlying *os.File.Sync returned an error",
		})
	}
	//: happy path — nothing to report.
	return nil
}

// Close closes the active file. Subsequent Writes fail with the kernel's "file
// already closed" error, which we wrap as WriteFailed.
func (s *rotatingSink) Close() error {
	//: stop the interval ticker FIRST, before taking mu: Stop joins the daemon
	//: goroutine, which itself locks mu inside Rotate — joining under mu would
	//: deadlock. After Stop returns the goroutine has exited, so the subsequent
	//: lock is uncontended by the ticker.
	if s.daemon != nil {
		//: signal + join the ticker so no rotation races the close.
		s.daemon.Stop()
	}
	//: serialise the close against in-flight writes via the same mutex.
	s.mu.Lock()
	cerr := s.f.Close()
	s.mu.Unlock()
	//: wrap any close error with the WriteFailed sentinel semantics.
	if cerr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return errs.Wrap(cerr, errs.WrapParams{
			Code:    CodeRotFileWriteFailed,
			Reason:  "ROT_FILE_WRITE_FAILED",
			Public:  "Rotating file close failed",
			Private: "service/writer/rotfile.Close underlying *os.File.Close returned an error",
		})
	}
	//: happy path — nothing to report.
	return nil
}
