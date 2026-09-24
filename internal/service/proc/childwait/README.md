# childwait (internal/service/proc)

The ledger that decides who receives a child's exit status (ADR 0093). Internal:
consumers never see it; they get its guarantee through `pkg/v1/process` and
`pkg/v1/reaper`.

## The guarantee

A child spawned through `pkg/v1/process` reports its own exit status from
`Wait`, even when a running `pkg/v1/reaper` collected it first: the reaper's
`wait4(-1)` takes the zombie, and the status goes to the `Process` that owns it.

## How

```
exec.Start ──Spawn(fork)──▶ pid claimed before any sweep can call it an orphan
reaper sweep ──ReapAny()──▶ wait4(-1); a claimed pid's status stored on its claim
handle.Wait ──Collected?──▶ already taken by a sweep: use it, never wait by pid
            ──own wait────▶ success: done (the only path when no reaper runs)
            ──ECHILD──────▶ Reclaim: wait for the sweep's hand-off, read the claim
```

A status that nobody in the SDK collected — something outside it reaped the
child — still comes back as `WAIT_FAILED`, and now that is the only thing it
means.

## Platforms

`ReapAny` and the populated `StatusValue` are Unix-only. Off Unix there is no
`wait4` and no reaper, so a claim is never filled and the owner's own wait is
the only waiter; the package still compiles everywhere.

## Tests

`childwait_internal_test.go` forces each race on a private ledger (spawn in
flight, a stale claim on a recycled pid, hand-off in flight, pid reuse, lost
status, nil claim);
`childwait_unix_internal_test.go` collects real children with known exit codes
through `ReapAny`. Coverage 100 %.
