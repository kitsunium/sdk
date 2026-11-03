package rotfile

import (
	"context"
	"os"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	corerotfile "github.com/kitsunium/sdk/internal/core/observe/logger/writer/rotfile"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/concur/worker"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/logger/internal/logfile"
)

// defaultFilePerm is the permission bitmask for the active file and every
// rotated sibling. 0600 restricts reads to the owning UID so diagnostic
// content is never world-readable; a gzip sibling is chmod'd to the same value
// rather than inheriting gzip's default.
const defaultFilePerm os.FileMode = 0o600

// maxRetainedBatch is the largest concatenation buffer WriteBatch keeps for
// the next batch; a burst that grew it past this is let go.
const maxRetainedBatch int = 1 << 20

// openRefusals is this sink's vocabulary for the hardened open both file sinks
// share (internal/service/observe/logger/internal/logfile): one sentinel,
// CodeRotFileOpenFailed, for the policy refusal of a planted link and for every
// failed open — the kernel's refusal of a link planted after the check among
// them — told apart by the "kind" field the shared open adds.
var openRefusals = logfile.RefusalSpec{
	Symlink: errs.WrapParams{
		Code:    corerotfile.CodeRotFileOpenFailed,
		Reason:  "ROT_FILE_OPEN_FAILED",
		Public:  "Rotating file sink refuses to open a symlink",
		Private: "service/observe/logger/writer/rotfile.refuseSymlink: path is a symlink; refusing per hardening policy",
	},
	Open: errs.WrapParams{
		Code:    corerotfile.CodeRotFileOpenFailed,
		Reason:  "ROT_FILE_OPEN_FAILED",
		Public:  "Rotating file sink could not open the destination file",
		Private: "service/observe/logger/writer/rotfile.openHardened: os.OpenFile returned an error",
	},
}

// rotatingSink is the unexported Sink that appends formatted records to Path
// and rotates the file when a write would exceed cfg.MaxBytes. It owns the
// descriptor directly so it can close + rename + reopen across a rotation.
type rotatingSink struct {
	// cfg is the immutable rotation policy (path, cap, backups, compress).
	cfg Config
	// mu serialises Write + rotation so the size accounting, rename shift, and
	// reopen are atomic against concurrent producers. Flush holds it only to
	// read f, never across the fsync (#815).
	mu sync.Mutex
	// pin keeps the descriptor a Flush is syncing open against a rotation:
	// Flush read-locks it while it still holds mu, and rotate write-locks it
	// (under mu) before closing the active file, so a rotation waits for an
	// fsync in progress — rotations are rare — while a Write never does.
	pin sync.RWMutex
	// f is the currently open active file at cfg.Path.
	f *os.File
	// batch is WriteBatch's concatenation buffer, reused under mu.
	batch []byte
	// syncFile, when set, replaces (*os.File).Sync as the fsync Flush runs: a
	// white-box test substitutes one that blocks, to prove a Write is not held
	// behind it. nil in production.
	syncFile func(f *os.File) error
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
	// closed is set by Close, so a sink that held no descriptor — its last
	// reopen failed — never reopens one after it was closed; guarded by mu.
	closed bool
}

// openHardened opens path with 0600 through the hardened open both file sinks
// share: a pre-existing symlink is refused, and O_NOFOLLOW makes the kernel
// refuse one planted after that check, on every platform that has it. It is
// this sink's single entry point, so the checks re-run identically at
// construction AND on every reopen after a rotation (CWE-59) — the rotation
// cycle never reopens through a weaker path.
func openHardened(path string) (file *os.File, err error) {
	//: the shared open, refusing under this sink's ROT_FILE_OPEN_FAILED.
	return logfile.Open(path, defaultFilePerm, &openRefusals)
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
			Code:    corerotfile.CodeRotFileOpenFailed,
			Reason:  "ROT_FILE_OPEN_FAILED",
			Public:  "Rotating file sink requires a non-empty path",
			Private: "service/observe/logger/writer/rotfile.newRotatingSink: empty path",
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
			Code:    corerotfile.CodeRotFileWriteFailed,
			Reason:  "ROT_FILE_WRITE_FAILED",
			Public:  "Rotating file write failed",
			Private: "service/observe/logger/writer/rotfile.Write saw a cancelled context",
		}, errs.Int("level", int(r.Level)))
	}
	//: serialise the threshold check, optional rotation, and the write so the
	//: size accounting stays consistent against concurrent producers.
	s.mu.Lock()
	defer s.mu.Unlock()
	//: the record's level rides on a write failure, as it always has.
	return s.writeLocked(p, errs.Int("level", int(r.Level)))
}

