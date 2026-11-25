<!-- updated: 2026-11-15T00:00:00Z -->
# .github/workflows/

## Purpose

CI/CD automation. The SDK lanes are `bazel-ci.yml` (gate), `post-commit.yml` (the other required check), `sdk-release.yml` (auto-tag after the gate) and `release-size.yml` (the size a pull request would publish, asked before it merges), beside the scans, runtime suites, docs and benchmarks below. No workflow is template-inherited any more: `release.yml` was deleted in #260 (its job ran only in the template's own repository), and the devcontainer image build was deleted with the devcontainer (ADR 0153).

## Merge queue

The two required checks are `bazel` (legacy branch protection of `main`, contexts `["bazel"]`) and `post-commit` (ruleset 21496691, integration 15368) — read with `gh api repos/kitsunium/sdk/branches/main/protection` and `.../rulesets/21496691` on 2026-10-14. A merge queue builds each entry on a `gh-readonly-queue/main/pr-<n>-<sha>` ref and waits for exactly those two to report there, so `bazel-ci.yml` and `post-commit.yml` carry `merge_group: { types: [checks_requested] }`. The jobs `bazel` needs (`shell-gates`, `lint-ktn`) are in the same file and run on the same event. Enabling the queue is a repository setting, not a file: the rules are not edited here.

What each piece does on a `merge_group` run (there is no `github.event.pull_request`, `github.ref` is the queue ref):

