# scheduler

Package `scheduler` declares the SDK time-driven execution port: the `Job` that
runs, the `Schedule` that says when, and the `Scheduler` that owns the pairing
and fires it — plus the `EntryValue` / `ResultValue` domain values and the typed
sentinels (InvalidEntry / DuplicateJob / SchedulerRunning / JobPanicked).

Both `Job` and `Schedule` are function ports rather than interfaces, so neither
can grow a method and break a downstream implementer (ADR 0039).

The cron parser and the concrete engine live in `internal/service/scheduler`;
facade: `pkg/v1/scheduler`. ADR 0041. See `CLAUDE.md`.
