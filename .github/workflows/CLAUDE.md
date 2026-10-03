<!-- updated: 2026-10-02T23:06:22Z -->
# .github/workflows/

## Purpose

CI/CD automation. The SDK lanes are `bazel-ci.yml` (gate), `post-commit.yml` (the other required check), `sdk-release.yml` (auto-tag after the gate) and `release-size.yml` (the size a pull request would publish, asked before it merges), beside the scans, runtime suites, docs and benchmarks below. No workflow is template-inherited any more: `release.yml` was deleted in #260 (its job ran only in the template's own repository), and the devcontainer image build was deleted with the devcontainer (ADR 0153).

## Workflows

| File | Trigger | Description |
|---|---|---|
| `bazel-ci.yml` | push to `main`, PRs | Primary SDK CI — drift check + build + test + coverage via Bazel 9 |
| `post-commit.yml` | PRs (opened, synchronised, reopened, ready for review), push to `main` or `master`, manual `workflow_dispatch` | The mandatory merge gate: `kodflow/post-commit@main` checks the whole history — no AI attribution, conventional commit subjects, no credentials added. The job must stay named `post-commit`, the status the branch ruleset requires; a failed run puts the pull request back into draft, and marking it ready asks for another verdict |
| `sdk-release.yml` | `workflow_run` after `SDK CI (Bazel)` success on `main`, plus manual `workflow_dispatch` | Impact-driven tags `pkg/vX.Y.Z` (ADR 0007; the `pkg/<major>/` prefix went away with the bare module path — ADR 0017). WHETHER comes from `scripts/release/compute-bumps.sh`; HOW BIG is the largest `release:*` label on the merged pull requests of the range, read by `scripts/release/cut-tags.sh` over the whole range since the last release, not from the checked-out HEAD (ADR 0085, ADR 0135). A `Release-bump:` line in a message only asks: unlabelled above a patch, it is refused — settled by labelling the named pull request and re-running, or by dispatching with the `bump` input. First release is held unless dispatched manually (`--allow-bootstrap`, ADR 0009). |
| `vuln-scan.yml` | daily schedule (06:17 UTC), manual `workflow_dispatch` | `make vuln-install && make vuln-check` — the same govulncheck gate the `bazel` job runs, on a clock, so a vulnerability published against a dependency surfaces within a day rather than on the next unrelated pull request (ADR 0136). An alarm; the gate is the `bazel` job. |
| `release-size.yml` | pull request opened, reopened, synchronised, labelled or unlabelled | `scripts/release/check-pr-size.sh`: the question `cut-tags.sh` asks a merge, asked of the pull request before it — fails when a branch commit asks for more than a patch and no `release:*` label decides it, naming the label that settles it (ADR 0135). Read-only token, no secret, `pull_request` never `pull_request_target`. Not required: requiring it is a repository setting. |
| `e2e-cross.yml` | push to any branch, manual `workflow_dispatch` | The runtime bar on real kernels (ADR 0018): the platform-sensitive packages on Linux, macOS, Windows, the three BSDs, and — in the `solarish` job, one OmniOS r151054 and one Oracle Solaris 11.4 guest booted by `vmactions`, with `pkg`'s `./v1/proc` beside them (ADR 0144) — illumos and Solaris, then — on macOS and Windows — every package (`go test -short ./...` per module, ADR 0094). The platform-sensitive list is per module: `KERNEL_PKGS`, `CORE_PKGS`, `SERVICE_PKGS` and, since ADR 0158 moved `entitlement` there, `FRAMEWORK_PKGS`, run from `framework/`. Both runs gate: Windows was an inventory (`continue-on-error`) under ADR 0094 until its first clean run, and ADR 0095 records how its 19 failing packages were resolved and made it a gate. |
| `e2e-vm.yml` | manual `workflow_dispatch` only | The platform-sensitive suites and the conformance binary on persistent Proxmox VMs of the lab (Debian and Fedora with systemd, Alpine with OpenRC, the three BSDs, Windows 11), over SSH from the `kitsunium-runner` scale set. Needs the lab and its four secrets, so nothing triggers it automatically. Every suite binary runs with `-test.timeout=3m` and every conformance check under `harness.CheckTimeout`, so a hang prints stacks instead of eating the 25-minute job; a failed SSH wait says whether port 22 is open (sshd refuses us) or closed (no sshd) — #118. |
| `docs-deploy.yml` | `workflow_run` after `SDK Release`, push to `main` on docs paths, manual `workflow_dispatch` | Build + deploy the versioned docs portal (`docs/site`) to GitHub Pages. Separate from release (deploy is a consequence, not a release step). |
| `bazel-bench.yml` | manual `workflow_dispatch` (`count` input), weekly schedule (Sunday 06:00 UTC), PRs labelled `run-bench` | Kernel benchmarks — not part of the PR gate: the bench targets are `manual` in Bazel, so the job runs `go test -bench` in `internal/kernel` directly, as `make sdk-bench` does locally |

