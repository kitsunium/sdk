<!-- updated: 2026-05-23T10:55:00Z -->
# tools/

## Purpose

Single-purpose scripts, data files and small Go programs the build tooling calls into. Four entries live here today: `workspace_status.sh` (feeds `bazel build --stamp`), `genindex/` (emits the symbol search index for the docs site), `alloc-lane-targets.txt` (the shared target list for the race-off allocation lane), and `sdkguard/` (enforces the SDK's consumer-facing rules on downstream codebases — ADR 0033).

## Contents

| Path | Called by | Emits |
|---|---|---|
| `workspace_status.sh` | `bazel build --stamp` (via `workspace_status_command` in `.bazelrc`) | `STABLE_VERSION <git-sha>` to stdout, falling back to `STABLE_VERSION dev` outside a git checkout |
| `genindex/` (Go program, stdlib-only) | `docs/site/scripts/gen-symbols.mjs` during `npm run prebuild` | `public/_search/symbols-<major>.json` — flat list of every exported Go symbol (kind, signature, doc synopsis, URL, source URL) consumed by the docs-site search modal |
| `sdkguard/` (Go program, stdlib-only) | a consumer's CI / `make`, or `go run github.com/kitsunium/sdk/tools/sdkguard@latest` | diagnostics on stderr in `file:line:col` form; exit 1 on findings, 2 on tool error — see `tools/sdkguard/CLAUDE.md` |
| `alloc-lane-targets.txt` (plain list, `#` comments) | `make test-alloc`, the `bazel-ci.yml` alloc step, and `scripts/pre-commit/check-alloc-lane-coverage.sh` | nothing — it IS the data: one Bazel test target per line for `bazel test --config=alloc` |

### `alloc-lane-targets.txt`

A test file carrying `//go:build !race` is dropped at compile time by the race suite (`bazel test --config=ci //...`, race on by default), so it runs in exactly one place: the race-off alloc lane. A `!race` test whose package is missing from this list therefore runs in **no lane at all** — it compiles, it is never executed, and nothing reports it as skipped.

Keeping the list in one file (rather than duplicated in the Makefile and the workflow) is what lets `check-alloc-lane-coverage.sh` verify the invariant mechanically: every directory holding a `//go:build !race` test must be covered by an entry here, directly or via a `/...` prefix. The guard runs in the pre-commit chain, in `make lint`, and as its own CI step.

## How they wire in

### `sdkguard/`

1. A downstream repo that imports the SDK runs `sdkguard ./...` in CI (or from its own `Makefile`).
2. It parses the consumer's source with `go/parser`, resolves each file's import aliases, and matches the five rules against constructs — not against imports, which is what keeps `*slog.Logger` and `fmt.Fprintln(os.Stdout, …)` legitimate.
3. Findings print in the standard Go diagnostic format and the process exits 1. `-level=invariant` runs only the rules whose violation is a correctness defect, so a team can adopt the tool before it has adopted every convention.
4. Separately, it warns when the consumer's `go.mod` pins an SDK older than the newest release — a warning that never moves the exit code, since being behind is a fact rather than a violation. Every failure path (no proxy, `GOPROXY=off`, a `replace` directive, a timeout) degrades to silence. `make guard` passes `-version-check=off` so `make lint` needs no network.

Why it is a CLI rather than an `init()` hook or a `go vet -vettool`: runtime detection was measured to be impossible (`log/slog` is not a module, so it never appears in `debug.ReadBuildInfo`; a locally held `slog.Logger` never touches `slog.Default`), and the vettool protocol lives in `golang.org/x/tools` — a dependency this tree cannot take. See ADR 0033.

### `workspace_status.sh`

1. Bazel runs `tools/workspace_status.sh` once per build (with `--stamp`); the output is a key/value table.
2. `rules_go`'s `go_library` reads `STABLE_VERSION` via `x_defs` and rewrites the placeholder `{STABLE_VERSION}` in `pkg/v1/logger.Version` at link time.
3. The resulting binary's `pkg/v1/logger.FrameworkVersion()` returns the same git SHA that built it — every emitted log record carries `framework_version` for traceability.

### `genindex/`

1. `docs/site/scripts/gen-symbols.mjs` (a Node prebuild step) reads `versions.json` + `build-info.json`, then spawns `go run github.com/kitsunium/sdk/tools/genindex` with `GOWORK=off` (keeping the 5-module invariant from /workspace/CLAUDE.md intact).
2. `genindex` walks every package under `-input` (e.g. `pkg/v1`), uses `go/parser` + `go/doc` to extract exported symbols, and emits one JSON row per func / type / method / const / var. `-source-url-prefix` (set to the current commit's GitHub blob URL) is concatenated with the repo-relative file path + line number to produce the per-symbol `sourceUrl`.
3. The docs site's `Search.astro` component lazy-loads the JSON on first ⌘K and feeds MiniSearch.

## Conventions

- Tools here MUST be self-contained. No third-party binaries beyond what the dev container guarantees (`bash`, `git`, `go`).
- Shell scripts: keep them small enough to read end-to-end. If a tool exceeds ~80 lines, split it into a helper package elsewhere and keep the entry point thin.
- Go programs (e.g. `genindex/`) live in their own subdirectory with `go.mod` OUTSIDE `go.work` (`GOWORK=off`) so the 5-module workspace invariant is preserved.
- Stdlib-only for Go tools. Pulling extra deps would pollute the build dependency graph for what is essentially a one-shot script.
- The `tools/**` tree is excluded from `ktn-linter` via `.ktn-linter.yaml` (Rule 8 tooling exemption in /workspace/CLAUDE.md). Tools are not library code; package-doc gates do not apply.

## Do NOT

- Add a dependency to `sdkguard/` (or any tool here). The stdlib-only rule is structural, not stylistic: a dependency-free module is what lets Bazel build `tools/*` while they sit outside `go.work`.
- Add a tool that mutates the working tree from inside a Bazel action. Stamp scripts are pure read-only probes.
- Rename `workspace_status.sh` — `.bazelrc` references it by exact path.
- Echo unrelated content from `workspace_status.sh`. Bazel treats unexpected lines as build failures when `--stamp` is enabled.
- Add `genindex/` to `go.work`. It must stay out of the umbrella so `GOWORK=off go run` works from a clean checkout and the SDK's 5-module count stays accurate.
- Inline the alloc-lane target list into the `Makefile` or `bazel-ci.yml`. All three call sites MUST read `alloc-lane-targets.txt`, or `check-alloc-lane-coverage.sh` starts verifying a list nobody runs.
- Silence `check-alloc-lane-coverage.sh` by deleting the offending entry. If a `!race` test genuinely should not run, drop the `!race` constraint or the test — do not leave it compiling but ungated.

## Verification

```
# workspace_status.sh
bazel build --stamp //pkg/v1/logger/... \
  && bazel-bin/pkg/v1/logger/logger_/logger -version  # if a binary is wired
bazel info workspace_status_command
cat $(bazel info output_path)/volatile-status.txt $(bazel info output_path)/stable-status.txt

# genindex
cd tools/genindex && GOWORK=off go vet ./...
GOWORK=off go run . -input ../../pkg/v1 -module github.com/kitsunium/sdk/pkg/v1 \
  -url-base /local/v1 -repo-root /workspace -source-url-prefix https://github.com/kitsunium/sdk/blob/main
```
