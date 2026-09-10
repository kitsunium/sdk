# internal/service/config/

## Purpose

Concrete configuration sources (env, file), the generic merge+decode+validate
`Load[T]`, the compiled **schema** (`NewSchemaValue` + `LoadSchema` — ADR 0061),
and a cross-OS poll `Watcher` implementing `core/config`. File parsing dispatches
through the codec registry (blank-import the format). Stdlib-only, cross-OS.
ADR 0028 + ADR 0061. Emits the core sentinels `0.2.10.*`.

## Contents

| File | Surface |
|---|---|
| `env_source.go` | `EnvSource(prefix)` — `PREFIX_KEY` env vars, whole-document-JSON-coerced values |
| `file_source.go` | `FileSource(format, path)` — codec-dispatched file parse |
| `merge.go` | `deepMerge` — recursive layer merge (later wins) |
| `load.go` | `Load[T]` / `LoadSchema[T]` — merge + key pass + JSON round-trip decode + constraints + Validate |
| `schema.go` | `SchemaValue[T]` + `NewSchemaValue` + `Check` + `Source` — the compiled schema |
| `schema_spec.go` | `SchemaSpec[T]` — the declaration (`Required` / `Defaults` / `AllowUnknownKeys` / `Rule`) |
| `schema_defaults.go` | construction-time resolution: the default layer, the required keys, and every refusal |
| `schema_keys.go` | the dotted key grammar and its resolution against the target type (leaf vs table) |
| `schema_presence.go` | the LOAD-time key pass: missing required keys + unknown keys, over the merged map |
| `schema_reject.go` | how a refusal is spelled — keys and rules, never a value |
| `schema_source.go` | the default layer seen as an ordinary `Source` |
| `poll_watcher.go` | `PollWatcher(path, interval)` — mtime+size poll (cross-OS) |
| `wrap.go` | `wrapAs(sentinel, cause)` — sentinel origin-wins + cause field |
| `BENCH.md` | the numbers, and one optimisation profiled, recorded and refused |

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
- **The key pass runs BEFORE the decode**, on the merged map — the only place an
  absent key and a key set to its zero are still distinguishable. Every missing
  key and every unknown key is collected in one pass; the VALUE pass is then not
  run, because a key nobody supplied decodes to a zero the operator never wrote.
- **An unknown key is refused by default** (`SchemaSpec.AllowUnknownKeys` opts
  out). The zero value is the safe one — ADR 0031.
- **A schema owns no rule vocabulary.** `Rule` is a `core/validation.Constraint`
  and the tags are compiled by `service/validation.Struct`; there is no `min`,
  `max` or `oneof` in this package and there must not be.
- **`NewSchemaValue` compiles; `LoadSchema` checks.** Everything decidable at
  construction is refused there — measured at 40 µs / 116 allocs versus 884 ns
  for the per-load key pass (`BENCH.md`).

## Do NOT

- Use `errors.New`/`fmt.Errorf` — the unknown-format cause wraps the sentinel + a `format` field.
- Add a native file-watch backend without an ADR note (deferred).
- Echo a configuration VALUE in any message. Keys and rule names only — it is a
  security property with its own tests on both the construction and the load
  side (ADR 0061 §Decision 6).
- Make `Load` strict. A load with no schema requires nothing and refuses
  nothing; that contract predates ADR 0061 and stays.

## Verification

```
bazel test --config=race //internal/service/config:config_test
```
