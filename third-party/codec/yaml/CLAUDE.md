<!-- updated: 2026-10-03T00:00:00Z -->
# third-party/codec/yaml/

## Purpose

The **full** YAML codec — `gopkg.in/yaml.v3` behind the universal
`core/data/codec.Codec` dispatch, registered under the Format **`"yaml-full"`**. It
reads everything yaml.v3 reads: anchors and aliases, tags, merge keys, complex
keys, multi-document streams, YAML 1.1 booleans and octals.

The SDK's own `"yaml"` Format is `internal/service/data/codec/yaml`: a native,
standard-library-only reader of a **named subset** of YAML 1.2.2 sized for
configuration, which refuses each of those constructs by name. This package is
the way out for the program that genuinely needs one of them. It is a Go
module of its own under `third-party/` (ADR 0157), so the public module stays
standard-library-only (ADR 0156: `pkg` links no `gopkg.in/yaml.v3`) and only a
consumer that names this module requires yaml.v3. **Opt-in**:
blank-import this package to register `"yaml-full"`; `pkg/v1/data/codec` and
`pkg/v1/data/codec/yaml` do NOT pull it.

## Surface

| Aspect | Value |
|---|---|
| Format name | `"yaml-full"` (`Format`) |
| MIME types | **none** |
| Extensions | **none** |
| Streaming | yes (`NewEncoder`, `NewDecoder`) — `---`-separated documents |
| Appender | yes (`Append(dst, v)`) — encodes into a pooled buffer, then appends |
| Code range | `0.3.77.*` |

## Why-this-shape

- **It claims no MIME type and no extension.** The registry panics when two
  codecs claim one alias, and `.yaml`, `.yml` and `application/yaml` belong to
  the native codec. Claiming none means importing this package never changes
  what an extension or a MIME lookup returns — `config.FileSource` reading
  `config.yaml` by extension keeps meaning the subset — and the full reader is
  reached only by naming `"yaml-full"`, which is a decision a reader of the call
  site can see.
- **A distinct Format, not a replacement.** Both can be linked into one
  program; a caller chooses per call.
- **Byte cap on `Unmarshal`**: `maxYAMLBytes = 10 << 20` (10 MiB). yaml.v3 caps
  alias expansion internally but has no byte budget (CWE-400 / CWE-776). The
  streaming decoder has no cap: callers size their own `io.LimitReader`.
- **The library error stays the cause.** Its text carries yaml.v3's line
  numbers; the wrap adds the code.

## Error codes (range `0.3.77.*`)

| Code | Var | Trigger |
|---|---|---|
| `0.3.77.1` | `MarshalFailed` (`YAML_FULL_MARSHAL_FAILED`) | yaml.v3 encode error, a `MarshalYAML` hook error, a failing stream writer |
| `0.3.77.2` | `UnmarshalFailed` (`YAML_FULL_UNMARSHAL_FAILED`) | yaml.v3 decode error, or input over `maxYAMLBytes` |

## The native subset's reference

This is the one module that links both readers, so it holds the differential
fuzzers that keep the native subset honest
(`differential_fuzz_external_test.go`):

| Fuzzer | Property |
|---|---|
| `FuzzNativeAgreesWithYAMLv3` | every document the native codec accepts, yaml.v3 accepts too and reads as the same value — bar the subset's three documented differences (a timestamp and a non-string key stay text; a YAML 1.1 number is text to the core schema) |
| `FuzzNativeOutputReadsTheSame` | everything the native encoder writes reads back as the same value in the native decoder AND in yaml.v3, strictly |

Every input either fuzzer has failed on is kept under
`testdata/fuzz/<Fuzzer>/`, so `go test` replays it on every run; each one
is a divergence that was fixed in the native codec: a `:` before a flow
indicator, a lone tab, a raw U+0085, the `\/` escape, a multi-line flow key, a
`...` closing nothing, a `\U` escape past Unicode, a second byte order mark,
an over-long flow key, and `+_0` written plain. A new failure is a bug in one
of the two readers: fix the native codec, keep the input.

## Cost

`BENCH.md` measures this codec on the same fixtures as the native one
(`internal/service/data/codec/yaml/BENCH.md`): the native subset decodes the same
documents several times faster with a fraction of the allocations, so choose
this package for what it reads, never for speed.

## Do NOT

- Claim `.yaml`, `.yml` or a YAML MIME type here: the registry would panic in
  every program importing both codecs.
- Add this package to `pkg/v1/data/codec`'s blank imports — it would put
  `gopkg.in/yaml.v3` back into the public module.
- Use `fmt.Errorf`/`errors.New`; wrap through `errs.Wrap`.

## Verification

```
bazel test --config=race //third-party/codec/yaml:yaml_test
bazel test --config=alloc //third-party/codec/yaml:yaml_test   # the !race budget test
cd third-party/codec/yaml && GOWORK=off go test -race ./...    # standalone: its own go.mod
cd third-party/codec/yaml && go test -run='^$' -fuzz='^FuzzNativeAgreesWithYAMLv3$' -fuzztime=60s .
cd third-party/codec/yaml && go test -run='^$' -fuzz='^FuzzNativeOutputReadsTheSame$' -fuzztime=60s .
```
