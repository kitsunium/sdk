# ADR 0041 — Time-driven execution domain (`scheduler`)

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Related**: [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (`clock.Waiter` / `ManualClock` — the thing that makes this domain testable), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero value is a safe default or an explicit refusal), [ADR 0016](0016-sdk-process-supervision-domain.md) / [ADR 0026](0026-sdk-resilience-domain.md) (the no-registry core-sibling precedents), [ADR 0018](0018-sdk-cross-platform-portability.md) (what is guaranteed on 8 GOOS), [ADR 0030](0030-stdout-is-a-protocol-channel.md) (a zero value must not be the dangerous one), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (published concrete shapes), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) / [ADR 0035](0035-pp-range-ownership-enforcement.md) (codes and range ownership)
- **Amends**: `internal/core`'s purpose statement — `scheduler` is its 12th sibling

## Context

Every downstream that runs anything on a timetable writes the same three things
by hand: a cron parser, a loop around `time.Ticker`, and — eventually, after an
incident — an overlap guard. The parser is where it goes wrong. Cron looks like
a small format and is not: five dialects disagree about the field count, about
whether the two day fields are an AND or an OR, and about a dozen operators, and
a permissive parser that "does its best" with an expression it does not
understand runs the job at the wrong time and reports success.

The SDK could not honestly ship this domain until now, for a reason worth
recording. `clock.Clock` had two methods, `Now` and `Since`; it could read time
and not wait for it. "Inject the clock and the scheduler becomes testable" was
an empty claim — there was nothing temporal to inject, so every cadence, every
missed deadline and every overlap test would have had to sleep, and a suite that
sleeps is a suite that grows tolerances and then gets deleted. ADR 0039 fixed
that: `clock.Waiter` (`After`/`NewTimer`/`NewTicker`/`Sleep`), `clock.Timed` as
the union, and `ManualClock` whose time moves only when a test moves it. This
domain is the first consumer of that surface, and it is built to prove it.

## Decision

1. **Add `internal/core/scheduler`** — a core sibling declaring three things and
   nothing else: `Job func(ctx) error`, `Schedule func(after time.Time)
   (time.Time, bool)`, and `Scheduler interface { Add(EntryValue) error;
   Run(ctx) error }`, plus `EntryValue`, `ResultValue` and the typed sentinels.
   **No registry** — there is one canonical engine and one canonical cron
   dialect, so a registry would be over-abstraction (the `proc` and `resilience`
   precedents, ADR 0016 / ADR 0026). Cron vocabulary does not appear in core at
   all: the port knows about instants, not expressions.

2. **Both ports are FUNCTION types, not interfaces.** `internal/core/CLAUDE.md`
   already admits a single-method function port (`resilience.Operation`), and
   ADR 0039's lesson makes it the right default here rather than a stylistic
   one: a published port cannot grow a method without breaking every downstream
   implementer at compile time, and **a func type cannot grow one at all**. It
   also avoids naming a single-method interface `Schedule`, which would have
   needed a `.ktn-linter.yaml` exemption to escape being called `Nexter`.
   `TestPortsAreFunctionsNotInterfaces` is the executable guard.

3. **`internal/service/scheduler`** ships the parser (`Parse` /
   `ParseInLocation`), the fixed-interval schedule (`Every`), and the engine
   (`New`). The engine depends on `clock.Timed` and **never on package `time`'s
   waiting surface**, which `TestPackageNeverWaitsOnTheWallClock` enforces over
   the production sources *and* the test suite by AST rather than by convention.

4. **`pkg/v1/scheduler`** aliases the port and the two values, re-exports the
   constructors and all nine sentinels.

5. **Two error blocks, split along the layer boundary.** Core owns `0.2.12.*`
   for what the *port* can go wrong about — `INVALID_ENTRY`, `DUPLICATE_JOB`,
   `SCHEDULER_RUNNING` (all `EX_CONFIG`, because re-running the same `Add` is
   refused identically), and `JOB_PANICKED` (`EX_SOFTWARE`). Service owns
   `0.3.43.*` for what *cron* can go wrong about — `INVALID_EXPRESSION`,
   `UNSUPPORTED_SYNTAX`, `UNREACHABLE_SCHEDULE`, `INVALID_LOCATION`,
   `INVALID_INTERVAL`. Putting the parser's codes in core would put cron's
   vocabulary in a package that must not know cron exists.

### The four arbitrages

Each of these has no universally correct answer. Each therefore has a
**documented** one, and a test that pins it.

#### 1. Time zones and DST

**A schedule is evaluated in an explicitly chosen `*time.Location`. `Parse`
means UTC. `ParseInLocation(expr, nil)` is refused.**

