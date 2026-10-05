# pkg/v1/data/codec/bson/

## Purpose

BSON, implemented with the standard library alone, as one package: importing it
registers the BSON codec — and no other — with the SDK's codec registry (ADR
0134), and it is also the public home of the Go types the BSON values Go has no
type of its own for decode into. Where the other per-format packages only
register, this one also encodes and decodes directly (`Marshal`, `Append`,
`Unmarshal`), because a program speaking BSON needs its value types and its
verbs in one import, without the full registry.

## Surface

| Symbol | Role |
|---|---|
| `Format` | the untyped constant `"bson"`, for `codec.Marshal`, `config.FSSource`, `i18n.LoadFS` |
| `Marshal`, `Append`, `Unmarshal` | the registered codec's own encode and decode |
| `D`, `E`, `M`, `A` | documents (ordered, element, unordered) and arrays |
| `ObjectID`, `NilObjectID`, `ObjectIDFromHex` | the twelve-byte identifier |
| `DateTime`, `NewDateTimeFromTime` | milliseconds since the epoch, UTC |
| `Decimal128`, `NewDecimal128`, `ParseDecimal128` | the 128-bit decimal, exact or refused |
| `Binary` + `Binary*` subtypes, `Regex`, `Timestamp`, `DBPointer`, `JavaScript`, `Symbol`, `CodeWithScope`, `MinKey`, `MaxKey`, `Undefined`, `Null` | the remaining BSON types |
| `CodeMarshalFailed` … `CodeValueInvalid`, `MarshalFailed` … `ValueInvalid` | the `0.3.36.*` codes and sentinels |

Every type is an alias of `internal/service/data/codec/bson`, which owns them (ADR
0074); every function forwards. The codes and sentinels alias
`internal/core/data/codec/bson`, where they are declared (ADR 0160). The
mapping, the hardening and the differences from the MongoDB driver are
documented in the service package.

## Why-this-shape

- **The driver's names, the SDK's types.** `bson.ObjectID`, `bson.D`,
  `bson.Binary` are the names a MongoDB-shaped program already speaks — the
  driver's own v2 put them in a package named `bson` too — so moving a type
  switch over decoded values off the driver is an import-path change.
- **Verbs here, not only `Format`.** `pkg/v1/data/codec/json`, `yaml` and `toml`
  serve programs that read configuration through the registry; BSON is read
  and written by programs that hold its values, and asking them to import the
  full registry for `codec.Marshal` would undo ADR 0134.
- **What it links is tested, not asserted.** The suite reads the registry
  (exactly one format), the modules the test binary was linked from (none
  outside the SDK), and `go list -deps` when a go tool is on PATH.

## Do NOT

- Import another codec here, or `pkg/v1/data/codec`.
- Declare a type here: alias the service package's, which owns it — and a
  code or a sentinel from the core's, which declares it.
- Hand-edit `README.md` — regenerate with `make docs-readme`.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/data/codec/bson.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: `doc.go` holds the package comment, which kit writes from the design (ADR 0167), and the hand-written files the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```
bazel test --config=race //pkg/v1/data/codec/bson:bson_test
cd pkg && GOWORK=off go test -race ./v1/data/codec/bson/
```

`TestItLinksNoModuleOutsideTheSDK` skips under Bazel, which records no module
information in a binary, and `TestGoListDepsNamesNoOtherCodec` without a go
tool on PATH; `TestItRegistersItsFormatAlone` holds under both.

## Reference

- ADR 0021, ADR 0134; `internal/service/data/codec/bson/CLAUDE.md`
