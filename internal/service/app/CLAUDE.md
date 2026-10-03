<!-- updated: 2026-10-03T06:30:00Z -->
# internal/service/app/

## Purpose

The app family's engines (ADR 0155): the concrete halves of the contracts
under `internal/core/app`, at the same paths. This directory holds no Go
code: it is a prefix, not a package, and nothing imports
`internal/service/app` itself. Each member is a package of the
`internal/service` module with its own `CLAUDE.md`, and each is published by
the facade at the same path under `pkg/v1/app` — `mail/spool`, the mail
outbox, included: `pkg/v1/app/mail/spool`, apart from the composer so that
sending a mail links no queue.

## The rule that put them together

A domain belongs here when it is a mechanism an APPLICATION is assembled from,
rather than a question about bytes, callers, the wire, the machine or
telemetry: how a program is configured and invoked (`config`, `cli`), how it
speaks to people — in their language, as a page, by mail — and checks what
they send (`i18n`, `view`, `mail`, `validation`), how its parts start, stop,
report their health and exclude one another (`lifecycle`, `health`, `lock`),
how work moves through it — announced in process, at a time, along declared
states (`events`, `scheduler`, `statemachine`) — how a call survives a failing
dependency (`resilience`), and how what it creates is named (`id`).

The members compose one another where an application would, and say so: the
`config` schema delegates a key's VALUE to `validation` (a `Rule` is a
`validation.Constraint`, and the struct-tag rules are `validation.Struct`'s —
ADR 0061), a `health` registry starts and drains as a `lifecycle` component,
and `mail/spool` mints its Message-IDs with `id`'s ULID unless the caller
supplies its own (ADR 0141). Outside the family they lean on the others
rather than repeat them: `config` and `i18n` decode through `data/codec`,
`mail/spool` is a `data/queue` consumer, `lifecycle` reaches `proc/signal`
and `proc/systemd/notify` and `health` the latter, `lock` walks its directory
with `kernel/fs/pathchain`, `config` and `mail` carry `security/secret`
values, and the members that wait do so on `kernel/clock`, the ones that
retry on the `kernel/backoff` curve.

## Members

