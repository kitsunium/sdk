<!-- updated: 2026-10-03T06:30:00Z -->
# internal/core/app/

## Purpose

The app family's contracts (ADR 0155): the ports, the values and the codes of
the mechanisms an application is assembled from. This directory holds no Go
code: it is a prefix, not a package, and nothing imports `internal/core/app`
itself. Each member is a package of the `internal/core` module with its own
`CLAUDE.md`, and each still follows the core's rules — interfaces and
immutable values, the standard library and the kernel only
(`internal/core/CLAUDE.md`). The engines are the same paths under
`internal/service/app`, and the facades the same paths under `pkg/v1/app`.

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

The other families keep what answers their narrower question, even when an
application is what uses it: the bytes and the places they are kept are
`data`'s — so the durable `queue` sits there and the in-process `events` bus
here, on the two sides of ADR 0053's frontier; callers and secrets are
`security`'s; the transport is `net`'s; the OS process is `proc`'s, and
`lifecycle` delegates signals and `sd_notify` to it rather than owning them;
telemetry is `observe`'s.

The family is one directory of fourteen, as ADR 0155 §1 lists it, with no
sub-family drawn inside it. In this layer no member imports another: each
contract stands on the standard library and the kernel alone.

## Members

| Package | What it declares | Code range | Engine |
|---|---|---|---|
| `cli/` | the `Action` and `Binder` FUNC ports — a `Binder` receives the stdlib's own `*flag.FlagSet`, unwrapped — the `Executor` port frozen at one method, `CommandValue` and `InvocationValue`; a command is a leaf or a group, never both and never neither; no registry (ADR 0065) | `0.2.32.*` | `internal/service/app/cli` |
| `config/` | the `Source` / `Validator` / `Watcher` ports, the `Describer` sibling and the `OriginValue` it reports — the layer behind each key, never the value (ADR 0097) — and `DeclaredValue`, a schema default (ADR 0061); no registry (ADR 0028) | `0.2.10.*` | `internal/service/app/config` |
| `events/` | the `Listener` FUNC port, the frozen three-method `Bus`, `SubscriptionValue` / `DispatchValue` / `Priority` and the `Halt` sentinel, keyed on the event's concrete Go type; no registry (ADR 0053) | `0.2.22.*` | `internal/service/app/events` |
| `health/` | `Check` (context-carrying) and `SelfCheck` (context-free, so a liveness check cannot reach a dependency), `Probe`, `Status`, `ResultValue`; no registry (ADR 0060) | `0.2.29.*` | `internal/service/app/health` |
| `i18n/` | the `Catalog` port frozen at two with `KeyLister` / `Fallbacker` siblings, `TagValue`, `MessageValue`, the CLDR `Form`, `CountValue` and `Args`; no registry (ADR 0063) | `0.2.30.*` | `internal/service/app/i18n` |
| `id/` | the `Generator` port and the `Scheme` registry — TypeID is constructor-only, so nothing is registered under it (ADR 0024, ADR 0038) | `0.2.7.*` | `internal/service/app/id` |
| `lifecycle/` | the `Start` and `Stop` FUNC ports, the three-method `Lifecycle`, `ComponentValue` / `TransitionValue` / `Phase`; the Add order is the dependency order, with no graph; no registry (ADR 0050) | `0.2.19.*` | `internal/service/app/lifecycle` |
| `lock/` | the `Locker` port frozen at two (no TTL on either), the `Lease` frozen at three, and the `Deadliner` sibling a lease implements only when it can expire; no registry (ADR 0052) | `0.2.21.*` | `internal/service/app/lock` |
| `mail/` | the `Transport` port frozen at one with `BatchSender` / `Outbox` siblings, the message as values, the `EnvelopeValue` where Bcc becomes RCPT TO and no header, and the injection gate every writer runs; no registry (ADR 0064) | `0.2.31.*` | `internal/service/app/mail`, and the outbox `internal/service/app/mail/spool` above it |
| `resilience/` | the `Runner` port, the `Operation` FUNC port and the sentinels the seven policies return; no registry (ADR 0026) | `0.2.8.*` | `internal/service/app/resilience` |
| `scheduler/` | the `Job` and `Schedule` FUNC ports, the `Scheduler`, `EntryValue` / `ResultValue`; no registry (ADR 0041) | `0.2.12.*` | `internal/service/app/scheduler` |
| `statemachine/` | the `Store[E]` port the caller implements, frozen at five where absence is an answer, the `Journal[S]` port frozen at three, `RecordValue` / `StepValue`, `Trigger` and `CreateEvent`; no registry (ADR 0120) | `0.2.56.*` | `internal/service/app/statemachine` |
| `validation/` | the `Constraint[T]` FUNC port, the located `ViolationValue`, the `ReportValue` that is not an `error`, and the path grammar; no registry (ADR 0046) | `0.2.15.*` | `internal/service/app/validation` |
| `view/` | the `Renderer` port frozen at two, the `Factory` registry, and `TrustedHTML` / `TrustHTML`, the one escaping bypass; no `text/template` representation (ADR 0058) | `0.2.27.*` | `internal/service/app/view` |

A code keeps its value when its package moves (ADR 0160): the fourteen ranges
above are the ones these packages declared before the family existed, and
`codeRangeOwners` (`internal/kernel/errs/registry_ownership_external_test.go`)
names the new directories under the same keys.

## Do NOT

- Put Go code in this directory. A file here would make `app` a package of
  its own, and the family a domain nobody chose.
- Give `events` an asynchronous or durable mode because an application wants
  one: a message that must outlive the process is `data/queue`'s (ADR 0053).
- Renumber a member's codes to match its new path. Nothing derives a code from
  a directory, and a consumer branches on the value (ADR 0160).
