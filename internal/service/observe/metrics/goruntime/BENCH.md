<!-- refresh with `cd internal && go test -run '^$' -bench Collect_Runtime -benchmem -count=3 ./service/observe/metrics/goruntime/` -->
# Benchmarks — `internal/service/observe/metrics/goruntime`

## The runtime instruments (#868)

`BenchmarkCollect_Runtime` collects a meter holding only the eight
`RuntimeInstruments`: 5.4 µs/op, 1 584 B/op, 13 allocs/op, every one of them
`Collect`'s own (the snapshot's maps and point slices): the
`runtime/metrics.Read` behind the eight callbacks reads into samples allocated
at registration, once per collection.

Measured on: AMD EPYC 7351P, 4 Ps (`GOMAXPROCS=4`), linux/amd64, go1.27.3,
shared VM, 2026-11-03, `-benchtime=200ms -count=3`, in the metrics package
before the instruments moved here.

**The two runtime histograms (2026-11-03).** On a meter that takes an
observable histogram — the in-memory one does — `RuntimeInstruments` adds
`go.schedule.duration` and `go.gc.duration`: 161 bounds and 162 counts each,
copied from the same `runtime/metrics.Read` into buffers allocated on the
first read. The collection of all ten instruments, three runs:

```
BenchmarkCollect_Runtime-4   73876   15494 ns/op   8640 B/op   21 allocs/op
BenchmarkCollect_Runtime-4   70491   16848 ns/op   8640 B/op   21 allocs/op
BenchmarkCollect_Runtime-4   77353   16225 ns/op   8640 B/op   21 allocs/op
```

The eight added allocations are `Collect`'s, not the readings': the
histograms map and its window, the two points' `Bounds` and `Counts` copies
a snapshot hands out (as every histogram's), and the two callbacks' closures
`runObservers` builds per collection. The read and the conversion allocate
nothing after the first collection — #868's claim for the readings; a
snapshot that shares no memory with the meter costs what it copies.
