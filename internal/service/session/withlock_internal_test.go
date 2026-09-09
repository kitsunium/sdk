// Package session — the regression guard for cross-goroutine exclusion.
package session

import (
	"context"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestWithLockExcludesGoroutines pins the half of the store's exclusion that
// flock does NOT provide.
//
// Measured on linux/amd64: flock on the same open file DESCRIPTION is a lock
// conversion, not a wait — it returns immediately. This store deliberately
// holds one descriptor for its whole lifetime, so before fileStore.mu existed
// every goroutine re-locked that one description and every one of them entered
// the read-modify-write cycle together. Cross-process exclusion was intact the
// whole time, which is why no test that only spawns processes could see it.
//
// The assertion is on observed OCCUPANCY rather than on a final counter: a
// racing counter can still land on the right total, so counting alone would
// pass against the bug. Removing f.mu.Lock from withLock fails this test.
func TestWithLockExcludesGoroutines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		callers int
	}{
		{"eight concurrent callers see occupancy one", 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, err := NewFileStore(FileConfig{
				IdleTimeout: 5 * time.Minute, AbsoluteTimeout: time.Hour,
				Clock: clock.NewManualClock(time.Date(2031, 3, 7, 4, 5, 6, 0, time.UTC)),
				Dir:   filepath.Join(t.TempDir(), "sessions"), Key: internalKey(t),
			})
			//: a platform without a native mechanic refuses at construction; it
			//: has no lock to test rather than a lock that does not work.
			if errs.HasReason(err, "UNSUPPORTED_PLATFORM") {
				t.Skip("file store has no native mechanic on this platform")
			}
			if err != nil {
				t.Fatalf("NewFileStore: %v", err)
			}
			concrete, ok := store.(*fileStore)
			if !ok {
				t.Fatalf("NewFileStore returned %T, want *fileStore", store)
			}

			var (
				mu       sync.Mutex
				inside   int
				maxSeen  int
				start    sync.WaitGroup
				finished sync.WaitGroup
			)
			start.Add(1)
			var failures atomic.Int64
			for range tc.callers {
				finished.Go(func() {
					//: every caller is released at once, so the sections
					//: genuinely compete instead of running one after another.
					start.Wait()
					//: a store fault would make the occupancy assertion vacuous,
					//: so it is counted rather than discarded.
					if lockErr := concrete.withLock(context.Background(), func() error {
						mu.Lock()
						inside++
						if inside > maxSeen {
							maxSeen = inside
						}
						mu.Unlock()
						//: widen the window a real read-modify-write would have,
						//: without sleeping — Gosched hands the scheduler an
						//: opportunity the bug would take.
						runtime.Gosched()
						mu.Lock()
						inside--
						mu.Unlock()
						return nil
					}); lockErr != nil {
						failures.Add(1)
					}
				})
			}
			start.Done()
			finished.Wait()

			if n := failures.Load(); n != 0 {
				t.Fatalf("withLock failed %d times; the occupancy assertion would be vacuous", n)
			}
			mu.Lock()
			got := maxSeen
			mu.Unlock()
			//: two holders at once is the defect; one is the contract.
			if got != 1 {
				t.Errorf("max occupancy = %d, want 1 — the critical section admitted %d goroutines at once", got, got)
			}
		})
	}
}
