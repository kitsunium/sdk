//go:build unix || windows

package exec

import (
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// defaultDrainGrace is how long Wait waits for the capture copiers after the
// exit when Spec.DrainGrace is zero: a bounded default (ADR 0031), of the same
// order as Stop's SIGKILL resend, long enough for a child's last write to
// cross the pipe and short enough that a daemonised grandchild does not hold
// Wait for its lifetime.
const defaultDrainGrace time.Duration = time.Second

// Standard-stream fd indices and their count, named so the wiring carries no
// bare magic numbers. The values are the kernel's fixed fd numbers (0/1/2), so
// iota lines up with them and stdioFdCount is the natural fourth value.
const (
	fdStdin int = iota
	fdStdout
	fdStderr
	stdioFdCount
)

// stdioField tags a wrapped stdio-setup failure with the stream it concerns.
func stdioField(stream string) errs.FieldValue {
	//: a single "stdio" field names which stream's fd setup failed.
	return errs.String("stdio", stream)
}

// swallowErr intentionally discards a non-actionable cleanup error — a copier's
// drained-pipe io.Copy or a best-effort close — recording the discard so the
// error audit treats it as deliberate rather than dropped.
func swallowErr(err error) {
	//: read the parameter so the unused-error audit treats this as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
	//: the error concerns a drained pipe or an already-finished fd — nothing to do.
}

// stdioState carries the child's three standard-stream files and the parent-side
// machinery to manage them across the spawn. It is built before os.StartProcess,
// closed via closeAll on a spawn failure, or driven by afterStart on success;
// Handle.Wait joins its output copiers so every byte is delivered before Wait
// returns. outSrc/outSink are parallel: outSrc[i] drains into outSink[i].
type stdioState struct {
	files      [stdioFdCount]*os.File // {stdin, stdout, stderr} for os.ProcAttr.Files
	opened     []*os.File             // every fd buildStdio created (never os.Std*); closeAll target
	closeChild []*os.File             // the child's ends the parent closes after a successful spawn
	outSrc     []*os.File             // capture pipe read ends, parallel to outSink
	outSink    []io.Writer            // caller writers, parallel to outSrc
	outNames   []string               // "stdout" / "stderr", parallel to outSrc
	inDst      *os.File               // stdin pipe write end (nil when no capture stdin)
	inSrc      io.Reader              // caller's stdin source
	drainGrace time.Duration          // Spec.DrainGrace: the copier join's bound after the exit
	drainClk   clock.Waiter           // what drainGrace runs on: clock.System, a ManualClock in a test
	ptyMaster  *os.File               // StdioPTY: the terminal's master side, handed to the caller by TTY
	wg         sync.WaitGroup
	copyMu     sync.RWMutex // guards copyErr and held
	copyErr    error        // first output-copier failure (a caller writer erroring), surfaced by Wait
	held       []string     // the streams a drain cut found still open
	cut        atomic.Bool  // the drain grace closed and the parent's ends were closed
	inLive     atomic.Bool  // the stdin feeder has not finished
	delivered  atomic.Int64 // bytes the output copiers delivered to the caller's writers
}

// recordCopyErr latches the first output-copier failure so Wait can surface it.
func (s *stdioState) recordCopyErr(err error) {
	//: a nil copy error is the success path — nothing to record.
	if err == nil {
		//: the writer accepted every byte the child produced.
		return
	}
	s.copyMu.Lock()
	//: keep the first failure; later copiers usually report the same cause.
	if s.copyErr == nil {
		//: latch the first writer failure.
		s.copyErr = err
	}
	s.copyMu.Unlock()
}

// copyError returns the latched first output-copier failure, or nil when capture
// delivered cleanly. Read it after wait() has joined the copiers.
func (s *stdioState) copyError() error {
	s.copyMu.RLock()
	defer s.copyMu.RUnlock()
	//: the latched first writer failure, or nil on a clean capture.
	return s.copyErr
}

// buildStdio assembles the stdioState for spec.Stdio. Any fd-setup failure
// (os.Pipe / opening the null device) closes whatever was opened and returns a
// typed SPAWN_FAILED — the spawn cannot proceed without its standard streams.
func buildStdio(spec coreproc.Spec) (state *stdioState, err error) {
	wired := &stdioState{drainGrace: spec.DrainGrace, drainClk: clock.System}
	//: dispatch on the requested stdio mode.
	switch spec.Stdio {
	//: capture mode connects pipes to the caller's writers/reader.
	case coreproc.StdioCapture:
		//: wire the caller's streams; nil members fall back to the null device.
		if berr := wired.build(spec.Stdin, spec.Stdout, spec.Stderr); berr != nil {
			//: release any half-opened fds before surfacing the failure.
			wired.closeAll()
			//: propagate the typed SPAWN_FAILED verbatim.
			return nil, berr
		}
		//: a fully-wired capture state.
		return wired, nil
	//: null mode points every stream at the platform null device.
	case coreproc.StdioNull:
		//: all-nil streams route every fd to the null device.
		if berr := wired.build(nil, nil, nil); berr != nil {
			//: release any half-opened null fds before surfacing the failure.
			wired.closeAll()
			//: propagate the typed SPAWN_FAILED verbatim.
			return nil, berr
		}
		//: a fully-wired null state.
		return wired, nil
	//: pty mode gives all three streams one pseudo-terminal's slave side.
	case coreproc.StdioPTY:
		//: allocate the terminal; the platform says whether it can.
		if berr := buildPTYStdio(wired, spec.PTY); berr != nil {
			//: release a half-opened terminal before surfacing the failure.
			wired.closeAll()
			//: propagate the typed PTY_FAILED / UNSUPPORTED_PLATFORM verbatim.
			return nil, berr
		}
		//: a terminal-wired state.
		return wired, nil
	//: inherit (the default and any unknown mode) shares the parent's streams.
	default:
		//: os.Std* are never closed by this state, so they carry no cleanup.
		wired.files = [stdioFdCount]*os.File{os.Stdin, os.Stdout, os.Stderr}
		//: an inherit state needs no copiers or cleanup.
		return wired, nil
	}
}

// build wires stdin from in, stdout to out, and stderr to errw. A nil stream
// falls back to the null device (StdioNull passes all-nil; StdioCapture passes
// the caller's values, nil meaning "discard this one stream").
func (s *stdioState) build(in io.Reader, out, errw io.Writer) error {
	//: stdin first so a failure aborts before opening the output ends.
	if ierr := s.wireInput(in); ierr != nil {
		//: propagate the typed SPAWN_FAILED verbatim.
		return ierr
	}
	//: stdout next.
	if oerr := s.wireOutput(fdStdout, out); oerr != nil {
		//: propagate the typed SPAWN_FAILED verbatim.
		return oerr
	}
	//: stderr last; each output stream is independent.
	return s.wireOutput(fdStderr, errw)
}

// wireInput connects the child's fd 0. A nil reader gives an immediate-EOF stdin
// from the null device; otherwise a pipe feeds reader→child, closed at EOF by a
// fire-and-forget copier that never blocks Wait.
func (s *stdioState) wireInput(reader io.Reader) error {
	//: no reader means an empty stdin straight from the null device.
	if reader == nil {
		//: open the null device read-only for fd 0.
		return s.wireNull(fdStdin)
	}
	pr, pw, perr := os.Pipe()
	//: a pipe-creation failure is a typed SPAWN_FAILED (fd exhaustion, etc.).
	if perr != nil {
		//: wrap the os.Pipe cause under the central SPAWN_FAILED fields.
		return wrapSpawn(perr, stdioField("stdin"))
	}
	//: the child reads fd 0 from the pipe's read end.
	s.files[fdStdin] = pr
	s.opened = append(s.opened, pr, pw)
	//: the parent closes its copy of the child's read end after the spawn.
	s.closeChild = append(s.closeChild, pr)
	//: register the feeder; afterStart launches it fire-and-forget.
	s.inDst, s.inSrc = pw, reader
	//: a wired stdin feeder.
	return nil
}

// wireOutput connects the child's fd idx (1=stdout, 2=stderr). A nil writer
// discards to the null device; otherwise a pipe drains child→writer via a copier
// joined by wait(), so every byte lands before Wait returns.
func (s *stdioState) wireOutput(idx int, w io.Writer) error {
	//: no writer means the stream is discarded to the null device.
	if w == nil {
		//: open the null device write-only for this output fd.
		return s.wireNull(idx)
	}
	pr, pw, perr := os.Pipe()
	//: a pipe-creation failure is a typed SPAWN_FAILED.
	if perr != nil {
		//: wrap the os.Pipe cause under the central SPAWN_FAILED fields.
		return wrapSpawn(perr, stdioField(outName(idx)))
	}
	//: the child writes this stream to the pipe's write end.
	s.files[idx] = pw
	s.opened = append(s.opened, pr, pw)
	//: the parent closes its copy of the child's write end after the spawn so the
	//: read end sees EOF once the child exits.
	s.closeChild = append(s.closeChild, pw)
	//: register the drain (read end + sink); afterStart launches it.
	s.outSrc = append(s.outSrc, pr)
	s.outSink = append(s.outSink, w)
	s.outNames = append(s.outNames, outName(idx))
	//: a wired output stream.
	return nil
}

// sharedNull is the null device every null stream of every spawn uses (#943):
// opened once, read-write and close-on-exec, never closed. sharedNullMu
// serialises the first open; later spawns read the pointer alone.
var (
	sharedNull   atomic.Pointer[os.File]
	sharedNullMu sync.Mutex
)

// nullDevice returns the process's shared null device, opening it on first
// use. A failed open is not remembered: the next spawn tries again.
func nullDevice() (*os.File, error) {
	//: the common case: already open.
	if f := sharedNull.Load(); f != nil {
		//: no syscall.
		return f, nil
	}
	sharedNullMu.Lock()
	defer sharedNullMu.Unlock()
	//: another spawn opened it while this one waited.
	if f := sharedNull.Load(); f != nil {
		//: theirs.
		return f, nil
	}
	f, err := openNull(os.O_RDWR)
	//: no null device to share.
	if err != nil {
		//: the typed SPAWN_FAILED.
		return nil, err
	}
	sharedNull.Store(f)
	//: open for the life of the process.
	return f, nil
}

// wireNull points the child's fd idx at the shared null device, read-write so
// it serves stdin and the outputs alike. It is on no cleanup list: the child
// gets its own copy through dup2, and closing the parent's would take it from
// every later spawn.
func (s *stdioState) wireNull(idx int) error {
	f, oerr := nullDevice()
	//: a null-open failure aborts the wiring.
	if oerr != nil {
		//: propagate the typed SPAWN_FAILED verbatim.
		return oerr
	}
	//: the child uses the shared null descriptor directly.
	s.files[idx] = f
	//: a wired null stream.
	return nil
}

// openNull opens the platform null device with flag and returns the descriptor;
// the caller owns and eventually closes it. Returning the fd (rather than storing
// it inline) keeps ownership transfer explicit.
func openNull(flag int) (f *os.File, err error) {
	null, oerr := os.OpenFile(os.DevNull, flag, 0)
	//: a null-device open failure is a typed SPAWN_FAILED.
	if oerr != nil {
		//: wrap the open cause under the central SPAWN_FAILED fields.
		return nil, wrapSpawn(oerr, stdioField("null"))
	}
	//: caller owns the returned null descriptor.
	return null, nil
}

// afterStart runs once os.StartProcess has succeeded: the child holds its own dup
// of every fd, so the parent closes its child-side copies (making EOF propagate
// on child exit) and launches the copiers. Output copiers are tracked by the
// WaitGroup; the stdin feeder is fire-and-forget so a non-EOF reader never blocks
// Wait.
func (s *stdioState) afterStart() {
	//: close the parent's copies of the child-side ends so EOF propagates.
	for _, f := range s.closeChild {
		//: a close error here is benign cleanup detail.
		swallowErr(f.Close())
	}
	//: launch each output drain under the WaitGroup.
	for i := range s.outSrc {
		s.wg.Add(1)
		//: drainOutput owns the read end and signals the WaitGroup on EOF.
		go s.drainOutput(s.outSrc[i], s.outSink[i])
	}
	//: feed stdin without joining when a capture feeder is registered.
	if s.inDst != nil {
		//: a drain cut reports stdin as held until the feeder ends.
		s.inLive.Store(true)
		//: fire-and-forget so Wait never blocks on the source reader.
		go s.feedInput(s.inDst, s.inSrc)
	}
}

// drainOutput copies the pipe read end src into sink until EOF, then releases src
// and signals the WaitGroup. A writer failure is latched (recordCopyErr) so Wait
// can surface it as StdioCaptureFailed — output is not silently truncated.
func (s *stdioState) drainOutput(src io.ReadCloser, sink io.Writer) {
	//: signal completion so wait() can join once the pipe drains.
	defer s.wg.Done()
	//: copy every byte the child emits into the caller's sink.
	n, cerr := io.Copy(sink, src)
	s.delivered.Add(n)
	//: a read end the drain cut closed is the grace closing, not a writer fault.
	if s.cut.Load() && errors.Is(cerr, os.ErrClosed) {
		//: name the stream that was still held.
		s.recordHeld(src)
		//: the cut already released the read end.
		return
	}
	//: latch a writer failure (e.g. a full disk) so Wait reports it, not silence.
	s.recordCopyErr(cerr)
	//: the child has closed its write end; release the read end.
	swallowErr(src.Close())
}

// recordHeld notes the stream of src as one the drain cut found still open.
func (s *stdioState) recordHeld(src io.ReadCloser) {
	s.copyMu.Lock()
	defer s.copyMu.Unlock()
	//: the parallel slices name the read end's stream.
	for i, out := range s.outSrc {
		//: the copier's own read end.
		if io.ReadCloser(out) == src {
			//: its stream name, for the error's held field.
			s.held = append(s.held, s.outNames[i])
		}
	}
}

// feedInput copies the caller's reader src into the pipe write end dst, then
// closes dst so the child's stdin sees EOF. It is fire-and-forget: never joined,
// so a reader that does not EOF cannot block Wait.
func (s *stdioState) feedInput(dst io.WriteCloser, src io.Reader) {
	//: feed reader→child until EOF or a child-gone write error.
	_, cerr := io.Copy(dst, src)
	//: a feed fault on a fire-and-forget copier is non-actionable.
	swallowErr(cerr)
	//: closing the write end propagates EOF to the child's stdin.
	swallowErr(dst.Close())
	//: the feeder is done: a later drain cut does not report stdin.
	s.inLive.Store(false)
}

// drainBounded joins the output copiers once the exit status is in hand,
// within s.drainGrace on s.drainClk (#1088): the zero grace is defaultDrainGrace, a
// negative one joins without a bound, as Wait always did. A copier blocks
// until every holder of its pipe's write end is gone, and a process the child
// left behind — a daemon, `sleep 60 &` — holds it past the child's exit. When
// the grace closes first, the parent's read ends and stdin write end are
// closed, so each copier returns at once (the pipes are pollable, and Windows
// cancels a pipe's pending read on close), and cut reports it. held names the
// streams that were still open; delivered counts the bytes the copiers handed
// to the caller's writers.
//
// Goroutine lifecycle: it launches one joiner, which returns once every copier
// has — at EOF, or at once after the cut — so none outlives the call.
func (s *stdioState) drainBounded() (cut bool, held []string, delivered int64) {
	//: inherit / null stdio has no copier; a negative grace keeps the old join.
	if len(s.outSrc) == 0 || s.drainGrace < 0 {
		//: wait for EOF however long the pipes stay open.
		s.wg.Wait()
		//: nothing was cut.
		return false, nil, s.delivered.Load()
	}
	grace := s.drainGrace
	//: the zero grace is the bounded default, never an unbounded wait.
	if grace == 0 {
		//: one second (ADR 0031).
		grace = defaultDrainGrace
	}
	joined := make(chan struct{})
	//: the joiner turns the WaitGroup into something select can race.
	go func() {
		//: every copier returned.
		s.wg.Wait()
		close(joined)
	}()
	clk := s.drainClk
	//: a state built by hand carries no clock: the system's.
	if clk == nil {
		clk = clock.System
	}
	timer := clk.NewTimer(grace)
	defer timer.Stop()
	//: the copiers reach EOF, or the grace closes.
	select {
	case <-joined:
		//: every byte delivered within the grace.
		return false, nil, s.delivered.Load()
	case <-timer.C():
		//: the pipes are still held — cut them below.
	}
	//: cut every read end and the stdin write end: the copiers return.
	return s.cutDrain(joined)
}

// cutDrain closes the parent's pipe ends after the drain grace closed and
// waits for the copiers, which return at once; see drainBounded.
func (s *stdioState) cutDrain(joined <-chan struct{}) (cut bool, held []string, delivered int64) {
	//: the copiers read a closed read end as the cut, not as a writer fault.
	s.cut.Store(true)
	//: closing a read end returns its blocked Read with os.ErrClosed.
	for _, src := range s.outSrc {
		//: a copier that reached EOF already closed it; the error is benign.
		swallowErr(src.Close())
	}
	stdinHeld := s.inLive.Load()
	//: the feeder may still be writing into a stdin the child no longer reads.
	if stdinHeld {
		//: its next write fails and it ends.
		swallowErr(s.inDst.Close())
	}
	//: the copiers return now that their read ends are closed.
	<-joined
	s.copyMu.RLock()
	held = append(held, s.held...)
	s.copyMu.RUnlock()
	//: the stdin feeder was still running.
	if stdinHeld {
		held = append(held, "stdin")
	}
	//: a copier that met EOF as the grace closed was not held after all.
	return len(held) > 0, held, s.delivered.Load()
}

// closeAll releases every fd buildStdio opened. It is used only on the spawn
// failure path (before afterStart), so it never races the copiers and never
// touches os.Std*.
func (s *stdioState) closeAll() {
	//: close every opened pipe/null fd; os.Std* are never in this list.
	for _, f := range s.opened {
		//: a close error during failure cleanup is non-actionable.
		swallowErr(f.Close())
	}
}

// outName maps an output fd index to its stream name for diagnostics.
func outName(idx int) string {
	//: fd 1 is stdout; every other output fd is stderr.
	if idx == fdStdout {
		//: the stdout stream name.
		return "stdout"
	}
	//: every other output fd is stderr.
	return "stderr"
}
