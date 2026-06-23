# ADR 0028 — Configuration domain (`config`)

- **Status**: Accepted
- **Date**: 2026-06-24
- **Deciders**: SDK maintainers
- **Related**: ADR 0024 (Phase-B wave — this ADR closes it), ADR 0003 (codec — file parsing dispatch), ADR 0018 (portability), ADR 0016 (no-registry sibling precedent)
- **Amends**: covered by the ADR 0024 Phase-B purpose-statement widening

## Context

The SDK has no **configuration** loader — every downstream re-implements
env+file layering, typed decode, validation, and reload. This is the last
Phase-B domain, and the only one with a runtime-portability dimension (file
watching), so it exercises the ADR 0018 cross-OS bar directly.

## Decision

1. **Add `internal/core/config`** — a core sibling declaring `Source`
   (`Load() (map[string]any, error)`), the optional `Validator` (the decoded
   struct's `Validate() error`), and `Watcher` (`Watch(ctx, onChange) error`),
   plus the typed failure sentinels. No registry (concrete constructors — the
   `proc`/`resilience` precedent).
2. **`internal/service/config`** ships `EnvSource(prefix)` (JSON-coercing env
   reader), `FileSource(format, path)` (codec-dispatched parse), the generic
   `Load[T](target, sources...)` (deep-merge later-wins → JSON round-trip decode
   → `Validate`), and `PollWatcher(path, interval)`.
3. **`pkg/v1/config`** aliases the port + re-exports `Load`/`EnvSource`/
   `FileSource`/`PollWatcher` + sentinels.
4. **Error block `0.2.10.*`**: `CONFIG_SOURCE_FAILED`, `CONFIG_DECODE_FAILED`,
   `CONFIG_VALIDATION_FAILED`, `CONFIG_WATCH_FAILED` (all `EX_CONFIG` = 78).

### Cross-platform (ADR 0018) — the watch decision

File watching is the portability crux. Native mechanisms diverge (`inotify`
Linux, `kqueue` BSD/macOS, `ReadDirectoryChangesW` Windows). v1 ships a
**poll watcher** (`os.Stat` mtime+size on an interval) — 100 % portable on all 8
GOOS, zero new deps, correct on every real kernel. Native event-driven backends
are deferred (additive behind the same `Watcher` port). This is the honest
build-bar-AND-runtime-bar choice from ADR 0018.

### Decode strategy

Sources yield `map[string]any`; the loader deep-merges (later wins) then decodes
via a `json.Marshal` → `json.Unmarshal` round-trip into the typed target —
reusing `encoding/json`'s struct-tag mapping, no `mapstructure` dependency. Env
values are JSON-coerced (`"8080"`→int, `"true"`→bool; bare words stay strings)
so typed fields populate from the environment.

## Consequences

- 10th core sibling; closes the Phase-B wave (id/cache/resilience/metrics/config).
  `pkg/v1` gains a dep-light `config` facade. Docs + `error-codes.yaml` updated
  per rule 11.
- `FileSource` depends on the codec registry — consumers blank-import the format
  (e.g. `pkg/v1/codec`); an unregistered format is a `CONFIG_SOURCE_FAILED`.

## Alternatives considered

- **viper-style all-in-one dep** — rejected: heavy, opinionated; the SDK composes
  its own codec + stdlib instead, staying dep-light.
- **mapstructure for decode** — rejected for v1: the JSON round-trip reuses the
  codec/struct-tag machinery already in the tree with no new dep.
- **inotify/kqueue watch in v1** — deferred: poll meets both ADR 0018 bars now;
  native backends are additive behind `Watcher`.

## Deferred

- Native event-driven watch backends (`inotify`/`kqueue`/Windows) behind `Watcher`.
- Secret redaction on decoded values + a `Secret` field type.
- Nested env keys (`PREFIX_DB__HOST` → `db.host`); v1 env is flat.

## References

- Impl: `internal/core/config/`, `internal/service/config/`, `pkg/v1/config/`.
- ADR 0024 (Phase-B wave), ADR 0003 (codec dispatch), ADR 0018 (portability).