- **`bazel` and its siblings**: no `if:` reads the pull request, so all run. `concurrency` keys on `github.ref`, unique per queue entry.
- **`lint-ktn`**: the fork/Dependabot skip tests `EVENT = pull_request`, so it does not apply; the run is the repository's own, receives the `id-token: write` the job grants, and the licence matches on `repository_owner_id` alone (kodflow/ktn README) — not on the event or the ref. A run that finds no token still FAILS.
- **`post-commit`**: the action takes its generic branch on this event: head = the queue commit, range = its tip commit (`github.event.before` is empty). `block-merge` is `pull_request`-only and is skipped.
- **`net-default-change.yml`** runs on the queue and reads the pull request through the API, its number taken from the queue ref, so the label and the body judged there are the ones the pull request carries.
- **`release-size.yml`** does not run on the queue (`pull_request` only) and is not required; the label was checked on the pull request before it entered.
- **`sdk-release.yml`** cannot fire on a queue run: it is `workflow_run` with `branches: [main]` (filters on the run's head branch, `gh-readonly-queue/...` here) and its job also requires `workflow_run.event == 'push'`. The release still fires after the push that lands the entry on `main`, which runs `bazel-ci.yml` through its `push: [main]` trigger, unchanged.
- **`net-kernel-gate.yml`'s `requiredCaps`** (ADR 0189): each leg requires every class A capability a kernel from 6.6 has — `uring-multishot-recv,uring-pbuf,uring-direct,epoll-exclusive,uring-defer-taskrun,uring-no-sqarray,uring-ring-fds`, and `uring-sock-getsockopt` (6.7, #974: on or available) — fail-closed; `uring-sq-rewind` (7.0) waits for a 7.x lane, and `uring-pbuf-inc` (6.12, ADR 0204) for a lane at 6.12 at least: the noble 6.8 leg runs its fallback.
- **The scope** (ADR 0250): `scripts/ci/scope.sh plan` answers `full` on `merge_group` and on a `gh-readonly-queue/…` branch push, so the queue — and `main` after it — tests everything a pull request's scope left out. Until the queue is enabled, that full run is the push to `main` after the merge, and the nightly run.
- **`net-kernel-gate.yml`** runs on the queue: `BASE_SHA` falls back to `merge_group.base_sha` and the evaluated commit is `github.sha`, the queue's head, so the filter and the bench judge what will land. Its concurrency never cancels a queue entry.
- **To require it** (a ruleset setting, not a file): add the status check `net-kernel-gate` — the aggregator, integration GitHub Actions — to the rules of `main` (ruleset 21496691 or the branch protection) and to the merge queue's required checks. Requiring `bench` or a `lane …` job as well adds nothing: the aggregator already fails with them, and a skipped job reports as passing, so only the aggregator tells a justified skip from a missing run. Leave `cancel-in-progress` and the triggers as they are: a required check must report on every pull request and every queue entry, which is why the filter is a job and not a `paths:` key.
- **Not covered**: `e2e-cross.yml` runs on a push to any branch, so a queue branch push may start it (to be measured on the first queue run). It is not required and not a queue check.

## bazel-ci.yml (the SDK lane)

Eleven jobs: `scope` (what the run tests — ADR 0250, §What a run tests), `bazel` (the gate), `shell-gates` (the checks that need neither Bazel
nor Go), `lint-ktn` (ktn-linter, the one job holding an id-token), `cross-build` (every module COMPILES, tests included, on every supported GOOS/GOARCH),
`test-386` (the 32-bit RUNTIME), `test-simd` (`internal/kernel/swar`'s AVX2 forms, built with `GOEXPERIMENT=simd`, the one lane that sets it), `garble-tiny` (`third-party/ebpf`'s smoke program built with `garble -tiny` and run, ADR 0215), `docs-site` (the docs portal, held to
`docs/api`) and `models` (the behaviour models, held to the code by the
pinned kit), and `scope-summary` (what each lane tested, against the plan). The last two lanes run raw `go` rather than
Bazel, for the reason stated under Do NOT below: Bazel here builds for the host
only, so a platform it cannot reach is covered by the toolchain that can, or by
nothing.

Job `bazel` on `ubuntu-latest`, timeout 120 min, `needs: [shell-gates, lint-ktn, models, scope]` with `if: ${{ !cancelled() }}`, and the workflow's `contents: read` only. Its first step fails unless `needs.shell-gates.result`, `needs.lint-ktn.result` and `needs.models.result` are all `success`: none is a required check, and `needs:` alone would make a failure SKIP `bazel`, which GitHub counts as a passing required check — so this is how their gates block a merge (#236). `scripts/ci-gates-check.sh` asserts all three pieces, for each. `scope` is needed for its plan, never as a gate: its result is read by the scope step, and a plan that failed runs the job whole. Steps in order — those marked *scoped* build and test what the plan reaches when its mode is `scoped`, and `//...` otherwise:

1. `bazel-contrib/setup-bazel@…` — caches `bazelisk`, disk cache keyed on `.bazelrc`+`.bazelversion`+`MODULE.bazel`+all `go.mod`/`go.sum`, plus the repository cache. `setup-go` reads the root module's `go.mod` and caches on every `go.sum` — the root, `internal` and the families require nothing outside the repository, so theirs are empty (ADR 0171).
   Then **the remote cache** — `scripts/ci/bazel-remote-cache.sh`, optional (§Remote cache) — and **the scope**: `scripts/ci/scope.sh bazel`. On a scoped plan it fetches target-determinator (`TD_VERSION`, the SHA-256 of its linux/amd64 binary `TD_SHA256`, both in the step), runs it between the pull request's base and the merge commit over `//... except attr(tags, "manual", //...)` with `--config=ci`, and writes the affected targets, the tests among them and the allocation lane's tests among them under `$RUNNER_TEMP/scope`; its `mode` is `scoped`, else `full` or `fallback` — a plan that failed, a full plan, a fallback, a tool or a query that fails.
2. **Drift check** — `bazel mod tidy && bazel run //:gazelle`, then `git diff --exit-code` AND a check for untracked `BUILD.bazel`/`go.mod`/`go.sum`. Catches both modifications and new files (post-audit finding #11).
3. **README drift** — `scripts/pre-commit/check-readme-drift.sh`: every README under `pkg/` and `framework/` is what `tools/genindex -write-readmes` writes from the committed docs/api (ADR 0167, amending ADR 0008) — the SDK's stdlib-only tool, so nothing is installed and no determinism step is needed; `make lint` runs the same script.
4. `bazel build --config=ci //...` — *scoped*: the affected targets (`--target_pattern_file`), nothing when none is.
5. `bazel test --config=ci //...` — *scoped*: the affected tests. Race on, so every `//go:build !race` file is dropped at compile time. Step 6 is their only gate.
   Then **framework suites** — `make test-framework` (*scoped*: `make test-framework SCOPE=<file>`, the framework packages `scope.sh affected` reaches): `go test -race`, `GOWORK=off`, over `./framework/...` in the root module and in each connector module of the census. It is the gate of `//framework/internal/app:app_test`, `manual` under Bazel because the suite reads its own sources and positions relative to its module root (ADR 0147, rule 12); `test-framework` is in `scripts/ci-gates-check.sh`'s GATES.
6. **Alloc + zero-alloc gates (race off)** — `bazel test --config=alloc <targets>`, where `<targets>` is read from `tools/alloc-lane-targets.txt` (*scoped*: its tests among the affected targets). That file is the single source of truth shared with `make test-alloc`; do NOT inline the list here.
7. **Exemption invariant** — `scripts/pre-commit/check-alloc-lane-coverage.sh` fails the build if any `//go:build !race` test lives in a package the step-6 list does not cover. Without it, adding such a test to a new package silently produces a test that no lane runs (root `CLAUDE.md` rule 12).
8. **Audit-coverage invariant** — `scripts/pre-commit/check-audit-coverage.sh` fails the build when a package declaring an `errs.Define` or `errs.Code` is absent from `//:audit_sources`. The errs AST audits can only judge the files staged as their runfiles, so such a package is audited by nothing AND passes green — the worst shape a verification gap can take.
9. **Domain-doc invariant** — `scripts/pre-commit/check-domain-docs.sh` fails the build when the root `CLAUDE.md`'s architecture tree no longer names exactly the Go packages under `internal/core`, at any depth (ADR 0155 §6), when a domain is described twice in the Purpose paragraph, or when two Purpose paragraphs coexist — and, since the same class of drift reached the ADR indexes, when the ADR index stops naming exactly the ADRs on disk, once each — three copies until ADR 0251, `docs/adr/INDEX.md` alone since, with an index row refused in the `CLAUDE.md` files that used to hold one. Every one of those had actually happened.
   Then **core-symmetry invariant** — `scripts/pre-commit/check-core-symmetry.sh` fails the build when a production file under `internal/service` declares an error code, when a service domain has no core package at the same path, or when an `internal/core` package declares a code no engine at its path accounts for (ADR 0160). The six tracks that moved every code to the core did it by hand, one family each, and both halves reopen silently: a service sentinel still compiles and is still audited, because the audits ask that a package be listed, not where it sits.
   Then **package docs, BENCH.md presence and error-code drift** — `check-pkg-docs.sh`, `check-bench-md.sh` and `check-error-codes-drift.sh`, which only the opt-in local pre-commit hook ran until it was removed (ADR 0153).
10. **Layer invariant** — `scripts/check-layer-deps.sh` asserts seven `bazel query` expressions empty (ADR 0068; the last three are the framework's, ADR 0147).
   Then **platforms invariant** — `scripts/pre-commit/check-platforms.sh` fails when `scripts/ci/platforms.sh`, the table tools/genindex judges doc links and writes `docs/api` on, stops naming exactly the cells of the `cross-build` matrix below, in its order. The matrix has to be written in the workflow, so the cells are written twice and this holds the two equal.
11. **Lint gate.** `make lint-check` (`gofumpt -l` + `make guard` + `make doclinks`, ADR 0138, + `make api-check`: `docs/api` regenerated from the code on every cell and compared byte for byte, the code's surface held to the markers of the pins kit writes from `design/` and every generated file to its design file's bytes — ADR 0163). Its third half, ktn-linter, is the `lint-ktn` job below, which this job needs. Those three are the checks of `make lint` that no lane ran until #236; the others are steps 2, 7, 8, 9 and 10 above, so this adds a CLASS of analysis rather than repeating one. These steps are in THIS job because `bazel` and `post-commit` are the only required checks on `main`.
12. **Vulnerability gate** — `make vuln-install` then `make vuln-check`: `govulncheck` in source mode in every module of the census (`scripts/ci/go-modules.sh`), `GOWORK=off`, one at a time. Fails on a REACHABLE vulnerable symbol (govulncheck's exit 3) and on a scan that did not complete — "no packages matched" included, from any module: the root is the SDK module since ADR 0162, no longer the empty anchor ADR 0157 §5 excused; an imported-but-uncalled vulnerable package is reported and passes. Blocking on purpose, and in THIS job because it is required: the SDK's requires are every consumer's floor, so a reachable vulnerability is ours to fix before anything else merges (ADR 0136, #210). The scanner version is pinned once, in the Makefile, and `vuln-check.sh` refuses any other.
13. `bazel coverage --combined_report=lcov //...` (*scoped*: the affected tests — a report of what the run tested; none, no artifact) → uploaded as `coverage-${{ github.run_number }}` artifact (per-run unique name so concurrent runs don't dedupe, post-audit finding #28). Note coverage runs under the default (race-on) config, so it does **not** reflect the step-6 tests.

Steps 2, 3, 7 to 12 — the drift checks, the guards, `make lint-check` (with `make api-check`) and the vulnerability scan — are never scoped: they are fast and read the whole tree.

Concurrency: `${{ github.workflow }}-${{ github.ref }}` with cancel-in-progress — the nightly run in a group of its own (`nightly`), or it would cancel the push run `sdk-release.yml` waits for, or be cancelled by it.

## Test matrix (rule 12)

Which tests run where, module × package × platform. 29 modules in the census, 333 packages with tests on Linux (332 on Windows, where one package's tests are all Unix-only). The table is a full run's — the merge queue, `main`, the nightly run, a manual run. A pull request (a branch push for `e2e-cross.yml`) runs, in each row, the packages its change reaches on that platform, and none in a lane it does not reach (ADR 0250, §What a run tests); each workflow's `scope-summary` says which.

| Platform | Lane | Packages executed | Mode |
|---|---|---|---|
| linux/amd64 | `bazel-ci.yml` `bazel`: `bazel test //...` | every `go_test` of the 26 `go.work` modules and `tools/genindex`, `tools/sdkguard` (`e2e/` is `.bazelignore`d) | race on, results cached by the Bazel disk cache |
| linux/amd64 | `bazel`: alloc lane | the `//go:build !race` files of the targets in `tools/alloc-lane-targets.txt` | race off |
| linux/amd64 | `bazel`: `make test-framework` | `./framework/...` and the four connector modules — the gate of the `manual` `//framework/internal/app:app_test` | `go test -race` |
| linux/386 | `test-386` | every package of every module of the census, `e2e` included | race off |
| linux/amd64, `GOEXPERIMENT=simd` | `test-simd` | `internal/kernel/swar`: the AVX2 forms and the SWAR forms against one oracle; fails closed on a runner without AVX2 | race on, then race off (the allocation budgets) |
| linux/amd64 | `garble-tiny` | `third-party/ebpf/cgroup`'s `TestGarbleTinyBuildLinksTheBackend`: `testdata/smoke` built with the pinned `garble -tiny` and run — the backend registers at import and answers typed (ADR 0215); opt-in by `KITSUNIUM_GARBLE` elsewhere | full |
| linux/amd64 | `e2e-cross` `native` (linux) | the kernel-sensitive list (`KERNEL_PKGS` … `FRAMEWORK_PKGS`, 21 packages) + conformance | full |
| linux, root, real init | `e2e-cross` `linux-init` (7 legs: runner systemd, Debian/Fedora systemd and Alpine OpenRC containers, Debian/Fedora/Alpine kernels under KVM) | the same kernel-sensitive list + conformance, and `third-party/ebpf/cgroup`'s live suite — real cgroup BPF programs in a group it creates (ADR 0215) | full; a pass on a retry fails, as flaky |
| darwin/arm64 | `e2e-cross` `native` (macos-15, shards `internal`, `root`, `rest`) | every package of every module | full |
| windows/amd64 | `e2e-cross` `native` (shards `internal`, `root`, `rest`) | every package of every module | full |
| freebsd 15.0, openbsd 7.9, netbsd 10.1 (amd64) | `e2e-cross` `bsd` (cross-platform-actions, KVM) | every package of every module (`test-binaries.sh`) + conformance; the consensus engine's suite in two runs, `^TestA` and the rest, each under the 5-minute cap (#701) | full; a pass on a retry fails, as flaky |
| illumos (OmniOS r151054), solaris 11.4 (amd64) | `e2e-cross` `solarish` (vmactions) | every package of every module (`test-binaries.sh`) + conformance, cut as on the BSDs | full; a pass on a retry fails, as flaky |
| dragonfly/amd64, linux/arm64, linux/arm | `cross-build` | none executed: `go build` + `go vet` (tests type-checked) | no guest in any lane |

Before this matrix the BSD and solarish guests ran the 21-package list (plus `pkg/v1/proc` on illumos/Solaris): 312 of 333 test packages had never executed on those five kernels; macOS, Windows, the BSDs and illumos/Solaris ran `-short`, which shrank the reaper's hand-off suite. Not run anywhere, by declaration: the `integration` (Docker) and `localstack` suites (`e2e/integration/CLAUDE.md`, the AWS writers' `CLAUDE.md`), `encwrite`'s `integration` socket test, `dmcrypt`, and the `manual` `codec_bench_test` benchmark target.

Durations, wall clock. Before (`e2e-cross` run 39143005853 on `main`): 505 s — macOS 494 s, Windows 380 s, the BSDs 107-154 s, illumos and Solaris 194-211 s, the 21-package list everywhere but macOS and Windows. After (run 39163747111, every package everywhere, its Go caches cold as on any branch before `main` saved them): 622 s — the guests 467-619 s, of which 170-230 s compile the 333 binaries, 30-120 s boot, 140-215 s run every suite; macOS 126-189 s and Windows 153-229 s per shard. `bazel-ci.yml` is unchanged in shape (751 s before, 827 s after, the `bazel` job 559-701 s against 634 s): its disk cache was already restored, and was being evicted by `vmactions`' images.

Caches. `bazel` restores Bazel's disk and repository caches (`setup-bazel`) and setup-go's on every `go.sum`. `e2e-cross` keeps the Go build cache (GOCACHE + GOMODCACHE) per job and target with `actions/cache`, key `go-<native|guest>-<target>-<hash of every go.sum>-<ISO week>`, restored on every run, saved only by a push to `main` when the week's entry is missing — a branch reads `main`'s, and the repository's 10 GB is not spent on an entry per commit. `vmactions` kept its OmniOS and Solaris images in the same 10 GB, 3.1 GB per branch, and was evicting the Bazel disk cache: `disable-cache: true` downloads the image instead.

## Conventions

- Action references SHA-pinned with a trailing `# vX` comment (e.g. `actions/checkout@93cb6efe…  # v5`) — except `kodflow/post-commit@main`, unpinned on purpose so a rule fixed there is live here on the next run (the file's header says why).
- `permissions: contents: read` only — nothing in the SDK lane writes back. The one other grant is `id-token: write`, on the `lint-ktn` job alone, for ktn-linter's OIDC licence check: it mints a token, it writes nothing. Never on `bazel`: the grant exports `ACTIONS_ID_TOKEN_REQUEST_URL`, which the entitlement suites read.
- `bazel-ci.yml` is the only source-of-truth gate. CI does NOT run `golangci-lint` directly (ADR 0004), and runs `go test` directly only where Bazel cannot — other GOOS/GOARCH (see Do NOT). It DOES run `govulncheck` directly, per module (`make vuln-check`, ADR 0136): the vulnerability database is the one input Bazel does not carry, which is why ADR 0004's retirement of the old scans left nothing scanning the SDK (#210).
- A retry never turns a red test green. Where a lane tries a failing suite again (`run-test-binaries.sh`, the conformance loops, `linux-init`'s `run.sh`), it is only to name the failure: a suite that passed on a retry fails the job as flaky (`::error::… is flaky on …`). CI detects a flaky test, it never masks one; the flake is a defect to fix in the test or the code.
- A job that loops over modules reads `scripts/ci/go-modules.sh` (ADR 0137). Writing a module list into a workflow is how `tools/` fell out of every 32-bit lane.

## Rules from ADR 0095

Superseded by ADR 0154 (the charter); ADR 0095 stays as the incident's record, and its rules live here.

- **The Windows whole-suite step of `e2e-cross.yml` gates**: no
  `continue-on-error`; a Windows regression fails the job as a macOS one does.
- **A failure is answered on the platform's own terms**: a production bug is
  fixed in the code; a Unix premise in a test — clock resolution, file modes,
  open handles, kernel buffering — is replaced by what the platform promises.
- **A skip is allowed only where the fixture cannot exist**, its message says
  why, and it follows an assertion of the platform's documented contract
  wherever one exists.
- *Lesson*: nineteen packages failed on Windows behind `continue-on-error`, nine
  of them production bugs — a multipart filename decoded per host, a git root
  never matched, a queue that refused every directory among them.

## Do NOT

- Re-introduce a `go test` lane for a platform **Bazel already covers**. That is
  the drift ADR 0004's "single build system" decision exists to prevent: two
  lanes running the same tests on the same platform, disagreeing, with nobody
  sure which is authoritative. A lane for a platform Bazel CANNOT build for is
  the opposite case — `cross-build` and `test-386` exist precisely because
  Bazel here builds for the host, and the alternative to raw `go` there is no
  coverage at all.
- Drop the drift check; gazelle-generated `BUILD.bazel` files must be committed.

## Reference in NOTES.md

Moved unchanged to `.github/workflows/NOTES.md`, under the same titles, and read only by a change that needs its mechanics: `Workflows`; `What a run tests (ADR 0250)`; `Remote cache (optional)`; `lint-ktn (ktn-linter, licensed by OIDC)`; `shell-gates (the gates that need no toolchain)`; `cross-build (the build bar) and test-386 (the runtime bar)`; `docs-site (the docs portal, held to the code)`; `models (the behaviour models, held to the code)`; `bench-ab.yml (the performance lot's A/B)`; `sdk-release.yml (the SDK release lane)`.
