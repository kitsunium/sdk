<!-- updated: 2026-10-03T00:00:00Z -->
# pkg/v1/data/codec/ndjson/

## Purpose

Registers the NDJSON codec — and no other — with the SDK's codec
registry when imported, blank usually (ADR 0134). Everything that dispatches
by format name then reads and writes it: `codec.Marshal` / `codec.Unmarshal`,
`config.FSSource`, `i18n.LoadFS`. It links the service codec
(`internal/service/data/codec/ndjson`), the core package declaring its codes
(`internal/core/data/codec/ndjson`) and the standard library — where
`pkg/v1/data/codec` links every format the SDK ships.

## Surface

| Symbol | Role |
|---|---|
| `Format` | the untyped constant `"ndjson"` |
| `Code*`, and the sentinels of the same names | the `0.3.11.*` codes, aliased from `internal/core/data/codec/ndjson`, where they are declared (ADR 0160, ADR 0074) |

## Why-this-shape

- **The import is the mechanism.** The implementation registers itself as Go
  initialises it; this package imports it. Importing it beside
  `pkg/v1/data/codec` is harmless — Go initialises a package once, so the
  format is registered once; `TestAFormatImportedTwiceIsRegisteredOnce` in
  `pkg/v1/data/codec` imports every per-format package beside the umbrella,
  which is itself built from them.
- **The codes travel with the format.** A program that links one format
  matches that format's refusals without reaching `internal/`.
- **What it links is tested, not asserted.** The suite reads the registry
  (exactly this package's formats), the modules the test binary was linked
  from (none outside the SDK — ADR 0156 §1) and `go list -deps` when a go tool
  is on PATH (no other codec, nothing outside the SDK and the standard
  library).

## Do NOT

- Import another codec here, or `pkg/v1/data/codec`.
- Declare a code or a type here: alias the core's codes and the service
  package's types, which own them.
- Hand-edit `README.md` — regenerate with `make docs-readme`.

## Verification

```sh
bazel test --config=race //pkg/v1/data/codec/ndjson:ndjson_test
cd pkg && GOWORK=off go test -race ./v1/data/codec/ndjson/
```

`TestItLinksNoModuleOutsideTheSDK` skips under Bazel, which records no module
information in a binary, and `TestGoListDepsNamesNoOtherCodec` without a go
tool on PATH; `TestItRegistersItsFormatAlone` holds under both.

## Reference

- ADR 0134, ADR 0156 §1, ADR 0160; `internal/service/data/codec/ndjson/CLAUDE.md`
