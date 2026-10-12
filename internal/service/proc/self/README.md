# self (internal/service/proc/self)

What the running process is doing right now. Internal service implementation
behind the public `pkg/v1/proc/process` facade (`Self`) — consumers import the
facade, not this package. What the binary was built from is
`internal/service/proc/buildinfo` (ADR 0172).

## API

```go
func ReadStats() StatsValue
func (d DistributionValue) Count() uint64
func (d DistributionValue) Quantile(q float64) time.Duration
```

- `ReadStats` reads the Go runtime's metrics once (goroutines, heap, heap goal,
  memory mapped, allocations, collections, the GC-pause and scheduling-latency
  histograms), the last collection's end once (`debug.ReadGCStats`), and the
  CPU time once — `getrusage(RUSAGE_SELF)` where the platform has it, the
  runtime's estimate (total minus idle) elsewhere, flagged `CPUEstimated`.

## Errors

None. Nothing here can fail in a way a caller could act on: a figure the
platform cannot give is zero.
