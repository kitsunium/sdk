package exec

import (
	"context"
	"errors"
	"sync"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// termSignal is SIGTERM: 15 on every Unix and in Go's Windows signal table,
// named here so Run compiles on every GOOS, plan9 included.
const termSignal coreproc.Signal = 15

// Run starts spec, waits for it, and returns its exit with the head of its
// output, bounded however much the child writes: stdout and stderr are captured
// into buffers that keep the first opts.MaxOutputBytes bytes each and discard the
// rest while still reading, so the child never blocks on a full pipe and the
// supervisor never holds more than the ceiling. spec's Stdio, Stdout and Stderr
// are replaced; its Stdin is kept. When ctx ends first the child is stopped —
// SIGTERM, opts.Grace, then SIGKILL — and Run returns what it had with ctx's
// error. A MaxOutputBytes that is not positive is refused with InvalidSpec
// before anything is spawned.
func Run(ctx context.Context, spec coreproc.Spec, opts coreproc.RunValue) (result coreproc.ResultValue, err error) {
	//: a ceiling of zero keeps nothing: refused rather than inert (ADR 0031).
	if opts.MaxOutputBytes <= 0 {
		//: nothing spawned.
		return coreproc.ResultValue{}, coreproc.InvalidSpec
	}
	stdout := &headBuffer{limit: opts.MaxOutputBytes}
	stderr := &headBuffer{limit: opts.MaxOutputBytes}
	spec.Stdio, spec.Stdout, spec.Stderr = coreproc.StdioCapture, stdout, stderr
	p, sErr := Start(ctx, spec)
	//: a refused or failed spawn has no output to report.
	if sErr != nil {
		//: the typed Start error verbatim.
		return coreproc.ResultValue{}, sErr
	}
	exit, wErr := waitOrStop(ctx, p, opts.Grace)
	//: the exit and the head of each stream, whatever ended the wait.
	return coreproc.ResultValue{
		Exit:      exit,
		Stdout:    stdout.bytes(),
		Stderr:    stderr.bytes(),
		Truncated: stdout.truncated() || stderr.truncated(),
	}, wErr
}

// waitOrStop waits for p within ctx; when ctx ends first it stops p with
// SIGTERM and grace, collects the exit, and reports ctx's error beside it.
func waitOrStop(ctx context.Context, p coreproc.Process, grace time.Duration) (exit coreproc.ExitValue, err error) {
	cw, bounded := p.(coreproc.ContextWaiter)
	//: a handle without the sibling port can only be waited on whole.
	if !bounded {
		//: the plain, unbounded wait.
		return p.Wait()
	}
	exit, err = cw.WaitContext(ctx)
	ctxErr := ctx.Err()
	//: the wait ended on its own: the exit, and Wait's own error.
	if ctxErr == nil || !errors.Is(err, ctxErr) {
		//: the outcome stands.
		return exit, err
	}
	//: ctx ended first: stop the child, uncancelled — the SIGKILL resend bounds it.
	stopErr := p.Stop(context.WithoutCancel(ctx), grace, termSignal)
	exit, waitErr := p.Wait()
	//: ctx's error leads; a stop or a wait that failed is joined to it.
	return exit, errors.Join(ctx.Err(), stopErr, waitErr)
}

// headBuffer is a capture sink that keeps the first limit bytes written to it
// and accepts, then discards, the rest: the copier keeps draining the pipe, so
// the child never blocks, and the memory held never exceeds limit.
type headBuffer struct {
	mu    sync.Mutex
	limit int
	buf   []byte
	over  bool
}

// Write keeps what fits under the limit and reports every byte as written.
func (b *headBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	keep := min(len(p), b.limit-len(b.buf))
	//: past the ceiling the bytes are read and dropped.
	b.over = b.over || keep < len(p)
	b.grow(keep)
	b.buf = append(b.buf, p[:keep]...)
	//: the whole write is accepted: the copier must not stop draining.
	return len(p), nil
}

// grow makes room for n more bytes by doubling, capped at the limit, so the
// buffer's allocations total at most twice the limit — append's own growth,
// a quarter at a time for a large slice, allocates five times what it keeps.
func (b *headBuffer) grow(n int) {
	need := len(b.buf) + n
	//: the room is already there.
	if need <= cap(b.buf) {
		//: nothing to allocate.
		return
	}
	grown := make([]byte, len(b.buf), min(max(need, 2*cap(b.buf)), b.limit))
	copy(grown, b.buf)
	b.buf = grown
}

// bytes returns what was kept.
func (b *headBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: the head of the stream, at most limit bytes.
	return b.buf
}

// truncated reports whether more than limit bytes were written.
func (b *headBuffer) truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	//: the ceiling was crossed.
	return b.over
}
