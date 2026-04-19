# `internal/kernel/clock`

**Layer**: kernel · **Code range**: 1300-1399 (reserved, no emissions today)

Minimal time-abstraction interface so higher layers can inject a fake clock in tests without coupling to `time.Now`. Stdlib-only.

## Surface

```go
type Clock interface {
    Now() time.Time
    Since(t time.Time) time.Duration
}

var System Clock  // backed by time.Now / time.Since
```

## Contract

- `Clock` carries **two** methods (`Now` + `Since`) on purpose — a single-method interface would be pinned to the `-er` naming convention by ktn-linter, and `Since` is useful in practice anyway.
- Implementations MUST be safe for concurrent use by multiple goroutines.
- `System` is the default wall-clock Clock. Replace it in tests by passing a fake into the struct being tested (dependency injection) — do NOT mutate `System` globally.

## Typical use (production)

```go
type handler struct {
    clk clock.Clock
}

func New(clk clock.Clock) *handler {
    if clk == nil {
        clk = clock.System
    }
    return &handler{clk: clk}
}

func (h *handler) tick() time.Duration {
    start := h.clk.Now()
    // ... work ...
    return h.clk.Since(start)
}
```

## Typical use (tests)

```go
type frozenClock struct{ at time.Time }
func (f frozenClock) Now() time.Time                 { return f.at }
func (f frozenClock) Since(t time.Time) time.Duration { return f.at.Sub(t) }

h := New(frozenClock{at: time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)})
```

## Do NOT

- Add `Sleep`, `After`, `NewTimer` here. Those pull the stdlib timer machinery into what is meant to be a thin time-source abstraction. Keep this package small.
- Replace `System` at package scope — you break parallel tests.

## Tests

- `clock_external_test.go` — wall-clock monotonicity and drift-vs-`time.Now` assertions.
- `clock_internal_test.go` — the unexported `systemClock` value satisfies both methods.
