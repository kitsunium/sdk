<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/data/codec/yaml/

## Purpose

Registers the YAML codec — and no other — with the SDK's codec registry when
imported, blank usually (ADR 0134), and names its codes — the refusals by
name among them. Everything that dispatches by format name
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
| `CodeMarshalFailed` … `CodeLeadingZeroRefused` and the sentinels of the same names (`MultipleDocumentsRefused` for `CodeMultiDocRefused`) | the eleven `0.3.4.*` codes — the two failures and the nine refusals by name — aliased from `internal/core/data/codec/yaml`, where they are declared (ADR 0160, ADR 0074) |

## Why-this-shape

- **The import is the mechanism.** The implementation registers itself as Go
  initialises it; this package imports it and nothing else. Importing it beside
  `pkg/v1/data/codec` is harmless: Go initialises a package once, so the format is
  registered once — `TestAFormatImportedTwiceIsRegisteredOnce` in
  `pkg/v1/data/codec` imports every per-format package beside the umbrella,
  which is itself built from them.
- **What it links is tested, not asserted.** The suite reads the registry
  (exactly one format), the modules the test binary was linked from — none
  outside the SDK, so no YAML library at all (ADR 0156 §1) — and
  `go list -deps` when a go tool is on PATH: no other codec, nothing outside
  the SDK and the standard library.

## Do NOT

- Import another codec here, or `pkg/v1/data/codec`.
- Hand-edit `README.md` — regenerate with `make docs-readme`.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/data/codec/yaml.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: `doc.go` holds the package comment, which kit writes from the design (ADR 0167), and the hand-written files the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```
bazel test --config=race //pkg/v1/data/codec/yaml:yaml_test
cd pkg && GOWORK=off go test -race ./v1/data/codec/yaml/
```

`TestItLinksNoModuleOutsideTheSDK` skips under Bazel, which records no module
information in a binary, and `TestGoListDepsNamesNoOtherCodec` without a go
tool on PATH; `TestItRegistersItsFormatAlone` holds under both.

## Reference

- ADR 0134; `pkg/v1/data/codec/CLAUDE.md`
