# seglog

A durable, append-only segmented log of records addressed by a contiguous
index, with crash recovery that classifies every hole before it repairs one
(#820, ADR 0230). Kernel-only: not published in `pkg/v1`.

```go
l, err := seglog.Open(dir, seglog.DefaultSegmentBytes)
if err != nil {
    return err // Damaged / VersionUnsupported: nothing was changed
}
defer l.Close()
_, next := l.Bounds()
if err := l.Append(next, kind, payload); err != nil {
    return err
}
if err := l.Sync(); err != nil {
    return err // final: the log refuses every later call
}
p, err := l.Read(next)
```

- `Append` buffers; `Sync` makes the batch durable with two syncs.
- `Range(lo, hi, maxBytes, dst)` reads a run with one `pread`.
- `DropBefore(i)` drops whole sealed segments below `i`.
- A torn tail is repaired at `Open`; any other damage is refused unchanged.
