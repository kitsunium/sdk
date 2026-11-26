package consensus

import (
	"math"
	"runtime"
	"testing"
	"time"
)

// The step function keeps a forward's answer for twice the resend window
// its transport states: the TCP transport writes a batch at most three
// times, each under WriteTimeout, dials once under HandshakeTimeout, waits
// at most a WriteTimeout for the back link's lock, and a write's bytes
// reach the peer within the kernel's bound, WriteTimeout on Linux, rounded
// up to the second on macOS and Windows (#1482, review Q3). A transport
// that states none leaves the step function's own default, 22 s, the TCP
// transport's under its defaults.
func TestTheForwardWindowIsTwiceTheTransportsResendWindow(t *testing.T) {
	var defaults TransportConfig
	if err := defaults.validate(); err == nil {
		t.Fatal("setup: an empty configuration was accepted")
	}
	type tc struct {
		name        string
		write, dial time.Duration
		want        time.Duration
	}
	runCase := func(t *testing.T, x tc) {
		t.Helper()
		tcp := &TCPTransport{cfg: defaults}
		if x.write != 0 {
			tcp.cfg.WriteTimeout, tcp.cfg.HandshakeTimeout = x.write, x.dial
		}
		if got := forwardWindow(tcp); got != x.want {
			t.Fatalf("%s: the forward window over WriteTimeout %v and HandshakeTimeout %v is %v, want %v (resend window %v)",
				x.name, tcp.cfg.WriteTimeout, tcp.cfg.HandshakeTimeout, got, x.want, tcp.resendWindow())
		}
	}
	quarter := 250 * time.Millisecond
	for _, x := range []tc{
		{"the defaults", 0, 0, 2 * (4*2*time.Second + time.Second + unackedLeg(2*time.Second))},
		{"a quarter of a second", quarter, 100 * time.Millisecond, 2 * (4*quarter + 100*time.Millisecond + unackedLeg(quarter))},
		{"a WriteTimeout past the saturation", math.MaxInt64 / 3, time.Second, math.MaxInt64},
		{"a resend window past half the range", math.MaxInt64 / 6, time.Second, math.MaxInt64},
	} {
		runCase(t, x)
	}
	if runtime.GOOS == "linux" {
		if got := (&TCPTransport{cfg: defaults}).resendWindow(); got != 11*time.Second {
			t.Fatalf("the resend window under the defaults on Linux is %v, want 11s, 5 x 2 s + 1 s", got)
		}
	}
	if got := forwardWindow(&MemoryTransport{}); got != 0 {
		t.Fatalf("the memory transport, which never delivers a frame twice, gives %v, want 0: the step function's default", got)
	}
}
