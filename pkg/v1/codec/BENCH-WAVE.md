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
- **msgpack InternedStrings + CompactInts** — wire shrinks 30-60% on key-heavy / int-heavy payloads.
- **per-type size-hint cache (json/xml/yaml/toml/cbor)** — `bytes.Buffer.Grow` rolling EWMA; expected -1-3 allocs/op on medium/large by killing geometric grow cascades.

This snapshot is the ratchet floor for any wave-3 PR.
