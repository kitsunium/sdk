<!-- updated: 2026-09-26T00:00:00Z -->
# .github/workflows/

## Purpose

CI/CD automation. The SDK lanes are `bazel-ci.yml` (gate), `sdk-release.yml` (auto-tag after the gate) and `release-size.yml` (the size a pull request would publish, asked before it merges); the remaining three workflows are inherited from the devcontainer-template repo and path-gated on `.devcontainer/**`.

## Workflows

| File | Trigger | Description |
|---|---|---|
| `bazel-ci.yml` | push to `main`, PRs | Primary SDK CI — drift check + build + test + coverage via Bazel 9 |
| `sdk-release.yml` | `workflow_run` after `SDK CI (Bazel)` success on `main`, plus manual `workflow_dispatch` | Impact-driven tags `pkg/vX.Y.Z` (ADR 0007; the `pkg/<major>/` prefix went away with the bare module path — ADR 0017). WHETHER comes from `scripts/release/compute-bumps.sh`; HOW BIG is the largest `release:*` label on the merged pull requests of the range, read by `scripts/release/cut-tags.sh` over the whole range since the last release, not from the checked-out HEAD (ADR 0085, ADR 0135). A `Release-bump:` line in a message only asks: unlabelled above a patch, it is refused — settled by labelling the named pull request and re-running, or by dispatching with the `bump` input. First release is held unless dispatched manually (`--allow-bootstrap`, ADR 0009). |
| `vuln-scan.yml` | daily schedule (06:17 UTC), manual `workflow_dispatch` | `make vuln-install && make vuln-check` — the same govulncheck gate the `bazel` job runs, on a clock, so a vulnerability published against a dependency surfaces within a day rather than on the next unrelated pull request (ADR 0136). An alarm; the gate is the `bazel` job. |
| `release-size.yml` | pull request opened, reopened, synchronised, labelled or unlabelled | `scripts/release/check-pr-size.sh`: the question `cut-tags.sh` asks a merge, asked of the pull request before it — fails when a branch commit asks for more than a patch and no `release:*` label decides it, naming the label that settles it (ADR 0135). Read-only token, no secret, `pull_request` never `pull_request_target`. Not required: requiring it is a repository setting. |
| `e2e-cross.yml` | push to any branch, manual `workflow_dispatch` | The runtime bar on real kernels (ADR 0018): the platform-sensitive packages on Linux, macOS, Windows and the three BSDs, then — on macOS and Windows — every package (`go test -short ./...` per module, ADR 0094). Both runs gate: Windows was an inventory (`continue-on-error`) under ADR 0094 until its first clean run, and ADR 0095 records how its 19 failing packages were resolved and made it a gate. |
| `docs-deploy.yml` | `workflow_run` after `SDK Release`, push to `main` on docs paths, manual `workflow_dispatch` | Build + deploy the versioned docs portal (`docs/site`) to GitHub Pages. Separate from release (deploy is a consequence, not a release step). |
| `docker-images.yml` | weekly + push to `.devcontainer/images/**` | Template-inherited; two-tier base+main image build |
| `publish-features.yml` | push to `.devcontainer/features/**` | Template-inherited; publishes OCI feature artifacts |
| `release.yml` | push to main on `.devcontainer/**` | Template-inherited; path-gated to `.devcontainer/**` and SDK-unaware — DO NOT edit for SDK reasons |

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
3. `bazel build --config=ci //...`
4. `bazel test --config=ci //...` — race on, so every `//go:build !race` file is dropped at compile time. Step 5 is their only gate.
5. **Alloc + zero-alloc gates (race off)** — `bazel test --config=alloc <targets>`, where `<targets>` is read from `tools/alloc-lane-targets.txt`. That file is the single source of truth shared with `make test-alloc`; do NOT inline the list here.
6. **Exemption invariant** — `scripts/pre-commit/check-alloc-lane-coverage.sh` fails the build if any `//go:build !race` test lives in a package the step-5 list does not cover. Without it, adding such a test to a new package silently produces a test that no lane runs (root `CLAUDE.md` rule 12).
8. **Domain-doc invariant** — `scripts/pre-commit/check-domain-docs.sh` fails the build when the root `CLAUDE.md`'s architecture tree no longer names exactly the directories under `internal/core`, when a domain is described twice in the Purpose paragraph, or when two Purpose paragraphs coexist — and, since the same class of drift reached the ADR indexes, when `docs/adr/CLAUDE.md` or the root Reference list stops naming exactly the ADRs on disk, once each. Every one of those had actually happened.
7. **Audit-coverage invariant** — `scripts/pre-commit/check-audit-coverage.sh` fails the build when a package declaring an `errs.Define` or `errs.Code` is absent from `//:audit_sources`. The errs AST audits can only judge the files staged as their runfiles, so such a package is audited by nothing AND passes green — the worst shape a verification gap can take.
9. **Layer invariant** — `scripts/check-layer-deps.sh` asserts four `bazel query` expressions empty (ADR 0068).
10. **Lint gate, in two halves.** `make lint-check` (`gofumpt -l` + `make guard`) runs unconditionally, and `make lint-ktn-check` (`ktn-linter`) runs only when a `KTN_LINTER_TOKEN` secret exists. Those three are the checks of `make lint` that no lane ran until #236; the other five are steps 2, 6, 7, 8 and 9 above, so this adds a CLASS of analysis rather than repeating one. The split is a measurement, not taste: `kodflow/ktn-linter` is private and a workflow token is scoped to the repository that issued it, so `gh release download v1.11.2 --repo kodflow/ktn-linter` answered `release not found` under `secrets.GITHUB_TOKEN` on this very lane, while the same command run by a credentialed account downloads the asset. `gh` and not `curl`, because the signed redirect drops the Authorization header and a `curl` 404 cannot tell "asset absent" from "not authorised". **`lint-ktn-check` is deliberately absent from `GATES`** — a step a missing secret can skip is not a gate, and the run emits a `::warning::` saying so. Add the secret and the target to `GATES` in the same commit. The install asserts the binary matches the pin, because 1.9.11 exits 0 with "No issues found" on a tree 1.11.2 rejects. These steps are in THIS job, not in `shell-gates`, because `bazel` and `post-commit` are the only required checks on `main` and a sibling job would report without blocking.
11. **Vulnerability gate** — `make vuln-install` then `make vuln-check`: `govulncheck` in source mode in every module of the census (`scripts/ci/go-modules.sh`), `GOWORK=off`, one at a time. Fails on a REACHABLE vulnerable symbol (govulncheck's exit 3) and on a scan that did not complete; an imported-but-uncalled vulnerable package is reported and passes. Blocking on purpose, and in THIS job because it is required: the SDK's requires are every consumer's floor, so a reachable vulnerability is ours to fix before anything else merges (ADR 0136, #210). The scanner version is pinned once, in the Makefile, and `vuln-check.sh` refuses any other.
12. `bazel coverage --combined_report=lcov //...` → uploaded as `coverage-${{ github.run_number }}` artifact (per-run unique name so concurrent runs don't dedupe, post-audit finding #28). Note coverage runs under the default (race-on) config, so it does **not** reflect the step-5 tests.

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
4. **Git hook regression** — `make hooks-check` → `scripts/hooks-test.sh`, which
   runs `scripts/test-commit-msg-hook.bats` against `.githooks/commit-msg`. That
   hook is the no-AI-attribution enforcement and its `printf | grep -q` failed
   INVERTED — it allowed what it exists to refuse, 40 times out of 40 past a few
   hundred KB (ADR 0088).
5. **CI script regression** — `make ci-scripts-check` → `scripts/ci-scripts-test.sh`,
   which runs `scripts/ci/*.bats`: the module census every module-looping lane
   reads and the govulncheck gate built on it (ADR 0136, ADR 0137). It also
   asserts that `cross-build` and `test-386` below still read the census rather
   than a list of their own — the shape of #242.
6. **Pre-commit guard regression** — `make pre-commit-check`.

These five, plus `make lint-check`, `make vuln-install`, `make vuln-check` and
`make lint-ktn-check` in the `bazel` job, are the only `make` invocations in
this workflow, and that is deliberate:
`ci-gates-check` asserts the Makefile↔CI link by target NAME, which only works
if CI goes through the target. The two lint targets are not in THIS job because
they need a Go toolchain and a downloaded binary — the two things `shell-gates`
exists to do without. Adding a gate means editing `GATES` **and** this
file, plus the Makefile's `.PHONY` — all three, or the check fails. That is not
a claim: adding `hooks-check` to `GATES` before wiring its step produced
`UNGATED: 'make hooks-check' exists but .github/workflows/bazel-ci.yml never
runs it`.

### cross-build (the build bar) and test-386 (the runtime bar)

`cross-build` runs `go build ./...` then `go vet ./...` per module across ten
GOOS/GOARCH cells, including linux/386 and linux/arm, so a platform-specific
low-level call can never silently drop a package (ADR 0018's build bar; local
equivalent `bash scripts/cross-platform-audit.sh`, which needs bash 4). `go
build` never compiles a `_test.go`; `go vet` type-checks it, so a test calling a
helper only one GOOS defines fails the cells it breaks (ADR 0094).

Both loop over the census — `scripts/ci/go-modules.sh`, every module whose
`go.mod` git tracks — never over a list (ADR 0137). `./...` stops at a nested
`go.mod`, and the lists these loops used to carry had drifted: `cross-build`
named six modules and `test-386` four, and neither named `tools/genindex` or
`tools/sdkguard`, whose tests had never run on 32 bits (#242). A lane that must
skip a module names it next to the loop, with the lane that covers it instead;
none does today.

`test-386` RUNS every module's tests on linux/386 — the four workspace modules
it always ran, plus the root module's `third-party/` tests, `e2e` and both tools,
all measured passing on linux/386 before they were added. Compiling and
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
3. **Compute majors to bump** — `compute-bumps.sh --explain --require-bazel`, or `inputs.force_bumps` on manual dispatch. The verdict and its reason go to the log and to the step output `reason`.
4. **Cut and push tags** — `cut-tags.sh` sizes the release from the `release:*` labels of the range's merged pull requests (ADR 0135), or from `inputs.bump` when dispatched with one (`--bump`, no lookup); rewrites the chain's go.mods, race-protected re-read, atomic push. Exit 65 is a refused size: its stderr names the pull request and both ways out. Exit 3 is a held bootstrap.
5. **Verify the computed release was actually published** — once a bump was computed, a run that pushed no `pkg/` tag fails (#227 defect 2).
6. `gh release create --generate-notes --verify-tag` per pushed tag.
7. **Release summary** — on EVERY run (`if: always()`), to the job summary and a `release-summary-${{ github.run_number }}` artifact: the verdict, the size and where it came from, and one of four renderings — refused (quoting `cut-tags.sh`), nothing to publish, held bootstrap, or the tags pushed.

**When a size is refused** (exit 65): label the pull request the refusal names — `release:minor` to grant the request, `release:patch` to decline it — and re-run the failed job, or dispatch this workflow with `bump` = patch/minor/major. `release_base()` advances only when a release is cut, so the refused merge stays in every later range until one of those happens. Never cut tags by hand: a range chosen to step over one merge steps over every size in it.

Concurrency: `sdk-release-${{ github.ref }}` with `cancel-in-progress: false` (NEVER cancel a tag-push mid-flight).

## Conventions

- Action references SHA-pinned with a trailing `# vX` comment (e.g. `actions/checkout@93cb6efe…  # v5`).
- `permissions: contents: read` only — nothing in the SDK lane writes back.
- `bazel-ci.yml` is the only source-of-truth gate. CI does NOT run `golangci-lint` directly (ADR 0004), and runs `go test` directly only where Bazel cannot — other GOOS/GOARCH (see Do NOT). It DOES run `govulncheck` directly, per module (`make vuln-check`, ADR 0136): the vulnerability database is the one input Bazel does not carry, which is why ADR 0004's retirement of the old scans left nothing scanning the SDK (#210).
- A job that loops over modules reads `scripts/ci/go-modules.sh` (ADR 0137). Writing a module list into a workflow is how `tools/` fell out of every 32-bit lane.

## Do NOT

- Re-introduce a `go test` lane for a platform **Bazel already covers**. That is
  the drift ADR 0004's "single build system" decision exists to prevent: two
  lanes running the same tests on the same platform, disagreeing, with nobody
  sure which is authoritative. A lane for a platform Bazel CANNOT build for is
  the opposite case — `cross-build` and `test-386` exist precisely because
  Bazel here builds for the host, and the alternative to raw `go` there is no
  coverage at all.
- Drop the drift check; gazelle-generated `BUILD.bazel` files must be committed.
- Edit the template-inherited workflows here for SDK reasons.
