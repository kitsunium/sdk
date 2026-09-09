# scheduler

Package `scheduler` provides the concrete half of the SDK scheduling domain: a
five-field POSIX cron parser (`Parse` / `ParseInLocation`), a fixed-interval
schedule (`Every`), and the engine (`New`) that fires
`internal/core/scheduler.Job` values.

It waits through `internal/kernel/clock.Timed`, never through package `time`, so
cadence, missed deadlines and overlap are asserted on a `ManualClock` without
sleeping — enforced by `TestPackageNeverWaitsOnTheWallClock`.

Missed deadlines are skipped and counted; an overlapping fire is skipped by
default and reported. Constructs from other cron dialects are refused by name.
Ports: `internal/core/scheduler`; facade: `pkg/v1/scheduler`. ADR 0041.
See `CLAUDE.md`.
