//go:build !race

// The fixture of encwrite's performance contract (design/sdk.yaml, budgets):
// it sets one call up and returns it, and the gen_perf_test.go kit gen writes
// beside this file counts its allocations against its budget. Race off.
package encwrite_test

import (
	"context"
	"runtime"
	"slices"
	"sync"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/service/observe/logger/middleware/encwrite"
)

// perfDiscard is a downstream sink that keeps nothing, so the count is the
// middleware's alone.
type perfDiscard struct{}

func (perfDiscard) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	return len(p), nil
}

func (perfDiscard) Flush(context.Context) error { return nil }

func (perfDiscard) Close() error { return nil }

// perfWriteErr keeps each call's result alive.
var perfWriteErr error

// perfSpareThreads is how many idle threads the fixture leaves the
// scheduler before the window opens.
const perfSpareThreads = 8

// perfWrite is Write's fixture (#813): a record of a log line's size sealed
// and framed for a downstream that keeps nothing — the cipher bound once at
// construction, the frame borrowed from the kernel's buffer pool. Each call
// draws its nonce from the system: on Solaris and illumos a getrandom(2)
// through libc, which hands the window's one P to another thread at every
// call (entersyscallblock); a thread the scheduler must start for it is 7
// heap objects there — the 6 a thread costs on Linux, measured, and the cgo
// traceback buffer Solaris adds (runtime.mcommoninit) —, the 7 allocations
// #1391 counted, and no call's cost. So the fixture leaves spare threads idle first, as the warm-up pays
// the runtime's lazy caches: the window then finds them, starts none, and
// counts what the call allocates. The budget is unchanged.
func perfWrite(tb testing.TB) func() {
	perfSpare(perfSpareThreads)
	key, err := corecrypto.NewKey(slices.Repeat([]byte{0x02}, corecrypto.KeyLen))
	if err != nil {
		tb.Fatalf("NewKey: %v", err)
	}
	w, err := encwrite.NewEncWriter(encwrite.Config{Sink: perfDiscard{}, Key: key})
	if err != nil {
		tb.Fatalf("NewEncWriter: %v", err)
	}
	ctx := context.Background()
	record := []byte(`time=2026-11-11T12:00:00Z level=INFO msg="request served" method=GET path=/v1/items status=200 dur=1.2ms`)
	return func() {
		_, perfWriteErr = w.Write(ctx, corelogger.RecordEvent{}, record)
	}
}

// perfSpare leaves n more threads idle in the scheduler: n goroutines each
// locked to a thread of its own at once, then unlocked and returned, which
// gives their threads back to the idle list, where the runtime keeps them.
func perfSpare(n int) {
	var locked, done sync.WaitGroup
	release := make(chan struct{})
	//: each on a thread of its own.
	for range n {
		locked.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			runtime.LockOSThread()
			locked.Done()
			<-release
			//: unlocked before it returns, or its thread would exit with it.
			runtime.UnlockOSThread()
		}()
	}
	locked.Wait()
	close(release)
	done.Wait()
}
