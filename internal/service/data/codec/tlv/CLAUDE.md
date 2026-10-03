<!-- updated: 2026-10-03T01:08:46Z -->
# internal/service/data/codec/tlv/

## Purpose

Self-describing Type-Length-Value codec. Reflection-driven encoder writes
arbitrary Go values; decoder reconstructs them as Go-native types
(`int64`, `uint64`, `float64`, `string`, `[]byte`, `[]any`, `map[any]any`,
`map[string]any`). Wire layout is `tag(1B) length(varint) value(...)`.

## Surface

| Item | Value |
|---|---|
| `Name()`         | `"tlv"` |
| `MIMETypes()`    | `application/x-tlv`, `application/vnd.tlv` |
| `Extensions()`   | `.tlv` |
| Constructor      | `New() codec.Codec` |
| Streaming        | yes (`NewEncoder`, `NewDecoder` — one record per Encode/Decode) |
| Appender         | yes (`Append(dst, v) ([]byte, error)`) — drops Marshal's fresh-slice alloc. Against a genuinely pre-sized `dst` it is **0 allocs/op** (`BENCH.md` §2); the "1 alloc/op" quoted in `pkg/v1/data/codec/BENCH.md` is that harness's recycled buffer occasionally growing — the fewest any measured codec shows there, tied with `cbor` and `flatbuffers` |
| `Tag`            | `uint8` — the 1-byte discriminator that opens every record; the tag values themselves are unexported |

## Error codes (range `0.3.22.*`)

| Code         | Var                | Trigger |
|---|---|---|
| `0.3.22.1`   | `MarshalFailed`    | encode-side failure (writer error, oversize field name) |
| `0.3.22.2`   | `UnmarshalFailed`  | malformed record, type mismatch, decoder shape error |
| `0.3.22.3`   | `UnsupportedType`  | chan / func / complex* / unsafe.Pointer source |
| `0.3.22.4`   | `DepthExceeded`    | nesting depth > `maxTLVDepth` (32) — CWE-674 |
| `0.3.22.5`   | `SizeExceeded`     | Unmarshal input > `maxTLVBytes` (10 MiB) — CWE-400 |
| `0.3.22.6`   | `Truncated`        | buffer ends mid-record |

## Conventions

- **Reflection-driven**, self-describing format. Tags are 1-byte
  discriminators grouped by family (`0x01` nil, `0x02-03` bool,
  `0x10-13` int*, `0x20-23` uint*, `0x30-31` float*, `0x40-41`
  string/bytes, `0x50` slice, `0x60` map, `0x70` struct).
- **Narrowest tag wins** on encode: an `int(42)` is emitted as `tagInt8`,
  not `tagInt64`. Decoding always upgrades to the widest Go type
  (`int64` / `uint64` / `float64`) and re-narrows via `reflect.Convert`
  when assigning back to a typed pointer.
- **The round trip converges; it is not the identity.** Decoded untyped, a
  struct record becomes `map[string]any`, which re-encodes as a map record,
  and a nil slice or map is the same bytes as an empty one — so
  `Unmarshal(Marshal(v))` need not equal `v`; what holds is that the pair
  reaches a fixed point after one round. `FuzzTLVUnmarshalAny` asserts that
  convergence.
- **Hardening caps**: input buffer ≤ 10 MiB (`maxTLVBytes`), nesting
  depth ≤ 32 (`maxTLVDepth`), struct field-name ≤ 255 bytes
  (`maxFieldNameBytes`), pre-allocation hint clamped to 4096
  (`sliceHintCap`). Mirrors the msgpack / NDJSON cap conventions.
- **`reflectView` wrapper**: a local named type (`type reflectView
  reflect.Value`) that lets package-internal helpers accept reflect
  values without the external concrete type tripping the linter's
  consumer-interface rule. Conversion (`reflect.Value(view)`) is
  zero-cost.
- **Appender contract**: `Append` snapshots `len(dst)` and rolls back to
  the prior length on a mid-encode failure, so callers never see a torn
  buffer.

## Performance (own-traversal; audit levers verified)

`BENCH.md` holds the package's own numbers — regenerate with
`cd internal/service && GOWORK=off go test -run='^$' -bench=. -benchmem ./data/codec/tlv/`.
Four facts a caller acts on: a scalar field costs **18.3 ns** to encode and a
**nesting level costs about seven fields** (~129 ns, of which ~92 ns is the
recursion itself), so flatten a payload rather than nest it; `Append` into a
sized buffer is **0 allocations** while `Marshal` pays 2-6 for the growth
cascade; and a **wire field name never becomes a Go string** on the typed
decode path — `parseFieldName` returns bytes aliasing the input and
`resolveFieldIndex` compares them against the per-type `nameBytes`. That last
one is load-bearing, not cosmetic: the string it replaced was 61.80 % of the
package's allocated objects and removing it took a 5-field struct decode from
15 allocations to 5. Do not "simplify" `parseFieldName` back into
`decodeFieldName` — and do not retain the bytes it returns, which alias the
caller's own input buffer.

