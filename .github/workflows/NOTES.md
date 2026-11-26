# .github/workflows/ — reference

Moved unchanged from `.github/workflows/CLAUDE.md` (ADR 0253); each section keeps its title.

## Workflows

| File | Trigger | Description |
|---|---|---|
| `bazel-ci.yml` | push to `main`, PRs, `merge_group` (`checks_requested`), nightly schedule (02:17 UTC), manual `workflow_dispatch` | Primary SDK CI — drift check + build + test + coverage via Bazel 9. A pull request tests what it touches (ADR 0250, §What a run tests): the `scope` job's plan, target-determinator for the `bazel` job, `go list` for `test-386`, `test-simd` and `garble-tiny`; the queue, `main`, the nightly and a manual run test everything |
| `post-commit.yml` | PRs (opened, synchronised, reopened, ready for review), push to `main` or `master`, `merge_group` (`checks_requested`), manual `workflow_dispatch` | The mandatory merge gate: `kodflow/post-commit@main` checks the whole history — no AI attribution, conventional commit subjects, no credentials added. The job must stay named `post-commit`, the status the branch ruleset requires; a failed run puts the pull request back into draft, and marking it ready asks for another verdict |
| `sdk-release.yml` | `workflow_run` after `SDK CI (Bazel)` success on `main` — only while the repository variable `RELEASE_ENABLED` is `true` —, plus manual `workflow_dispatch`, a dry run by default (`dry_run`) | **Locked by default (ADR 0171)**: an automatic run is skipped unless `RELEASE_ENABLED` is `true`; a dispatch with `dry_run` (the default) prints the tags cut-tags.sh would cut (`--dry-run`) and publishes nothing; a dispatch with `dry_run=false` fails in its first step while the variable is not `true`. The notes and the published-tag count go through `scripts/release/release-notes.sh`, which classifies each tag by `lib/tag-format.sh`'s anchored shapes — a prefix match read a module tag under `pkg/` as a tombstone. Impact-driven, per module (ADR 0171): each module `go.work` names that changed since its own tag — the SDK module `vX.Y.Z`, a family `pkg/v1/<family>/vX.Y.Z`, `internal/vX.Y.Z`, a vendor or connector `<dir>/vX.Y.Z` — tagged at its OWN version on one release commit in one atomic push, `release-manifest.json` written and checked against the tags pushed, and ONE GitHub release on its first tag with the manifest attached (ADR 0162 had one SDK tag per release; `pkg/vX.Y.Z` before it — ADR 0007, ADR 0017). WHETHER comes from `scripts/release/compute-bumps.sh`; HOW BIG is the largest `release:*` label on the merged pull requests of the range, read by `scripts/release/cut-tags.sh` over the whole range since the last release, not from the checked-out HEAD (ADR 0085, ADR 0135). A `Release-bump:` line in a message only asks: unlabelled above a patch, it is refused — settled by labelling the named pull request and re-running, or by dispatching with the `bump` input. The first release of the SDK module — the very first, and its first root tag after the `pkg/vX.Y.Z` history — is held unless dispatched manually (`--allow-bootstrap`, ADR 0009, ADR 0162), and refused as a patch. That first root tag alone also cuts, in its push, the tombstones `pkg/vX.Y.Z` and `framework/vX.Y.Z` — a go.mod that requires it and no package, so an import of those paths resolves to the SDK module. |
| `vuln-scan.yml` | daily schedule (06:17 UTC), manual `workflow_dispatch` | `make vuln-install && make vuln-check` — the same govulncheck gate the `bazel` job runs, on a clock, so a vulnerability published against a dependency surfaces within a day rather than on the next unrelated pull request (ADR 0136). An alarm; the gate is the `bazel` job. |
| `net-default-change.yml` | pull request opened, edited, reopened, synchronised, labelled or unlabelled; `merge_group` (`checks_requested`) | The gate of the `net:default-change` label (ADR 0188 §D6): the pull request — read through the API on both events, its number from the queue ref `gh-readonly-queue/<base>/pr-<n>-<sha>` in the queue — passes without the label, and with it its body's `Override:` block must match `design/net.yaml`'s capability registry (`scripts/ci/check-net-default-change.sh`). Read-only token, `pull_request` never `pull_request_target`, `cancel-in-progress` on pull requests only. Not required yet: requiring the `net-default-change` status is a ruleset setting. The release only renders the block back (`release-notes.sh default-changes`) |
| `release-size.yml` | pull request opened, reopened, synchronised, labelled or unlabelled | `scripts/release/check-pr-size.sh`: the question `cut-tags.sh` asks a merge, asked of the pull request before it — fails when a branch commit asks for more than a patch and no `release:*` label decides it, naming the label that settles it (ADR 0135). Read-only token, no secret, `pull_request` never `pull_request_target`. Not required: requiring it is a repository setting. |
| `e2e-cross.yml` | push to any branch or any tag but `pin/**` (a pin marks a commit and builds nothing — #296; `branches: ['**']` beside `tags-ignore`, or no branch push would run it — `scripts/ci/test-ci-scripts.bats` holds every workflow to both), manual `workflow_dispatch`, nightly schedule (02:47 UTC) | The runtime bar on real kernels (ADR 0018). A push to a branch other than `main` and the queue's tests what the branch changed since it left `main` (ADR 0250, §What a run tests): a lane nothing reaches is skipped, a leg tests the packages its platform reaches and boots no guest when none is; a push to `main`, a queue branch, a tag, the nightly and a manual run test everything: EVERY package with tests of EVERY module of the census, full size (no `-short` anywhere), on macOS and Windows (`native`: `go test ./...` per module, ADR 0094/0095, in three shards per OS — `internal`, `root` (`.`) and `rest`, the census minus those two —, so the two largest modules run beside the others), the three BSDs (`bsd`) and illumos and Solaris (`solarish`, one OmniOS r151054 and one Oracle Solaris 11.4 guest booted by `vmactions`, ADR 0144). The guests have no Go toolchain: `scripts/ci/test-binaries.sh` cross-compiles a `go test -c` binary per package on the host and `scripts/ci/run-test-binaries.sh` runs each from its package directory inside the guest; a failing suite is tried up to three times (once when it timed out, after 5 min) only to name it — a suite `test-binaries.sh` cuts into parts, the consensus engine's at `^TestA` (#701), is one such run per part, each with its own cap — — a suite that passed only on a retry fails the job as flaky, like one that never passed. The conformance program and the `linux-init` legs' `run.sh` follow the same rule. The guest is booted by its own step (capped at 8 min, the measured boot hang) and the tests run in the next one (`shell: cpa.sh {0}` / `shell: guest {0}`, capped at 25). Linux runs the kernel-sensitive list (`KERNEL_PKGS`, `CORE_PKGS`, `SERVICE_PKGS`, `SERVICE_FILE_PKGS`, `FRAMEWORK_PKGS`) natively and — in the `linux-init` job (ADR 0177, #118) — as root with a delegated cgroup v2 subtree and a real init as PID 1: the runner's own systemd (`ubuntu-systemd`), a privileged Debian and a privileged Fedora container whose PID 1 is their systemd (`debian-systemd`, `fedora-systemd`), and a privileged Alpine container whose PID 1 is busybox init running OpenRC (`alpine-openrc`); the containers share the runner's kernel, so three more legs (`debian-kernel`, `fedora-kernel`, `alpine-kernel`) boot the distro's official cloud image under QEMU/KVM (URL and digest pinned in the matrix) and run its own kernel with its own init as PID 1. Every other Linux package is Bazel's (`bazel-ci.yml`). The Go build cache is the workflow's own (`actions/cache`, per job and target, keyed on every `go.sum` and the ISO week, saved by a push to `main` only); `vmactions`' image cache is off (`disable-cache`), see §Test matrix. Every step gates. |
| `docs-deploy.yml` | `workflow_run` after `SDK Release`, push to `main` on docs paths, manual `workflow_dispatch` | Build + deploy the versioned docs portal (`docs/site`) to GitHub Pages. Separate from release (deploy is a consequence, not a release step). Before it deploys, `npm run check` holds the build to the code: the ⌘K index and API sections of the working tree equal `docs/api`, and every link of every release resolves under the deploy base (`docs/site/CLAUDE.md` §Checks). The releases are built a batch at a time (`docs/site/scripts/build-site.mjs`): one build of every release held them all in Astro's heap and died at Node's default 4 GB; the job's `NODE_OPTIONS=--max-old-space-size=8192` is a margin over a batch's peak (`docs/site/CLAUDE.md` §Build pipeline). |
| `consensus-fuzz.yml` | nightly schedule (03:41 UTC), manual `workflow_dispatch` (`fuzztime` input) | `scripts/ci/consensus-fuzz.sh`: every fuzz target of the consensus domain (ADR 0152 D15), and those of `kernel/fs/seglog` extracted from its log (#820), for a bounded time each, 8 minutes by default; a crasher fails the run and its input is uploaded as the `consensus-fuzz-crashers-<run>` artefact, for the fix to commit under `testdata/fuzz`. An alarm; the seed corpus runs as plain tests in the `bazel` job. |
| `repeat.yml` | manual `workflow_dispatch` only | One test run many times on the runner (`scripts/ci/repeat.sh`): the measurement a flaky test is closed on, e.g. 500 passes under `-race` with 0 failures. Inputs: `module` (a module of the census, default `internal`), `package` (`./…`, default `./...`), `run` (required: `[A-Za-z0-9_]` names joined by `\|`, run as `-run '^(…)$'`), `count` (1..2000, default 100), `race` (default true), `failfast`, `gomaxprocs` (optional), `runner` (`ubuntu-latest`, `macos-latest` or `windows-latest`) and `deadline_minutes` (no new batch after it, default 120, at most 330). Each input reaches the script through `env:` and is validated against an allow-list there, never interpolated into a `run:`; `GOWORK=off` in the module, batches of 100 passes (`go test -count`), a pattern matching no test fails the run, a run cut by the deadline is not a PASS. Uploads `repeat-<run>-<attempt>` (raw output, `summary.md`, `result.env`) and writes the summary — passes, failures, the first failure trace, the runner's CPU — to the step summary. Example: `gh workflow run repeat.yml -R kitsunium/sdk --ref main -f package=./service/app/consensus -f run=TestALostReadIndexOrForwardIsAnsweredWithoutALeaderChange -f count=500 -f race=true`. Never a gate; read-only token. |
| `codec-fuzz.yml` | nightly schedule (04:53 UTC), manual `workflow_dispatch` (`fuzztime` input) | `scripts/ci/codec-fuzz.sh`: every fuzz target of the codec and transform families (#897) — format codecs, compressed frame, stdlib and vendor transform engines, vendor parsers — for a bounded time each, 1 minute by default; a crasher fails the run and its input is uploaded as the `codec-fuzz-crashers-<run>` artefact. An alarm, like the consensus lane; the seed corpus runs as plain tests in the `bazel` job. |
| `net-kernel-gate.yml` | pull request opened, synchronised or reopened; `merge_group` (`checks_requested`) | The network engines' merge gate (plan P8-infra, R7). `changes` runs `scripts/ci/net-gate-paths.sh` between the base and the evaluated commit — the base branch's tip and the merge commit on a pull request, the queue's `base_sha` and `head_sha` in the queue —, so no path filter sits on the trigger; `lanes` runs the net suites on the runner's kernel (`host`, race on) and on Ubuntu noble's own 6.8 under QEMU/KVM (`noble-6.8`, booted by `scripts/ci/boot-guest.sh`, one retry of the boot), each leg declaring `requiredBackends` and `requiredCaps`, linked into the loop suite, which fails closed; `bench` runs engine A against engine B on L1, L1big, L1h2, L2 and L3, ten interleaved rounds on one GitHub-hosted runner, after checking the checkout IS the evaluated commit, then `TestShardsL3_oneSocketIsTheBase` on the pair as `up` left it (#977: L3's load, every datagram stamped, to a datagram group with `Shards(1)` and to one without the option in the receiver's namespace, each one socket without `SO_REUSEPORT` reading all 400 000 once, byte for byte — the test's one lane, a skip fails the step), then ten rounds of Sparse once `scripts/ci/net-veth.sh gro` gave the veth pair's far end a NAPI instance (busy polling, #979), then, as root on the veth pair (the addresses handed through `sudo env`, the runner's sudo ignoring `-E`; a skip fails the step), `TestBlackhole_*` (#971: a peer that vanishes with 1 MiB unsent fails within 3 s of a 1 s `UserTimeout` on every loop backend and the goroutine engine, and stays open 60 s with `tcp-user-timeout` off — the tests' one lane, they skip elsewhere), then `tools/netgate` judges the plan's gates and the samples and the report are uploaded as `net-bench-<sha>`; `bench-darwin` (macos-15, pull requests only, no part of the verdict) shortens the MSL to 1 s and widens the ephemeral range from 16384, since each round leaves 8 000 loopback ports in TIME_WAIT, then runs L1's kqueue pairs ten rounds and `netgate -only kqueue,cal-L1-kqueue` reports them as `net-bench-darwin-<sha>` (#976); `net-kernel-gate` (`if: always()`) passes through `scripts/ci/net-gate-verdict.sh` when the filter succeeded and either found nothing or the lanes and the bench both passed. Read-only token, `cancel-in-progress` on pull requests only, every job capped within the 45-minute budget. Not required yet: see §Merge queue |
| `bazel-bench.yml` | manual `workflow_dispatch` (`count` input), weekly schedule (Sunday 06:00 UTC), PRs labelled `run-bench` | Kernel benchmarks — not part of the PR gate: the bench targets are `manual` in Bazel, so the job runs `go test -bench` in `internal/kernel` directly, as `make sdk-bench` does locally |
| `bench-ab.yml` | manual `workflow_dispatch` (`suites`, `mode`, `base`, `head`, `pattern`, `benchtime`, `rounds`, `runner`, `enforce`; dispatches that differ in `suites`, `head`, `mode`, `base` or `runner` run side by side); pull request opened, synchronised or reopened touching a suite's package or the lane's files (`paths:`) | The performance lot's A/B (§bench-ab.yml): per suite of `scripts/ci/bench-ab.conf`, a base binary — the head's benchmark files laid over it where they compile — and a head binary, interleaved rounds A B, B A, benchstat, step 0 (`-gcflags=-m=2`, `perf stat`, the mutex, block and CPU profiles a suite asks for), and a verdict per threshold row headed by the runner's processor model, published as `bench-ab-<suite>-<run>-<attempt>`. Never a merge gate; read-only token, hosted runners |

## bazel-ci.yml (the SDK lane)

### What a run tests (ADR 0250)

`scope` (`ubuntu-latest`, 15 min, full clone, `setup-go`) runs `scripts/ci/scope.sh plan` and writes it to the job summary. A pull request is **scoped**; `merge_group`, a push to `main`, the nightly schedule and a dispatch are **full**. A scoped plan **falls back** to everything when `MODULE.bazel`, `MODULE.bazel.lock`, `.bazelrc`, `.bazelversion`, `.bazelignore`, the root `BUILD.bazel`, a `WORKSPACE`, a `go.mod`, a `go.sum`, `go.work`, `go.work.sum`, anything under `tools/`, `.gitattributes`, this workflow, `scripts/ci/` or the `Makefile` changed, when a path no rule accounts for changed, and when anything fails. Otherwise it names `touched` — the Go packages whose compiled code changed, in scope with every package depending on them, their tests included — and `tests` — packages whose tests alone are in scope: a test file, a `testdata/` file, a file a test reads outside its package (the table in the script, held by `scope.bats` to every cross-package `data` label of the BUILD files). Its `lanes` step then decides `test386` (linux/386), `simd` (`GOEXPERIMENT=simd`, `internal/kernel/swar`) and `garble` (`third-party/ebpf/cgroup`, its testdata smoke program included): `run` or `skip`, `go list` deciding with the lane's platform.

Each of those lanes carries `needs: scope` and `if: ${{ !cancelled() && needs.scope.outputs.<lane> != 'skip' }}`: a plan that failed answers nothing and the lane runs whole; `test-386` then tests, scoped, the packages linux/386 reaches (`scope.sh affected`, `scope.sh go-test`). `cross-build`, `shell-gates`, `lint-ktn`, `models` and `docs-site` are never scoped. `e2e-cross.yml` has its own `scope` job, on a push: a branch other than `main` and the queue's is compared with its merge base with `origin/main`, its own file and `scripts/ci/` fall back, and the lanes decided are `native` (the kernel-sensitive list and the conformance program on Linux with the loop's tags, every package on darwin and windows), `linux_init` (the list, `pkg/v1/proc`, `third-party/ebpf/cgroup`, the conformance program), `bsd` (FreeBSD, OpenBSD, NetBSD) and `solarish` (illumos, Solaris). Each leg computes the packages its own platform reaches (`scope.sh affected`): the macOS and Windows shards test those, a hundred import paths a `go test`, the consensus split when it is in scope; the Linux legs `scope.sh keep` their lists; the guest legs pass them to `test-binaries.sh` (`TEST_BINARIES_ONLY`) and boot no guest when nothing compiled; the conformance program is built when it is in scope.

`scope-summary` (`if: ${{ !cancelled() }}`, every lane needed) writes, per lane, *ran in full*, *fallback to full*, *ran (scoped)* or *skipped by scope* with the packages judged out of its reach, the `bazel` job's mode and counts beside it, and fails when a lane that had to run was skipped. It is not required: the `if:` is the guarantee, `scope.bats` asserts it.

### Remote cache (optional)

`scripts/ci/bazel-remote-cache.sh`, a step of the `bazel` job, appends a remote cache to the runner's `~/.bazelrc` when the variable (or secret) `BAZEL_REMOTE_CACHE_URL` names one (`grpcs://…`, `https://…`), and nothing otherwise — no URL, no change. It writes (`--remote_upload_local_results=true`) only on a push to `main`, a merge queue entry and the nightly run, with `BAZEL_REMOTE_CACHE_WRITE_HEADER` (`Name=Value`) or `BAZEL_REMOTE_CACHE_CLIENT_CERT` + `BAZEL_REMOTE_CACHE_CLIENT_KEY` (mTLS), secrets the workflow hands to that step on those three events alone; every other run reads only, with `BAZEL_REMOTE_CACHE_READ_HEADER` or anonymously when `BAZEL_REMOTE_CACHE_READ` is `anonymous`, else not at all. `BAZEL_REMOTE_CACHE_CA` names a private authority. A fork receives no secret: the server, refusing unauthenticated writes, keeps it out. A malformed configuration turns the cache off with a warning. The `bazel` job's output `remote-cache` (`off`, `read-only`, `read-write`) is in the summary.

### lint-ktn (ktn-linter, licensed by OIDC)

`ubuntu-latest`, timeout 30 min, `permissions: { contents: read, id-token: write }` — the only job of the workflow holding an id-token, and the workflow root does not grant it. Checkout, `setup-go` (same pin and cache as `bazel`), then `make lint-ktn-check` (`ktn-linter run --skip-phases=tests ./...`), in `scripts/ci-gates-check.sh`'s `GATES`; it blocks a merge because `bazel` needs it and fails on its result.

**Why a job of its own.** A job granted an id-token exports `ACTIONS_ID_TOKEN_REQUEST_URL` to every process, and `framework/internal/service/entitlement` decides it is on CI from exactly that variable: with the grant on `bazel`, `make test-framework` sent device-path entitlement tests down the CI path and failed `CI_NOT_ENTITLED` (#300's first run). The suites were made hermetic too; the separation keeps `bazel`'s environment what it was.

**ktn-linter comes from the vendor's public channel**: `kodflow/ktn/setup`, pinned by commit, installs the release binary (its installer verifies the published checksum) and runs `ktn-linter license status`, which proves the licence with the runner's OIDC token — no key, no repository secret, no device seat. The licence covers the run when the published roster lists the NUMERIC id of the repository's owner (`repository_owner_id`) — for this repository the `kitsunium` organisation, not a person. The action's own steps are pinned; the installer (`kodflow/ktn@main/install.sh`) and the binary are rolling-latest by the vendor's design — one release is kept and the signed roster's minimum version upgrades an older binary — so the version a run used is read from its log, not from this repository.

**The one skip is a pull request GitHub gives no id-token** — from a fork, or from Dependabot: a step before the action detects exactly those two, emits a `::warning::` and skips the two steps after it, and the job succeeds; any other run that finds no `ACTIONS_ID_TOKEN_REQUEST_URL` FAILS, since a lost permission must not read as a skipped gate. No step continues on error. This replaces a from-source build of the private `kodflow/ktn-linter` fetched with a repository secret that was never created, so its steps were skipped on every run and the target was held out of `GATES`; `make lint-ktn-install` remains the local from-source alternative.

### shell-gates (the gates that need no toolchain)

`ubuntu-latest`, timeout 10 min, `checkout` only — no Bazel, no Go. A sibling
job so a broken release script is reported in the first minute rather than
behind the 120-minute Bazel lane. It is not a required check: the `bazel` job
needs it and fails at once when it did not succeed, which is what makes its
gates block a merge.

1. **install bats-core** — `apt-get install -y bats`. bats is NOT vendored and
   `release-scripts-test.sh` deliberately does not fetch it: a gate that clones
   a third-party repository on every run is a flake, and `make lint` already
   refuses to depend on network egress.
2. **CI gate enforcement** — `make ci-gates-check` → `scripts/ci-gates-check.sh`.
   Fails when a target in its `GATES` manifest does not exist, is not `.PHONY`,
   or is not invoked by this file — matched against the executable `run:`
   commands, never the raw YAML, so a gate named only in a surviving comment
   cannot satisfy enforcement. A gate CI reaches through a listed gate's recipe
   — `make lint-check` runs `$(MAKE) api-check` — counts as run; deleting that
   recipe line reports it UNGATED like a deleted step. It also fails when a
   guard in its `GUARDS` manifest (the
   `bash scripts/…` checks of steps 7–10 that `make lint` runs too) is missing,
   is no `run:` step of the `bazel` job, or is no line of the `lint` recipe: a
   deleted step left the guard in `make lint`, gating nothing, and was the one
   shape the script did not see. It fails, third, when a gate or guard runs only
   in a job whose failure does not fail the required `bazel` check — not
   needed by `bazel`, needed but its result never checked, or needed by a
   `bazel` with no `!cancelled()`/`always()` and therefore skipped, which
   passes (`NOT BLOCKING`, #236); `docs-check` is its one `ADVISORY` entry, run
   by the `docs-site` sibling and reported as such. `ci-gates-check` is in its
   own manifest, which catches every gate but itself: nothing deleted can
   report its own deletion (ADR 0088 §Deferred) — but now that `bazel` needs
   this job, deleting its step still leaves the others it runs blocking.
3. **Release script regression** — `make release-scripts-check` →
   `scripts/release/release-scripts-test.sh`, which runs `scripts/release/*.bats`.
   That suite existed from ADR 0085 and NOTHING executed it until this job
   (ADR 0088).
4. **CI script regression** — `make ci-scripts-check` → `scripts/ci-scripts-test.sh`,
   which runs `scripts/ci/*.bats`: the module census every module-looping lane
   reads and the govulncheck gate built on it (ADR 0136, ADR 0137). It also
   asserts that `cross-build` and `test-386` below still read the census rather
   than a list of their own — the shape of #242 — and that no census loop skips
   the root module (ADR 0162).
5. **Pre-commit guard regression** — `make pre-commit-check`.

These four, plus `make test-framework`, `make lint-check`, `make vuln-install`,
`make vuln-check` in the `bazel` job, `make lint-ktn-check` in the `lint-ktn` job, `make docs-check` in the
`docs-site` job and `make models-check` in the `models` job, are the only `make` invocations in this workflow, and that is deliberate:
`ci-gates-check` asserts the Makefile↔CI link by target NAME, which only works
if CI goes through the target. The two lint targets are not in THIS job because
they need a Go toolchain, and ktn-linter an installed, licensed binary — the
things `shell-gates` exists to do without. Adding a gate means editing `GATES` **and** this
file, plus the Makefile's `.PHONY` — all three, or the check fails. That is not
a claim: adding a gate to `GATES` before wiring its step produced
`UNGATED: 'make <target>' exists but .github/workflows/bazel-ci.yml never
runs it` (ADR 0088).

### cross-build (the build bar) and test-386 (the runtime bar)

`cross-build` runs `go build ./...` then `go vet ./...` per module across twelve
GOOS/GOARCH cells, including linux/386, linux/arm, illumos/amd64 and
solaris/amd64 (ADR 0144) — the cells of `scripts/ci/platforms.sh`, which the
platforms invariant of the `bazel` job holds equal to this matrix —, so a platform-specific
low-level call can never silently drop a package (ADR 0018's build bar; local
equivalent `bash scripts/cross-platform-audit.sh`, which needs bash 4). `go
build` never compiles a `_test.go`; `go vet` type-checks it, so a test calling a
helper only one GOOS defines fails the cells it breaks (ADR 0094). The pins
kit writes from `design/` (`gen_api*_test.go`, ADR 0163) are test files, so
this is where every cell compiles them: a symbol that differs from its design
on one platform fails that platform's cell.

Both loop over the census — `scripts/ci/go-modules.sh`, every module whose
`go.mod` git tracks — never over a list (ADR 0137). `./...` stops at a nested
`go.mod`, and the lists these loops used to carry had drifted: `cross-build`
named six modules and `test-386` four, and neither named `tools/genindex` or
`tools/sdkguard`, whose tests had never run on 32 bits (#242). A lane that must
skip a module names it next to the loop, with the lane that covers it instead.
None is named today. The root module `.` was, by every census loop, while it
was the workspace's empty anchor (ADR 0157 §5); since ADR 0162 it is the SDK
module — `internal/`, `pkg/`, `framework/` — and every loop builds, vets, tests
and scans it like any other.

`test-386` RUNS every module's tests on linux/386 — the four workspace modules
it always ran, plus the root module's `third-party/` tests, `e2e` and both tools,
all measured passing on linux/386 before they were added; those `third-party/`
tests now live in one module per vendor (ADR 0157), each in the census, the
ssh identity among them is the framework's connector module (ADR 0158), and the
four workspace modules are the SDK module at the root since ADR 0162. Compiling and
behaving are different questions, and the gap between them is where a 64-bit
assumption survives: `int(0xffffffff)` is `-1` where `int` is 32 bits and passes
every `> cap` check, which is exactly the bound `internal/service/security/session`'s
at-rest frame relies on — two of its malformed frames can only fail there.
Adding the job found two defects immediately: a bench helper that did not
compile (`Statfs_t.Type` is `int32` on 386 and `int64` elsewhere) and a test
synthesising a slice longer than a 32-bit `len` can hold.

Linux/amd64 runners execute 386 binaries natively, so there is no emulation. It
runs without `-race`, which has no 386 support at all — which also makes it the
SECOND lane to compile the `//go:build !race` files, after the alloc lane.

### docs-site (the docs portal, held to the code)

`ubuntu-latest`, timeout 15 min, checkout with full history (the Home page's
feature catalogue dates each feature by `git log --follow`), Node 24, then
`make docs-check` — named in `scripts/ci-gates-check.sh`'s GATES: `npm ci`,
`npm test`, a build of the working tree's release alone (`DOCS_RELEASES=local`:
no release snapshot, no `gh`, no network — no go command either: the portal
reads `docs/api`), then `npm run check`. That check fails unless the ⌘K index
and the anchors of every package page's API section equal `docs/api` — every
exported symbol of `pkg/v1`, and every method an alias reaches at its owner —,
counted by `docs/site/scripts/check-api-counts.mjs`, which shares no code with
the writer, and every link and ⌘K entry of the build resolves under the deploy
base, fragments included (`check-links.mjs`). `make api-check`, in the `bazel`
job, holds `docs/api` to the code; this job holds the portal to `docs/api`. A
sibling of the required jobs, so it reports without blocking; the deploy runs
the same check over every release before it publishes.

### models (the behaviour models, held to the code)

`ubuntu-latest`, timeout 20 min, `GOPRIVATE=github.com/kitsunium/studio`.
It installs kit at the version `design/sdk.yaml` pins (`kit_pin`,
`scripts/kit-pinned.sh`) with `go install
github.com/kitsunium/studio/cmd/kit@<pin>`, reading kitsunium/studio, which
is private, through the repository secret `KIT_READ_TOKEN`. It then runs
`make models-check`: the pinned kit's `kit design derive -check`, which fails
on a model of `design/behaviour` that is missing, is not what the current code
derives, or is left over — a body changed without `make models`
(kitsunium/studio ADR 0013). The job BLOCKS: `bazel` needs it and checks
`needs.models.result`, so a stale model fails the required check, and
`models-check` is in `scripts/ci-gates-check.sh`'s GATES, not in ADVISORY.
Without the secret the job fails, like `lint-ktn` without its id-token, with
one exception GitHub forces: a pull request from a fork or from Dependabot
receives no repository secret, so there the job warns and passes, the models
NOT checked, and the maintainer's push that lands it is checked. Any other run
with an empty `KIT_READ_TOKEN` means the secret was removed, and fails.

## bench-ab.yml (the performance lot's A/B)

The measurements of kitsunium/sdk#1038, #1059, #808, #834, #811, #1016, #1035,
#827, #840, #1034, #1102, #910, #892, #912, #1364, #920 and #1365, on a GitHub-hosted runner — never on a development VM, whose clock, overlay
file system and memory balloon decide its numbers. The table is
`scripts/ci/bench-ab.conf`: its **suites** (a package directory — under
`internal/`, `pkg/v1/` or `third-party/` —, the
`-test.bench` expression, `-test.benchtime`, `-test.cpu`, the CPUs a rule needs,
the runner, the base commit the issues cite — `ee1f48ba2` for all of them) and its
**rules**, one a threshold row, each naming its issue; a `step0` record names the
profiles a suite wants recorded. The suites: `kcache*` (#1038, #834, #808;
`kcache-policy` is #834's protocol — 100 million accesses a cell, the trace
drawn on the fly from a PCG of fixed seed, the two judged cells under LRU,
S3-FIFO and W-TinyLFU, every row four levels deep),
`dcache*` (#1059, #808), `dcache-bulk` (#840: `FetchMany_16` per key, 16 × 0.40,
and its million-entry row in `dcache-scale`), `queue` and `queue-scale` (#811),
`queue-publish` (#827: `PublishMany/file/16` per message, 16 × 0.55; vfs's
changes run it too), `logger` (#1016), `sflight` and `sflight-8` (#1035:
singleflight at `-4` and, on a runner of 8 CPUs or more, `-8`), `ring` and
`ring-spsc` (#1034: two suites, because a two-level `-test.bench` expression on a
benchmark with no sub-benchmark discovers nothing and reports nothing),
`transform` (#1102, `internal/service/data/transform`: zero allocations and
bytes into a sized `dst`, a nil `dst`'s B/op at most 1.5 × the plaintext — 36
`value` rules on the head's rows), `codec-promote` (#910,
`pkg/v1/data/codec`; the form codec's engine runs it too: the promoted form's
allocations, four fewer than the base's — a `delta` rule) and `tstream` (#892,
`third-party/transform`: each stream row against its block row, both the
head's, at 4 MiB — eight `ratio` rules at 1.10 — and two floor rows,
`BenchmarkStreamFloor`, a measured base of this implementation's copy and
CRC-32C on a family, not a proof about every stream, when they fail too; a CPU profile of
the s2 stream's decompression as its `step0`), `cturn` (#912,
`internal/service/app/consensus`: the leader's loop turn at 1 000 proposals/s,
three and five nodes over loopback mTLS, one paced run of 5 s an op, its p99
taken from every observation a test `Meter` keeps raw — `p99-us/turn`, a metric
the benchmark reports itself — head over base at most 1.00, two `ab` rules; a
CPU profile of the turn as its `step0`; #1364 dispatches it with
`base=bench/912-parent`, the parent of #912's commit with the one line that
builds it), `cenc` (#912: a leader turn's bytes, the shared body over the
per-follower frames the driver keeps for a transport without `BatchSender`,
both the head's, at most 1/N — two `ratio` rules on `B/op`) and `cstream`
(#920, #1365: the data lane's commit p99 while a snapshot stream paced at
8 MiB/s runs, three voters over loopback mTLS on `data-disk` and on `tmpfs`, a
learner on tmpfs, one op the windows A, B and A of one cluster, ~25 s; its
two rows `paced` and `paced+stream` both the head's — the base has no pacing
and leaves the file out —, B over A at most 1.10, two `ratio` rules on
`p99-ms/commit`; a CPU profile of the op as its `step0`), with
`cstream-scale` publishing 2 and 32 MiB/s under the same bound on a dispatch
with `mode=scale`. A metric is
`<what>/<per>`, letters, digits, `%` and `-` before the slash, letters and
digits after it.

**Outside `internal/`.** A suite's directory may be a package of `pkg/v1/` or of
a vendor module under `third-party/`. `bench-ab-build.sh` and `bench-ab-step0.sh`
build in the nearest `go.mod` above the package with `GOWORK=off`, so a vendor
module is built as its consumers build it, through the `replace` directives of
its own `go.mod` (`third-party/transform` needs `../../internal` and downloads
`klauspost/compress`: the leg needs the network, as `go install benchstat`
does). A pull request that changes `third-party/transform/` or `pkg/v1/data/codec/`
runs the suite through the workflow's `paths:` (`bench-ab.bats` holds the list
to the table's).

Per suite, one leg (`ab <suite>`):

1. **Two trees and two binaries.** `bench-ab-tree.sh` checks the base and the
   head out as detached worktrees, the base fetched one commit deep into the
   shallow clone. `bench-ab-build.sh` compiles each with `go test -c`
   (`GOWORK=off`, `GOTOOLCHAIN=local`: one toolchain for both); the base gets
   the head's `*_bench_test.go` — all of them where they compile together, one
   at a time otherwise, its own version kept where the head's does not compile
   (`overlay=all|partial|none`, in `overlay.txt` and `env.txt`).
2. **Interleaved rounds.** `bench-ab-run.sh`: `ROUNDS` rounds (20, 10 in scale
   mode, 10..200), A then B in odd rounds, B then A in even ones, each binary
   from its package directory with `-test.count=1 -test.benchmem`.
3. **benchstat** (pinned) over the two sides: the human reading.
4. **Step 0**: `bench-ab-step0.sh` writes `go build -gcflags=-m=2` and
   `perf stat` (cycles, instructions, branch misses, L1 data misses) at both
   commits; `perf` comes from the runner kernel's `linux-tools` when the image
   can install it, and the record says when it could not or when the
   hypervisor hides the counters. A suite whose table has a `step0 SUITE
   PROFILES PATTERN` record (singleflight's, today: `mutex,block,cpu` over
   `DistinctKeysParallel` and its one-shard row, #1035) also gets one run of
   each binary a profile — `-test.mutexprofile`, `-test.blockprofile`,
   `-test.cpuprofile` — written as `<side>-<kind>.pprof` with `go tool pprof
   -top` beside it (`<side>-<kind>-top.txt`): the group's lock, the shared
   context's channel (`runtime.lock2` under `selectgo`) and the scheduler
   (`newproc`, `gopark`) told apart before a fix is written. The `.pprof`
   keeps every stack, so `-focus=OneShard` splits the rows one run measured
   together. The workflow hands the record down as `STEP0_PROFILES` and
   `STEP0_PATTERN`.
5. **The verdict**: `bench-ab-verdict.sh`, one line a rule — `ab` (a row's head
   over its base), `delta` (a row's head MINUS its base, in the metric's own
   unit — a count or a size the head must lower by so much, #910's `<= -4`
   allocs/op —, labelled `head-base`; `missing` when the base lacks the row, as
   a delta against nothing would be a delta against zero), `ratio` (two head rows), `diff` (two head rows' difference),
   `value` (one head row) or `cross` (a head row over a DIFFERENT row of the
   base: a row only the head carries, an internal benchmark or a floor row,
   against the base's own — #1035's step 1 alone, #1034's `SCStorePair`
   floor), computed round by round; the rounds' values
   summarised as benchstat summarises a sample (median, distribution-free 95 %
   interval: the 6th and 15th of 20); the rule **holds when the interval's
   bound — the high one for `<=`, the low one for `>=` — meets the threshold**,
   so a median on the right side of it with an interval across it fails.
   Fewer than 10 rounds carrying the rows is `missing`, a suite on fewer CPUs
   than its `MINCPUS` is `unjudged`; both fail a gate. A row the binary it is
   read from does not carry — a benchmark whose pull request has not landed
   yet: `PublishMany`, `FetchMany_16`, `OneShard`, `Emit_Bound` on the issues'
   base — leaves its rule `missing`, never passing: a `report` rule says so and
   fails nothing, a `gate` rule fails. The table goes to the job summary,
   **headed by the runner's processor model and vCPU count** (`/proc/cpuinfo`'s
   "model name"; the last two columns of `verdict.tsv` and the first lines of
   `env.txt` say the same). Hosted jobs land on at least six models — EPYC
   Zen 3, 4 and 5, Xeon Ice Lake, Emerald Rapids and Granite Rapids — whose
   costs differ (an `XCHG` throughputs at 7.5 cycles on Zen 3 and Zen 4 and at
   17 to 20 on the Intel parts and Zen 5, #1034), so a verdict is read with the
   machine that gave it; a rule that matters is judged on several dispatches, never on one
   draw of the pool.

Everything — the raw rounds, `base.txt`/`head.txt`, benchstat, step 0,
`env.txt` (processor model, CPUs, `lscpu`, clock source, governor, disk and inodes), the
verdict — is uploaded as `bench-ab-<suite>-<run_id>-<attempt>` even when a
step failed.

**Gate or report.** Every rule is `report` today but two: a row that does not hold
yet is judged and written but never fails the leg, and #1034's raised bounds
stay `report` too, less than 1 % above the interval one runner family
measured. The pull request that makes a row hold turns it to `gate` in the
table — the threshold itself is never edited there but under the owner's rule
of 2026-11-14: when no design that keeps the package's contract reaches it, it
becomes the high bound of the best design's 95 % interval on the runner,
rounded up to the hundredth, its trail in the package's `BENCH.md` (#1034's
three). `bench-ab.bats` pins every one. Two rows gate today, #834's S3-FIFO hit ratios
(`834-zipf-s1.0-cap10`, `834-scan-cap10`): a ratio of hits on a fixed trace is
the same on every run and every machine, so the rounds repeat one value. A
dispatch with `enforce` gates every row for that run.

**Scale mode** (dispatch only, `mode=scale`): the suites `*-scale`, whose rows
are the large ones — a row whose name ends in `_<n>M` or `_Scale`. The queue's
`BenchmarkReceiveBacklog_Scale` reads the depths of `KITSUNIUM_BENCH_SCALE`
(10 000, 100 000, 1 000 000) over a backlog `FillReadyForTest` writes without a
flush, once a depth; it skips without the variable, and this mode is its lane.

**#808, #1035 and their 8 CPUs.** The `-cpu=8` rows (`kcache-par`, `dcache-par`,
and #1035's committed `-8` row in `sflight-8`) need
eight dedicated cores, and `ubuntu-latest` has four vCPU on a public
repository: there they run oversubscribed and their rules read `unjudged`. No
larger runner is available to the `kitsunium` organisation on its current
plan (the API answers "GitHub hosted runners are not supported for this
organization"; no self-hosted runner is registered). Judging #808 needs a
GitHub-hosted larger runner (a Team or Enterprise plan, a runner group allowed
on public repositories, 8 cores or more, its label given as `runner`) — never a
self-hosted runner reachable from a fork's pull request.

**Dispatches side by side.** A pull request keeps the group `<workflow>-<ref>`
and cancels the run a push replaces. A dispatch's group adds `suites`, `head`,
`mode`, `base` and `runner` to it, and nothing cancels a dispatch: two
dispatches of different suites, heads, bases or runners — #892's one dispatch a
runner family — run at the same time, where the former group (the ref alone)
made every dispatch from `main` replace the one still waiting. A dispatch that
repeats all five of one in progress waits for it, and a third replaces the second
still waiting (GitHub keeps one pending run a group): `rounds`, `pattern` and
`benchtime` are not in the group, so a repeat is told apart by its `runner`,
`base` or `head`.

**A run by hand**, once the workflow is on `main`:

```sh
gh workflow run bench-ab.yml -R kitsunium/sdk -f suites=kcache            # one suite, against ee1f48ba2
gh workflow run bench-ab.yml -R kitsunium/sdk -f suites=all -f head=<branch or SHA>
gh workflow run bench-ab.yml -R kitsunium/sdk -f suites=internal/service/data/queue -f rounds=30
gh workflow run bench-ab.yml -R kitsunium/sdk -f suites=queue-scale -f mode=scale
gh workflow run bench-ab.yml -R kitsunium/sdk -f suites=kcache-par,dcache-par,sflight-8 -f runner=<label of 8+ CPUs>
gh workflow run bench-ab.yml -R kitsunium/sdk -f suites=kcache -f base=<sha> -f head=<sha>   # base = head: the runner against itself
gh run download <run-id> -R kitsunium/sdk                                 # the artefacts
```

## sdk-release.yml (the SDK release lane)

Single job `release` on `ubuntu-latest`, gated by `workflow_run` on `SDK CI (Bazel)` success AND the repository variable `RELEASE_ENABLED == 'true'` (ADR 0171), or by a manual dispatch — a dry run by default (`dry_run`) —, with `contents: write` (tags, releases) and `pull-requests: read` (the labels that size the release). Steps:

0. **Refuse a cut while releases are locked** — a dispatch with `dry_run=false` fails here, naming the variable, while `RELEASE_ENABLED` is not `true`.
1. `actions/checkout@93cb6efe…  # v5` with `fetch-depth: 0` — full history, so both scripts can walk each module's range since its own last tag.
2. `actions/setup-go@…` + `bazel-contrib/setup-bazel@…` (with its caches) — Bazel answers the `rdeps` query inside `compute-bumps.sh`; a step reports whether it can start.
3. **Compute the release token** — `compute-bumps.sh --explain --require-bazel`: the directory of every module that changed since its own tag, one per line (`.` the SDK module — ADR 0171), or `inputs.force_bumps` on manual dispatch (module directories, or `sdk` — the SDK module plus every module that changed —, `pkg`, `framework` and `third-party` meaning the same). The verdict and its reason go to the log and to the step output `reason`.
3b. **Check the version floor** — `check-version-floor.sh` asks proxy.golang.org, not the clone, that no module's last tag here is below the highest version it lists and that no module never tagged here is a path it already serves, so the next cut is above every published version (ADR 0171 §2); a proxy that cannot answer fails the step. Runs in a dry run too.
3c. **Rehearse the release on a file proxy** — `rehearse-release.sh`, fed what the cut is fed: the same cut made with `--no-push` in a throwaway clone, then `proxy-rehearsal.sh --manifest` — the families added to a consumer of the monolithic root, `go get -u ./...` from it, a consumer on `…/pkg` v0.17.0, every family the release does not cut built at its last tag against the new internal and families. Red stops the release; a held bootstrap or a refused size is left to step 4 to report. Runs in a dry run too (ADR 0171 §7).
4. **Cut and push tags** — `cut-tags.sh --manifest=/tmp/release-manifest.json` (`--dry-run` when the dispatch asked for one): each module sized from the `release:*` labels of the merges that changed IT (ADR 0135, ADR 0171), or from `inputs.bump` for every module (`--bump`, no lookup); each module's version continues its own last tag, a module never tagged starts at `v0.1.0`, the root's continues the newest `pkg/vX.Y.Z` before its first tag; every module tagged has its go.mod rewritten to require each module of the repository at the version this release gives it, or at its own last tag, with no `replace`; `git fetch --prune --prune-tags origin` first, every module's last tag re-read and EACH exact ref checked on origin before tagging (exit 2 on a race), one atomic push of every tag on one detached release commit — and, when the root's base is a `pkg/` tag, of the two tombstones on children of it, an existing tombstone tag being refused (exit 1) before anything is tagged; stdout lists the tags in `go.work`'s order, the SDK's first when cut, then the tombstones. Exit 65 is a refused size: its stderr names the pull request and both ways out — and the first root tag sized as a patch, refused because the module change every consumer makes is a minor at least (`release:minor`, or `bump=minor`). Exit 3 is a held bootstrap — the very first release, or the SDK module's first root tag, held whenever the base is a `pkg/vX.Y.Z` tag, whatever root tag sits below it.
4b. **Check the dry run's tags against the version floor** — in a dry run only, `check-version-floor.sh --manifest=…` over every tag the dry run would cut, modules and tombstones, so the dry run shows a version the proxy would refuse before the real cut is dispatched.
5. **Verify the computed release was actually published** — once a bump was computed, a run whose tag list is empty, holds a tag no anchored shape accepts (`release-notes.sh count`), or differs from what `release-manifest.json` names fails (#227 defect 2). Skipped in a dry run.
6. **Create the GitHub release** — ONE `gh release create --generate-notes --notes-start-tag <previous> --verify-tag` per run, on the manifest's `release` (the first tag printed), `release-manifest.json` attached, its notes (`release-notes.sh notes`) listing every tag the run cut, the tombstones apart, prepended to the generated ones. The generated notes start at the primary module's own last tag, the manifest's `previous` (`release-notes.sh start-tag`; the flag is omitted only for a module's first tag): every tag names a release commit made off `main`, so the last release's tag is never an ancestor of the new one, and GitHub, left to choose, chose no start — v0.18.0 to v0.28.0 each listed every pull request since #130, with a "Full Changelog" to `commits/<tag>`. Skipped in a dry run.
6b. **Check the release on the module proxy** — `check-published.sh --manifest=…`: every tag of the manifest, modules and tombstones, served by proxy.golang.org (`.info`, retried against its negative cache) and fetched by `go mod download` with the checksum database on; a failure is loud and the table goes to the job summary. After the GitHub release, so a slow proxy never leaves pushed tags without their record. Skipped in a dry run.
7. **Release summary** — on EVERY run (`if: always()`), to the job summary and a `release-summary-${{ github.run_number }}` artifact: the verdict, the size and where it came from, and one of five renderings — refused (quoting `cut-tags.sh`), nothing to publish, a dry run (the tags it would cut and the manifest it would write), held bootstrap, or the tags pushed. It must not fail when the cut step was skipped (no `/tmp/cut.err`): the default `bash -e` outlives `set -uo pipefail`, so every read of a file the cut writes carries `|| true` or an `||` fallback; `scripts/ci/test-ci-scripts.bats` runs the block with those files absent.

**When a size is refused** (exit 65): label the pull request the refusal names — `release:minor` to grant the request, `release:patch` to decline it — and re-run the failed job, or dispatch this workflow with `bump` = patch/minor/major. `release_base()` advances only when a release is cut, so the refused merge stays in every later range until one of those happens. Never cut tags by hand: a range chosen to step over one merge steps over every size in it.

**When the tags were pushed and the GitHub release was not** (step 6 failed after step 4 succeeded): a re-run will not repair it — the range now starts at the new tag, finds nothing, and goes green as "Nothing was published" — and `docs-deploy.yml` lists versions through `gh release list`, so the version is missing from the docs site too. Create the release by hand from the tags already on the release commit, then dispatch Docs Deploy. The release is on the run's primary tag — the manifest's `release`, which is the SDK's `vX.Y.Z` only when the root was cut, and otherwise a family's `pkg/v1/<family>/vX.Y.Z`, `internal/vX.Y.Z` or a vendor's tag —, and its generated notes start where the workflow's would, at `release-notes.sh start-tag` over that run's `release-manifest.json` (the run's summary prints it), with `--notes-start-tag` added only when that tag is not empty (a module's first tag has none):

```sh
tag="$(jq -r '.release' release-manifest.json)"   # the primary tag that has no GitHub release
prev="$(bash scripts/release/release-notes.sh start-tag release-manifest.json)"
from=()
if [ -n "$prev" ]; then
  from=(--notes-start-tag "$prev")   # without it the notes list the whole history
fi
gh release create "$tag" release-manifest.json --verify-tag --generate-notes ${from[@]+"${from[@]}"} --title "$tag" \
  --notes "$(git tag --points-at "$tag^{commit}" | grep -vx "$tag" | sed 's/^/- `/; s/$/`/')"
```

The tags are the publication; the release only lists them, so this repairs the record without touching anything a consumer resolves. For v0.18.0 the tombstones `pkg/v0.18.0` and `framework/v0.18.0` sit on children of the release commit, so that list leaves them out.

Concurrency: `sdk-release-${{ github.ref }}` with `cancel-in-progress: false` (NEVER cancel a tag-push mid-flight).
