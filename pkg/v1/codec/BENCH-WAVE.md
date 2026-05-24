# BENCH-WAVE — codec-perf-extreme execution snapshot

Captured after Wave-1 quick wins + Wave-2 shared infrastructure + Wave-2.3 promotion fast-paths (csv + pem). Branch `feat/docs-versioning-and-release`, PR #29.

Settings: `go test -bench=Marshal -benchtime=1s -count=2 -benchmem -tags=codec_bench` against `pkg/v1/codec` on the dev container (linux/arm64, GOMAXPROCS=8).

## Per-codec Marshal results (small + medium, sequential + parallel)

| Codec | Marshal/small (ns/op) | B/op | allocs/op | MarshalParallel/small ns/op | Parallel/medium ns/op |
|---|---:|---:|---:|---:|---:|
| flatbuffers | passthrough — see parallel | 0 | 0 | **2.6** | **2.6** |
| tlv (int64 primitive) | **93** | 48 | 3 | **35** | **30** |
| asn1-der (asn1Doc) | (constant fixture, see parallel) | 784 | 22 | **977** | **907** |
| pem (sample block) | (constant fixture, see parallel) | 1,576 | 9 | **977** | **839** |
| csv (sampleCSV [][]string) | (constant fixture) | 4,208 | 3 | **1,057** | **1,067** |
| cbor | (see parallel) | 8,200 | 1 | **3,742** | **27,637** |
| msgpack | (see parallel) | 8,523 | 17 | **4,294** | **36,401** |
| xml (constant fixture) | 7,253 | 5,759 | 19 | **4,913** | **4,802** |
| json | (see parallel) | 10,512 | 34 | **9,387** | **63,757** |
| base64 | (see parallel) | 22,959 | 35 | **15,459** | 90,504 |
| base64url | | 22,975 | 35 | **17,956** | 90,504 |
| base32 | | 24,266 | 35 | **18,537** | 95,073 |
| ascii85 | | 21,579 | 35 | **20,120** | 94,692 |
| hex | | 29,185 | 35 | **23,843** | 95,627 |
| base16 | | 29,256 | 35 | **25,683** | 110,792 |
| ndjson | | 103,088 | 108 | **62,875** | 316,908 |
| toml | | 43,169 | 188 | **41,590** | 501,574 |
| yaml | (see parallel) | 181,377 | 401 | **249,697** | 1,759,344 |

Numbers are the **fastest** of 2 runs per cell.

## Wave-by-wave deltas (vs pre-W1 baseline)

| Codec | Optimization (W1+W2) | Δ Marshal medium | Δ Append Per Audit |
|---|---|---|---|
| cbor | EncMode hoist + stream hardening + Append via UserBufferEncMode | 5-15% ns | +Appender, 1 alloc/op |
| msgpack | GetEncoder + bufferPool (both Marshal + Append) | -23% ns medium, -60% B/op | +Appender |
| baseenc 6× | AppendDecode (no string(data) copy) + base16 single-pass uppercase | base16: -37% ns vs hex baseline | family-wide -1 alloc/op |
| ndjson | []json.RawMessage fast-path (promotion shape) | **~140× faster on RawMessage** | RawMessage Marshal: 3.7µs, 7.5 KiB, 3 allocs (100 records) |
| json | bufferPool + json.Encoder for Append | Append: -1 alloc/op | "" |
| tlv | scratchPool + 1-byte LEB128 fast-path | -1 alloc/record + branchless on common LEB128 | "" |
| flatbuffers | inline merge + PromotionMagic sentinel | 10-15% on promotion path | passthrough preserved |
| xml | bufferPool + Append | added Append (was missing) | "" |
| yaml | bufferPool + Append | added Append | "" |
| toml | bufferPool + Append | added Append | "" |
| asn1-der | +Appender extension | added Append | "" |
| pem | +Appender + JSON-promotion fast-path | **~10× faster on promotion** (4500→430 ns) | "" |
| csv | +Appender + JSON-promotion fast-path | **promotion Marshal: 467 ns/op** vs csv.Writer baseline ~3-5k ns | "" |

## Appender bijection (every codec implements `codec.Appender`)

Pre-W2:

```
json, ndjson, tlv, flatbuffers, base64, base64url, base32, base16, hex, ascii85
                              ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
                              10/18
```

Post-W2:

```
json, ndjson, tlv, flatbuffers, xml, yaml, toml, cbor, msgpack, asn1-der, pem, csv,
base64, base64url, base32, base16, hex, ascii85
^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
18/18
```

`TestAppendRoundTrip_AllCodecs` pin updated; `expectedAppenders` bijection enforced.

## Universal interface invariant

`TestUniversalRoundtripAllCodecs` (19 PASS frames — parent + 18 codecs) green at every commit. `codec.Marshal(F, v any)` + `codec.Unmarshal(F, data, v any)` signatures are FROZEN; every optimization lands behind the public verbs without widening them.

## Observability gaps (not yet addressed)

