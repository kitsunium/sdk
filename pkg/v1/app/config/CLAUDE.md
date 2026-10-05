# pkg/v1/app/config/

## Purpose

Public facade for the SDK configuration domain (ADR 0028 + ADR 0061 + ADR 0097).
Aliases the `Source`/`Validator`/`Watcher` port, the `Default`/`Schema`/`SchemaSpec`
schema shapes and the `Origin`/`Describer` provenance pair, exposes the generic
`Load[T]` and `LoadSchema[T]` and their traced twins `LoadWithOrigins[T]` /
`LoadSchemaWithOrigins[T]`, the `NewSchema` compiler, the
`EnvSource`/`FileSource`/`FSSource`/`PollWatcher` constructors, the `Layer*` names, and the
sentinels. Stdlib-only → dep-light; cross-OS (poll watcher).

## Surface

| Symbol | Notes |
|---|---|
| `Source` / `Validator` / `Watcher` | aliases onto `core/app/config` |
| `Default` | alias onto `core/app/config.DeclaredValue` — one key + the value it takes when nobody supplied it |
| `Schema[T]` / `SchemaSpec[T]` | aliases onto `service/app/config.SchemaValue[T]` / `SchemaSpec[T]` |
| `Load[T](target, sources...)` | merge (later wins) + decode + Validate — no schema, nothing required, nothing refused |
| `NewSchema[T](spec)` | compiles a declaration; refuses at construction what cannot work |
| `LoadSchema[T](target, schema, sources...)` | defaults **under** every source, key pass before the decode, constraints after |
| `Schema.Check(v)` | the full located report (messages the error does not carry) |
| `LoadWithOrigins[T]` / `LoadSchemaWithOrigins[T]` | the same loads, plus one `Origin` per leaf key: the layer that supplied its final value and the variable or path behind it — never the value (ADR 0097) |
| `Origin` / `Describer` | aliases onto `core/app/config.OriginValue` / `Describer`; `EnvSource`, `FileSource`, `FSSource` and `Schema.Source` implement `Describer` |
| `LayerDefault` / `LayerFile` / `LayerEnv` / `LayerSource` | the layer names a traced load reports; a source may name its own |
| `EnvSource(prefix)` / `FileSource(format, path)` / `PollWatcher(path, interval)` | constructors |
| `FSSource(fsys, format, path)` | `FileSource` over an `io/fs.FS` — a configuration embedded in the binary; same codecs, same `SourceFailed` (a file the FS does not hold included), reported as `LayerFile` with the path as given (ADR 0097 §Amendment) |
| `SourceFailed` / `DecodeFailed` / `ValidationFailed` / `WatchFailed` / `SchemaInvalid` / `KeyMissing` / `UnknownKey` | sentinels |

## Conventions

- **Type aliases, not new types**; `Load` is generic + delegates.
- **`FileSource` and `FSSource` need the codec registered** — blank-import `pkg/v1/data/codec` (or the format's package).
- **A missing file is `SourceFailed`, never an empty layer**, on disk and in an
  `fs.FS` alike; an optional layer is the caller's `fs.Stat`.
- Env values are JSON-coerced **only when the whole value is one complete JSON
  document** (numbers/bools typed); anything else keeps its exact string, so
  `0A0A01`, `1500ms`, `10.45.0.0/16` and `2026-09-03` survive intact. Cross-OS
  poll watch.
- **A required key fails at start-up, not at first access**, and every missing
  key is named in one error. It is a KEY-level check (was the key supplied);
  `validate:"required"` is the VALUE-level one (is the value non-zero). `port = 0`
  satisfies the first and fails the second.
- **An unknown key is refused by default.** `SchemaSpec.AllowUnknownKeys` opts
  out — needed for a shared file or an unprefixed `EnvSource`, which hands over
  the whole environment.
- **A key is never both required and defaulted** — refused by `NewSchema`.
- **No message ever repeats a configuration value.** Keys and rule names only.
- **A `secret.Value` field is marked `Secret` in an origin and reads the
  environment's raw text**, bypassing the JSON coercion that would re-spell a
  numeric secret — on every load, traced or not (ADR 0097).
- README written by `tools/genindex` from `docs/api` (ADR 0167); the package
  comment is in `doc.go`, which kit writes from `design/app/config.yaml`: edit the
  design, run `kit gen`, then `make api` and `make docs-readme`.

## Do NOT

- Reimplement merge/decode/schema here — the facade is aliases + thin wrappers.
- Add a rule vocabulary. `SchemaSpec.Rule` is a `validation.Constraint`; the
  schema composes `pkg/v1/app/validation` and must not grow a second engine.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/app/config.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the declarations of their own, and `doc.go` — kit's too (ADR 0167) — the package comment. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```
bazel test --config=race //pkg/v1/app/config:config_test
```
