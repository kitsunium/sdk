<!-- updated: 2026-05-18T14:30:00Z -->
# tools/

## Purpose

Single-purpose scripts that the Bazel build calls into. Today only one tool lives here: `workspace_status.sh`, which feeds `bazel build --stamp` so every artifact records the source revision it was built from.

## Contents

| File | Called by | Emits |
|---|---|---|
| `workspace_status.sh` | `bazel build --stamp` (via `workspace_status_command` in `.bazelrc`) | `STABLE_VERSION <git-sha>` to stdout, falling back to `STABLE_VERSION dev` outside a git checkout |

## How it wires into the build

1. Bazel runs `tools/workspace_status.sh` once per build (with `--stamp`); the output is a key/value table.
2. `rules_go`'s `go_library` reads `STABLE_VERSION` via `x_defs` and rewrites the placeholder `{STABLE_VERSION}` in `pkg/v1/logger.Version` at link time.
3. The resulting binary's `pkg/v1/logger.FrameworkVersion()` returns the same git SHA that built it — every emitted log record carries `framework_version` for traceability.

## Conventions

- Tools here MUST be self-contained. No third-party binaries beyond what the dev container guarantees (`bash`, `git`).
- Keep them small enough to read end-to-end. If a tool exceeds ~80 lines, split it into a helper package elsewhere and keep the entry point thin.
- Output to stdout only; errors to stderr. Bazel parses stdout strictly as `KEY VALUE\n` lines.

## Do NOT

- Add a tool that mutates the working tree from inside a Bazel action. Stamp scripts are pure read-only probes.
- Rename `workspace_status.sh` — `.bazelrc` references it by exact path.
- Echo unrelated content. Bazel treats unexpected lines as build failures when `--stamp` is enabled.

## Verification

```
bazel build --stamp //pkg/v1/logger/... \
  && bazel-bin/pkg/v1/logger/logger_/logger -version  # if a binary is wired
# Or inspect the stamp file directly:
bazel info workspace_status_command
cat $(bazel info output_path)/volatile-status.txt $(bazel info output_path)/stable-status.txt
```
