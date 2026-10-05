//go:build !race

// The fixtures of errs's performance contracts (design/sdk.yaml, budgets):
// each sets one call up and returns it, and the perf_gen_test.go kit gen
// writes beside this file counts the call's allocations against its
// budget — the total over 30 000 calls after as many warm-up calls, so a
// regression that allocates on some calls only is not averaged away.
//
// They were the errs half of the kernel's zero-allocation gate
// (internal/kernel/zeroalloc_gate_integration_test.go, TestZeroAllocInvariant,
// AllocsPerOp == 0 through testing.Benchmark): every bound is the gate's,
// 0, and every probe is the gate's call. Race off, since the race detector
// allocates on every memory access: the allocation lane runs it.
package errs_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The sinks keep each call's result alive, so the compiler cannot drop the
// call it measures.
var (
	perfCode   errs.Code
	perfString string
	perfBool   bool
)

// perfSentinel is a representative *Error for the accessors and HasCode.
var perfSentinel = errs.Define(
	errs.Pack(2, 3, 4, 5),
	"ZEROALLOC_GATE",
	"zero-alloc gate sentinel",
	"zero-alloc gate private detail",
)

// perfPack is Pack's fixture: the dotted quad packed from its four bytes,
// pure bit operations.
func perfPack(testing.TB) func() {
	return func() { perfCode = errs.Pack(2, 3, 4, 5) }
}

// perfErrorCode is (*Error).Code's fixture: a field read.
func perfErrorCode(testing.TB) func() {
	return func() { perfCode = perfSentinel.Code() }
}

// perfErrorReason is (*Error).Reason's fixture.
func perfErrorReason(testing.TB) func() {
	return func() { perfString = perfSentinel.Reason() }
}

// perfErrorPublic is (*Error).Public's fixture.
func perfErrorPublic(testing.TB) func() {
	return func() { perfString = perfSentinel.Public() }
}

// perfErrorPrivate is (*Error).Private's fixture.
func perfErrorPrivate(testing.TB) func() {
	return func() { perfString = perfSentinel.Private() }
}

// perfHasCode is HasCode's fixture: the code-matching walk over a sentinel.
func perfHasCode(testing.TB) func() {
	code := perfSentinel.Code()
	return func() { perfBool = errs.HasCode(perfSentinel, code) }
}
