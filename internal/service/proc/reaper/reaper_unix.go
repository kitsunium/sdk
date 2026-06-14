//go:build unix

// Package reaper — Unix SIGCHLD waitpid reaping loop (all Unix). The
// PR_SET_CHILD_SUBREAPER arming lives in the Linux-only sibling.
package reaper

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// exitOSErr is sysexits.h EX_OSERR (71): the exit status the proc domain assigns
// to OS-level operation failures, restated here so a wrapped errno carries the
// same exit semantics as the central ReapFailed/SubreaperFailed sentinels.
const exitOSErr int = 71

// unixReaper is the Unix coreproc.Reaper: a SIGCHLD-driven waitpid loop that
// drains every terminated child to ECHILD on each signal, with a final drain on
// Stop so shutdown leaves no zombies. One concrete reaper type per file.
type unixReaper struct {
	// onReap mirrors config.onReap: an optional post-sweep observer.
	onReap func(int)
	// mu guards running/sigCh/done/closeDone/lastErr across Start/Stop and the
	// concurrent LastError read so repeated Start/Stop cycles neither
	// double-install a handler nor leak a goroutine. Wait4 itself is
	// kernel-serialised and needs no extra lock.
	mu sync.RWMutex
	// running reports whether the background loop goroutine is live; it makes
	// Start idempotent and Stop safe without a prior Start. It stays true until
	// the goroutine has fully exited so a concurrent Stop cannot observe a torn
	// state and return before the loop is gone.
	running bool
	// stopping reports that a Stop for the current cycle is already in flight; a
	// second concurrent Stop joins the same stopped channel instead of issuing a
	// duplicate close, and Start refuses to launch a new loop until shutdown
	// completes so a fresh cycle can never overlap a draining one.
	stopping bool
	// sigCh receives SIGCHLD notifications from os/signal while running; the loop
	// owns its lifecycle and detaches it via defer signal.Stop on exit.
	sigCh chan os.Signal
	// done requests the loop's final drain and exit; closeDone guards the close
	// so even a mis-sequenced concurrent Stop never double-closes the channel.
	done chan struct{}
	// closeDone closes done exactly once per Start/Stop cycle; it is reset to a
	// fresh Once on every Start so a later cycle can close again.
	closeDone *sync.Once
	// stopped is closed by the loop just before it returns, letting Stop block
	// until the goroutine has fully exited (no goroutine leak across cycles).
	stopped chan struct{}
	// lastErr records the most recent error a background sweep hit (nil on a
	// clean sweep). It is guarded by mu and surfaced via LastError so a
	// background reap failure is observable without blocking the loop.
	lastErr error
}

// New returns a Unix reaper configured by opts. The reaper is created idle; call
// Start to begin background reaping. It does not arm subreaper mode — call
// SetChildSubreaper separately when the supervisor is not PID1.
func New(opts ...Option) coreproc.Reaper {
	//: fold the options so the reaper carries only resolved values.
	cfg := resolve(opts)
	//: hand back an idle reaper; channels are created lazily in Start.
	return &unixReaper{onReap: cfg.onReap}
}

// Start begins the background SIGCHLD loop. It is idempotent: a second Start
// while already running is a no-op.
func (r *unixReaper) Start() {
	//: serialise lifecycle mutation against Stop and concurrent Start.
	r.mu.Lock()
	//: release the lock on every Start path.
	defer r.mu.Unlock()
	//: a second Start while running, or a Start racing an in-flight Stop, must
	//: not install a second handler or overlap a draining cycle.
	if r.running || r.stopping {
		//: already running or still tearing down — return without side effects.
		return
	}
	//: fresh per-cycle channels so a later Start after Stop starts clean.
	r.sigCh = make(chan os.Signal, 1)
	r.done = make(chan struct{})
	r.stopped = make(chan struct{})
	//: a fresh Once per cycle so this cycle's done can be closed exactly once.
	r.closeDone = &sync.Once{}
	r.running = true
	//: run the drain loop on its own goroutine until Stop closes done; the loop
	//: owns the SIGCHLD subscription so Notify and signal.Stop stay paired.
	go r.loop(r.sigCh, r.done, r.stopped)
}