`time.Local` is never a default: it depends on `TZ` and on the host, so one
expression would mean different instants on two machines in the same fleet, and
the symptom is a job that ran an hour early on one node. A `nil` location is
refused rather than read as UTC because `nil` is exactly what an unchecked
`time.LoadLocation` leaves behind — accepting it converts a missing tz database
into a schedule running on a clock the caller did not choose, silently. This is
ADR 0031's refuse half: the caller who called `ParseInLocation` was asking for a
specific zone, and substituting one answers a question they did not ask.

Across a transition:

| Transition | Behaviour | Why |
|---|---|---|
| **Spring forward** — a wall-clock time that does not exist that day | **Does not fire.** `0 2 * * *` in a zone that skips 02:00–03:00 skips that day entirely. | Shifting to 03:00 runs the job at an instant the expression does not name; shifting to 01:00 runs it early. Skipping is the only answer that preserves what the expression says, and it costs one missed run a year. |
| **Fall back** — a wall-clock time that happens twice | **Fires once, at the first occurrence.** | `Schedule` is strictly increasing by contract; once 01:30 EDT has been returned, the next answer must be after it, and the walk moves on to the next calendar day rather than re-offering the same reading in the other offset. |

Both fall out of one mechanic rather than out of special cases: the walk
advances a **calendar cursor** (wall-clock fields normalised through UTC, which
has no transitions and is therefore a pure calendar normaliser), and only then
materialises a candidate with `time.Date` in the target location, **accepting it
only if the fields come back unchanged**. `time.Date` normalises a non-existent
local time into the neighbouring offset, so the field comparison is what turns a
silent one-hour shift into an honest "this minute does not exist". For an
ambiguous time the fields match, and the first occurrence has to be ASKED for:
`time.Date` documents that it guarantees neither, and in every zone east of UTC
its lookup returns the LATER one — 02:30 on 2026-10-25 in Europe/Berlin comes
back as 01:30Z, not 00:30Z. The engine therefore tries the same fields under
the offset in force before the transition (read from the zone table, since
Australia/Lord_Howe moves by thirty minutes) and keeps the earlier instant when
it reads back unchanged; the strictly-increasing contract does the rest.
(Amended 2026-09-11: the first version relied on `time.Date` returning the
first occurrence, which holds only west of UTC, so a fall-back job fired once
but an hour late in Europe and Australia.)

Resolving a named location needs the host's tz database or a blank import of
`time/tzdata` in the consumer's binary. **The SDK does not import `time/tzdata`
for you**: it is ~450 KB in every binary that links it, and that is the
consumer's call. The test suite imports it, so the DST assertions run on every
GOOS and inside a hermetic sandbox rather than being skipped where tzdata is
absent — a skipped DST test is precisely the lane rule 12 warns about.

#### 2. Missed deadlines

**Skip, and count.** When several due instants have passed by the time the
scheduler wakes — the machine slept, the VM was paused, a run held the slot —
the job runs **once**, attributed to the **most recent** due instant, and
`ResultValue.Missed` reports how many older ones were dropped.

Catch-up was rejected: a job that runs hourly and was unreachable for three days
comes back to seventy-two immediate invocations, which is a stampede aimed at
whatever was already unhealthy. Silent skipping was rejected too — it is the
same defect ADR 0031 names, an entry that is hours behind looking exactly like
one that is on time. Attributing the run to the *latest* instant rather than the
oldest is what makes "skip" coherent: the run that happens is the current one,
and the stale ones are what got dropped.

**Fires missed while the process was not running are invisible.** The scheduler
keeps no state across restarts, `Missed` counts only within one `Run`, and this
is stated rather than papered over. Persisting the last-fire instant is a
different component — it needs a store, a schema, and an answer to "what happens
when two replicas share it" — and inventing one here would be the worst kind of
half-measure.

#### 3. Overlap

**Skip, by default, and report the skip.** If the previous run of an entry has
not returned when its next deadline arrives, the fire is dropped and surfaces as
`ResultValue{Skipped: true}`.

- *Queueing* was rejected: behind a persistently slow job the queue grows without
  bound, and it is a memory leak wearing a scheduler's name. A bounded queue
  raises "what do you drop", which is a decision only the caller can make — in
  their own job body.
- *Parallel* was rejected as the default: it duplicates a side effect the SDK
  cannot know is safe. That is `resilience.HedgeConfig.Idempotent`'s argument
  exactly, and the same instrument is used — `EntryValue.AllowOverlap` is an
  **in-code assertion** whose zero value is the safe answer, per ADR 0030's rule
  that a zero value is the choice made by someone who has not yet learned the
  question exists.

Reporting the skip is not decoration. It is the difference between an entry that
is keeping up and one that has not completed a run in a week, and it is also the
synchronisation point that lets the test suite assert a *negative* — "this fire
did not run" — without sleeping to prove it.

#### 4. Cron dialect

**Five-field POSIX, plus five macros. Everything else is refused by name.**

