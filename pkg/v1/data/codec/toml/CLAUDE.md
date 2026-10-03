<!-- updated: 2026-10-03T00:00:00Z -->
# pkg/v1/data/codec/toml/

## Purpose

Registers the TOML codec — and no other — with the SDK's codec registry when
imported, blank usually (ADR 0134). Everything that dispatches by format name
then reads and writes TOML: `config.FileSource` / `config.FSSource`,
`i18n.LoadFS`, `codec.Marshal` / `codec.Unmarshal`. It links the TOML
codec, which the SDK implements with the standard library alone, and no module
outside the SDK — where `pkg/v1/data/codec` links every format the SDK ships. The
codec itself is documented in
`internal/service/data/codec/toml/CLAUDE.md`.

## Surface

| Symbol | Role |
|---|---|
| `Format` | the untyped constant `"toml"`: goes into `config.FSSource`'s string and `i18n.LoadFS`'s `codec.Format` without a conversion |
| `CodeMarshalFailed`, `CodeUnmarshalFailed` and the sentinels `MarshalFailed`, `UnmarshalFailed` | the `0.3.5.*` codes, aliased from `internal/core/data/codec/toml`, where they are declared (ADR 0160, ADR 0074) |
| `LocalDate`, `LocalTime`, `LocalDateTime` | aliases of the codec's own types (ADR 0074: the alias points at the layer that owns the type): what a TOML local date, time and date-time decode to in `map[string]any` or `any`. They replaced the github.com/pelletier/go-toml/v2 types of the same names and fields, so a type switch migrates by changing its import |

## Why-this-shape

- **The import is the mechanism.** The implementation registers itself as Go
  initialises it; this package imports it, re-exports its three local types,
  and does nothing else. Importing it beside
  `pkg/v1/data/codec` is harmless: Go initialises a package once, so the format is
  registered once — `TestAFormatImportedTwiceIsRegisteredOnce` in
  `pkg/v1/data/codec` imports every per-format package beside the umbrella,
  which is itself built from them.
- **What it links is tested, not asserted.** The suite reads the registry
  (exactly one format), the modules the test binary was linked from — the
  SDK's and nothing else (`TestItLinksNoModuleOutsideTheSDK`) — and
  `go list -deps` when a go tool is on PATH, which must name no package outside
  the standard library and the SDK.

## Do NOT

- Import another codec here, or `pkg/v1/data/codec`.
- Hand-edit `README.md` — regenerate with `make docs-readme`.

## Verification

```
bazel test --config=race //pkg/v1/data/codec/toml:toml_test
cd pkg && GOWORK=off go test -race ./v1/data/codec/toml/
```

`TestItLinksNoModuleOutsideTheSDK` skips
under Bazel, which records no module information in a binary, and
`TestGoListDepsNamesNoOtherCodec` without a go tool on PATH;
`TestItRegistersItsFormatAlone` and `TestLocalTypesAreTheDecodedOnes` hold under
both.

## Reference

- ADR 0134; `pkg/v1/data/codec/CLAUDE.md`
