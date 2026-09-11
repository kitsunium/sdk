// Package signal — the measurement separates the two halves of this package
// that a caller confuses. Signal DELIVERY latency belongs to the kernel and is
// not measured here; signal REGISTRATION is the package's own cost, is paid on
// every Notify, and turns out to be four orders of magnitude larger than the
// per-signal work it wraps.
//
// It is an INTERNAL benchmark so the translation helpers Notify runs on every
// delivery (toSignal, forward) can be priced against the registration they sit
// behind — the comparison is the whole point.
package signal

import (
	"os"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// benchQuietSignal is what the single-signal rows subscribe to. SIGALRM is never
// raised by this benchmark nor by the Go runtime, so registering and detaching
// it millions of times changes nothing about the process — the same reasoning
// signal_internal_test.go's quietSignal applies, restated here so the two files
// stay independent.
const benchQuietSignal syscall.Signal = syscall.SIGALRM

// Sinks defeat dead-code elimination on the value-returning helpers.
var (
	errSink  error
	sigSink  coreproc.Signal
	boolSink bool
	chanSink <-chan coreproc.Signal
	stopSink func()
)

// benchSignals is a realistic subscription for a supervisor: the terminal
// signals plus the reload convention. The constants are exactly the ones the
// package's own UNTAGGED internal tests already use, so this file compiles on
// every GOOS the package itself compiles on.
var benchSignals = []coreproc.Signal{
	coreproc.Signal(syscall.SIGTERM),
	coreproc.Signal(syscall.SIGINT),
	coreproc.Signal(syscall.SIGHUP),
	coreproc.Signal(syscall.SIGALRM),
}

// BenchmarkNotify_Stop_One is the whole lifecycle for a single signal: one
// goroutine launched, one os/signal registration installed, the readiness
// handshake awaited, then the teardown that detaches the registration and joins
// the goroutine. Notify deliberately blocks until the registration is live, so
// this cost cannot be deferred — it is what a caller pays at wiring time.
func BenchmarkNotify_Stop_One(b *testing.B) {
	sig := coreproc.Signal(benchQuietSignal)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ch, stop := Notify(sig)
		stop()
		chanSink, stopSink = ch, stop
	}
}

// BenchmarkNotify_Stop_Four subscribes to four signals in one call. The delta
// against the single-signal row is what each additional signal costs inside one
// registration — the number that decides whether to make one Notify call or
// several.
func BenchmarkNotify_Stop_Four(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ch, stop := Notify(benchSignals...)
		stop()
		chanSink, stopSink = ch, stop
	}
}

// BenchmarkStop_Idempotent prices the second and every later call to stop. The
// contract promises it is a safe no-op; this row says what a no-op costs.
func BenchmarkStop_Idempotent(b *testing.B) {
	_, stop := Notify(coreproc.Signal(benchQuietSignal))
	stop()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		stop()
	}
	stopSink = stop
}

// BenchmarkToSignal is the per-delivery translation: a type assertion and a
// re-key. It is measured to be compared against the registration rows above.
func BenchmarkToSignal(b *testing.B) {
	carrier := os.Signal(syscall.SIGTERM)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sigSink = toSignal(carrier)
	}
}

// BenchmarkForward is the send half of the translator loop, measured against a
// buffered channel a reader immediately drains — the shape Notify hands back.
func BenchmarkForward(b *testing.B) {
	dst := make(chan coreproc.Signal, 1)
	done := make(chan struct{})
	sig := coreproc.Signal(syscall.SIGTERM)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink = forward(dst, done, sig)
		<-dst
	}
}

// BenchmarkRelay_Batch1 and BenchmarkRelay_Batch64 run the same loop over a
// different number of deliveries. Both build their source channel INSIDE the
// timed region, so the difference between the two rows is 63 extra kill(2) calls
// and nothing else — which isolates the per-signal cost from the per-call cost
// without either row having to be believed on its own.
//
// Signal 0 is used deliberately: kill(pid, 0) is the POSIX existence probe, so
// the kernel does the full permission walk and delivers nothing. It is the only
// way to issue millions of kill(2) calls at this process without perturbing it.
// Where kill(2) does not exist (the !unix stub) the benchmark skips rather than
// timing a refusal.
func BenchmarkRelay_Batch1(b *testing.B) {
	benchRelay(b, 1)
}

func BenchmarkRelay_Batch64(b *testing.B) {
	benchRelay(b, 64)
}

// benchRelay drains n signal-0 deliveries through Relay per iteration.
func benchRelay(b *testing.B, n int) {
	b.Helper()
	if !relaySupported(b) {
		b.Skip("kill(2) relay is not supported on this platform")
	}
	target := Target(os.Getpid())
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		src := make(chan coreproc.Signal, n)
		for range n {
			src <- coreproc.Signal(0)
		}
		close(src)
		errSink = Relay(src, target)
	}
	b.StopTimer()
	if errSink != nil {
		b.Fatalf("Relay: %v", errSink)
	}
}

// BenchmarkRelay_ReservedTarget is the refusal path: target 0 and -1 are
// rejected before any delivery. It is measured because the guard runs on every
// Relay call, including the ones that go on to deliver thousands of signals.
func BenchmarkRelay_ReservedTarget(b *testing.B) {
	src := make(chan coreproc.Signal)
	close(src)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = Relay(src, Target(0))
	}
}

// relaySupported reports whether this platform has a real kill(2) relay, by
// draining an already-closed source: the Unix implementation returns nil, the
// !unix stub returns UnsupportedPlatform before looking at the channel.
func relaySupported(b *testing.B) bool {
	b.Helper()
	src := make(chan coreproc.Signal)
	close(src)
	return Relay(src, Target(os.Getpid())) == nil
}
