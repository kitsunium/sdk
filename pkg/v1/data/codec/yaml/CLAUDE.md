<!-- updated: 2026-10-03T00:00:00Z -->
# pkg/v1/data/codec/yaml/

## Purpose

Registers the YAML codec — and no other — with the SDK's codec registry when
imported, blank usually (ADR 0134). Everything that dispatches by format name
then reads and writes YAML: `config.FileSource` / `config.FSSource`,
`i18n.LoadFS`, `codec.Marshal` / `codec.Unmarshal`. It links the YAML codec
and nothing else — the codec is native, a named subset of YAML 1.2.2 on the
standard library alone (`internal/service/data/codec/yaml`) — where `pkg/v1/data/codec`
links every format the SDK ships.

The subset and its refusals by name are documented on the package comment
(rendered into `README.md`) and in `internal/service/data/codec/yaml/CLAUDE.md`.
The full yaml.v3 reader is `third-party/codec/yaml`, the opt-in `"yaml-full"`
Format, which claims no extension.

## Surface

| Symbol | Role |
|---|---|
| `Format` | the untyped constant `"yaml"`: goes into `config.FSSource`'s string and `i18n.LoadFS`'s `codec.Format` without a conversion |

## Why-this-shape

- **The import is the mechanism.** The implementation registers itself as Go
  initialises it; this package imports it and nothing else. Importing it beside
  `pkg/v1/data/codec` is harmless: Go initialises a package once, so the format is
  registered once — `TestAFormatImportedTwiceIsRegisteredOnce` in
  `pkg/v1/data/codec` imports all four.
- **What it links is tested, not asserted.** The suite reads the registry
  (exactly one format), the modules the test binary was linked from — no other
  format's library, and no YAML library at all — and `go list -deps` when a go
  tool is on PATH.

## Do NOT

- Import another codec here, or `pkg/v1/data/codec`.
- Hand-edit `README.md` — regenerate with `make docs-readme`.

## Verification

```
bazel test --config=race //pkg/v1/data/codec/yaml:yaml_test
cd pkg && GOWORK=off go test -race ./v1/data/codec/yaml/
```

`TestItLinksNoOtherFormatsLibrary` skips under Bazel, which records no module
information in a binary, and `TestGoListDepsNamesNoOtherCodec` without a go
tool on PATH; `TestItRegistersItsFormatAlone` holds under both.

## Reference

- ADR 0134; `pkg/v1/data/codec/CLAUDE.md`
