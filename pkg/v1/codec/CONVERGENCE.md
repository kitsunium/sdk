# Codec performance convergence

Closure record for the `codec-perf-micro-pprof` loop (ADR 0003 codecs).
The methodology (`docs/PROFILING.md`) iterates pprof → lever → benchstat
until no `p<0.05` win remains. This documents the converged state.

## Verdict: converged — 0 further cycles warranted

The two reducible hot-path wins were shipped; every other audited lever was
**verified** to be a non-win or library-bound, not deferred. No `p<0.05`
improvement remains identified, so the loop's 3-cycle cap was not needed.

## Wins shipped (benchstat-verified)

| Codec | Lever | Result | Commit |
|---|---|---|---|
| baseenc | `marshalJSONPooled` returns the pooled buffer instead of a capturing release-closure | Marshal/{6 variants}/small **36 → 35 allocs/op**, −2.78% (p=0.000, n=10); no ns regression | `648a6ad` |
| ndjson | `decodeLines` decodes in place into a pre-sized slice (drops per-record `reflect.New` + `reflect.Append`) | **−1 alloc per record** (scales with record count); ns `~` (interleaved n=8) | `13274e5` |

## Verified non-wins (do NOT re-attempt — see each codec's CLAUDE.md)

| Lever | Verdict |
|---|---|
| tlv `encodeInt/encodeUint` inline-split | escape annotation is **benign** — the appends write into a pre-sized pooled scratch, so 0 runtime alloc (Marshal/scalar = 3 allocs, none from these fns) |
| baseenc `encodeBytes` → `AppendEncode` | **deliberate** alloc — `Marshal` returns a fresh `[]byte`, there is no caller `dst` to append into |
| csv Append-into-dst (promotion shape) | **unmeasurable** — the bench fixtures produce no promotion-shape Append rows |
| tlv `decodeString` `unsafe.String` | **unsafe** — decoded values outlive the `Unmarshal` call while the caller may reuse `data` (use-after-free); the copy stays |

## Library-bound (no reachable lever — CLAUDE.md rationale each)

asn1, cbor, json, msgpack, toml, xml, yaml: the reflect walk lives inside
the stdlib / third-party library and is unreachable from this layer. The
only lever was buffer pooling, now mutualised.

## Structural wins (shipped earlier this initiative)

- **Pool mutualization** (`1b67ad6` + follow-ups): all 10 `*bytes.Buffer`
  pools + 2 `*bytes.Reader` pools collapsed into one shared
  `internal/core/codec/scratch` (BufferPool + ReaderPool); net −170 lines.
- **Registry**: `atomic.Pointer[map]` replaced `sync.Map` (−29% Lookup).
- **Parallel benches**: Append/StreamEncode/StreamDecode `RunParallel`
  variants added (`51e57b1`); `make profile -cpu=1,2,4,8,16`.
- **Regression gate**: per-codec `AllocsPerRun` budgets on the race-off
  `alloc` Bazel lane — all 13 codecs (`f4faa56`, `abbbc1b`, `fc81688`).

## Re-opening the loop

If a future change regresses a budget, the `alloc` lane fails in CI. To
hunt new wins: `make profile WAVE=<slug>` → `make benchstat-diff
BEFORE=baseline AFTER=<slug>` and look for a fresh `p<0.05` cell, per
`docs/PROFILING.md`.
