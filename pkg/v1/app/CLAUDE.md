<!-- updated: 2026-10-05T00:00:00Z -->
# pkg/v1/app/

## Purpose

The app family's public facades (ADR 0155). This directory holds no Go code:
it is a prefix, not a package, and there is no
`github.com/kitsunium/sdk/pkg/v1/app` to import. Each member is a package of
the SDK module with its own `CLAUDE.md`, `README.md` (written by
`tools/genindex` from `docs/api`, ADR 0167) and, where it measures something, `BENCH.md`.
A member is imported by its full path —
`github.com/kitsunium/sdk/pkg/v1/app/config` — and links what it imports and
never this directory or its siblings, because Go links a package's imports and
not its parent (the measurement ADR 0155 records).

## The rule that put them together

A domain belongs here when it is a mechanism an APPLICATION is assembled from,
rather than a question about bytes, callers, the wire, the machine or
telemetry: how a program is configured and invoked (`config`, `cli`), how it
speaks to people — in their language, as a page, by mail — and checks what
they send (`i18n`, `view`, `mail`, `validation`), how its parts start, stop,
report their health and exclude one another (`lifecycle`, `health`, `lock`),
how work moves through it — announced in process, at a time, along declared
states (`events`, `scheduler`, `statemachine`) — how a call survives a failing
dependency (`resilience`), and how what it creates is named (`id`). A message
that must outlive the process is not here: the durable queue is
`pkg/v1/data/queue`, and `events` is the in-process bus on the other side of
ADR 0053's frontier.

## Members

| Package | What it publishes | Aliases onto | README |
|---|---|---|---|
| `cli/` | `New` / `Execute` → an `Executor` over `Command` trees to any depth on the stdlib `flag`, a typed exit `Status`, and `FlagSource`, the flags as the last `config` layer; nothing in it can end the process (ADR 0065) | `internal/core/app/cli`, `internal/service/app/cli` | `cli/README.md` |
| `config/` | `Load[T]` over `EnvSource` / `FileSource` / `FSSource`, the schema — `NewSchema` / `LoadSchema`, required keys, defaults as a layer, unknown keys refused — the origins of every key, and the `PollWatcher` (ADR 0028, ADR 0061, ADR 0097) | `internal/core/app/config`, `internal/service/app/config` | `config/README.md` |
| `events/` | `New` → a `Bus`, `On[E]` / `Off[E]` for a typed `Handler[E]`, `Publish`; synchronous, and not a queue (ADR 0053) | `internal/core/app/events`, `internal/service/app/events` | `events/README.md` |
| `health/` | `New` → the startup, readiness and liveness registry, an HTTP handler per probe, the lifecycle `Component`, and `Ask`, the loopback readiness probe that believes only 200 (ADR 0060, ADR 0131) | `internal/core/app/health`, `internal/service/app/health` | `health/README.md` |
| `i18n/` | translated messages with CLDR plurals over a named thirteen-language subset, every other language refused by name, and `Accept-Language` negotiation that never fails a request (ADR 0063) | `internal/core/app/i18n`, `internal/service/app/i18n` | `i18n/README.md` |
| `id/` | `New(scheme)` and the helpers `UUIDv4` / `UUIDv7` / `ULID` / `Snowflake` / `NanoID` / `KSUID`, the configured `NewSnowflake` / `NewNanoID` / `NewTypeID`, and the TypeID and KSUID decoders (ADR 0024, ADR 0038) | `internal/core/app/id`, `internal/service/app/id` | `id/README.md` |
| `lifecycle/` | `New` → a `Lifecycle` you `Add` components to, `Run` for a whole service main, and the supervisor that keeps a loop running (ADR 0050, ADR 0112) | `internal/core/app/lifecycle`, `internal/service/app/lifecycle` | `lifecycle/README.md` |
| `lock/` | `NewMemory` / `NewFileLocker` → a `Locker` whose `Lease` carries a fencing token, and `Keepalive`; a zero TTL is refused (ADR 0052) | `internal/core/app/lock`, `internal/service/app/lock` | `lock/README.md` |
| `mail/` | composition, the guards and the SMTP `Transport` with header injection refused, `Validate` and `EnvelopeOf` (ADR 0064) — and nothing of the outbox, so a program that only sends links no queue | `internal/core/app/mail`, `internal/service/app/mail` | `mail/README.md` |
| `mail/spool/` | the durable outbox: `New` → a `Spool` that validates and stamps at `Send`, retries on a backoff and dead-letters with the last failure, and `SendWithID` for an identifier the caller minted (ADR 0111, ADR 0141) | `internal/core/app/mail/spool`, `internal/service/app/mail/spool` | `mail/spool/README.md` |
| `resilience/` | `NewRetry` / `NewCircuitBreaker` / `NewRateLimiter` / `NewKeyedRateLimiter` / `NewBulkhead` / `NewTimeout` / `NewFallback` / `NewHedge` → composable `Runner`s, and `Backoff`, the published curve (ADR 0026, ADR 0103) | `internal/core/app/resilience`, `internal/service/app/resilience`, `internal/kernel/backoff` | `resilience/README.md` |
| `scheduler/` | `Parse` / `ParseInLocation` (five-field cron) and `Every`, and `New` → a `Scheduler` you `Add` to and `Run` (ADR 0041) | `internal/core/app/scheduler`, `internal/service/app/scheduler` | `scheduler/README.md` |
| `statemachine/` | `Define` → a `Definition`, `New` → a `Machine` over the caller's `Store` and `Journal`, on an agenda rather than a sweep (ADR 0120) | `internal/core/app/statemachine`, `internal/service/app/statemachine` | `statemachine/README.md` |
| `validation/` | `Constraint` / `Violation` / `Report`, the combinators and built-ins, and `Struct[T]`, the struct-tag front end (ADR 0046) | `internal/core/app/validation`, `internal/service/app/validation` | `validation/README.md` |
| `view/` | `New(Config{FS: …})` → a `Renderer` on `html/template`, `TrustHTML` the one bypass (ADR 0058) | `internal/core/app/view`, `internal/service/app/view` | `view/README.md` |

No facade imports a sibling facade. `config`'s schema takes a
`validation.Constraint`, `health`'s `Component` is a `lifecycle` component and
`cli`'s `FlagSource` is a `config` source because a public type is an alias of
the internal one, so every facade naming it shares its identity
(`pkg/v1/CLAUDE.md` §Public surface contract), not because one facade links
the other.

Each that existed before moved here from `pkg/v1/<name>` in one minor
release, with no alias left at the old path — a clean break, permitted only
while the module is v0 (ADR 0155 §4, extending ADR 0040). `mail/spool` is new in the same release: the spool
was published by `mail` until ADR 0160 gave it a facade of its own.

## Do NOT

- Put Go code in this directory. A file here would publish a package nobody
  designed, at a path every member would then appear to belong to.
- Hand-edit a member's `README.md`: edit its doc comment in the design, run
  `kit gen`, `make api` and `make docs-readme` (ADR 0167).
- Leave an alias package at an old `pkg/v1/<name>` path. ADR 0155 §4 refuses
  it: it would double the surface for consumers nobody can name.