// WriteBatch appends every payload to the active file in ONE write, under
// the local mutex, rotating first when the batch would push the file past
// MaxBytes — the threshold is checked once per batch, as a single record is
// once per write (#807). It reports how many records landed whole.
func (s *rotatingSink) WriteBatch(ctx context.Context, recs []corelogger.RecordEvent, payloads [][]byte) (written int, err error) {
	//: honour context cancellation: cancelled contexts skip the write entirely.
	if ctx != nil && ctx.Err() != nil {
		//: wrap ctx.Err() so consumers get both our reason and stdlib Is().
		return 0, errs.Wrap(ctx.Err(), errs.WrapParams{
			Code:    corerotfile.CodeRotFileWriteFailed,
			Reason:  "ROT_FILE_WRITE_FAILED",
			Public:  "Rotating file write failed",
			Private: "service/observe/logger/writer/rotfile.WriteBatch saw a cancelled context",
		}, errs.Int("records", len(recs)))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	//: one buffer, so one write(2), one threshold check and one size update.
	buf := s.batch[:0]
	for _, p := range payloads {
		buf = append(buf, p...)
	}
	//: keep the buffer for the next batch unless a burst grew it too far.
	defer func() { s.batch = retained(buf) }()
	n, werr := s.writeLocked(buf, errs.Int("records", len(recs)))
	written = wholeRecords(payloads, n)
	//: the rotation, reopen or write error, as Write surfaces it.
	if werr != nil {
		//: recs[written:] were not delivered.
		return written, werr
	}
	//: every record landed.
	return len(payloads), nil
}

// writeLocked is Write's body after the context check: the stashed interval
// error reported, the descriptor reopened when the last reopen failed, the
// rotation when p would cross MaxBytes, then the write, whose failure carries
// the caller's field beside the byte count. The caller holds s.mu.
func (s *rotatingSink) writeLocked(p []byte, field errs.FieldValue) (n int, err error) {
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
	//: a sink whose last reopen failed opens Path again first (#463).
	if oerr := s.ensureOpen(); oerr != nil {
		//: still refused — RotFileOpenFailed, the record not written.
		return 0, oerr
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
			Code:    corerotfile.CodeRotFileWriteFailed,
			Reason:  "ROT_FILE_WRITE_FAILED",
			Public:  "Rotating file write failed",
			Private: "service/observe/logger/writer/rotfile.Write underlying *os.File returned an error",
		}, errs.Int("bytes", len(p)), field)
	}
	//: happy path — return the byte count.
	return written, nil
}

// retained is buf emptied for reuse, or nil when it grew past
// maxRetainedBatch.
func retained(buf []byte) []byte {
	//: a burst's buffer is not kept for the sink's lifetime.
	if cap(buf) > maxRetainedBatch {
		//: the next batch allocates a fresh one.
		return nil
	}
	//: reuse the backing array.
	return buf[:0]
}

// wholeRecords counts the payloads, from the first, that lie entirely within
// the first n bytes of their concatenation.
func wholeRecords(payloads [][]byte, n int) int {
	//: walk the payloads until one does not fit in what was written.
	for i, p := range payloads {
		//: this record was cut, or never started.
		if len(p) > n {
			return i
		}
		n -= len(p)
	}
	//: all of them.
	return len(payloads)
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
			Code:    corerotfile.CodeRotFileWriteFailed,
			Reason:  "ROT_FILE_WRITE_FAILED",
			Public:  "Rotating file flush failed",
			Private: "service/observe/logger/writer/rotfile.Flush saw a cancelled context",
		})
	}
	//: read the active descriptor under mu and pin it against a rotation, then
	//: fsync OUTSIDE mu, so a Write is never held behind the disk (#815).
	s.mu.Lock()
	f := s.f
	s.pin.RLock()
	s.mu.Unlock()
	serr := s.syncOpen(f)
	s.pin.RUnlock()
	//: wrap any sync error with the WriteFailed sentinel semantics.
	if serr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return errs.Wrap(serr, errs.WrapParams{
			Code:    corerotfile.CodeRotFileWriteFailed,
			Reason:  "ROT_FILE_WRITE_FAILED",
			Public:  "Rotating file flush failed",
			Private: "service/observe/logger/writer/rotfile.Flush underlying *os.File.Sync returned an error",
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
	s.closed = true
	cerr := s.closeOpen()
	s.mu.Unlock()
	//: wrap any close error with the WriteFailed sentinel semantics.
	if cerr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return errs.Wrap(cerr, errs.WrapParams{
			Code:    corerotfile.CodeRotFileWriteFailed,
			Reason:  "ROT_FILE_WRITE_FAILED",
			Public:  "Rotating file close failed",
			Private: "service/observe/logger/writer/rotfile.Close underlying *os.File.Close returned an error",
		})
	}
	//: happy path — nothing to report.
	return nil
}

// syncOpen syncs f, the descriptor Flush read under mu; a sink whose last
// reopen failed holds none, and there is nothing it wrote to sync. A Close
// racing the sync makes Sync report the file closed — correct for a closing
// sink — and never touches a reused descriptor: os.File keeps it open until
// the in-flight Sync returns. The caller holds s.pin, not s.mu.
func (s *rotatingSink) syncOpen(f *os.File) error {
	//: no descriptor since a failed reopen: nothing unsynced.
	if f == nil {
		//: nothing to sync.
		return nil
	}
	//: a white-box test's blocking sync.
	if s.syncFile != nil {
		return s.syncFile(f)
	}
	//: the active file's fsync.
	return f.Sync()
}

// closeOpen closes the active descriptor; a sink whose last reopen failed
// holds none, and closing it releases nothing. The caller holds s.mu.
func (s *rotatingSink) closeOpen() error {
	//: no descriptor since a failed reopen: nothing to release.
	if s.f == nil {
		//: closed already.
		return nil
	}
	//: the active file's close.
	return s.f.Close()
}
