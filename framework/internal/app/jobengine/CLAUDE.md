<!-- updated: 2026-10-18T02:00:00Z -->
# framework/internal/app/jobengine — the jobs' scheduler

What `framework/app/jobs` plugs into kit (`plug.Jobs`, ADR 0179 §1, step 7):
the scheduler kit's jobs run on — the SDK's `scheduler`, on the app's clock —
and the schedules it reads from what a job declares, a period (`Every`) or a
five-field cron expression in the process's local time zone (`Cron`). kit's
core imports none of the SDK's scheduler: it reads every job's schedule at the
start through `plug.JobDriverConfig`, opens the scheduler with a
`plug.JobSchedulerConfig` and holds it as a `plug.JobScheduler`, and a program
that does not import `framework/app/jobs` links none of this package
(`framework/app`'s `TestTheSmallestProductLinksOnlyTheSubsystemsStillInTheCore`)
and is refused at the start for a job it declares (`CodeSubsystemMissing`).

## Contents

| File | Holds |
|---|---|
| `jobengine.go` | `Enable`, which sets `plug.Jobs` (`every`, `cron`, `open`); `every` and `cron` — the SDK's `scheduler.Every` and `scheduler.Parse` as a `plug.JobScheduleFunc`; `open` — the SDK's `scheduler.New` on the app's clock, kit's `OnResult` told each decision as a `plug.JobResultValue` (`resultOf`); `runner`, the SDK's scheduler as kit's core uses it (`Add` — the entry's `Overlap` and `StartingDeadline` the SDK's `scheduler.Entry`'s, ADR 0184 —, `Run`); `resultOf` carries the SDK's `Expired` and `Replaced` |
| `clock.go` | `sdkClock`, the app's clock (`plug.Clock`) as the SDK's scheduler stamps and waits on it — a test's manual clock included |
| `jobengine_compliance.go` | the compile-time assertions: the scheduler is a `plug.JobScheduler`, the clock the SDK's, and the numbers `plug.JobEntryValue.Overlap` gives the overlap policies — 0 skip, 1 allow, 2 replace — the SDK scheduler's `OverlapPolicy` values |
| `jobengine_internal_test.go` | the schedules are the SDK's (a period and its refusal, a cron expression and its refusal); the scheduler fires on a manual clock, kit hears the run, a name twice is the SDK's `DuplicateJob`, and the run ends with its context; a job's overlap and starting deadline are the SDK's (issue #483 part 4): a fire past its deadline heard expired and never run, a run a newer fire replaced heard replaced, an overlap the SDK does not define its `InvalidEntry` |
| `doc.go`, `README.md` | the package comment (kit writes it from the design, ADR 0167); the README, written by `tools/genindex` from `docs/api` (`make docs-readme`) |

## Rules

- It imports `plug` and the SDK (`app/scheduler`, `clock`), never kit's core nor another `*kit` package: the core imports no `*kit` package (ADR 0179 §3), and a job needs no other subsystem — `framework/app/jobs` imports no other registration package.
- What the scheduler decides about time is the SDK's, unchanged: a fire due while the previous run of the same job is going is skipped and counted — or run beside it, or the run replaced, as the job's overlap says —, a missed deadline is skipped and counted, never caught up, a fire past the job's starting deadline is not run and counted missed, and a cron expression crosses a DST change as the SDK's `cron` does. The core's suites hold it through the scheduler they run (`framework/internal/app`'s runtime and Studio tests), the SDK's own suites (`internal/service/app/scheduler`) hold the rest.
- The core keeps the scheduler's own loop, each job's loop, the overlap with a run the Studio started (`errOverlapped`, which the SDK reports as the run's error, unwrapped) and the workflows' timers, which start with the scheduler's component; an app that declares no job runs the core's `idleScheduler` and needs no subsystem.

## Verify

```sh
cd framework && GOWORK=off go vet ./internal/app/jobengine/ && GOWORK=off go test -race ./internal/app/jobengine/ && GOWORK=off go test -race ./internal/app/ -run 'Job|Scheduler|Runtime|Loop|DeclarationProblems'
```
