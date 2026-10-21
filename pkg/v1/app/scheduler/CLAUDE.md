# pkg/v1/app/scheduler/

## Purpose

Public facade for the SDK's time-driven execution domain (ADR 0041). Aliases the
`Job`/`Schedule`/`Scheduler` port and the two domain values, and re-exports the
four constructors and all nine sentinels. Stdlib-only → dep-light; cross-OS
portable.

## Surface

| Symbol | Notes |
|---|---|
| `Job` / `Schedule` / `Scheduler` | aliases onto `core/app/scheduler`. Both `Job` and `Schedule` are FUNC types, so neither can grow a method (ADR 0039) |
| `Entry` / `Result` | short aliases for `EntryValue` / `ResultValue` |
| `Config` | alias onto `service/app/scheduler` — `Clock` + `OnResult` |
| `New` | cannot fail: a nil `Clock` falls back to the wall clock, a nil `OnResult` to no observation |
| `Parse` / `ParseInLocation` | five-field POSIX cron; UTC by default, a nil location refused |
| `Every` | fixed interval; a non-positive period refused. Deliberately not spelled `@every` |
| `OverlapPolicy` + `OverlapSkip` / `OverlapAllow` / `OverlapReplace` | alias onto `core/app/scheduler`: what a fire does while the previous run is still going — `Entry.Overlap`; Replace cancels it and runs the fire once it returned (ADR 0184) |
| `Entry.StartingDeadline` | a fire reached later than this after its instant is not run but reported `Result.Expired`; zero is no deadline (ADR 0184) |
| `InvalidEntry` / `DuplicateJob` / `SchedulerRunning` / `JobPanicked` | core sentinels (`0.2.12.*`) |
| `InvalidExpression` / `UnsupportedSyntax` / `UnreachableSchedule` / `InvalidLocation` / `InvalidInterval` | parser sentinels (`0.3.43.*`) |

## Conventions

- **Type aliases, not new types**; the four constructors are thin delegations.
- **An empty scheduler is legitimate; an unusable entry is not.** `Run` with
  nothing registered waits for its context and returns nil. `Add` refuses an
  empty name, a nil `Schedule` or a nil `Job`, and `Parse` refuses an expression
  that could never fire. ADR 0031's trap, answered on both sides.
- **The dialect is closed and the refusals are named.** Six/seven-field
  expressions, `@reboot`, `@every`, Quartz `L`/`W`/`#`/`?`, a step over a single
  value, an inverted range, `7` for Sunday, and `0 0 30 2 *` all come back as a
  typed error at construction, never as a job that runs at the wrong time.
- **DST is decided, not emergent.** Spring forward skips the day; fall back
  fires once, at the first occurrence. `Parse` is UTC; `time.Local` is never a
  default because it differs across a fleet.
- **`Result.Skipped`, `Expired`, `Replaced` and `Missed` are the observability contract.** A
  scheduler that reported only successful runs would make a permanently-skipping
  entry look healthy.
- **`Entry.AllowOverlap` is an assertion, not a hint** — the same instrument as
  `resilience.HedgeConfig.Idempotent`, and its zero value is the safe one.
- README written by `tools/genindex` from `docs/api` (ADR 0167); the package
  comment is in `doc.go`, which kit writes from `design/app/scheduler.yaml`: edit the
  design, run `kit gen`, then `make api` and `make docs-readme`.

## Do NOT

- Reimplement parsing or scheduling here — the facade is aliases + delegations.
- Import `time/tzdata` on the consumer's behalf. A named location needs the
  host's tz database or the consumer's own blank import; ~450 KB in every binary
  is their decision, and the package doc says so.

## Generated

`gen_facade.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/app/scheduler.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `gen_facade.go`: the hand-written files keep the declarations of their own, and `doc.go` — kit's too (ADR 0167) — the package comment. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `gen_facade.go` edited by hand.

## Verification

```
bazel test --config=race //pkg/v1/app/scheduler:scheduler_test
# OR
cd pkg && GOWORK=off go test -race ./v1/app/scheduler
```
