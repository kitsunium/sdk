<!-- updated: 2026-10-14T20:30:00Z -->
# e2e/harness/

## Purpose

The runner the e2e conformance binary is built on: a `Check` exercises one
public-API behaviour on the host and returns a `Result`, a `CheckGroup` is a
domain's checks, and `Run` executes every group, prints one table and returns
the number of failures — `e2e/main.go` turns it into the exit status. The
binary's rules are in `e2e/CLAUDE.md`; the groups are `../checks/`.

## Contents

| File | Holds |
|---|---|
| `checkgroup.go` | `Check` (`func() Result`, never meant to panic) and `CheckGroup` (`Domain` + `Checks`, run in order) — the domain is carried by the group, so a check cannot label itself into another domain |
| `harness.go` | `Status` and its four values (`PASS`, `FAIL`, `UNSUPPORTED`, `SKIP`); `Result` (`Domain`, `Name`, `Status`, `Detail`) built only by `Passed` / `Failed` / `NotSupported` / `Skipped`; `Run` over `runWithin`; `watchedRun` (the `CheckTimeout` watchdog), `allStacks` (the goroutine dump, grown to fit up to 16 MiB), `safeRun` (a panic recovered as a Fail), `report` (the sorted table and the tally), `stderrf` |
| `harness_external_test.go` | the four constructors, `Run` end to end, and the table's ordering |
| `harness_internal_test.go` | containment of a panic (a nil dereference only off Windows — §Rules), the watchdog, a hung check costing its own row only, the tally, and a write fault that does not change the verdict |

## Rules

- **Only `FAIL` counts.** `UNSUPPORTED` is the expected off-platform contract
  and `SKIP` makes no claim; neither moves the exit status.
- **A check that panics or hangs costs its own row and nothing else.** A panic
  is recovered as a Fail; a check still running after `CheckTimeout` (one
  minute) is a Fail named by its position in its group (`check 2 of 5`), and
  every goroutine's stack is printed before the table, which names the stuck
  function. The goroutine is abandoned, not stopped — the process is about to
  print and exit. Debian and Fedora VM legs once ran to the job's 25-minute cap
  with no output because the table printed only after every check returned
  (#118).
- **The suite takes no hardware fault on Windows.** A recovered access
  violation on a Windows host whose exception frame carries the AMX tile state
  is dispatched below the goroutine stack and corrupts the heap span beneath it
  (golang/go#81238, open in go1.27.2): the nil-dereference case of
  `Test_safeRun` killed this package on windows-latest with `fatal error:
  receive on synctest channel from outside bubble`, in a binary that uses no
  synctest (#304). That case is skipped on Windows and runs on linux/386
  (`bazel-ci.yml`'s `test-386`, over the census) and macOS (`e2e-cross.yml`'s
  every-package step) — its rule-12 lanes; a nil-map write — a runtime error the
  runtime raises without a fault — pins containment everywhere. Lift the skip
  once a Go release carries the fix (go.dev/cl/828724). The same defect would
  corrupt the conformance binary itself if a check dereferenced nil on such a
  host, which is one more reason a `Check` must not panic.
- **`Detail` is always set**, so a failure is diagnosable from the table alone.
- **The table is sorted** by domain, then check name, so two machines' runs
  diff line for line.
- **A write fault never changes the verdict**: the tally is computed from the
  results, not from what reached the output.

## Verify

```sh
cd e2e && GOWORK=off go test -race ./harness/
```
