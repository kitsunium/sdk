<!-- updated: 2026-10-03T00:00:00Z -->
# third-party/codec/yaml/

## Purpose

The **full** YAML codec — `gopkg.in/yaml.v3` behind the universal
`core/codec.Codec` dispatch, registered under the Format **`"yaml-full"`**. It
reads everything yaml.v3 reads: anchors and aliases, tags, merge keys, complex
keys, multi-document streams, YAML 1.1 booleans and octals.

The SDK's own `"yaml"` Format is `internal/service/codec/yaml`: a native,
standard-library-only reader of a **named subset** of YAML 1.2.2 sized for
configuration, which refuses each of those constructs by name. This package is
the way out for the program that genuinely needs one of them. It lives under
`third-party/` (root module) so the public module stays standard-library-only
(D6 of the tree reorganisation: `pkg` links no `gopkg.in/yaml.v3`). **Opt-in**:
blank-import this package to register `"yaml-full"`; `pkg/v1/codec` and
`pkg/v1/codec/yaml` do NOT pull it.

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

## Cost

`BENCH.md` measures this codec on the same fixtures as the native one
(`internal/service/codec/yaml/BENCH.md`): the native subset decodes the same
documents several times faster with a fraction of the allocations, so choose
this package for what it reads, never for speed.

## Do NOT

- Claim `.yaml`, `.yml` or a YAML MIME type here: the registry would panic in
  every program importing both codecs.
- Add this package to `pkg/v1/codec`'s blank imports — it would put
  `gopkg.in/yaml.v3` back into the public module.
- Use `fmt.Errorf`/`errors.New`; wrap through `errs.Wrap`.

## Verification

```
bazel test --config=race //third-party/codec/yaml:yaml_test
bazel test --config=alloc //third-party/codec/yaml:yaml_test   # the !race budget test
go test -race ./third-party/codec/yaml/
```