| Package | What it is | Implements | Code range | Facade |
|---|---|---|---|---|
| `cli/` | the resolution loop, the whole-tree validation, the generated help and the `config` adapter over the stdlib `flag`; nothing in it can end the process (ADR 0065) | `core/app/cli` | `0.3.62.*` and `0.2.32.*`, both declared in `core/app/cli` | `pkg/v1/app/cli` |
| `config/` | env, file and `fs.FS` sources, the layered decode, the schema and the origins, and the cross-OS poll watcher (ADR 0028, ADR 0061, ADR 0097) | `core/app/config` | none of its own — core's `0.2.10.*` | `pkg/v1/app/config` |
| `events/` | the synchronous, priority-ordered bus and the typed `On[E]` / `Off[E]` front end, over `kernel/concur/snapshot` (ADR 0053) | `core/app/events` | `0.3.52.*` and `0.2.22.*`, both declared in `core/app/events` | `pkg/v1/app/events` |
| `health/` | the check registry, per-check timeouts, panic recovery, the drain latch, the HTTP handler, and `Ask`, the client half of a readiness probe (ADR 0060, ADR 0072, ADR 0131) | `core/app/health` | `0.3.59.*` and `0.2.29.*`, both declared in `core/app/health` | `pkg/v1/app/health` |
| `i18n/` | the thirteen-language CLDR plural table, the catalogue store that refuses an incomplete translation at load, the negotiator and the printer (ADR 0063) | `core/app/i18n` | `0.3.60.*` and `0.2.30.*`, both declared in `core/app/i18n` | `pkg/v1/app/i18n` |
| `id/` | UUIDv4/v7, ULID, snowflake, NanoID and KSUID, self-registered, and TypeID, constructor-only (ADR 0024, ADR 0038) | `core/app/id` | `0.3.39.*` and `0.2.7.*`, both declared in `core/app/id` | `pkg/v1/app/id` |
| `lifecycle/` | the ordering engine, the per-component stop budget, the opt-in `Run` over `proc/{signal,systemd/notify}`, and the supervisor that restarts a loop (ADR 0050, ADR 0112) | `core/app/lifecycle` | `0.3.49.*` and `0.2.19.*`, both declared in `core/app/lifecycle` | `pkg/v1/app/lifecycle` |
| `lock/` | the in-process locker, whose leases expire, and the file locker over `flock(2)` or `LockFileEx`, whose leases do not; the keepalive; the lock path refused when redirected or replaced (ADR 0052, ADR 0081–ADR 0084, ADR 0086) | `core/app/lock` | `0.3.51.*` and `0.2.21.*`, both declared in `core/app/lock` | `pkg/v1/app/lock` |
| `mail/` | the guards every writer runs (the injection gate, the addr-spec subset, `Validate`, `Envelope` — ADR 0160), MIME composition and the SMTP transport over `net/smtp`, with memory and capture doubles that compose as production does (ADR 0064) | `core/app/mail` | `0.3.61.*` and `0.2.31.*`, both declared in `core/app/mail` | `pkg/v1/app/mail` |
| `mail/spool/` | the durable outbox: validated and stamped at `Send`, delivered from a `data/queue` consumer, a failure parked on the backoff curve and the last one dead-lettered with its cause (ADR 0111, ADR 0141) | none — it composes `core/app/mail` and `core/data/queue` | `0.3.81.*`, declared in `core/app/mail/spool` | `pkg/v1/app/mail/spool` |
| `resilience/` | retry, circuit breaker, rate limit (one bucket, or one per caller), bulkhead, timeout, fallback and hedging, and `BackoffValue`, an alias of `kernel/backoff`'s curve (ADR 0026, ADR 0031, ADR 0103) | `core/app/resilience` | none of its own — core's `0.2.8.*` | `pkg/v1/app/resilience` |
| `scheduler/` | the five-field cron parser, the fixed interval and the firing engine, waiting on `kernel/clock` (ADR 0041) | `core/app/scheduler` | `0.3.43.*` and `0.2.12.*`, both declared in `core/app/scheduler` | `pkg/v1/app/scheduler` |
| `statemachine/` | declarations frozen per machine, per-entity transitions, and the agenda heap the loop sleeps on (ADR 0120) | `core/app/statemachine` | `0.3.88.*` and `0.2.56.*`, both declared in `core/app/statemachine` | `pkg/v1/app/statemachine` |
| `validation/` | the built-in constraints, the reflection-free `Field` / `Each` combinators, and the struct-tag front end whose plan is cached per type (ADR 0046) | `core/app/validation` | `0.3.47.*` and `0.2.15.*`, both declared in `core/app/validation` | `pkg/v1/app/validation` |
| `view/` | the `html/template` engine registered under `core/app/view.HTML`, the type-gated trust scan, and templates parsed once (ADR 0058) | `core/app/view` | `0.3.57.*` and `0.2.27.*`, both declared in `core/app/view` | `pkg/v1/app/view` |

Every range above kept its value when its package moved, and when its
declaration moved to the core mirror (ADR 0160): no package of this family
calls `errs.Define`. `codeRangeOwners`
(`internal/kernel/errs/registry_ownership_external_test.go`) names the core
directories under the same keys, and `//:audit_sources` lists the core labels;
it keeps `cli`, `health`, `lifecycle`, `lock`, `scheduler` and `view` from this
family only because their own tests read their sources at runtime.

## Do NOT

- Put Go code in this directory. A file here would make `app` a package of
  its own, and the family a domain nobody chose.
- Reimplement a primitive here because an application engine needs it: a wait
  goes to `kernel/clock`, a retry delay to `kernel/backoff`, a durable message
  to `data/queue`, a signal to `proc/signal`.
- Import one member from another without saying so in both `CLAUDE.md` files:
  today `config` reaches `validation`, `health` reaches `lifecycle`, and
  `mail/spool` reaches `mail` and `id`, and no other member reaches a sibling.
