# internal/service/config/

## Purpose

Concrete configuration sources (env, file), the generic merge+decode+validate
`Load[T]`, and a cross-OS poll `Watcher` implementing `core/config`. File parsing
dispatches through the codec registry (blank-import the format). Stdlib-only,
cross-OS. ADR 0028. Emits the core sentinels `0.2.10.*`.

## Contents

| File | Surface |
|---|---|
| `env_source.go` | `EnvSource(prefix)` — `PREFIX_KEY` env vars, JSON-coerced values |
| `file_source.go` | `FileSource(format, path)` — codec-dispatched file parse |
| `merge.go` | `deepMerge` — recursive layer merge (later wins) |
| `load.go` | `Load[T](target, sources...)` — merge + JSON round-trip decode + Validate |
| `poll_watcher.go` | `PollWatcher(path, interval)` — mtime+size poll (cross-OS) |
| `wrap.go` | `wrapAs(sentinel, cause)` — sentinel origin-wins + cause field |

## Conventions

- **Env values are JSON-coerced**: `"8080"`→int, `"true"`→bool, bare words stay strings.
- **Decode via JSON round-trip**: merged map → `json.Marshal` → `json.Unmarshal` into the
  typed target (reuses struct tags; no mapstructure dep).
- **Cross-OS watch is poll-based** (`os.Stat` mtime+size) — no inotify/kqueue.
- No `errs.Define` here — service emits the `core/config` sentinels via `wrapAs`.

## Do NOT

- Use `errors.New`/`fmt.Errorf` — the unknown-format cause wraps the sentinel + a `format` field.
- Add a native file-watch backend without an ADR note (deferred).

## Verification

```
bazel test --config=race //internal/service/config:config_test
```
