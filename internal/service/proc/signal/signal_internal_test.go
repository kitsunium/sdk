// Package signal — white-box tests for the translation goroutine and its two
// helpers. All three are unexported and all three carry a decision that Notify's
// surface cannot show: what happens to a carrier the runtime should never send,
// and what happens when teardown races a delivery.
package signal

import (
	"os"
	ossignal "os/signal"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// helperTimeout bounds every wait on something that must already be in flight.
const helperTimeout time.Duration = 10 * time.Second

// quietSignal is the signal Test_translate registers for: one no test in this
// binary raises and the Go runtime does not use, so the registration window
// before the subscription is detached catches nothing at all.
//
// It is chosen from the set every GOOS defines — SIGUSR1, SIGURG and SIGXFSZ do
// not exist on Windows — because the translation loop itself is
// platform-neutral and deserves coverage everywhere the package builds.
const quietSignal syscall.Signal = syscall.SIGALRM

// fakeSignal is an os.Signal that is NOT a syscall.Signal, which is the carrier
// toSignal has to defend against. os/signal never delivers one, but a caller
// holding the channel could.
type fakeSignal struct{}

// String names the fake for diagnostics.
func (fakeSignal) String() string { return "fake" }

// Signal marks the type as an os.Signal.
func (fakeSignal) Signal() {}

// Test_toSignal pins the re-keying and its fallback. A non-syscall carrier must
// collapse to the reserved invalid signal rather than panicking: a panic in the
// translator would take the whole process down for a value that is merely
// unexpected.
func Test_toSignal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   os.Signal
		want coreproc.Signal
	}
	tests := []tc{
		{"SIGTERM", syscall.SIGTERM, coreproc.Signal(syscall.SIGTERM)},
		{"SIGINT", syscall.SIGINT, coreproc.Signal(syscall.SIGINT)},
		{"SIGHUP", syscall.SIGHUP, coreproc.Signal(syscall.SIGHUP)},
		{"a carrier that is not a syscall signal", fakeSignal{}, coreproc.Signal(0)},
		{"a nil carrier", nil, coreproc.Signal(0)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := toSignal(c.in); got != c.want {
			t.Errorf("toSignal(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_forward pins that a delivery never wins over teardown. Without the
// select, a translator blocked sending into a channel nobody is reading would
// outlive its own stop call — the goroutine leak the whole done-channel design
// exists to prevent.
func Test_forward(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the destination buffer; zero means an unbuffered channel nobody
		//: reads, so the send can only complete if teardown loses.
		buffer    int
		closeDone bool
		want      bool
	}
	tests := []tc{
		{"a buffered destination accepts", 1, false, true},
		{"a roomy destination accepts", 8, false, true},
		{"teardown wins over a blocked send", 0, true, false},
		//: both are ready: the send must still be allowed to complete, since
		//: dropping a deliverable signal is a real loss.
		{"a buffered destination with teardown pending", 1, true, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dst := make(chan coreproc.Signal, c.buffer)
		done := make(chan struct{})
		if c.closeDone {
			close(done)
		}

		got := forward(dst, done, coreproc.Signal(syscall.SIGTERM))

		//: the "both ready" case is a coin toss by design — select picks at
		//: random — so it is only asserted not to block.
		if c.buffer > 0 && c.closeDone {
			return
		}
		if got != c.want {
			t.Fatalf("forward() = %v, want %v", got, c.want)
		}
		if !got {
			return
		}
		//: a reported delivery must actually be on the channel.
		select {
		case sig := <-dst:
			if sig != coreproc.Signal(syscall.SIGTERM) {
				t.Errorf("delivered %v, want SIGTERM", sig)
			}
		default:
			t.Error("forward reported a delivery but the channel is empty")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_translate pins the goroutine's lifecycle end to end: it signals
// readiness only once the registration is live, forwards translated values, and
// closes the destination from its defer on every exit path.
//
// The close is the load-bearing part. It is how a reader learns the stream
// ended, and it is also the only observable proof the goroutine returned —
// which is why Notify's teardown needs nothing more than one channel close.
func Test_translate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how the translator is torn down: by closing done, or by closing the
		//: source out from under it.
		closeSource bool
		//: signals pushed through the source before teardown.
		sends []syscall.Signal
	}
	tests := []tc{
		{"teardown with nothing in flight", false, nil},
		{"teardown after one delivery", false, []syscall.Signal{syscall.SIGTERM}},
		{"teardown after several deliveries", false, []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP}},
		//: a closed source is not something os/signal does, but the loop guards
		//: against it rather than hot-spinning on a permanently ready channel.
		{"a source closed under the translator", true, nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		src := make(chan os.Signal, len(c.sends)+1)
		dst := make(chan coreproc.Signal, len(c.sends)+1)
		done := make(chan struct{})
		ready := make(chan struct{})

		//: translate registers src with os/signal unconditionally, and an EMPTY
		//: list means "every signal" — os/signal documents it that way. This
		//: channel would then receive the SIGURG the Go scheduler raises for
		//: preemption, many times a second, plus everything the sibling tests
		//: send themselves. Naming one signal nothing raises keeps the window
		//: empty; detaching below closes it altogether.
		go translate(src, dst, done, ready, []os.Signal{quietSignal})

		select {
		case <-ready:
		case <-time.After(helperTimeout):
			t.Fatal("translate never signalled readiness")
		}
		//: detach the subscription now that registration is proven live. The
		//: subject here is the translation LOOP, which this test drives by hand;
		//: a real delivery landing in src would be forwarded into dst and read
		//: as one of the values asserted below, and in the closed-source case it
		//: would panic the runtime with a send on a closed channel. Stop
		//: guarantees no further send once it returns, which is what makes this
		//: test independent of whatever signals its siblings raise.
		ossignal.Stop(src)

		for _, sig := range c.sends {
			src <- sig
			select {
			case got := <-dst:
				if got != coreproc.Signal(sig) {
					t.Fatalf("translated %v into %v", sig, got)
				}
			case <-time.After(helperTimeout):
				t.Fatalf("translate never forwarded %v", sig)
			}
		}

		if c.closeSource {
			//: safe to close only because the subscription was detached above:
			//: os/signal sends without knowing whether the channel is still
			//: open, and a delivery landing here would panic the runtime's own
			//: goroutine, where nothing recovers it.
			close(src)
		} else {
			close(done)
		}

		//: whichever way it ended, the defer must close dst.
		select {
		case _, ok := <-dst:
			if ok {
				t.Fatal("dst yielded a value after teardown, want it closed")
			}
		case <-time.After(helperTimeout):
			t.Fatal("translate did not close dst — the goroutine is still running")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
