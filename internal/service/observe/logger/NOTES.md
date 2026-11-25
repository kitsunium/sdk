# Notes — <!-- updated: 2026-10-03T05:20:00Z -->

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles. `CLAUDE.md` keeps what to obey; this file holds the rest.

## Conventions

- **Types that belong to one another share a file** (`KTN-STRUCT-PARTITION`);
  unrelated ones still may not (`KTN-STRUCT-COHESION`).
- **KTN-FUNC-MAXLOC ≤ 50.** `TextHandler.Handle` is intentionally split
  into `renderLine` + `writeLine` for that reason.
- **IFACE-PLUGIN.** `Build` returns the `Builder` *interface*, never the
  recycled concrete type — see ktn-linter `KTN-IFACE-PLUGIN`.
- **Origin wins on wrap.** `WriteFailed` / `CtxCancelled` wrap their cause
  via `errs.Wrap`; `errors.Is(err, context.Canceled)` still holds.
- **Caller PC capture, only when asked (#1014).** A `loggerImpl` asks its
  handler ONCE, when it is built (`over`, also on `With` / `WithGroup`),
  whether it implements `corelogger.SourceRequester` and answers true; the
  three emission paths (`Log`, `LogAttrs`, `Builder.Send`) then call
  `callerPC`, which walks the stack (`runtime.Callers(callerSkipDepth+1, …)`)
  only for such a logger and returns 0 otherwise. `sourceHandler` (installed by
  `WithCaller`) answers true; `genericHandler` and `TextHandler` do not
  implement the port — they never read `PC` — and a foreign Handler that reads
  `PC` must implement it. The walk was 22.5 % of every emit, paid for an
  attribute nothing rendered (`pkg/v1/observe/logger/BENCH.md`: static emit
  764–815 ns → 471–498 ns).
- **Bound attributes are rendered once (#1016).** When the encoder implements
  `corelogger.PrefixEncoder` (both in-tree encoders do), `WithAttrs` and
  `WithGroup` render the bound set under the current group stack into
  `prefix` — again on `WithGroup`, because the stack prefixes every key, bound
  ones included — and `Handle` passes it to `AppendWithPrefix` with the
  record's own attributes. A foreign encoder takes the old path.
- **The sink is handed what it reads, and no copy it does not need (#1016).**
  The record a sink receives carries the bound attributes then its own,
  since route predicates and the memory sink read them. A record with none of
  its own carries `h.attrs[:n:n]` itself — immutable once bound, and capped so
  a sink's `append` reallocates instead of writing into the array every later
  record carries (`TestASinkAppendingToTheSharedBoundAttrsLeavesThemIntact`,
  race on). A record with its own is merged (`mergeAttrs`, the one
  allocation) — unless the sink answered false to `corelogger.AttrsRequester`,
  asked once at construction (`sinkAttrs`) like `SourceRequester`: then it is
  handed the record's own attributes as they came, unmerged and uncopied. The
  byte sinks (console, file, syslog, journald, nettransport, rotfile) answer
  false, the decorators (levelgate, sample, async, recover, encwrite) forward
  their sink's answer, the fan-outs (multi, failover, tee) OR their branches',
  and any sink that does not implement the port — memory, route, the keyed
  sampler, otlpjson, dbsink, a consumer's — is merged for, as before. An
  encoder that is no `PrefixEncoder` needs the merged set to render it, and
  the sink is then handed that.
- **A live floor (#1015).** `genericHandler` and `TextHandler` hold a
  `level.Leveler` and `Enabled` reads it per record: `NewHandler` /
  `NewTextHandler` wrap their `level.Level` in `level.Fixed`, the `…Leveled`
  siblings take the caller's (a `level.Var` moved with `Set`), a nil one is
  `level.Info`. Derived handlers share the Leveler, so one `Set` reaches every
  logger derived from the one it was built with.
- **Swallow on emit.** `Logger.Log` MUST NOT propagate handler errors —
  see `swallowHandlerError` in `logger.go`. A future commit may swap this
  for a configurable `OnError` hook.
- **Trace correlation is injected, never imported** (ADR 0062).
  `NewWithTraceContext(h, src)` binds a `core/observe/logger.TraceContextSource`; the
  three emission paths (`Log`, `LogAttrs`, `Builder.Send`) call it and stamp
  `RecordEvent.TraceContext`. This package therefore keeps **zero edges to any
  other service-layer domain** — it never learns that `trace` exists, which is
  the point. That is ADR 0062's decision, not a layer rule: a service package
  MAY import a sibling (`internal/CLAUDE.md` §Dependency direction), and this one
  does not import `internal/service/observe/trace` because the two domains meet only at
  the top layer. `pkg/v1/observe/logger` supplies the binding; `New(h)` leaves it nil and
  a nil source costs nothing per emit.
- **The source is read AFTER the `Enabled` gate**, in all three paths, so a
  record dropped by the level threshold pays no `context` walk. It is read
  before `Handle`, so every Handler, Sink and middleware sees the identity on
  the record.
- **A Logger can own its writers, and then it can release them** (ADR 0095).
  `Owning(lg, closer)` returns a copy of `lg` that holds `closer`, and
  `loggerImpl.Close` releases it once (`onceCloser` repeats the first answer).
  It is for a constructor that opens writers the caller never holds —
  `pkg/v1/observe/logger.NewMulti` — whose Logger used to be the only thing referring to
  them, so they lived until a collection ran their finalizers: a descriptor leak
  for a program that rebuilds its logger, and on Windows a log file nothing
  could delete or rotate while the process ran. The owner stays a `*loggerImpl`,
  so `Build`/`LogAttrs` keep their fast path; `With`/`WithGroup` build fresh
  values that own nothing, so closing a child never pulls the writers out from
  under its parent. That includes `WithGroup("")`, whose no-op hands the
  receiver back — except from an owner, which answers with a non-owning twin
  rather than itself. `TestAnOwningLoggerReleasesWhatItOwnsOnce`.
- **A `!race` alloc guard covers this.** `TestT34TraceCorrelationAddsNoAllocation`
  in `pkg/v1/observe/logger` asserts **exactly 1** alloc/op on all three emission paths,
  in and out of a span — stricter than `TestPerfAllocsBuildSend`,
  which only asserts `>= 1`. Same race-off alloc lane; profiled in
  `pkg/v1/observe/logger/BENCH.md`.
