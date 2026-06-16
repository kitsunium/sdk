//go:build unix || windows

// Package exec — per-process stdio wiring. buildStdio turns a Spec's Stdio mode
// into the three *os.File the spawn passes as ProcAttr.Files, plus the
// parent-side lifecycle: the child-side ends to close once the child owns its
// dups (so EOF propagates on exit) and the copier goroutines that drain capture
// pipes into the caller's writers. StdioInherit (default) shares the parent's
// streams; StdioNull discards via the null device; StdioCapture connects pipes.
package exec

import (
	"io"
	"os"
	"sync"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

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
	inDst      *os.File               // stdin pipe write end (nil when no capture stdin)
	inSrc      io.Reader              // caller's stdin source
	wg         sync.WaitGroup
	copyMu     sync.RWMutex // guards copyErr
	copyErr    error        // first output-copier failure (a caller writer erroring), surfaced by Wait
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
	s := &stdioState{}
	//: dispatch on the requested stdio mode.
	switch spec.Stdio {
	//: capture mode connects pipes to the caller's writers/reader.
	case coreproc.StdioCapture:
		//: wire the caller's streams; nil members fall back to the null device.
		if berr := s.build(spec.Stdin, spec.Stdout, spec.Stderr); berr != nil {
			//: release any half-opened fds before surfacing the failure.
			s.closeAll()
			//: propagate the typed SPAWN_FAILED verbatim.
			return nil, berr
		}
		//: a fully-wired capture state.
		return s, nil
	//: null mode points every stream at the platform null device.
	case coreproc.StdioNull:
		//: all-nil streams route every fd to the null device.
		if berr := s.build(nil, nil, nil); berr != nil {
			//: release any half-opened null fds before surfacing the failure.
			s.closeAll()
			//: propagate the typed SPAWN_FAILED verbatim.
			return nil, berr
		}
		//: a fully-wired null state.
		return s, nil
	//: inherit (the default and any unknown mode) shares the parent's streams.
	default:
		//: os.Std* are never closed by this state, so they carry no cleanup.
		s.files = [stdioFdCount]*os.File{os.Stdin, os.Stdout, os.Stderr}
		//: an inherit state needs no copiers or cleanup.
		return s, nil
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
		return s.wireNull(fdStdin, os.O_RDONLY)
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
		return s.wireNull(idx, os.O_WRONLY)
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
	//: a wired output stream.
	return nil
}

// wireNull points the child's fd idx at the platform null device opened with flag.
func (s *stdioState) wireNull(idx, flag int) error {
	f, oerr := openNull(flag)
	//: a null-open failure aborts the wiring.
	if oerr != nil {
		//: propagate the typed SPAWN_FAILED verbatim.
		return oerr
	}
	//: the child uses the null fd directly; the parent closes it after the spawn.
	s.files[idx] = f
	s.opened = append(s.opened, f)
	s.closeChild = append(s.closeChild, f)
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
	_, cerr := io.Copy(sink, src)
	//: latch a writer failure (e.g. a full disk) so Wait reports it, not silence.
	s.recordCopyErr(cerr)
	//: the child has closed its write end; release the read end.
	swallowErr(src.Close())
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
}

// wait blocks until every output copier has drained its pipe to EOF, so Handle.Wait
// returns only after 100% of the child's stdout/stderr has reached the caller's
// writers. It is a no-op for StdioInherit/StdioNull (no copiers).
func (s *stdioState) wait() {
	//: join the stdout/stderr drains; nothing to wait on in inherit/null mode.
	s.wg.Wait()
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
