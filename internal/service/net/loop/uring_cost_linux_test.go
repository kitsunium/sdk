//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — what the io_uring loop pays a batch outside the handler's
// work: the wait when a completion is already there, the accept watchdog on a
// listener with nothing queued (ADR 0202).
package loop

import (
	"runtime"
	"syscall"
	"testing"
	"time"
)

// costBatchesPerTick is how many batches a tick holds in the sparse load
// BenchmarkLoadSparse measures: about 1,600 batches a second.
const costBatchesPerTick = 16

// BenchmarkURingWait_ready measures one wait on a ring whose CQ already holds
// a completion and whose SQ holds nothing: what a batch pays when the last
// reap left work behind or a secondary's signal arrived during the batch.
func BenchmarkURingWait_ready(b *testing.B) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	u, step, errno := openURing(16, setupBase)
	if errno != 0 {
		b.Skipf("io_uring %s: %v", step, errno)
	}
	defer u.close()
	s := u.get()
	if s == nil {
		b.Fatal("no SQE")
	}
	s.opcode = opNop
	tick := kernelTimespec{nsec: int64(time.Second)}
	u.wait(&tick, 1)
	if u.ready() != 1 {
		b.Fatalf("ready = %d, want 1", u.ready())
	}
	b.ReportAllocs()
	for b.Loop() {
		u.wait(&tick, 1)
	}
	if u.ready() != 1 || u.pending() != 0 {
		b.Fatalf("ready %d, pending %d", u.ready(), u.pending())
	}
}

// BenchmarkURingWatchdog_quiet measures the accept watchdog of a ring whose
// listener has nothing queued and whose last accept is old — every open
// connection accepted long ago — at costBatchesPerTick batches a tick.
func BenchmarkURingWatchdog_quiet(b *testing.B) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = syscall.Close(fd) }()
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		b.Fatal(err)
	}
	if err := syscall.Listen(fd, 64); err != nil {
		b.Fatal(err)
	}
	lp := &loop{tick: 10 * watchdogTicks}
	rr := &uRing{ul: &uLoop{lp: lp, lfds: []int{fd}}, r: &ring{}, armed: []int{1}}
	i := 0
	b.ReportAllocs()
	for b.Loop() {
		i++
		if i%costBatchesPerTick == 0 {
			lp.tick++
		}
		rr.watchdog()
	}
	if rr.r.st.watchdog != 0 {
		b.Fatalf("the watchdog fired %d times on an empty queue", rr.r.st.watchdog)
	}
}
