<!-- updated: 2026-05-23T10:55:00Z -->
# tools/

## Purpose

Single-purpose scripts and small Go programs the build tooling calls into. Two tools live here today: `workspace_status.sh` (feeds `bazel build --stamp`) and `genindex/` (emits the symbol search index for the docs site).

## Contents

| Path | Called by | Emits |
|---|---|---|
| `workspace_status.sh` | `bazel build --stamp` (via `workspace_status_command` in `.bazelrc`) | `STABLE_VERSION <git-sha>` to stdout, falling back to `STABLE_VERSION dev` outside a git checkout |
| `genindex/` (Go program, stdlib-only) | `docs/site/scripts/gen-symbols.mjs` during `npm run prebuild` | `public/_search/symbols-<major>.json` — flat list of every exported Go symbol (kind, signature, doc synopsis, URL, source URL) consumed by the docs-site search modal |

## How they wire in

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

- Add a tool that mutates the working tree from inside a Bazel action. Stamp scripts are pure read-only probes.
- Rename `workspace_status.sh` — `.bazelrc` references it by exact path.
- Echo unrelated content from `workspace_status.sh`. Bazel treats unexpected lines as build failures when `--stamp` is enabled.
- Add `genindex/` to `go.work`. It must stay out of the umbrella so `GOWORK=off go run` works from a clean checkout and the SDK's 5-module count stays accurate.

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
