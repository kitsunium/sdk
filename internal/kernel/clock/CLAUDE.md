<!-- updated: 2026-05-18T14:30:00Z -->
# internal/kernel/clock/

## Purpose

Minimal time-abstraction so higher layers can inject a fake clock in tests without coupling to `time.Now`. Stdlib-only, domain-neutral. Code range `1300-1399` reserved (no sentinels emitted today).

## Surface

```go
type Clock interface {
    Now() time.Time
    Since(t time.Time) time.Duration
}

var System Clock // backed by time.Now / time.Since
```

Backed by the unexported `systemClock` (`clock.go`). The compile-time assertion `var _ Clock = (*systemClock)(nil)` lives in `clock_compliance.go` per `KTN-IFACE-ASSERT-PLACEMENT` — diagnostic-only declarations stay out of the production source file so the runtime binary carries no unused-symbol cost.

## Conventions

- **Two methods on purpose.** A single-method interface would be pinned to the `-er` naming convention by ktn-linter (`Nower`?), and `Since` is useful in practice anyway.
- **Inject, don't mutate.** Pass a `Clock` into the struct under test (constructor takes `clk clock.Clock`; nil falls back to `clock.System`). NEVER mutate `clock.System` at package scope — it breaks parallel tests.
- **Implementations MUST be safe for concurrent use.** `systemClock` is a zero-sized value type; user fakes typically are too.

## Typical use

```go
// Production
type handler struct{ clk clock.Clock }
func New(clk clock.Clock) *handler {
    if clk == nil { clk = clock.System }
    return &handler{clk: clk}
}

// Test
type frozenClock struct{ at time.Time }
func (f frozenClock) Now() time.Time                  { return f.at }
func (f frozenClock) Since(t time.Time) time.Duration { return f.at.Sub(t) }
h := New(frozenClock{at: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)})
```

## Do NOT

- Add `Sleep`, `After`, `NewTimer` here. Those pull stdlib timer machinery into what is meant to be a thin time-source abstraction; keep this package small.
- Replace `System` at package scope — it breaks parallel tests. Inject a fake instead.
- Introduce monotonic-vs-wall split methods — the stdlib already handles that inside `time.Since`.

## Verification

```
bazel test --config=race //internal/kernel/clock:clock_test
# OR
cd internal/kernel && GOWORK=off go test -race -cover ./clock
# coverage target: 100%
```

Tests: `clock_external_test.go` (wall-clock monotonicity, drift-vs-`time.Now` assertions), `clock_internal_test.go` (the unexported `systemClock` value satisfies both methods).

A longer-form companion lives in `README.md`.
