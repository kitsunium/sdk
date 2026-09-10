# internal/core/config/

## Purpose

Declares the **configuration port** — layered config from multiple `Source`s, an
optional `Validator` the decoded struct implements, a `Watcher` for change
notification, and the `DeclaredValue` a schema uses to say what a key holds when
nobody supplied it. A core sibling admitted by **ADR 0028** (closes the Phase-B
wave), extended in place by **ADR 0061** (the schema). Concrete sources (env,
file), the merge+decode loader, the schema compiler and the cross-OS poll
watcher live in `internal/service/config`; this package owns only the contract,
the one domain value, and the typed failure sentinels.

Code range: `0.2.10.*` (ADR 0028 + ADR 0061).

## Contents

| File | Surface |
|---|---|
| `source.go` | `Source` (`Load() (map[string]any, error)`) |
| `validator.go` | `Validator` (`Validate() error`) — optional, implemented by the decoded struct |
| `watcher.go` | `Watcher` (`Watch(ctx, onChange) error`) |
| `schema.go` | `DeclaredValue` (`Key` + `Value`) — one key and what it takes when NO source supplied it (ADR 0061) |
| `codes.go` / `errors.go` | `0.2.10.*` (CONFIG_SOURCE_FAILED, CONFIG_DECODE_FAILED, CONFIG_VALIDATION_FAILED, CONFIG_WATCH_FAILED, CONFIG_SCHEMA_INVALID, CONFIG_KEY_MISSING, CONFIG_UNKNOWN_KEY) |

## Conventions

- **Sources yield flat/nested maps**; the loader merges (later wins) then decodes.
- **No registry** — sources/watcher are concrete constructors (`proc` precedent).
- Sentinels carry `EX_CONFIG` (78).
- **A default is a LAYER, never a post-decode fallback.** `DeclaredValue` says so
  in its own doc: after the decode an absent key and a key set to its zero are
  the same bytes, so presence has to be decided while the key is still a key.
- **`CONFIG_SCHEMA_INVALID` is a CONSTRUCTION outcome; `CONFIG_KEY_MISSING` and
  `CONFIG_UNKNOWN_KEY` are LOAD outcomes.** The first blames the author, the
  other two blame the deployment — the split is the point, not a nuance.

## Do NOT

- Put loader/merge/watch/schema bodies here — they live in `service/config`.
- Add native inotify/kqueue to the port — the default watcher is cross-OS poll.
- Grow a rule vocabulary here (`min`, `max`, `oneof`…). `validation` owns those
  and the schema composes it — ADR 0061 §What the schema does NOT do.

## Verification

```
bazel test --config=race //internal/core/config:config_test
```
