package sdkwarn_test

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/sdkwarn"
)

// installCaptor installs a captor hook that buffers every message and
// returns a snapshot accessor + a restore func. Callers Cleanup the restore
// to keep the process-global hookSlot pristine.
func installCaptor(t *testing.T) (snap func() []string, restore func()) {
	t.Helper()
	var mu sync.Mutex
	var buf []string
	prev := sdkwarn.SetHook(func(s string) {
		mu.Lock()
		buf = append(buf, s)
		mu.Unlock()
	})
	snap = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(buf)
	}
	restore = func() { sdkwarn.SetHook(prev) }
	return snap, restore
}

// TestEmit verifies that an installed hook receives the formatted message
// verbatim — the core contract every middleware relies on at construction
// time. Covers literal, single arg, multi arg, and empty format.
func TestEmit(t *testing.T) {
	type tc struct {
		name   string
		format string
		args   []any
		want   string
	}
	tests := []tc{
		{"plain literal", "warning", nil, "warning"},
		{"single arg", "warn %d", []any{42}, "warn 42"},
		{"multi arg", "warn %d %s", []any{1, "two"}, "warn 1 two"},
		{"empty format", "", nil, ""},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; each row formats its own message and asserts the hook saw the
	//: exact expected string. Tests share the process-global hookSlot and
	//: cannot run in parallel — they call installCaptor sequentially.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		snap, restore := installCaptor(t)
		t.Cleanup(restore)
		sdkwarn.Emit(tc.format, tc.args...)
		got := snap()
		//: hook must have been invoked exactly once for the single Emit call.
		if len(got) != 1 {
			t.Fatalf("hook invocations = %d, want 1", len(got))
		}
		//: captured message must equal the expected Sprintf result.
		if got[0] != tc.want {
			t.Fatalf("hook saw %q, want %q", got[0], tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runCase(t, tc)
		})
	}
}

// TestSetHook verifies install/uninstall stack semantics callers depend on
// for nested test setup. The single test exercises every state transition:
// clear → install A → install B → uninstall → uninstall again.
func TestSetHook(t *testing.T) {
	type tc struct {
		name string
	}
	tests := []tc{
		{"install A install B uninstall returns B then A"},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the test mutates the process-global hookSlot and runs
	//: sequentially with respect to other slot-mutating tests.
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		hookA := func(string) {}
		hookB := func(string) {}
		//: clear the slot to a known state; previous may be nil or stale.
		saved := sdkwarn.SetHook(nil)
		t.Cleanup(func() { sdkwarn.SetHook(saved) })
		//: install A from the cleared state; previous must be nil.
		if prev := sdkwarn.SetHook(hookA); prev != nil {
			t.Fatalf("SetHook(A) prev = %v, want nil", prev)
		}
		//: install B; previous must be the hookA captor — we only check
		//: non-nil because function-pointer equality on closures isn't
		//: meaningful, but the captor's identity is the contract.
		if prev := sdkwarn.SetHook(hookB); prev == nil {
			t.Fatalf("SetHook(B) prev = nil, want non-nil")
		}
		//: uninstall (nil); previous must be the hookB captor.
		if prev := sdkwarn.SetHook(nil); prev == nil {
			t.Fatalf("SetHook(nil) prev = nil, want non-nil")
		}
		//: uninstall a second time; previous must be nil (no hook to restore).
		if prev := sdkwarn.SetHook(nil); prev != nil {
			t.Fatalf("SetHook(nil) x2 prev = %v, want nil", prev)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runCase(t, tc)
		})
	}
}

// TestEmitConcurrentSafeUnderRace fans goroutines × Emit calls through a
// counting hook. The -race-built test verifies no race on the atomic-pointer
// slot.
func TestEmitConcurrentSafeUnderRace(t *testing.T) {
	const goroutines int = 32
	const perGoroutine int = 256
	type tc struct {
		name         string
		goroutines   int
		perGoroutine int
	}
	tests := []tc{
		{"32 goroutines x 256 Emit each", goroutines, perGoroutine},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the captor counts every hook invocation and the test asserts
	//: the total matches the expected fan-out.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var count atomic.Int64
		prev := sdkwarn.SetHook(func(string) { count.Add(1) })
		t.Cleanup(func() { sdkwarn.SetHook(prev) })
		var wg sync.WaitGroup
		//: spawn N goroutines that each Emit perGoroutine messages.
		for range tc.goroutines {
			wg.Go(func() {
				for range tc.perGoroutine {
					sdkwarn.Emit("race")
				}
			})
		}
		wg.Wait()
		//: every Emit must have produced exactly one hook invocation.
		want := int64(tc.goroutines * tc.perGoroutine)
		if got := count.Load(); got != want {
			t.Fatalf("hook invocations = %d, want %d", got, want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runCase(t, tc)
		})
	}
}

// TestSetHookEmitRaceUnderRace specifically targets the install-during-emit
// race lane: an installer goroutine flips hooks while an emitter calls Emit.
// -race must report clean.
func TestSetHookEmitRaceUnderRace(t *testing.T) {
	type tc struct {
		name       string
		iterations int
	}
	tests := []tc{
		{"10000 emits with concurrent installer", 10000},
	}
	//: runCase executes one row directly so the static analyser credits the
	//: branch; the test verifies the atomic.Pointer publish/load contract via
	//: a count assertion: every Emit must invoke whatever hook was current.
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var emitCount atomic.Int64
		saved := sdkwarn.SetHook(func(string) { emitCount.Add(1) })
		t.Cleanup(func() { sdkwarn.SetHook(saved) })
		stop := make(chan struct{})
		var wg sync.WaitGroup
		//: installer goroutine flips the hook to identical no-op captors so
		//: even when its SetHook wins the race, Emit still produces a count.
		wg.Go(func() {
			for {
				//: stop signal: emitter goroutine closes the channel.
				select {
				case <-stop:
					return
				default:
					//: install a fresh captor that also bumps the counter so
					//: the count assertion stays meaningful regardless of which
					//: hook a given Emit observed.
					sdkwarn.SetHook(func(string) { emitCount.Add(1) })
				}
			}
		})
		//: emitter goroutine drives the Emit count.
		wg.Go(func() {
			for range tc.iterations {
				sdkwarn.Emit("race")
			}
			close(stop)
		})
		wg.Wait()
		//: every Emit produced at least one counter bump (the installer races
		//: do not lose Emits because the hookSlot publish is atomic).
		if got := emitCount.Load(); got < int64(tc.iterations) {
			t.Fatalf("emit count = %d, want >= %d", got, tc.iterations)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runCase(t, tc)
		})
	}
}
