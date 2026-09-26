<!-- updated: 2026-09-26T00:00:00Z -->
# pkg/v1/codec/toml/

## Purpose

Registers the TOML codec — and no other — with the SDK's codec registry when
imported, blank usually (ADR 0134). Everything that dispatches by format name
then reads and writes TOML: `config.FileSource` / `config.FSSource`,
`i18n.LoadFS`, `codec.Marshal` / `codec.Unmarshal`. It links the TOML
codec and github.com/pelletier/go-toml/v2, and nothing else — where `pkg/v1/codec` links every format
the SDK ships, the MongoDB driver included.

## Surface

| Symbol | Role |
|---|---|
| `Format` | the untyped constant `"toml"`: goes into `config.FSSource`'s string and `i18n.LoadFS`'s `codec.Format` without a conversion |

## Why-this-shape

- **The import is the mechanism.** The implementation registers itself as Go
  initialises it; this package imports it and nothing else. Importing it beside
  `pkg/v1/codec` is harmless: Go initialises a package once, so the format is
  registered once — `TestAFormatImportedTwiceIsRegisteredOnce` in
  `pkg/v1/codec` imports all four.
- **What it links is tested, not asserted.** The suite reads the registry
  (exactly one format), the modules the test binary was linked from, and
  `go list -deps` when a go tool is on PATH.

## Do NOT

- Import another codec here, or `pkg/v1/codec`.
- Hand-edit `README.md` — regenerate with `make docs-readme`.

## Verification

```
bazel test --config=race //pkg/v1/codec/toml:toml_test
cd pkg && GOWORK=off go test -race ./v1/codec/toml/
```

`TestItLinksNoOtherFormatsLibrary` skips under Bazel, which records no module
information in a binary, and `TestGoListDepsNamesNoOtherCodec` without a go
tool on PATH; `TestItRegistersItsFormatAlone` holds under both.

## Reference

- ADR 0134; `pkg/v1/codec/CLAUDE.md`
