// Package sdkwarn emits one-shot construction-time warnings to stderr with an
// installable audit hook for tests. Stdlib-only (fmt + log + os + sync/atomic),
// zero domain vocabulary. Callers use Emit to surface "this composition is
// dubious but not wrong" — sinks/middlewares spot redundant decoration
// (CircuitBreaker over a Local sink, async-over-async, etc.) at construction
// time; tests install SetHook to capture the warning string without piping
// os.Stderr.
//
// sdkwarn is construction-time ONLY. It MUST NOT appear on the hot path of
// Write/Flush/Close — there is no rate limit, no buffering, no batching. A
// caller invoking Emit per record will saturate stderr and starve the GC.
//
// # Hook deadlock contract
//
// The installed hook runs in the caller goroutine of Emit. A hook that blocks
// indefinitely (waits on a channel, takes a lock the caller holds) will
// deadlock the construction path. Hook implementations MUST return promptly;
// tests typically use a simple append-to-slice closure or a non-blocking
// channel send. sdkwarn does NOT recover() from a panicking hook — a panic
// propagates to the caller as documented Go semantics. Test hooks that
// intentionally panic for fault-injection MUST be paired with a recover() in
// the test goroutine.
//
// # Stderr-path allocation budget
//
// When no hook is installed, Emit writes to os.Stderr via the stdlib log
// package (log.Logger.Println, which discards write errors internally — same
// policy as log.Default()). This path allocates 2+ times (fmt.Sprintf result,
// log.Logger.Output internal buffering). The hook-installed path allocates
// only the fmt.Sprintf result (1 alloc/op). Tests gate the hook-installed
// path at <= 1.0 allocs/op (testing.AllocsPerRun); the stderr path is NOT
// gated.
//
// # Thread safety
//
// Emit and SetHook are concurrent-safe via atomic.Pointer. SetHook is process-
// global; tests using t.Parallel that mutate the hook MUST use a captor
// pattern (one hook captures all parallel events) rather than per-test
// install/restore.
package sdkwarn

import (
	"fmt"
	"log"
	"os"
	"sync/atomic"
)

// HookFn is the audit-hook signature. Aliased so consumers and tests share the
// type name; func(string) anywhere in the SDK refers to the same shape.
type HookFn = func(string)

// Package-private state. Grouped per KTN-VAR-GROUP — a single var() block
// captures every mutable shared slot the primitive owns.
var (
	// hookSlot stores the currently installed *HookFn — nil means "no hook;
	// route to stderr". Read on every Emit (no lock), written on SetHook
	// (atomic). The double-pointer indirection is unavoidable: atomic.Pointer
	// needs a typed pointee, and a function value is not addressable.
	hookSlot atomic.Pointer[HookFn]

	// stderrLogger is the default writer used when no hook is installed. We
	// use log.Logger so the stderr-write error (rare: closed stderr, /dev/null
	// swap) is handled by the stdlib log package — log.Logger.Println discards
	// the underlying io.Writer.Write error internally, matching log.Default()
	// behaviour.
	stderrLogger = log.New(os.Stderr, "", 0)
)

// Emit formats msg via fmt.Sprintf and routes the resulting string to the
// installed hook, or — when no hook is installed — to os.Stderr with the
// "sdk: " prefix via the stdlib log package. Emit is concurrent-safe. It is
// NOT designed for the hot path: callers MUST limit invocations to
// construction time (sink/middleware New) or to "once per anomaly per
// process" patterns. There is no rate limit.
func Emit(format string, args ...any) {
	//: build the formatted message once; both the hook path and stderr path
	//: receive the same string so callers see a stable observation.
	msg := fmt.Sprintf(format, args...)
	//: hook-installed path: forward the formatted message to the audit captor.
	if hookPtr := hookSlot.Load(); hookPtr != nil {
		//: invoke the hook in the caller's goroutine — see the deadlock contract
		//: in the package doc.
		(*hookPtr)(msg)
		//: short-circuit so the stderr fallback below does not also fire.
		return
	}
	//: default path: stdlib log handles the io.Writer.Write error internally
	//: (same policy as log.Default()), so this call returns no error to
	//: propagate or discard at our level.
	stderrLogger.Println("sdk:", msg)
}

// SetHook installs fn as the warning sink. Passing nil restores the default
// (stderr). The previous hook (or nil) is returned so tests can stack
// install/restore without leaking state across t.Parallel boundaries.
//
// SetHook is process-global. Tests that mutate the hook MUST defer-restore the
// returned previous value; running such tests with t.Parallel is unsafe
// because concurrent SetHook calls race for the slot. This is documented
// intentionally — a per-goroutine hook would require context plumbing on a
// non-hot-path primitive, which is over-engineering.
func SetHook(fn HookFn) HookFn {
	//: encode "no hook" as a nil *HookFn so Emit's nil-check sees the same
	//: shape as an unwritten zero-value slot.
	var newPtr *HookFn
	//: non-nil installs need a heap-allocated copy so the atomic pointer can
	//: address it across concurrent reads.
	if fn != nil {
		//: pin the function value to a local before taking its address.
		captured := fn
		newPtr = &captured
	}
	//: atomic swap publishes the new slot and returns the previous *HookFn.
	prev := hookSlot.Swap(newPtr)
	//: empty-slot path: nothing to hand back.
	if prev == nil {
		//: explicit nil signals "no previous hook" to the caller's restore stack.
		return nil
	}
	//: hand back the previous HookFn value so the caller can stack-restore it.
	return *prev
}