Accepted: `minute hour day-of-month month day-of-week`, values `0-59`, `0-23`,
`1-31`, `1-12` or `JAN`–`DEC`, `0-6` or `SUN`–`SAT`; per field `*`, a value, an
`a-b` range, a comma list, and a `*/n` or `a-b/n` step; names case-insensitive;
`@yearly`/`@annually`, `@monthly`, `@weekly`, `@daily`/`@midnight`, `@hourly`.

The POSIX day rule is implemented as POSIX writes it: **when both day fields are
restricted, a day matches if EITHER matches.** `0 0 13 * FRI` fires on every
13th *and* on every Friday — not on Friday the 13th. This is the rule
implementations most often get wrong, and it has its own test with the AND
reading spelled out in the failure message.

Refused, each with a message naming the construct:

| Refused | Why not just accept it |
|---|---|
| Six / seven fields (seconds, year) | Read as five fields, `0 0 12 * * *` means something else entirely and runs at the wrong time reporting success. A seconds field would also promise a resolution this domain explicitly does not guarantee. |
| `@reboot` | A library has no boot to observe — `Run` starts when the caller calls it. Accepting it would mean inventing a meaning. |
| `@every <d>` | Not cron. An interval has no month, no weekday and no DST question. It gets its own constructor, `Every(d)`, because conflating the two is how the confusion starts. |
| Quartz `L`, `W`, `#`, `?` | Silently ignoring an `L` runs the job on the wrong day. The check runs only on tokens that are neither a number nor a known name, so `JUL` and `WED` are never mistaken for operators. |
| `7` for Sunday | The subset is 0–6. The refusal names the accepted range and says 7 is a Vixie extension, so the fix is one character. |
| A step over a single value (`5/10`) | A Vixie extension that reads as a typo far more often than as itself. |
| An inverted range (`FRI-MON`) | Some dialects wrap. Silently selecting a different set than the text reads is the failure this parser exists to prevent. |
| **A valid expression matching no date** (`0 0 30 2 *`) | Registering it would create a job that never runs while the caller believes it does. Detected at parse time by probing a fixed origin over a nine-year horizon — nine because the widest gap a reachable expression can have is Feb 29 across a non-leap century year. The probe uses a **fixed** year, never "now", so a parse verdict never depends on the day it runs. |

### The empty scheduler is legitimate; the empty entry is not

This is the trap ADR 0031 describes, and the two halves are answered
differently on purpose:

- A `Scheduler` with **no entries** runs, waits for its context, and stops
  cleanly. That is a service whose jobs are all behind a feature flag; refusing
  it would be wrong.
- An **entry** that could never run — empty name, nil `Schedule`, nil `Job` — is
  refused by `Add` with `INVALID_ENTRY`, naming the missing part.
- An **expression** that could never fire is refused by `Parse`.

The failure is not "nothing is registered". It is "something is registered that
cannot work, and nobody was told".

### Cross-platform (ADR 0018) — what is guaranteed

100 % portable Go (`time`, `context`, `sync`, `sync/atomic`, `strconv`,
`strings`, `slices`). No OS-specific code; the build bar is trivial on all 8
GOOS. The runtime bar is stated as a bound rather than implied:

> **A job is never fired early. Lateness has no upper bound.**

Lateness is whatever the platform's timer resolution, the machine's load and the
OS's scheduling impose, and on a machine that suspends it is unbounded. Timer
granularity is not uniform across the support matrix — this is the concrete
reason cron's finest field here is the minute and the reason a seconds field is
refused rather than supported at a resolution the SDK cannot promise everywhere.

## Consequences / Semantics

- **12th core sibling, no registry.** `pkg/v1` gains a dep-light facade
  (stdlib + kernel + core only). Docs and `docs/error-codes.yaml` updated in the
  same change, per rule 11.
- **Jobs run on their own goroutine.** A slow job never delays another entry's
  deadline — which is what makes overlap a real question rather than a
  theoretical one.
- **`Run` drains.** It returns only after every in-flight job has returned. A
  job that ignores `ctx` delays shutdown by its own duration; the scheduler will
  not abandon it, because work continuing after the caller believes the
  scheduler stopped is worse than a slow stop.
- **A failing job never stops the scheduler**, and its error is reported
  **verbatim** — not relabelled — so the caller's `errors.Is` against their own
  sentinels keeps working. A **panicking** job is recovered into `JOB_PANICKED`
  and the scheduler keeps running: one job's bug must not take the process, and
  every other entry, down with it. The recovered value travels as a field, never
  as the wrap origin, so a panic carrying an `*errs.Error` cannot hijack the code.
- **`OnResult` is serialised and is NOT panic-recovered.** Serialising means the
  hook need not be concurrency-safe; the cost, stated in its doc comment, is
  that a hook which blocks blocks the scheduler. It is not recovered because it
  is the caller's own code, and hiding an observer's panic hides the defect in
  the code that was supposed to be watching.