// loop drains children on each SIGCHLD until done is closed, then performs one
// final drain so no zombie outlives Stop, detaches the signal handler, and
// closes stopped to acknowledge.
func (r *unixReaper) loop(sigCh chan os.Signal, done, stopped chan struct{}) {
	//: subscribe to SIGCHLD here so Notify and its defer signal.Stop are paired.
	signal.Notify(sigCh, syscall.SIGCHLD)
	//: detach the SIGCHLD handler when the loop returns so it never leaks.
	defer signal.Stop(sigCh)
	//: announce exit exactly once so Stop can block until we have returned. The
	//: defers run LIFO, so this close fires AFTER markStopped clears the cycle
	//: flags — every Stop waiter therefore observes a fully idle reaper on wake.
	defer close(stopped)
	//: clear running/stopping under the lock before the close above unblocks any
	//: Stop caller, so the very first waiter to wake sees a clean cycle and a
	//: later Start can launch a fresh loop without racing this one.
	defer r.markStopped()
	//: an initial drain catches children that exited before we subscribed.
	r.drain()
	//: react to signals until shutdown is requested.
	for {
		//: wait for either a SIGCHLD or the stop request.
		select {
		case _, ok := <-sigCh:
			//: a closed sigCh would spin; only drain on a real delivery.
			if !ok {
				//: the channel was closed unexpectedly — stop looping.
				return
			}
			//: a child changed state — drain every reapable child to ECHILD.
			r.drain()
		case <-done:
			//: shutdown requested — one last sweep, then exit the goroutine.
			r.drain()
			//: final drain complete; the deferred signal.Stop/close run now.
			return
		}
	}
}

// Stop ends the background loop after a final draining sweep and blocks until
// the goroutine has exited. It is safe to call without a prior Start and is
// idempotent across repeated calls.
func (r *unixReaper) Stop() {
	//: serialise against Start and concurrent Stop, capturing the stopped chan.
	r.mu.Lock()
	//: a Stop with no live loop (never started, or already fully stopped) is a
	//: no-op. running stays true through teardown, so this only fires when no
	//: cycle is active.
	if !r.running {
		//: nothing to tear down — release and return without side effects.
		r.mu.Unlock()
		//: no live loop to stop.
		return
	}
	//: capture the per-cycle channel so every caller blocks on the same barrier.
	stopped := r.stopped
	//: a Stop is already draining this cycle — join its barrier rather than
	//: issue a duplicate close, and return only once the loop has truly exited.
	if r.stopping {
		//: release before blocking so the initiating Stop can finish teardown.
		r.mu.Unlock()
		//: wait for the in-flight Stop's loop to exit — a full barrier.
		<-stopped
		//: the loop is gone; this concurrent Stop has synchronised with exit.
		return
	}
	//: this is the initiating Stop for the cycle — capture done and its closer.
	done := r.done
	closeDone := r.closeDone
	//: mark the cycle as tearing down so concurrent Starts and Stops defer to
	//: this one; running stays true until the goroutine is actually joined.
	r.stopping = true
	r.mu.Unlock()
	//: request the loop's final drain and exit; the Once guards double-close.
	closeDone.Do(func() {
		//: close exactly once for this cycle.
		close(done)
	})
	//: block until the loop has finished its final drain (no goroutine leak); the
	//: loop clears running/stopping before it closes stopped, so on wake the
	//: cycle is already idle and a later Start can launch cleanly.
	<-stopped
}

// markStopped clears the per-cycle running/stopping flags under the lock. The
// loop runs it via defer just before it closes stopped, so every Stop caller
// that wakes on <-stopped observes a fully idle reaper rather than a torn,
// mid-teardown state.
func (r *unixReaper) markStopped() {
	//: take the lock so the flag clear is atomic against Start/Stop/LastError.
	r.mu.Lock()
	//: the loop is exiting — the cycle is no longer running or stopping.
	r.running = false
	r.stopping = false
	r.mu.Unlock()
}

// ReapOnce performs a single non-blocking drain sweep and reports how many
// children were collected. It is safe to call concurrently with the background
// loop: Wait4 is kernel-serialised, so the count is simply split across whoever
// observes each exit.
func (r *unixReaper) ReapOnce() (reaped int, err error) {
	//: delegate to the shared drain, which also fires the onReap observer.
	return r.drainResult()
}

