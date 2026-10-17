<!-- updated: 2026-10-18T02:00:00Z -->
# framework/kit/jobs — the jobs, for a product that declares them

A subsystem of `framework/kit`, enabled by a blank import:
`import _ "github.com/kitsunium/sdk/framework/kit/jobs"` (ADR 0179 §1,
step 7). It plugs in the scheduler kit's jobs run on — the SDK's scheduler,
on the app's clock — and the schedules it reads from a job's declaration, a
period (`Service.Every`) or a five-field cron expression (`Service.Cron`)
(`jobkit.Enable`). A product that declares no job links none of it; one that
declares a job without the import is refused at the start, every job at
once, the refusal naming this import and carrying `CodeSubsystemMissing`
(0.4.2.83).

It imports no other registration package: a job runs over nothing a store,
a queue, a workflow or a mail subsystem provides (ADR 0179 Q4 names no
import for it). Until ADR 0179's step 10, `framework/kit` imports this
package itself (`subsystems.go`, Q8): no product needs the line yet, and
`kit gen` will write it (§5). The status line declares two jobs in its
daemon role, so it imports it once the products are edited (step 9).

kit's own job code — the declaration (`Job`, `Every`, `Cron`), the
scheduler's component and loop, each job's loop and span, the overlap with a
run the Studio started — names no SDK type and stays in the core; a job's
schedule is read at the start, through the subsystem, never at the
declaration, which a package's init may run before this package's.

## Contents

| File | Holds |
|---|---|
| `jobs.go` | the package documentation and `enable`, which calls `jobkit.Enable` |
| `README.md` | written by `tools/genindex` from `docs/api` (`make docs-readme`, ADR 0167) |

## Rules

- It holds no behaviour and exports nothing: the call it makes is into `framework/internal/kit/jobkit`; the refusal naming the import lives in the core (`subsystem.go`, `jobs_plug.go`, `subsystems_internal_test.go`).
- The call runs from a blank package-level value (`var _ = …()`), never an `init`: the SDK's convention (`KTN-FUNC-NOINIT`).

## Verify

```sh
cd framework && GOWORK=off go vet ./kit/jobs/ && GOWORK=off go test ./kit/ -run 'ImportsTheJobs|TheSmallestProduct' && GOWORK=off go test ./internal/kit/ -run 'AJob|Scheduler'
```
