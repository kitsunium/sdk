// Package signal — white-box tests for the translation goroutine and its two
// helpers. All three are unexported and all three carry a decision that Notify's
// surface cannot show: what happens to a carrier the runtime should never send,
// and what happens when teardown races a delivery.
package signal

import (
	"os"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// helperTimeout bounds every wait on something that must already be in flight.
const helperTimeout time.Duration = 10 * time.Second

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
		{"SIGUSR1", syscall.SIGUSR1, coreproc.Signal(syscall.SIGUSR1)},
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

		//: no osSigs: registering for real would make this test depend on
		//: process-wide signal delivery, which is what the black-box test
		//: covers. The translation loop is the subject here.
		go translate(src, dst, done, ready, nil)

		select {
		case <-ready:
		case <-time.After(helperTimeout):
			t.Fatal("translate never signalled readiness")
		}

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
