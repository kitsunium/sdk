# Kernel benchmark template

Every `*_bench_test.go` under `internal/kernel/*` follows this shape. It is the
canonical reference for the bench-coverage work tracked by #16 (sub-issues #17
errs, #18 buffer, #19 clock, #20 ring). The living example is
`internal/kernel/batcher/batcher_bench_test.go`.

## File naming

`<source>_bench_test.go` — e.g. `ring.go` → `ring_bench_test.go`. Enforced by
ktn-linter `KTN-TEST-SUFFIX`.

## Package line

White-box is the default in the kernel: `package <pkg>` (same package as the
source), so benchmarks can reach unexported helpers and the steady-state
internal state the zero-alloc claims depend on. `batcher_bench_test.go` declares
`package batcher` for exactly this reason. An external `package <pkg>_test` is
acceptable when a benchmark only needs the exported surface.

## Required structure

```go
package foo

import "testing"

// BenchmarkBar measures <the exact path> and <why it matters> — e.g. the
// steady-state Get/Put round-trip that the 0-alloc pool claim rests on.
func BenchmarkBar(b *testing.B) {
	input := makeInput()  // setup hoisted ABOVE the loop
	b.ReportAllocs()      // MANDATORY — the zero-alloc claim only has teeth
	b.ResetTimer()        // drop setup cost from the measurement
	for i := 0; b.Loop(); i++ {
		//: consume the result so the compiler cannot dead-code the body.
		sink = foo.Bar(input)
	}
}

// BenchmarkBar_Parallel — MANDATORY for every concurrent-safe API.
// EXCEPTION: SPSC primitives (ring) use a dedicated 2-goroutine
// producer/consumer pattern, NOT b.RunParallel.
func BenchmarkBar_Parallel(b *testing.B) {
	input := makeInput()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			//: each P exercises the read path concurrently.
			sink = foo.Bar(input)
		}
	})
}

// Package-level sink keeps Bench return values alive across the loop.
var sink any
```

## Conventions (ktn-linter)

- Each `Benchmark*` carries a doc comment naming **what** it measures and
  **why** (the perf claim it backs).
- Every control block inside the body takes a `//:` intent comment — same rule
  as production code.
- `b.ReportAllocs()` in **every** body, before the loop.

## Invocation

```bash
# Run every kernel bench (benchstat-friendly output → .bench.out)
make sdk-bench                 # COUNT=N overrides the sample count (default 10)

# CPU / mem / block / mutex profiles → profiles/
make sdk-bench-profile

# A/B vs main
git checkout main && make sdk-bench && mv .bench.out .bench.main.out
git checkout <branch> && make sdk-bench
make sdk-bench-compare

# Single package via go test
cd internal/kernel && GOWORK=off go test -run='^$' -bench=. -benchmem ./ring/
```

## Anti-patterns (forbidden)

- `for i := 0; i < b.N; i++` — legacy loop, flagged by `KTN-TEST-BLOOP`. Use
  `for b.Loop()` (Go 1.24+).
- Setup inside the measured loop — skews the number.
- Missing `b.ReportAllocs()` — the zero-alloc claim has no teeth.
- Discarding return values — the compiler dead-codes the benchmark body; assign
  to a package-level sink.
- `b.RunParallel` on an SPSC primitive — violates the single-producer /
  single-consumer contract; use a dedicated goroutine pair instead.

## Profile interpretation

- `-benchmem`: `B/op` and `allocs/op` per iteration — the headline numbers.
- `-memprofile` + `go tool pprof`: source-level allocation attribution.
- `-blockprofile`: goroutines stalling on sync primitives.
- `-mutexprofile`: contention on mutexes.

For the SDK's hot-path claims, `allocs/op` MUST be `0` on the documented
zero-alloc benches; a CI gate (#23) asserts this continuously.
