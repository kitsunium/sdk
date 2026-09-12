# pkg/v1/memlimit/

## Purpose

Thin **public facade** over `internal/service/proc/memlimit` (ADR 0075): derive
the Go soft memory limit from the control-group allowance governing this process
and install it. Consumers import this package; the service and `core/proc` stay
internal.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `Source` | type alias | `= coreproc.MemorySource` — never a new named type |
| `Limit` | type alias | `= coreproc.MemoryLimitValue` (allowance + limit + source) |
| `MemorySource*` | const | re-exported enum values, typed `Source` |
| `Apply()` | func | delegates verbatim to the service |

All types are **aliases**, so values cross the facade boundary without
conversion. The function is a one-line delegation — no logic lives here.

## Why one function, and no error

Every outcome is a value. Not being in a container is the normal state on a
developer laptop, not a failure, so `Apply` cannot fail and cannot panic. A
caller that only wants to know whether it acted reads `Applied()`; one that wants
to log *why* it did not reads `Source`, which separates an operator-set
`GOMEMLIMIT` from an uncapped host from a cap too tight to honour. Collapsing
those three into a bare `false` is what the linter's original `(int64, bool)`
shape did, and it is the one thing this facade deliberately does differently.

## Relation to cgroup and rlimit

Three directions on one subject, and the doc comment states all three:

| Package | Direction |
|---|---|
| `cgroup` | WRITES a control group to bound a child process |
| `rlimit` | applies a `setrlimit(2)` ceiling the kernel enforces by failing allocations |
| `memlimit` | READS the cgroup cap already bounding THIS process and tunes the collector to live inside it |

Nothing is enforced by `memlimit`: the collector simply works harder as the heap
approaches the limit. It reduces OOM pressure rather than eliminating it, and the
package doc says so rather than implying a guarantee.

## README is generated

`README.md` is produced by `gomarkdoc` from the package doc comment in
`memlimit.go` (ADR 0008). Edit the doc comment, then run `make docs-readme` (or
the `//go:generate` line). Do **not** hand-edit `README.md`.

## Verification

```sh
bazel test --config=race //pkg/v1/memlimit:memlimit_test
go test -race ./pkg/v1/memlimit/...
```
