<!-- updated: 2026-11-03T22:00:00Z -->
# internal/service/data/codec/asn1/

## Purpose

ASN.1 DER codec wrapping stdlib `encoding/asn1`. Emits DER exclusively (the stdlib produces nothing else). `Unmarshal` reads DER plus the few BER relaxations `encoding/asn1` reads — not BER in general (#904, below).

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"asn1-der"` |
| `MIMETypes()`    | `application/pkix-cert` |
| `Extensions()`   | `.der`, `.cer` |
| Constructor      | `New() codec.Codec` (returns the registered singleton) · `NewStrict() codec.Codec` (fresh, unregistered: refuses trailing bytes — #904) |
| Streaming        | not implemented |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) — delegates to Marshal (encoding/asn1 has no append API) then appends onto dst |

## Error codes (range `0.3.9.*`)

Declared in `internal/core/data/codec/asn1` — `codes.go` and `errors.go` — and used
here as `coreasn1.<Var>` (ADR 0160 §2: a code lives in the core at the path that
mirrors the package emitting it). The values, reasons and texts are the ones this
package always emitted; only the declaration moved.

| Code         | Var               | Trigger |
|---|---|---|
| `0.3.9.1`    | `MarshalFailed`   | `encoding/asn1.Marshal` returned an error |
| `0.3.9.2`    | `UnmarshalFailed` | `encoding/asn1.Unmarshal` returned an error |
| `0.3.9.3`    | `TrailingData`    | `NewStrict`'s instance found bytes past the first structure — field `trailing` counts them |

Reasons `MARSHAL_FAILED` / `UNMARSHAL_FAILED` / `TRAILING_DATA`.

## Conventions

- Stateless singleton; `New()` returns `Codec`.
- `Unmarshal` accepts trailing bytes past the first decoded structure (stdlib contract), so a signed blob with bytes appended decodes equal to the original — the signature-malleability lever for anything that hashes what it decoded rather than what it received. `NewStrict()` (#904) returns an unregistered instance — the `csv.NewWithEscape` shape — whose `Unmarshal` refuses a non-empty rest with `TRAILING_DATA`; `Marshal` and `Append` are the registered codec's. The registered Format keeps the lenient contract. On that refusal the target holds what `encoding/asn1` decoded. Re-exported by `pkg/v1/data/codec/asn1`. Adopting it in the crypto packages that parse DER they sign or verify is theirs to do: none of them goes through this codec today.
- **Which BER `encoding/asn1` reads** (pinned by `TestUnmarshalReadsTheBERFormsTheDocNames`, both instances alike). Accepted: a UTCTime without seconds or with a `+hhmm` offset, a DEFAULT value written out, a SET OF in any order, a PrintableString with `*` or `&`. Refused with `UNMARSHAL_FAILED`: an indefinite length, a length or tag number not in its shortest form, a non-minimal INTEGER, a BOOLEAN other than `0x00`/`0xFF`, a constructed (segmented) string.

## Performance (lib-bound)

`encoding/asn1` drives Marshal/Unmarshal entirely through reflection
internal to the stdlib and exposes no append-style or buffer-reuse API, so
there is no reachable reflect-elimination or pooling lever at this layer —
`asn1.Marshal` allocates and returns a fresh `[]byte` we cannot intercept.
The codec is intentionally allocation-bound by its library; optimisation
would have to land upstream. (Verified during the codec-perf-micro-pprof
pass — do not re-attempt a local pool or reflect cache here.)

## Verification

```
bazel test --config=race //internal/service/data/codec/asn1:asn1_test
```

## Fuzz (#897)

`fuzz_external_test.go` — `FuzzUnmarshal`: any input decodes without a panic under `New` and `NewStrict`; a decoded record re-encodes as DER that reads back equal — the BER relaxations are normalised, never kept. The seed corpus runs with the package's tests; `scripts/ci/codec-fuzz.sh` (nightly, `codec-fuzz.yml`) explores it.