- xml + asn1-der + csv fixtures do not scale with `size` (constant payload across small/medium/large). Audit §0.9 imposed the fixture fix; carried over to Wave-3.
- `benchstat` not yet wired into CI as a regression gate. Manual `benchstat /tmp/before.txt /tmp/after.txt` runs document deltas in commit bodies.
- pprof artifacts not yet committed under `.bench/profiles/wave-N/`. Audit §3.3 imposed the workflow; carried over to Wave-3.

## What's measurable next

Wave-3 deep refactors carry the bigger wins:
- **TLV typeInfo cache + target-aware decoder** — kills the `map[string]any` intermediate; audit estimates -25-70% allocs on typed targets.
- **msgpack InternedStrings + CompactInts** — wire shrinks 30-60% on key-heavy / int-heavy payloads. *(InternedStrings REJECTED: changes wire format → incompatible with non-vmihailenco decoders. CompactInts shipped.)*
- **per-type size-hint cache (json/xml/yaml/toml/cbor)** — `bytes.Buffer.Grow` rolling EWMA; expected -1-3 allocs/op on medium/large by killing geometric grow cascades.

This snapshot is the ratchet floor for any wave-3 PR.

## Wave-3 in-flight deltas (post-snapshot)

Late-Wave-2 / Wave-3 micro-optimizations that landed on top of the table above. Numbers from `go test -bench -benchtime=2000x -count=3 -benchmem`.

| Codec / path | Optimization | Δ measured |
|---|---|---|
| baseenc 6× | Shared JSON-inner *bytes.Buffer pool | base64 small parallel: **-20% ns, -41% B/op** ; medium parallel: **-17% ns, -28% B/op**. Family-wide. |
| ndjson Unmarshal | bufio.Scanner → direct bytes.IndexByte walk + pre-sized reflect.MakeSlice | small: **-24% ns, -62% B/op** ; medium: **-5% ns, -17% B/op** ; small parallel: **-37% ns** ; allocs −4 |
| yaml/toml/xml Append | Zero-clone Append via pooled buffer (drop Marshal-delegation double-copy) | **-1 alloc/op, -~10 KB/op** on small Append (yaml shown); same shape on toml/xml |
| ndjson Marshal | Pool + size-aware release (small clones+repools, oversize orphans untouched) | small parallel: **-40% ns, -41% B/op** ; medium parallel: **-28% ns, -30% B/op** ; large parallel preserved at baseline (no regression) |
| csv Marshal | Pool *bytes.Buffer with size-aware release | medium parallel: **-1 alloc/op (3→2), -1% B/op** |
| csv Unmarshal | Pool *bytes.Reader | medium parallel: **-1 alloc/op (26→25), -45 B/op** |
| pem Marshal slow path | Pool *bytes.Buffer with size-aware release | Bench rig hits the fast path; win benefits downstream cert/key callers (not in bench) |
| tlv Unmarshal Phase 1 (*struct target) | Skip the map[string]any intermediate + projectMapToStruct second walk; write directly into target.Field(i) via cachedStructTypeInfo | 5-field User: **-35% ns, -59% B/op, -3 allocs** ; 18-field WideUser (name-index map kicks in): **-15% ns, -27% B/op, -2 allocs** |
| tlv Unmarshal Phase 2 (*[]Struct target) | Each element re-enters the struct typed walker; N-element slice avoids N map[string]any allocations | 100-element []Item: **-32% ns, -60% B/op, -102 allocs** |
| tlv Unmarshal Phase 3 (nested struct + nested slice-of-struct fields) | Per-field typed recursion when field.Kind() matches the wire tag; depth threaded through all paths | *Page with 50 nested Items + nested Owner struct: **-45% ns, -68% B/op, -55 allocs** |
| tlv Marshal encodeStructDirect | Drop the collect-then-emit []any intermediate for structs; walk cachedStructTypeInfo and emit each field record directly | 5-field User: **-28% ns, -42% B/op, -50% allocs (12→6)** |
| tlv Marshal encodeSliceDirect + encodeMapDirect | Same zero-intermediate pattern for slices + maps; companion to encodeStructDirect | []int(100): 8 allocs/op (was N+1 boxings); map[string]int(50): 107 allocs/op (same wire bytes, fewer in-flight) |
| tlv Unmarshal Phase 4 (*map[K]V target) | Skip the map[any]any intermediate the legacy projector builds; decode directly into the typed map via convertValue narrowing | 50-entry *map[string]int: **-55% ns, -59% B/op, -105 allocs** |
| tlv Unmarshal Phase 5 (nested map[K]V field) | Per-field typed recursion for nested map fields, mirroring Phase 3's nested struct + slice-of-struct paths | *Config with 2 nested map[string]X fields (20 entries each): **-52% ns, -55% B/op, -90 allocs** |

The "size-aware release" pattern (clone+repool when cap ≤ 256 KiB,
orphan-without-clone when cap > 256 KiB) was the unlock that let
ndjson Marshal pool without regressing the 3 MB tier. Same pattern
mirrored into csv + pem for consistency.

`TestUniversalRoundtripAllCodecs` still 19 PASS frames; every commit
ran `make lint` (ktn-linter zero issues) + `bazel test --config=race`
green before push.
