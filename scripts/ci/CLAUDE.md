<!-- updated: 2026-10-27T03:00:00Z -->
# scripts/ci/

## Purpose

The helpers the lanes share: the module census every lane that loops over Go
modules reads (ADR 0137), the platforms table every lane that loops over
GOOS/GOARCH cells reads, and the govulncheck gate built on the census
(ADR 0136), with the BATS suite that pins them. Tooling, not library code — the tree's general
rules (portability, the ADR 0088 and ADR 0137 rules, the Do NOT list) are in
`scripts/CLAUDE.md`.

## Contents

| File | What it does | Run by |
|---|---|---|
| `go-modules.sh` | the census: one directory per `go.mod` git tracks, `.` for the root, sorted; a `go.mod` under `testdata/` is a fixture and left out, an untracked one is not reported; exit 1 with the reason on stderr when git cannot answer or answers nothing | `bazel-ci.yml`'s `cross-build` and `test-386`, `e2e-cross.yml` (and its `test-binaries.sh`), `make test-framework`, `scripts/cross-platform-audit.sh`, `vuln-check.sh` |
| `platforms.sh` | the platforms table: the twelve GOOS/GOARCH cells, one `goos/goarch` per line, in the order of `bazel-ci.yml`'s `cross-build` matrix, printed from its one here-document — which tools/genindex reads without running the script | `scripts/cross-platform-audit.sh`; tools/genindex (`make doclinks`, `make api`, `make api-check`); `scripts/pre-commit/check-platforms.sh`, which holds it to the matrix |
| `vuln-check.sh` | govulncheck in source mode, `GOWORK=off`, one module at a time — the whole census, or the modules named as arguments. govulncheck's exit 3 (a reachable vulnerable symbol) fails and names the module; any other non-zero fails as a scan that did not complete; "no packages matched" fails as an incomplete scan from any module — the root `.` included, the SDK module since ADR 0162. With `GOVULNCHECK_VERSION` set the scanner must report exactly that version | `make vuln-check` — a step of `bazel-ci.yml`'s `bazel` job and of the daily `vuln-scan.yml` |
| `test-binaries.sh OUTDIR [MODULE...]` | one `go test -c` binary (`-s -w`) for EVERY package with tests, in every module of the census (or the modules named), for the GOOS/GOARCH of the environment, compiled in parallel (`TEST_BINARIES_JOBS`, default the CPU count) — one invocation per package, because two packages of one module share a name and `go test -c -o DIR/` refuses them; writes `OUTDIR/manifest` (binary, package directory), and for a suite its `parts` table cuts — the consensus engine's, at `^TestA` (#701) — one line per run (binary, directory, `k/n`, its `-test.run`, its `-test.skip`, `-` for none): run k takes what the k-th regular expression matches and no earlier one does, the last what none matches, so a test of any name runs once; exit 1 naming each package that does not compile, and on an empty set | `e2e-cross.yml`'s `bsd` and `solarish` jobs, on the host before the guest boots |
| `run-test-binaries.sh BINDIR [FLAG...]` | POSIX sh (the guests have no bash and no Go): runs every binary of the manifest from its package directory, as `go test` does, stdin `/dev/null`, `-test.timeout` (`TEST_TIMEOUT`, 5m) — per run, a part of a cut suite being a run of its own, named `DIR[k/n]`, its selection passed before the caller's flags and never globbed —, a failing suite tried up to `TEST_ATTEMPTS` (3) times only to tell a defect from a flake — once for a suite that timed out, so a hang cannot eat the step's cap; a suite that failed every attempt (`::error::… fails`) and one that passed only on a retry (`::error::… is flaky`) BOTH fail the run (exit 1): the retry names a flake, it never turns it green; an empty or missing manifest is refused | inside the BSD, OmniOS and Solaris guests of `e2e-cross.yml` |
| `consensus-fuzz.sh` | every fuzz target of the consensus domain (ADR 0152 D15) for a bounded time each — the first argument, else `FUZZ_TIME`, else 5m; a crasher fails it and its input stays under the package's `testdata/fuzz/<Target>/` | `consensus-fuzz.yml` (nightly); locally `scripts/ci/consensus-fuzz.sh 30s` |
| `check-net-default-change.sh BODY [DESIGN]` | the gate of the `net:default-change` label (ADR 0188 §D6): BODY must hold an `Override:` block — `Override:` alone on a line, then `KITSUNIUM_NET_ENGINE=<token>` and/or `KITSUNIUM_NET_DISABLE=<token>,…` lines, each at most once, up to a blank line or a fence — whose tokens are `design/net.yaml`'s `Capability` constants (DESIGN, default the repository's), an ENGINE token one of the engines, never a kernel capability (ADR 0189), a DISABLE token one of the switchable blocks; exit 1 naming each line refused, 64 on bad usage | `.github/workflows/net-default-change.yml`, on the pull request and in the merge queue, when the pull request carries the label |
| `net-gate-paths.sh BASE EVAL` | the path filter of the network merge gate: `git diff --name-only BASE EVAL` against the engines (`internal/service/net/`, `internal/core/net/`), the kernel layer, `pkg/v1/net/`, `design/net.yaml`, `tools/netgate/`, the go lines and the gate's own files; prints `relevant=true` or `relevant=false` for `$GITHUB_OUTPUT` and the matched paths on stderr; exit 1 when git cannot resolve a commit or the diff fails — never `relevant=false`, which would skip the gate as a pass | `.github/workflows/net-kernel-gate.yml`'s `changes` job, with the queue's base on a `merge_group` |
| `net-gate-verdict.sh CHANGES RELEVANT LANES BENCH` | the `net-kernel-gate` aggregator's verdict on its needs' results: the filter must have succeeded; nothing relevant passes (a skip counts as success); otherwise the lanes and the bench must both have succeeded — skipped, failed and cancelled fail alike | the `net-kernel-gate` job |
| `boot-guest.sh DIR` | a cloud image (`DIR/base.qcow2`) booted under QEMU/KVM with a cloud-init seed giving a fresh ssh key to the default user and root, waited on through ssh (`BOOT_POLLS` × `BOOT_POLL_SECS`); a boot that never answers is torn down — qemu killed, the overlay rebuilt from the base — and tried ONCE more, a second failure exits 1; the commands are variables the suite stubs | `net-kernel-gate.yml`'s guest leg (`noble-6.8`) |
| `net-veth.sh up\|down` | the veth pair L3 runs over: namespace `netgate-l3`, `ng-a` 198.18.0.1 here and `ng-b` 198.18.0.2 inside, RFC 2544's benchmarking range; `up` prints the bench's `KITSUNIUM_NETGATE_L3_*` environment for `$GITHUB_ENV`, `down` removes the namespace and succeeds when there is none; needs root | `net-kernel-gate.yml`'s `bench` job |
| `net-kernel-gate.bats` | the four scripts above — the filter in throwaway repositories (relevant, not, an unknown base failing), every verdict, a boot up at once, up on the retry and failing twice with no third try, the veth commands and the environment printed — and the workflow's shape: both events, no `paths:` on the trigger, no cancel on the queue, the queue's base compared, SHA-pinned actions, a read-only token, hosted runners only, every leg declaring non-empty `requiredBackends` and `requiredCaps`, the aggregator needing every job with `always()`, and every timeout within 45 minutes | `make ci-scripts-check` |
| `test-ci-scripts.bats` | the census in throwaway repositories (nesting, `testdata/`, untracked, empty, a failing git, this repository's tools), the lanes reading it and skipping no module, no workflow's `push` trigger running for a `pin/**` tag nor losing its branches to a lone `tags-ignore` (#296), the platforms table and the local audit reading it, every govulncheck verdict through a stub scanner, the test-binary runner (each binary from its package directory, a failure named and the rest still run, a pass on a retry failing the run named as flaky, a first-attempt pass the only green, a timed-out suite run once, an empty manifest refused, each part of a cut suite run, tried and named on its own with its selection unglobbed, and `test-binaries.sh`'s manifest over a stub go command: a line per part of the consensus suite, one for a whole suite) and the guest jobs running the census's binaries with no `-short`, and `scripts/ci-gates-check.sh`: its guards, a gate run through a listed gate's recipe, and a gate or guard that runs and blocks nothing — the required `bazel` job not needing its job, ignoring its result (a comment naming it included), or skippable by its failure, and the one `ADVISORY` gate (#236); the release workflow's summary step run as the runner runs it (`bash -e`) with the cut step's files absent, which must exit 0; and ktn-linter's lane — `lint-ktn-check` a gate of the `lint-ktn` job, blocking through `bazel` (and NOT BLOCKING once `bazel` ignores its result), `kodflow/ktn/setup` pinned by a full commit SHA, no secret and no private fetch, `id-token: write` on that job alone — not `bazel`, not the workflow —, no `continue-on-error`, and only a fork or Dependabot pull request skipping it, with a warning (#236); `check-net-default-change.sh` — a block in the registry's tokens, a block ended by a fence, no block, an unknown token, a floor switched off, a foreign variable, a repeated one, a registry read from the design (a renamed token refused), and its workflow on the pull request and the queue with no write grant | `make ci-scripts-check` (`scripts/ci-scripts-test.sh`), in `bazel-ci.yml`'s `shell-gates` job |

## Rules

- **A lane that loops over modules reads the census**, never a list of its own
  — four hand-written lists had drifted apart (#242). A module a lane must skip
  is named next to the loop with the lane that covers it instead; none is
  today. The root `.` was, by every census loop, while it was the workspace's
  empty anchor (ADR 0157 §5); it is the SDK module since ADR 0162, and the
  suite asserts no loop skips it.
- **An empty answer is an error.** A lane looping over nothing passes having
  built nothing, which is the defect the census exists to close.
- **The census answers for what git tracks**: CI checks out exactly that, so a
  module is in every lane the moment its `go.mod` is committed.
- **Every module is scanned even after one fails**, so one run reports them all;
  a scan that did not run is never reported as a clean scan.
- **A flake fails the lane.** A retry exists only to tell a flaky suite from
  a broken one in the error it prints; a suite that passed only on a retry
  fails the run exactly as one that never passed. CI detects a flaky test, it
  never masks one — the flake is a defect, answered in the test or the code.
- **The suite tests the scripts where they answer.** Each script answers for the
  repository it lives in, so the suite copies it into a fixture repository; the
  scanner is a stub answering from marker files, so no case needs the network.

## Verify

```sh
bash scripts/ci/go-modules.sh                # prints the census
bash scripts/ci/platforms.sh                 # prints the twelve cells
make ci-scripts-check                        # needs bats on PATH
make vuln-install && make vuln-check         # needs vuln.go.dev
```
