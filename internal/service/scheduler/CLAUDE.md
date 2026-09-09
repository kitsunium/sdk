# internal/service/scheduler/

## Purpose

The concrete half of the scheduling domain (**ADR 0041**): a five-field POSIX
cron parser, a fixed-interval `Every` schedule, and the engine that fires
`core/scheduler.Job` values on a `core/scheduler.Schedule`.

Code range: `0.3.43.*` — the **parser's** failure modes. The engine emits core's
`0.2.12.*` sentinels. The split is the layer boundary: `core` must not know what
a cron expression is, so cron's refusals cannot live there.

## Contents

| File | Surface |
|---|---|
| `cron.go` | `Parse` / `ParseInLocation`, `cronSchedule`, the calendar walk, the POSIX day rule |
| `field_spec.go` | `fieldSpec` — one field's bounds, name table and hint; the item/range/step parser |
| `cursor.go` | `cursor` — the wall-clock calendar position the walk advances |
| `every.go` | `Every(period)` — the fixed-interval Schedule |
| `scheduler.go` | the engine struct, `New`, `Add`, `begin`/`finish`, `emit` |
| `config.go` | `Config` — `Clock clock.Timed` + `OnResult func(ResultValue)` |
| `entry.go` | `entry` — per-registration run state, `arm`, `due` (the missed-deadline walk) |
| `run.go` | `Run`, the wait/fire/re-arm loop, `fire`, `run`, `invoke` (panic recovery) |
| `codes.go` / `errors.go` / `reject.go` | the `0.3.43.*` block, its sentinels, and the refusal helpers |

## The accepted cron dialect

Five fields: `minute hour day-of-month month day-of-week` — `0-59`, `0-23`,
`1-31`, `1-12` or `JAN`–`DEC`, `0-6` or `SUN`–`SAT` (Sunday = 0). Per field:
`*`, a value, `a-b`, a comma list, `*/n`, `a-b/n`. Names are case-insensitive.
Macros: `@yearly`/`@annually`, `@monthly`, `@weekly`, `@daily`/`@midnight`,
`@hourly`.

**Both day fields restricted ⇒ OR**, per `crontab(5)`. `0 0 13 * FRI` fires on
every 13th AND on every Friday, not on Friday the 13th. This is the rule cron
implementations most often get wrong; `TestBothDayFieldsRestrictedIsAnOr` pins
it, and `TestOneRestrictedDayFieldIsNotAnOr` is the contrast that stops it
passing for the wrong reason.

Refused **by name**, each with a message naming the construct — six/seven-field
(seconds, year) expressions, `@reboot`, `@every`, Quartz `L` `W` `#` `?`, a step
over a single value, an inverted range, `7` for Sunday, and a valid expression
that matches no date (`0 0 30 2 *`). The rationale for each is in ADR 0041
§Decision 4.

## Why the walk looks like this

The parser advances a **calendar cursor** — wall-clock fields carried in UTC,
because UTC has no transitions and `time.Date` there is therefore a pure
calendar normaliser (`Feb 30` → `Mar 1`, `hour 24` → next day). Only once every
field matches is the candidate **materialised** into the target location, and it
is accepted only if the fields come back unchanged.

That one mechanic produces both DST behaviours without a special case:

- **Spring forward** — `time.Date` normalises a non-existent local time into the
  neighbouring offset (02:30 → 03:30), the field comparison fails, the minute is
  skipped. `0 2 * * *` skips the transition day entirely.
- **Fall back** — `time.Date` returns the FIRST occurrence, the fields match, and
  the strictly-increasing contract means the second occurrence is never revisited.
  The job fires once.

Doing both with one `time.Time` in the target location is how implementations
end up shifting a job by an hour twice a year.

The search is hierarchical — a non-matching month jumps to the 1st of the next,
a non-matching day to 00:00 of the next — so `0 0 29 2 *` costs a few hundred
steps, not four years of minutes. `horizonYears = 9` bounds it: the widest gap a
*reachable* expression can have is Feb 29 across a non-leap century year
(2096 → 2104). The reachability probe runs from a **fixed** year (`probeYear`),
never from `time.Now`, so a parse verdict never depends on the day it runs.

## Engine semantics

- **`clock.Timed`, never package `time`.** `Config.Clock` is the injected source
  for both reading and waiting. `TestPackageNeverWaitsOnTheWallClock` enforces
  it by AST over the production sources *and* the tests, and
  `TestWallClockAuditDetectsAViolation` proves the audit fires.
- **Missed deadlines: skip and count.** One run for the LATEST due instant at or
  before the wake-up; `ResultValue.Missed` counts the older ones dropped. No
  catch-up. Fires missed while the process was down are invisible — there is no
  persisted state, and ADR 0041 says so rather than implying otherwise.
- **Overlap: skip by default, and report it.** `EntryValue.AllowOverlap` is the
  caller's in-code assertion that concurrent copies are safe (the
  `resilience.HedgeConfig.Idempotent` instrument). Queueing is not offered.
- **One goroutine per fired job**, so a slow entry never delays another's
  deadline. `Run` waits for all of them before returning.
- **A panicking job is recovered** into `core/scheduler.JobPanicked` and the
  scheduler keeps running; the recovered value travels as a field, never as the
  wrap origin. A job's ordinary error is reported **verbatim**.
- **`OnResult` is serialised** (so it need not be concurrency-safe) and is **not**
  panic-recovered (it is the caller's own code).
- **The entry set is frozen for the duration of a `Run`** and thaws when it
  returns, so the loop needs no wake-up channel; `Add` and a second `Run` are
  refused with `SCHEDULER_RUNNING` meanwhile.

## Do NOT

- **Call `time.Sleep` / `time.After` / `time.NewTicker` here — or in the tests.**
  There is a named test that fails on it. Wait through `clock.Timed`.
- **Accept a cron construct "to be helpful".** A parser that guesses runs the
  job at the wrong time and reports success. Refuse, and name what was refused.
- **Make the reachability probe depend on `Now`.** A parse verdict that changes
  with the calendar passes CI and fails in production.
- **Add a queueing overlap mode.** An unbounded queue is a memory leak with a
  scheduler's name on it; a bounded one raises "what do you drop", which is the
  caller's decision to make inside their own job.

## Verification

```
bazel test --config=race //internal/service/scheduler:scheduler_test
# OR
cd internal/service && GOWORK=off go test -race -cover ./scheduler
# coverage target: 100%; the suite asserts multi-hour cadences and runs in
# milliseconds, because nothing in it sleeps.
```

Tests:

| File | Covers |
|---|---|
| `cron_external_test.go` | every accepted form's first instant; every refusal's CODE; the POSIX day rule and its contrast; strict monotonicity |
| `cron_dst_external_test.go` | spring-forward skip, fall-back fires once (asserted in UTC), the days either side, and a UTC control. Imports `time/tzdata` so `LoadLocation` resolves everywhere — the DST tests never skip |
| `run_external_test.go` | the engine on a `ManualClock`: cadence, injected-clock timestamps, missed deadlines, overlap both ways, panic recovery, verbatim job errors, the legitimate empty scheduler, `Add` refusals, the frozen entry set, the drain, reuse, exhausted and misbehaving schedules |
| `config_external_test.go` | the zero-`Config` fallbacks, jobs running with no hook, independent entries, and the refusal echo clip |
| `nosleep_external_test.go` | the wall-clock-wait audit, plus the fixture proving it detects a violation |
