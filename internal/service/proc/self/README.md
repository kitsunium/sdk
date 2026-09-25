# self (internal/service/proc/self)

What the running process can say about itself: what it was built from, and what
it is doing right now. Internal service implementation behind the public
`pkg/v1/process` facade (`Self`, `Build`, `ParseBuild`) — consumers import the
facade, not this package.

## API

```go
func ReadStats() StatsValue
func ReadBuild() (BuildValue, bool)
func ParseBuild(info *debug.BuildInfo) BuildValue

func (b *BuildValue) Module(path string) (ModuleValue, bool)
func (d DistributionValue) Count() uint64
func (d DistributionValue) Quantile(q float64) time.Duration
```

- `ReadStats` reads the Go runtime's metrics once (goroutines, heap, heap goal,
  memory mapped, allocations, collections, the GC-pause and scheduling-latency
  histograms), the last collection's end once (`debug.ReadGCStats`), and the
  CPU time once — `getrusage(RUSAGE_SELF)` where the platform has it, the
  runtime's estimate (total minus idle) elsewhere, flagged `CPUEstimated`.
- `ReadBuild` / `ParseBuild` read `runtime/debug.BuildInfo` into a main module
  and its dependencies, each followed through its replacement, with a release,
  a commit and a local directory kept apart. Pseudo-versions are recognised
  and split by `golang.org/x/mod/module`, not by a hand-written pattern.

## Errors

None. Nothing here can fail in a way a caller could act on: a figure the
platform cannot give is zero, a binary without build information is a `false`.