## bazel-ci.yml (the SDK lane)

Four jobs: `bazel` (the gate), `shell-gates` (the checks that need neither Bazel
nor Go), `cross-build` (every module COMPILES, tests included, on every supported GOOS/GOARCH)
and `test-386` (the 32-bit RUNTIME). The last two run raw `go` rather than
Bazel, for the reason stated under Do NOT below: Bazel here builds for the host
only, so a platform it cannot reach is covered by the toolchain that can, or by
nothing.

Job `bazel` on `ubuntu-latest`, timeout 120 min. Steps in order:

1. `bazel-contrib/setup-bazel@…` — caches `bazelisk`, disk cache keyed on `.bazelrc`+`.bazelversion`+`MODULE.bazel`+all `go.mod`/`go.sum`, plus the repository cache.
2. **Drift check** — `bazel mod tidy && bazel run //:gazelle`, then `git diff --exit-code` AND a check for untracked `BUILD.bazel`/`go.mod`/`go.sum`. Catches both modifications and new files (post-audit finding #11).
3. **README drift + determinism** — `gomarkdoc@v1.1.0` installed, then `scripts/pre-commit/check-readme-drift.sh` (every `pkg/v1` README matches its package doc comment) and `check-readme-determinism.sh` (two renderings agree) — ADR 0008.
4. `bazel build --config=ci //...`
5. `bazel test --config=ci //...` — race on, so every `//go:build !race` file is dropped at compile time. Step 6 is their only gate.
   Then **framework suites** — `make test-framework`: `go test -race`, `GOWORK=off`, in every framework module of the census. It is the gate of `//framework/internal/kit:kit_test`, `manual` under Bazel because the suite reads its own sources and positions relative to its module root (ADR 0147, rule 12); `test-framework` is in `scripts/ci-gates-check.sh`'s GATES.
6. **Alloc + zero-alloc gates (race off)** — `bazel test --config=alloc <targets>`, where `<targets>` is read from `tools/alloc-lane-targets.txt`. That file is the single source of truth shared with `make test-alloc`; do NOT inline the list here.
7. **Exemption invariant** — `scripts/pre-commit/check-alloc-lane-coverage.sh` fails the build if any `//go:build !race` test lives in a package the step-6 list does not cover. Without it, adding such a test to a new package silently produces a test that no lane runs (root `CLAUDE.md` rule 12).
8. **Audit-coverage invariant** — `scripts/pre-commit/check-audit-coverage.sh` fails the build when a package declaring an `errs.Define` or `errs.Code` is absent from `//:audit_sources`. The errs AST audits can only judge the files staged as their runfiles, so such a package is audited by nothing AND passes green — the worst shape a verification gap can take.
9. **Domain-doc invariant** — `scripts/pre-commit/check-domain-docs.sh` fails the build when the root `CLAUDE.md`'s architecture tree no longer names exactly the Go packages under `internal/core`, at any depth (ADR 0155 §6), when a domain is described twice in the Purpose paragraph, or when two Purpose paragraphs coexist — and, since the same class of drift reached the ADR indexes, when `docs/adr/CLAUDE.md`, `docs/CLAUDE.md` or the root Reference list stops naming exactly the ADRs on disk, once each. Every one of those had actually happened.
   Then **package docs, BENCH.md presence and error-code drift** — `check-pkg-docs.sh`, `check-bench-md.sh` and `check-error-codes-drift.sh`, which only the opt-in local pre-commit hook ran until it was removed (ADR 0153).
10. **Layer invariant** — `scripts/check-layer-deps.sh` asserts seven `bazel query` expressions empty (ADR 0068; the last three are the framework's, ADR 0147).
11. **Lint gate, in two halves.** `make lint-check` (`gofumpt -l` + `make guard` + `make doclinks`, ADR 0138) runs unconditionally, and `make lint-ktn-check` (`ktn-linter`, after the vulnerability gate) runs only when a `KTN_LINTER_TOKEN` secret exists. Those three are the checks of `make lint` that no lane ran until #236; the others are steps 2, 7, 8, 9 and 10 above, so this adds a CLASS of analysis rather than repeating one. The split is a measurement, not taste: `kodflow/ktn-linter` is private and a workflow token is scoped to the repository that issued it, so `gh release download v1.11.2 --repo kodflow/ktn-linter` answered `release not found` under `secrets.GITHUB_TOKEN` on this very lane, while the same command run by a credentialed account downloads the asset. `gh` and not `curl`, because the signed redirect drops the Authorization header and a `curl` 404 cannot tell "asset absent" from "not authorised". **`lint-ktn-check` is deliberately absent from `GATES`** — a step a missing secret can skip is not a gate, and the run emits a `::warning::` saying so. Add the secret and the target to `GATES` in the same commit. The pin is v1.45.4 — the build the tree is kept clean under, so the step judges the tree with the rules it was checked against, not with a build 34 releases older — and the install asserts the binary matches it, because 1.9.11 exits 0 with "No issues found" on a tree 1.11.2 rejects. These steps are in THIS job, not in `shell-gates`, because `bazel` and `post-commit` are the only required checks on `main` and a sibling job would report without blocking.
12. **Vulnerability gate** — `make vuln-install` then `make vuln-check`: `govulncheck` in source mode in every module of the census (`scripts/ci/go-modules.sh`), `GOWORK=off`, one at a time. Fails on a REACHABLE vulnerable symbol (govulncheck's exit 3) and on a scan that did not complete; an imported-but-uncalled vulnerable package is reported and passes. The root module, which holds no package (ADR 0157 §5), is reported as nothing to scan — for `.` alone, and only on govulncheck's own "no packages matched" answer. Blocking on purpose, and in THIS job because it is required: the SDK's requires are every consumer's floor, so a reachable vulnerability is ours to fix before anything else merges (ADR 0136, #210). The scanner version is pinned once, in the Makefile, and `vuln-check.sh` refuses any other.
13. `bazel coverage --combined_report=lcov //...` → uploaded as `coverage-${{ github.run_number }}` artifact (per-run unique name so concurrent runs don't dedupe, post-audit finding #28). Note coverage runs under the default (race-on) config, so it does **not** reflect the step-6 tests.

Concurrency: `${{ github.workflow }}-${{ github.ref }}` with cancel-in-progress.

### shell-gates (the gates that need no toolchain)

`ubuntu-latest`, timeout 10 min, `checkout` only — no Bazel, no Go. A sibling
job so a broken release script is reported in the first minute rather than
behind the 120-minute Bazel lane.

1. **install bats-core** — `apt-get install -y bats`. bats is NOT vendored and
   `release-scripts-test.sh` deliberately does not fetch it: a gate that clones
   a third-party repository on every run is a flake, and `make lint` already
   refuses to depend on network egress.
2. **CI gate enforcement** — `make ci-gates-check` → `scripts/ci-gates-check.sh`.
   Fails when a target in its `GATES` manifest does not exist, is not `.PHONY`,
   or is not invoked by this file — matched against the executable `run:`
   commands, never the raw YAML, so a gate named only in a surviving comment
   cannot satisfy enforcement. `ci-gates-check` is in its own manifest, which
   catches every gate but itself: nothing deleted can report its own deletion,
   and `main` currently requires only the `bazel` check (ADR 0088 §Deferred).
3. **Release script regression** — `make release-scripts-check` →
   `scripts/release/release-scripts-test.sh`, which runs `scripts/release/*.bats`.
   That suite existed from ADR 0085 and NOTHING executed it until this job
   (ADR 0088).
4. **CI script regression** — `make ci-scripts-check` → `scripts/ci-scripts-test.sh`,
   which runs `scripts/ci/*.bats`: the module census every module-looping lane
   reads and the govulncheck gate built on it (ADR 0136, ADR 0137). It also
   asserts that `cross-build` and `test-386` below still read the census rather
   than a list of their own — the shape of #242.
5. **Pre-commit guard regression** — `make pre-commit-check`.

These four, plus `make test-framework`, `make lint-check`, `make vuln-install`,
`make vuln-check` and `make lint-ktn-check` in the `bazel` job, are the only `make` invocations in
this workflow, and that is deliberate:
`ci-gates-check` asserts the Makefile↔CI link by target NAME, which only works
if CI goes through the target. The two lint targets are not in THIS job because
they need a Go toolchain and a downloaded binary — the two things `shell-gates`
exists to do without. Adding a gate means editing `GATES` **and** this
file, plus the Makefile's `.PHONY` — all three, or the check fails. That is not
a claim: adding a gate to `GATES` before wiring its step produced
`UNGATED: 'make <target>' exists but .github/workflows/bazel-ci.yml never
runs it` (ADR 0088).

### cross-build (the build bar) and test-386 (the runtime bar)

`cross-build` runs `go build ./...` then `go vet ./...` per module across twelve
GOOS/GOARCH cells, including linux/386, linux/arm, illumos/amd64 and
solaris/amd64 (ADR 0144), so a platform-specific
low-level call can never silently drop a package (ADR 0018's build bar; local
equivalent `bash scripts/cross-platform-audit.sh`, which needs bash 4). `go
build` never compiles a `_test.go`; `go vet` type-checks it, so a test calling a
helper only one GOOS defines fails the cells it breaks (ADR 0094).

Both loop over the census — `scripts/ci/go-modules.sh`, every module whose
`go.mod` git tracks — never over a list (ADR 0137). `./...` stops at a nested
`go.mod`, and the lists these loops used to carry had drifted: `cross-build`
named six modules and `test-386` four, and neither named `tools/genindex` or
`tools/sdkguard`, whose tests had never run on 32 bits (#242). A lane that must
skip a module names it next to the loop, with the lane that covers it instead.
One is named today, by every census loop: the root module `.`, the workspace's
anchor, which holds no package once its last one — entitlement's ssh identity —
is the framework's (ADR 0157 §5, ADR 0158). `go vet ./...` and `go test ./...`
exit 1 on a pattern that matches nothing, so each loop skips `.` while
`go list ./...` reports it empty, and builds it again the moment it holds a
package; `vuln-check.sh` reads govulncheck's own "no packages matched" answer
the same way, for `.` alone.

`test-386` RUNS every module's tests on linux/386 — the four workspace modules
it always ran, plus the root module's `third-party/` tests, `e2e` and both tools,
all measured passing on linux/386 before they were added; those `third-party/`
tests now live in one module per vendor (ADR 0157), each in the census, and the
ssh identity among them is the framework's connector module (ADR 0158). Compiling and
behaving are different questions, and the gap between them is where a 64-bit
assumption survives: `int(0xffffffff)` is `-1` where `int` is 32 bits and passes
every `> cap` check, which is exactly the bound `internal/service/session`'s
at-rest frame relies on — two of its malformed frames can only fail there.
Adding the job found two defects immediately: a bench helper that did not
compile (`Statfs_t.Type` is `int32` on 386 and `int64` elsewhere) and a test
synthesising a slice longer than a 32-bit `len` can hold.

Linux/amd64 runners execute 386 binaries natively, so there is no emulation. It
runs without `-race`, which has no 386 support at all — which also makes it the
SECOND lane to compile the `//go:build !race` files, after the alloc lane.

## sdk-release.yml (the SDK release lane)

Single job `release` on `ubuntu-latest`, gated by `workflow_run` on `SDK CI (Bazel)` success, with `contents: write` (tags, releases) and `pull-requests: read` (the labels that size the release). Steps:

1. `actions/checkout@93cb6efe…  # v5` with `fetch-depth: 0` — full history, so both scripts can walk the range since the last release.
2. `actions/setup-go@…` + `bazel-contrib/setup-bazel@…` (with its caches) — Bazel answers the `rdeps` query inside `compute-bumps.sh`; a step reports whether it can start.
3. **Compute majors to bump** — `compute-bumps.sh --explain --require-bazel` (tokens `pkg`, `framework` and/or `third-party` — ADR 0147, ADR 0157), or `inputs.force_bumps` on manual dispatch. The verdict and its reason go to the log and to the step output `reason`.
4. **Cut and push tags** — `cut-tags.sh` sizes the release from the `release:*` labels of the range's merged pull requests (ADR 0135), or from `inputs.bump` when dispatched with one (`--bump`, no lookup); rewrites the chain's go.mods — the chain is every module `go.work` uses but the root, tagged in lockstep and cut ONCE whatever the tokens (ADR 0147 §9) —, race-protected re-read, atomic push; stdout lists the `pkg/` tag, then the `framework/` ones, then the `third-party/` ones (one module per vendor — ADR 0157). Exit 65 is a refused size: its stderr names the pull request and both ways out. Exit 3 is a held bootstrap.
5. **Verify the computed release was actually published** — once a bump was computed, a run that pushed no `pkg/` tag fails (#227 defect 2).
6. `gh release create --generate-notes --verify-tag` per pushed tag.
7. **Release summary** — on EVERY run (`if: always()`), to the job summary and a `release-summary-${{ github.run_number }}` artifact: the verdict, the size and where it came from, and one of four renderings — refused (quoting `cut-tags.sh`), nothing to publish, held bootstrap, or the tags pushed.

**When a size is refused** (exit 65): label the pull request the refusal names — `release:minor` to grant the request, `release:patch` to decline it — and re-run the failed job, or dispatch this workflow with `bump` = patch/minor/major. `release_base()` advances only when a release is cut, so the refused merge stays in every later range until one of those happens. Never cut tags by hand: a range chosen to step over one merge steps over every size in it.

Concurrency: `sdk-release-${{ github.ref }}` with `cancel-in-progress: false` (NEVER cancel a tag-push mid-flight).

## Conventions

- Action references SHA-pinned with a trailing `# vX` comment (e.g. `actions/checkout@93cb6efe…  # v5`) — except `kodflow/post-commit@main`, unpinned on purpose so a rule fixed there is live here on the next run (the file's header says why).
- `permissions: contents: read` only — nothing in the SDK lane writes back.
- `bazel-ci.yml` is the only source-of-truth gate. CI does NOT run `golangci-lint` directly (ADR 0004), and runs `go test` directly only where Bazel cannot — other GOOS/GOARCH (see Do NOT). It DOES run `govulncheck` directly, per module (`make vuln-check`, ADR 0136): the vulnerability database is the one input Bazel does not carry, which is why ADR 0004's retirement of the old scans left nothing scanning the SDK (#210).
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