**`cachedStructTypeInfo` is NOT a `singleflight` candidate — measured, `BENCH.md`
§4.** A cache miss (`buildStructTypeInfo`) costs 234.7 ns at one field, 896.1 ns
at five and 2 356 ns at sixteen, against a **2 007 ns** leading `singleflight.Do`
(`internal/kernel/concur/singleflight/BENCH.md`); it happens once per Go type per
process; and the hit path is 26.23 ns serial, 3.71 ns aggregate across 8 P, so
there is no contention to relieve either. Wrapping it would make the worst case
slower in wall time to save ~16 µs of CPU, once, for the life of the process.

The `-gcflags=-m=2` "escapes to heap" annotations on `encodeInt`/`encodeUint`'s
`append` calls are benign where the buffer already has capacity — `Append`
into a sized destination and the streaming `Encoder`'s pooled scratch, both
0 allocations (`BENCH.md` §1, §7). `Marshal` is the exception, and what it
pays is exactly those appends: it hands `Append` a nil destination, so a
scalar `int64` costs **2 allocations, 24 B** — `appendTagLen` allocating the
first 8 bytes and `AppendUint64` growing them to 16. (`TestAllocBudget` reads
3 and 48 B for the same call because its probe also boxes the result into an
`any` sink.) Splitting the two functions to fit the inline budget would remove
neither: inlining does not change whether an `append` onto a nil slice
allocates, and the result escapes to the caller either way. It stays churn
with no measurable win. A `decodeString` `unsafe.String` fast-path is also unsafe
here: decoded values outlive the `Unmarshal` call while the caller may
reuse the input `data`, so aliasing it risks a use-after-free — the
`string(rest[:length])` copy is correct and stays. The realised wins are
the cached `structTypeInfo`, inline-scalar dispatch, pre-computed name
prefixes, and typed root decode (Phases 6-8).

The encode scratch is `encodeScratch` (`encoder.go`), a
`recycler.CappedPool[*[]byte]` — the kernel primitive of ADR 0010, in the shape
`kernel/concur/buffer` has — and only the streaming `Encoder` rents from it: `Marshal`
hands `Append` a nil destination and `Append` writes into the caller's. It
resets a buffer to zero length on `Put`, not on `Get`, and orphans one a record
grew past `scratch.MaxRetainedBufBytes` instead (discard-before-reset), so an
oversized record pays for its own buffer and cannot pin it in the pool. The
ceiling is read from `internal/core/data/codec/scratch`, the codec domain's single
source for it, and never copied into a local constant. Two neighbours were
rejected on their shape: `scratch.AcquireBuffer` pools `*bytes.Buffer`, which
cannot adopt the raw `[]byte` TLV's varint encoder grows by `append` without
copying the record; `kernel/concur/buffer` pools the right type but with the logger's
thresholds — a 1 KiB first buffer and a 64 KiB ceiling — so records between 64
and 256 KiB would stop being recycled. Moving off the hand-rolled `sync.Pool`
it replaced changed no allocation count and costs **3.5 ns per rent and
return** — `CappedPool.Put` is not inlined and calls its cap check and its
reset through function values, the indirection every `CappedPool` consumer
pays (`BENCH.md` §7). Do not hand-roll a `sync.Pool` again to win those
nanoseconds back: it re-creates the duplicated cap-discard ADR 0010
consolidated.

## Verification

```
bazel test --config=race //internal/service/data/codec/tlv:tlv_test
# fuzz one target at a time (fuzz_external_test.go): FuzzTLVUnmarshalAny,
# FuzzTLVUnmarshalTyped, FuzzTLVDecodeStream
cd internal/service && GOWORK=off go test -run='^$' -fuzz='^FuzzTLVUnmarshalAny$' -fuzztime=30s ./data/codec/tlv/
```

The three fuzz targets assert convergence and a size bound on the untyped
decode, idempotence on a typed target, and — for the streaming decoder —
progress on every record and agreement with the buffered path. Their seeds, and
the corpus entry under `testdata/fuzz/`, run with the ordinary suite; the
`go_test` ships `testdata/**` as data.

The encode scratch pool has two gates, each shown to fail on the defect it
guards (`BENCH.md` §7). `TestAllocBudget/stream-encode` holds the streaming
`Encoder` at **0 allocations** — it reports 2 when `Encode` bypasses the pool —
and runs only on the race-off alloc lane (`make test-alloc`), like every
`codec_integration_test.go`. `Test_tlvEncoder_Encode` rents the pool back after
every case — a record that fails halfway and an over-ceiling record included —
and fails if a buffer comes out non-empty, narrower than `scratchInitialCap` or
wider than the retain ceiling: a reset that stopped truncating shows up as
`len=11` after the mid-record failure, and an unbounded pool as a 532 480-byte
buffer handed out again.
