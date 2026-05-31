# tee

```go
import "github.com/kitsunium/sdk/internal/service/logger/middleware/tee"
```

Logger middleware that fans each record out to every primary `core/logger.Sink`
and, **only when every primary rejects it**, spills the record to an optional
dead-letter sink (ADR 0014 §D5).

`New(cfg)` returns a `*TeeSink`. The `Config` carries the `Primaries` slice and
an optional `Spill` sink.

Spill semantics: a record accepted by at least one primary is never spilled; a
record rejected by every primary is routed to the spill sink. All-failed
surfaces `CodeTeeAllBranchesFailed` (wrapping the joined causes); if the spill
also fails, `CodeSpillFailed` is surfaced instead.

This middleware adds only the dead-letter seam. Plain fan-out belongs to
`multi`; ordered fallback belongs to `failover`. Durable retry/backoff is out
of scope — the spill sink is a dead-letter seam, not a retry queue.
