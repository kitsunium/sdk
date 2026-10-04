<!-- updated: 2026-10-04T10:00:00Z -->
# tools/

## Purpose

Single-purpose scripts, data files and small Go programs the build tooling calls into. Four entries live here today: `workspace_status.sh` (feeds `bazel build --stamp`), `genindex/` (writes and checks `docs/api` — every exported symbol, read from the code, which the docs site renders —, checks doc links, and keeps the go/doc symbol index the site read before), `alloc-lane-targets.txt` (the shared target list for the race-off allocation lane), and `sdkguard/` (enforces the SDK's consumer-facing rules on downstream codebases — ADR 0033).

## Contents

| Path | Called by | Emits |
|---|---|---|
| `workspace_status.sh` | `bazel build --stamp` (via `workspace_status_command` in `.bazelrc`) | `STABLE_VERSION <git-sha>` to stdout, falling back to `STABLE_VERSION dev` outside a git checkout |
| `genindex/` (Go program, stdlib-only) | with `-check-doclinks`, `make doclinks`; with `-write-api`, `make api`; with `-check-api`, `make api-check` — `make lint-check` runs `doclinks` and `api-check`. Its default mode, the go/doc symbol index, no build step runs: the docs site builds its ⌘K index from `docs/api` | by default, a flat JSON list of every exported Go symbol of `-input` (kind, signature, doc synopsis, URL, source URL), as go/doc sees it — the docs-site search index until `docs/api`, which also reaches the members of an alias. With `-check-doclinks <dir>…` it emits no index and fails on every same-package doc link that names no declared symbol, at the line that writes it, judged on each platform that compiles the file (ADR 0138, ADR 0144). With `-write-api` it writes `docs/api/<module>.json` for every module of `go.work` — every exported symbol with its id, kind, signatures, owner, doc, cells, file, codes, layer and family, read with go/types on every cell (`docs/api/schema.json`); with `-check-api` it writes nothing and fails on any byte that differs — and, with `-markers -digests` as `make api-check` runs it, on a code surface that differs from the markers of the pins kit writes from `design/`, or a generated file whose digests no longer hold (ADR 0163). With `-write-error-codes` it writes `docs/error-codes.yaml` from `docs/api` (`make error-codes`), and `-check-error-codes` is the drift guard's. The cells are the platforms table, `scripts/ci/platforms.sh` — the cross-build lane's twelve, which `scripts/pre-commit/check-platforms.sh` holds equal to `bazel-ci.yml`'s matrix (see `genindex/CLAUDE.md`) |
| `sdkguard/` (Go program, stdlib-only) | a consumer's CI / `make`, or `go run github.com/kitsunium/sdk/tools/sdkguard@latest` | diagnostics on stderr in `file:line:col` form; exit 1 on findings, 2 on tool error — see `tools/sdkguard/CLAUDE.md` |
| `alloc-lane-targets.txt` (plain list, `#` comments) | `make test-alloc`, the `bazel-ci.yml` alloc step, and `scripts/pre-commit/check-alloc-lane-coverage.sh` | nothing — it IS the data: one Bazel test target per line for `bazel test --config=alloc` |

### `alloc-lane-targets.txt`

A test file carrying `//go:build !race` is dropped at compile time by the race suite (`bazel test --config=ci //...`, race on by default), so it runs in exactly one place: the race-off alloc lane. A `!race` test whose package is missing from this list therefore runs in **no lane at all** — it compiles, it is never executed, and nothing reports it as skipped.

Keeping the list in one file (rather than duplicated in the Makefile and the workflow) is what lets `check-alloc-lane-coverage.sh` verify the invariant mechanically: every directory holding a `//go:build !race` test must be covered by an entry here, directly or via a `/...` prefix. The guard runs in `make lint` and as its own step of CI's `bazel` job (the pre-commit hook that also ran it went with ADR 0153).

## How they wire in

### `sdkguard/`

1. A downstream repo that imports the SDK runs `sdkguard ./...` in CI (or from its own `Makefile`).
2. It parses the consumer's source with `go/parser`, resolves each file's import aliases, and matches the five rules against constructs — not against imports, which is what keeps `*slog.Logger` and `fmt.Fprintln(os.Stdout, …)` legitimate.
3. Findings print in the standard Go diagnostic format and the process exits 1. `-level=invariant` runs only the rules whose violation is a correctness defect, so a team can adopt the tool before it has adopted every convention.
4. Separately, it warns when the consumer's `go.mod` pins an SDK older than the newest release — a warning that never moves the exit code, since being behind is a fact rather than a violation. Every failure path (no proxy, `GOPROXY=off`, a `replace` directive, a timeout) degrades to silence. `make guard` passes `-version-check=off` so `make lint` needs no network.
5. The SDK runs it on itself: `make guard` — part of `make lint` and of CI's lint gate — runs the invariants over the whole tree, then SDK002 alone over `internal/`, `pkg/`, `third-party/` and `framework/`, which is how SDK rule 2 (no `fmt.Errorf`, no `errors.New` in production code) is enforced. SDK002 stays a convention for a consumer; the SDK grants itself no `//sdkguard:allow SDK002`.

Why it is a CLI rather than an `init()` hook or a `go vet -vettool`: runtime detection was measured to be impossible (`log/slog` is not a module, so it never appears in `debug.ReadBuildInfo`; a locally held `slog.Logger` never touches `slog.Default`), and the vettool protocol lives in `golang.org/x/tools` — a dependency this tree cannot take. See ADR 0033.

### `workspace_status.sh`

1. Bazel runs `tools/workspace_status.sh` once per build (with `--stamp`); the output is a key/value table.
2. `rules_go`'s `go_library` reads `STABLE_VERSION` via `x_defs` and rewrites the placeholder `{STABLE_VERSION}` in `pkg/v1/observe/logger.Version` at link time.
3. The resulting binary's `pkg/v1/observe/logger.FrameworkVersion()` returns the same git SHA that built it — every emitted log record carries `framework_version` for traceability.

### `genindex/`

1. `make api` runs it with `-write-api -repo-root $(CURDIR)`: one `go list -deps` per cell of `scripts/ci/platforms.sh` lists the workspace's universe, go/types checks every package of it from source with function bodies ignored, and `docs/api/<module>.json` is written for each module of `go.work`. `make api-check` — a line of `make lint-check`, so CI's required job runs it — does the same in memory and fails on any byte that differs from what is committed, then holds each cell's exported surface to the pins' markers and every generated file's header to its design file's bytes (ADR 0163). A doc edit needs `make api` and nothing else; an API change starts in `design/` and `kit gen`.
2. The docs site reads what it wrote, with no go command: `docs/site/scripts/sync-versions.mjs` turns each release's `docs/api` into the API section of every package page, and `gen-symbols.mjs` into its ⌘K index (`docs/site/CLAUDE.md`); `make docs-check` holds both to `docs/api`.
3. Its default mode walks every package under `-input` with `go/parser` + `go/doc` and emits one JSON row per func / type / method / const / var — the search index the docs site loaded until it read `docs/api`. No build step runs it now; it stays a mode of the tool, tested, until it is retired.

## Conventions

- Tools here MUST be self-contained. No third-party binaries beyond `bash`, `git` and `go`.
- Shell scripts: keep them small enough to read end-to-end. If a tool exceeds ~80 lines, split it into a helper package elsewhere and keep the entry point thin.
- Go programs (e.g. `genindex/`) live in their own subdirectory with `go.mod` OUTSIDE `go.work` (`GOWORK=off`) so the workspace invariant is preserved: every module `go.work` names beside the SDK may be tagged by a release (ADR 0162), and a tool is not published.
- Stdlib-only for Go tools. Pulling extra deps would pollute the build dependency graph for what is essentially a one-shot script.
- The `tools/**` tree is linted like everything else. It used to be excluded via `.ktn-linter.yaml` on the grounds that tools are not library code, but the rules turned out to apply perfectly well to a build script — and the exemption was mostly hiding missing tests. Fix the finding rather than re-adding the exclusion.

## Do NOT

- Add a dependency to `sdkguard/` (or any tool here). The stdlib-only rule is structural, not stylistic: a dependency-free module is what lets Bazel build `tools/*` while they sit outside `go.work`.
- Add a tool that mutates the working tree from inside a Bazel action. Stamp scripts are pure read-only probes.
- Rename `workspace_status.sh` — `.bazelrc` references it by exact path.
- Echo unrelated content from `workspace_status.sh`. Bazel treats unexpected lines as build failures when `--stamp` is enabled.
- Add `genindex/` to `go.work`. It must stay out of the umbrella so `GOWORK=off go run` works from a clean checkout — and because a release tags the modules `go.work` names, so a tool there would be published with the SDK (and `vendor_modules` refuses a `go.work` that names it).
- Inline the alloc-lane target list into the `Makefile` or `bazel-ci.yml`. All three call sites MUST read `alloc-lane-targets.txt`, or `check-alloc-lane-coverage.sh` starts verifying a list nobody runs.
- Silence `check-alloc-lane-coverage.sh` by deleting the offending entry. If a `!race` test genuinely should not run, drop the `!race` constraint or the test — do not leave it compiling but ungated.

## Verification

```
# workspace_status.sh
bazel build --stamp //pkg/v1/observe/logger/... \
  && bazel-bin/pkg/v1/observe/logger/logger_/logger -version  # if a binary is wired
bazel info workspace_status_command
cat $(bazel info output_path)/volatile-status.txt $(bazel info output_path)/stable-status.txt

# genindex
cd tools/genindex && GOWORK=off go vet ./...
GOWORK=off go run . -check-doclinks "$(git rev-parse --show-toplevel)"   # = make doclinks
GOWORK=off go run . -write-api       # = make api: docs/api under ../.. (-repo-root defaults to two levels up)
GOWORK=off go run . -check-api -markers -digests   # = make api-check
GOWORK=off go run . -write-error-codes             # = make error-codes
GOWORK=off go run . -input ../../pkg/v1 -module github.com/kitsunium/sdk/pkg/v1 \
  -url-base /local/v1 -repo-root ../.. -source-url-prefix https://github.com/kitsunium/sdk/blob/main
```
