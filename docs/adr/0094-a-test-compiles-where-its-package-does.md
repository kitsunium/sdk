# ADR 0094 — a test compiles where its package does, and macOS runs every one

- **Status**: Accepted
- **Date**: 2026-09-25
- **Deciders**: SDK maintainers
- **Related**: [ADR 0018](0018-sdk-cross-platform-portability.md) (the two portability bars this tightens), [ADR 0088](0088-a-suite-nothing-runs-is-not-a-test-suite.md) (a gate is what CI runs and says)

## Context

ADR 0018 gates portability with two bars. The build bar is `go build ./...` per
module across ten GOOS/GOARCH targets — the `cross-build` job, and
`scripts/cross-platform-audit.sh` locally. `go build` never compiles a
`_test.go` file, so a test calling a helper that one GOOS alone defines passed
every cell of the matrix while its package's tests could compile nowhere else:

- `internal/service/net/server`: `Test_Server_adoptStreamSockets`,
  `Test_Server_adoptPacketSockets` and `Test_Server_readLoop` sit in files every
  GOOS compiles and called `adoptableListenerFile`, `adoptablePacketFile`,
  `releaseOnCleanup` and `udpPair`, defined in `//go:build linux` files.
  `go vet ./...` failed on darwin and windows.
- `internal/service/writer/file/file_linux_bench_test.go` said its `_linux`
  suffix was load-bearing. It was not: Go reads a GOOS only as the last element
  before `_test.go`, and `_bench` follows it. The file compiled on every GOOS,
  and on windows, which has no `syscall.O_NOFOLLOW`, it broke the package's
  tests.
- The root module's `go.sum` lacked the `go.mod` hash of
  `github.com/fxamacker/cbor/v2 v2.9.3`, which its tests reach, so
  `GOWORK=off go vet ./...` failed there on every GOOS.

The runtime bar ran the platform-sensitive packages only, on the premise that
pure-Go packages behave identically everywhere. Their tests did not. The whole
suite, run on darwin/arm64 for the first time, failed in two ways, each a test
assuming Linux:

- A Unix socket bound under `t.TempDir()`. That path carries the test's name,
  and macOS's `$TMPDIR` is already 49 bytes: past the 104 bytes a `sun_path`
  holds there (108 on Linux), bind fails with `EINVAL` — 5 cases in
  `net/server`, 3 in `pkg/v1/server`.
- A tree laid out under `t.TempDir()` to stand for a path git reports. On macOS
  `/var` is a link to `/private/var`, so the "canonical" root was not canonical
  — 2 cases of `Test_spelledTopLevel` in `vcs/git`. The code under test was
  right; its fixture was not.

## Decision

1. **The build bar compiles the tests.** `cross-build` runs `go vet ./...` after
   `go build ./...` in every module and cell, and `cross-platform-audit.sh` vets
   every package after building it. `go vet` type-checks the test files, so a
   test only one GOOS can compile fails the cells it breaks. Measured after the
   fixes below: 10 targets × 6 modules, all green.
2. **A test helper lives where every GOOS that runs its callers compiles it.**
   `udpPair` and `releaseOnCleanup` moved to `testsockets_internal_test.go`, with
   no constraint. The descriptor a supervisor would pass is made in
   `testsockets_unix_internal_test.go` (`unix`: a socket can become an
   `*os.File`); `testsockets_other_internal_test.go` skips the case elsewhere,
   where `net`'s `File` answers "not supported by windows". The Linux-only
   socket identity (`socketOf`, read from `/proc/self/fd`) stays in
   `adopt_internal_test.go`. The benchmark carries `//go:build linux`.
3. **A test binds its Unix sockets under `socketDir(t)`** — a short directory
   the test owns (`os.MkdirTemp("", "sock")`, removed on cleanup) — never under
   `t.TempDir()`.
4. **A fixture that stands for a canonical path resolves its temporary
   directory first** (`filepath.EvalSymlinks(t.TempDir())`).
5. **The macOS lane runs every package** (`e2e-cross.yml`, native job, about two
   minutes) and gates. **The Windows lane runs every package as an inventory**,
   `continue-on-error`, until a clean run makes it a gate: the suite has never
   run there, and a gate that fails on day one for reasons nobody has looked at
   blocks every PR instead of guarding anything.

## Consequences

- A helper defined for one GOOS and called from a file every GOOS compiles now
  fails `cross-build` on the others, before merge.
- `cross-build` takes longer: `go vet` loads each module's test dependencies.
- A test that binds a Unix socket, or treats a temporary directory as canonical,
  now runs on macOS on every PR; the Linux lane alone could not see either.

## Deferred

- **Windows at runtime.** The inventory step's first runs are the list to work
  through; the step becomes a gate once they are clean.
- **`scripts/cross-platform-audit.sh` needs bash 4** (`mapfile`, associative
  arrays) and macOS ships bash 3.2. It runs in the devcontainer and in CI; on a
  Mac, with Homebrew's `bash`.

## Verification

- `GOWORK=off CGO_ENABLED=0 GOOS=… GOARCH=… go vet ./...` in `internal/kernel`,
  `internal/core`, `internal/service`, `pkg`, `.` and `e2e`, for the ten
  `cross-build` targets: all green.
- darwin/arm64, `GOWORK=off CGO_ENABLED=0 go test -count=1 -short ./...` in the
  same six modules: all green, about 100 seconds in total.
- `go test -race` for `net/server`, `pkg/v1/server`, `vcs/git` and `writer/file`
  on darwin/arm64: green.
