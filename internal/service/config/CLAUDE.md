# internal/service/config/

## Purpose

Concrete configuration sources (env, file), the generic merge+decode+validate
`Load[T]`, and a cross-OS poll `Watcher` implementing `core/config`. File parsing
dispatches through the codec registry (blank-import the format). Stdlib-only,
cross-OS. ADR 0028. Emits the core sentinels `0.2.10.*`.

## Contents

| File | Surface |
|---|---|
| `env_source.go` | `EnvSource(prefix)` — `PREFIX_KEY` env vars, whole-document-JSON-coerced values |
| `file_source.go` | `FileSource(format, path)` — codec-dispatched file parse |
| `merge.go` | `deepMerge` — recursive layer merge (later wins) |
| `load.go` | `Load[T](target, sources...)` — merge + JSON round-trip decode + Validate |
| `poll_watcher.go` | `PollWatcher(path, interval)` — mtime+size poll (cross-OS) |
| `wrap.go` | `wrapAs(sentinel, cause)` — sentinel origin-wins + cause field |

## Conventions

- **Key mapping is mechanical**: the JSON tag is the variable name with the
  prefix removed, lower-cased, underscores kept. `EnvSource("APP")` maps
  `APP_SDM_SERVER_NAME` to the key `sdm_server_name`, so the field needs
  `json:"sdm_server_name"`. The separating underscore is supplied by the Source,
  and a trailing one on the prefix is absorbed — `EnvSource("APP")` and
  `EnvSource("APP_")` are the same namespace. An empty prefix reads every
  variable and lower-cases the key as-is.
- **Env values are JSON-coerced ONLY when the whole value is one complete JSON
  document**: `"8080"`→int64, `"true"`→bool, everything else stays the exact
  string it was. The gate is `json.Valid` over the entire input, because
  `json.Decoder.Decode` returns the first token and reports no error for the
  trailing bytes — without it `0A0A01` becomes `0`, `1500ms` becomes `1500`,
  `2026-09-03` becomes `2026`, `10.45.0.0/16` becomes `10.45`. Surrounding
  whitespace is still tolerated (`" 8080 "`→int64), which is why the check spans
  the raw value rather than a trimmed copy.
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
