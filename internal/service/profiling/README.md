# profiling (service)

The running process's own profiles (ADR 0121): captured, decoded with the
standard library, folded onto the owners you name, and its goroutines read and
grouped.

```go
p, err := profiling.CaptureCPU(ctx, 10*time.Second)     // one CPU profiler per process
f, err := profiling.Fold(p, profiling.FoldConfig{Attribute: byLabel("component")})
// f.Total == f.Unattributed + Σ f.Owners[i].Value, in nanoseconds

gs, err := profiling.Goroutines()
groups := profiling.GroupGoroutines(gs, profiling.GroupConfig{Labels: []string{"component"}})
```

A heap profile is sampled and a CPU profile counts about a hundred samples a
second: ask where the cost is, not exactly how much.

| Code | Reason | When |
|---|---|---|
| `0.3.89.1` | `WINDOW_INVALID` | a CPU window not positive or over `MaxCPUWindow` (400) |
| `0.3.89.2` | `PROFILER_BUSY` | the process's one CPU profiler is running (409) |
| `0.3.89.3` | `CAPTURE_CANCELED` | the context ended before the window; no profile (503) |
| `0.3.89.4` | `CAPTURE_FAILED` | runtime/pprof could not write a profile |
| `0.3.89.5` | `PROFILE_MALFORMED` | not a well-formed pprof profile; the part is named, the input never quoted — or a nil or short-valued profile handed to `Fold` |
| `0.3.89.6` | `PROFILE_TOO_LARGE` | over `MaxProfileBytes`, compressed or inflated, or stacks past `MaxFrames` |
| `0.3.89.7` | `SAMPLE_TYPE_MISSING` | the profile does not measure the sample type asked |

Public facade: `pkg/v1/profiling`. See `CLAUDE.md`.