// drain runs a sweep on the loop's own path, recording any error into lastErr
// so a background failure stays observable via LastError while the loop keeps
// running (the next SIGCHLD retries). The count surfaces through onReap.
func (r *unixReaper) drain() {
	//: run the sweep; its count is reported by onReap, its error stored below.
	_, err := r.drainResult()
	//: record the outcome so LastError reflects the latest background sweep.
	r.mu.Lock()
	//: store nil on a clean sweep so a prior error does not stick forever.
	r.lastErr = err
	r.mu.Unlock()
}

// LastError reports the error from the most recent background drain sweep, or
// nil if the last sweep was clean. It is safe to call concurrently with the
// loop. ReapOnce returns its own error directly; this is only for the loop path.
func (r *unixReaper) LastError() error {
	//: a read lock serialises against the loop's recording write.
	r.mu.RLock()
	//: copy under the lock so the returned value is a stable snapshot.
	err := r.lastErr
	r.mu.RUnlock()
	//: hand back the latest background sweep error (nil when clean).
	return err
}

// drainResult repeatedly calls Wait4 with WNOHANG until no further child is
// reapable, returning the count and (on a non-ECHILD failure) a ReapFailed
// error. ECHILD ("no children") and 0 ("children exist, none exited") both end
// the sweep without error.
func (r *unixReaper) drainResult() (reaped int, err error) {
	//: drain until Wait4 reports nothing more to collect.
	for {
		var ws syscall.WaitStatus
		var ru syscall.Rusage
		//: non-blocking reap of any child (-1) that has changed state.
		pid, waitErr := syscall.Wait4(-1, &ws, syscall.WNOHANG, &ru)
		//: a non-nil error needs classification: transient, benign, or fatal.
		if waitErr != nil {
			//: classify the errno; benign ECHILD ends the sweep cleanly.
			done, fatal := r.classifyWaitErr(waitErr, reaped)
			//: EINTR returns done=false to retry the same sweep step.
			if !done {
				//: transient interruption — loop again without counting.
				continue
			}
			//: a fatal errno carries the wrapped sentinel; ECHILD carries nil.
			return reaped, fatal
		}
		//: pid <= 0 with no error means no child was ready — sweep is drained.
		if pid <= 0 {
			//: report the tally to the observer before returning.
			r.fireOnReap(reaped)
			//: a fully drained sweep returns its count with no error.
			return reaped, nil
		}
		//: a positive pid was collected — count it and keep draining.
		reaped++
	}
}

// classifyWaitErr maps a non-nil Wait4 errno to (done, fatal): done=false means
// retry (EINTR); done=true with fatal=nil means a benign ECHILD end; done=true
// with a non-nil fatal means a wrapped ReapFailed. It fires the onReap observer
// on every terminal outcome so the sweep count is always reported once.
func (r *unixReaper) classifyWaitErr(waitErr error, reaped int) (done bool, fatal error) {
	//: EINTR — the call was interrupted; signal a retry without ending.
	if waitErr == syscall.EINTR {
		//: not terminal — caller continues the same sweep.
		return false, nil
	}
	//: ECHILD means no children remain — a clean, terminal end, not an error.
	if waitErr == syscall.ECHILD {
		//: report the tally reaped before children ran out.
		r.fireOnReap(reaped)
		//: terminal but benign — done with no fatal error.
		return true, nil
	}
	//: any other errno is a real fault — report the tally and wrap the sentinel.
	r.fireOnReap(reaped)
	//: terminal failure carrying the central ReapFailed sentinel.
	return true, errs.Wrap(waitErr, errs.WrapParams{
		Code:     coreproc.CodeReapFailed,
		Reason:   "REAP_FAILED",
		Public:   "Could not reap child processes",
		Private:  "service/proc/reaper.ReapOnce: wait4 failed with an error other than ECHILD",
		ExitCode: exitOSErr,
	}, errs.Int("reaped", reaped))
}

// fireOnReap invokes the optional post-sweep observer with n, if one is set.
func (r *unixReaper) fireOnReap(n int) {
	//: skip the call entirely when no observer was registered.
	if r.onReap == nil {
		//: no hook — nothing to do.
		return
	}
	//: notify the observer with the sweep's reaped count.
	r.onReap(n)
}

// IsPID1 reports whether the current process is the init process (pid 1), the
// canonical signal that this process is the system's ultimate reaper.
func IsPID1() bool {
	//: pid 1 is, by definition, the init/PID1 process.
	return os.Getpid() == 1
}
