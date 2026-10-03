# backoff

The SDK's one exponential backoff curve — stdlib-only, no domain vocabulary.
The wait after the n-th consecutive failure is `BaseDelay × Multiplier^(n−1)`,
held at `MaxDelay`, widened by `Jitter`:

```go
curve := backoff.Value{BaseDelay: time.Second, MaxDelay: time.Minute}

wait := curve.Delay(failures) // 1s, 2s, 4s, … 1m, 1m, …
```

Three edges worth knowing before you use it:

- **It never returns a negative duration**, however large the attempt: the
  growth is compared with its bound before it is converted, so it stops at the
  ceiling — or at the longest `time.Duration` — instead of wrapping.
- **Nothing is refused, everything is normalised.** A `Multiplier` of 1 or
  below (or NaN) doubles; a `Jitter` outside `[0, 1]` is clamped and NaN is
  none; a zero `BaseDelay` waits nothing.
- **Jitter is ADDED after the ceiling**, so with one set a wait may reach
  `MaxDelay × (1 + Jitter)`. Zero jitter is the deterministic curve.

`Grow` and `Widen` are the two halves `Delay` composes — the pure growth and
the randomisation — for a caller that needs to assert one without the other.
It is published as `pkg/v1/app/resilience.Backoff`. See `CLAUDE.md` for the design.
