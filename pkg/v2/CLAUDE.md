<!-- updated: 2026-05-22T00:00:00Z -->
# pkg/v2/

> **Preview / placeholder.** This directory is a documentation-only
> stub used to validate the v1/v2 dropdown on the docs site. No Go
> source ships here yet — `pkg/v2/` becomes a real module the day a
> breaking change against `pkg/v1` is ready to land (see
> ADR 0007 §9 "Major version migration").

## Purpose

When `pkg/v2/` graduates to a real module, it will:

- Hold the next major-version API surface (`github.com/kitsunium/sdk/pkg/v2`).
- Coexist with `pkg/v1/` so downstream consumers can migrate one
  package at a time (`go.work` lists both).
- Receive its own patch-bump lane in `scripts/release/cut-tags.sh`
  (tags `pkg/v2/vX.Y.Z`).

## Migration checklist (when real v2 lands)

1. Replace the markdown stubs in this tree with real Go source.
2. Update `pkg/v2/go.mod`: `module github.com/kitsunium/sdk/pkg/v2`.
3. Add `./pkg/v2` to `go.work`.
4. First release tag: `pkg/v2/v2.0.0`.

Until then this stub keeps the docs portal showing the v1 ↔ v2
dropdown so the navigation pattern is exercised end-to-end.