- **A nil `OnResult` is a working configuration, not a refusal** — a caller whose
  jobs log their own failures has nothing to observe. It does mean a job's error
  goes nowhere; the SDK will not write it to stderr on the caller's behalf
  (ADR 0030), and the doc comment says so plainly.
- **A misbehaving `Schedule` disarms its own entry.** A downstream `Schedule`
  that returns a non-advancing instant breaks the port's strictly-increasing
  contract and would spin the missed-deadline walk forever. The engine treats it
  as exhausted, so one caller's bug costs one entry rather than the whole
  scheduler. Its test does not *fail* without the guard — it hangs, which is the
  failure mode being prevented.
- **`ResultValue` is a published concrete shape** (`pkg/v1/scheduler.Result` is
  an alias), so ADR 0040 applies: it may still change while the module is v0,
  said out loud, and not after v1.
- **The no-sleep claim is enforced, not asserted.** `TestPackageNeverWaitsOnTheWallClock`
  parses every `.go` file in `internal/service/scheduler` — production and tests
  — and fails on any use of package `time`'s waiting surface (`Sleep`, `After`,
  `AfterFunc`, `Tick`, `NewTimer`, `NewTicker`). It reads the AST rather than
  grepping, so the forbidden identifiers can still be written in prose, it fails
  loudly when it scans nothing, and `TestWallClockAuditDetectsAViolation` proves
  it fires on the violations it claims to catch. The suite asserts three-hour
  cadences, a five-hour gap and an overlap window; on a ManualClock, the whole
  package runs in milliseconds.

## Breaking changes

None. `scheduler` is a new domain in this change set — there is no prior
published surface to break.

## Alternatives considered

- **Wrap a third-party cron library.** Rejected: the popular ones bring their
  own dialect decisions (six fields, `@every`, Quartz operators) and their own
  DST behaviour, which is exactly the set of choices this ADR exists to make
  deliberately. They also schedule on package `time`, which would put the
  untestability ADR 0039 removed straight back in.
- **`Schedule` as an interface with `Next` + `String`.** Tempting — a concrete
  schedule that can describe itself makes better diagnostics. Rejected on
  ADR 0039: a two-method published port is twice the surface to freeze, and the
  entry's `Name` already identifies it in every result and every error field. A
  concrete implementation may still carry a `String` discoverable by type
  assertion, the way `codec.Appender` works (ADR 0037).
- **`Add` / `Remove` while running.** Rejected for this cut: it needs a wake-up
  channel and a lock on the loop's hot path, and the common shape — declare the
  jobs at boot, run — does not need it. The entry set is frozen for the duration
  of a `Run` and thaws when it returns, so a service that stops and restarts its
  scheduler does not need a new one. Recorded under Deferred rather than
  designed hastily.
- **Refuse `Config.OnResult == nil`.** Considered under ADR 0031 and rejected on
  its own test: a nil hook is not an SDK-chosen value substituted for the
  caller's intent, it is a coherent "no thanks". The engine still schedules and
  still runs. The risk it carries — invisible job failures — is answered with
  documentation rather than a refusal.
- **Attribute a coalesced run to the OLDEST missed instant.** Rejected as
  incoherent with the decision above: running for the oldest deadline is
  catch-up wearing a skip's clothes.

## Deferred

- **Dynamic `Add` / `Remove` during a run**, with the wake-up mechanics that
  implies.
- **Persisted last-fire state**, which is what would make deadlines missed
  across a restart visible — and what raises the multi-replica question this
  domain deliberately does not answer.
- **Distributed / leader-elected scheduling.** One process, one scheduler. Two
  replicas of a service both running this scheduler both fire every job; that is
  a coordination problem, not a scheduling one.
- **Sub-minute cron.** `Every` covers fixed intervals below the minute. A
  seconds *field* stays refused until the domain can state a lateness bound it
  actually holds on every supported GOOS.
- **A jittered schedule decorator**, to spread a fleet's `0 0 * * *` jobs. It is
  a `Schedule` wrapper and needs no port change, which is why it can wait.

## References

- Impl: `internal/core/scheduler/`, `internal/service/scheduler/`, `pkg/v1/scheduler/`.
- `internal/kernel/clock/CLAUDE.md` §"clock vs `testing/synctest`" — why this domain uses `ManualClock` rather than a bubble (parallel table tests, an origin that is not 2000-01-01, and a clock a constructor demands).
- `crontab(5)` — the day-field OR rule quoted in Decision §4.
- ADR 0039 (the `Waiter`/`Timed`/`ManualClock` surface), ADR 0031 (clamp vs refuse), ADR 0016 / ADR 0026 (no-registry siblings), ADR 0018 (portability).
