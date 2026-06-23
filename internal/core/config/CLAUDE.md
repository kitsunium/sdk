# internal/core/config/

## Purpose

Declares the **configuration port** — layered config from multiple `Source`s, an
optional `Validator` the decoded struct implements, and a `Watcher` for change
notification. A core sibling admitted by **ADR 0028** (closes the Phase-B wave).
Concrete sources (env, file), the merge+decode loader, and the cross-OS poll
watcher live in `internal/service/config`; this package owns only the contract +
the typed failure sentinels.

Code range: `0.2.10.*` (ADR 0028).

## Contents

| File | Surface |
|---|---|
| `source.go` | `Source` (`Load() (map[string]any, error)`) |
| `validator.go` | `Validator` (`Validate() error`) — optional, implemented by the decoded struct |
| `watcher.go` | `Watcher` (`Watch(ctx, onChange) error`) |
| `codes.go` / `errors.go` | `0.2.10.*` (CONFIG_SOURCE_FAILED, CONFIG_DECODE_FAILED, CONFIG_VALIDATION_FAILED, CONFIG_WATCH_FAILED) |

## Conventions

- **Sources yield flat/nested maps**; the loader merges (later wins) then decodes.
- **No registry** — sources/watcher are concrete constructors (`proc` precedent).
- Sentinels carry `EX_CONFIG` (78).

## Do NOT

- Put loader/merge/watch bodies here — they live in `service/config`.
- Add native inotify/kqueue to the port — the default watcher is cross-OS poll.

## Verification

```
bazel test --config=race //internal/core/config:config_test
```
