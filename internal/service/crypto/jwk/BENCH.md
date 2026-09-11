<!-- generated from internal/service/crypto/jwk/jwk_bench_test.go — run `cd internal/service && GOWORK=off go test -run=NONE -bench=. -benchmem -benchtime=1s -count=3 ./crypto/jwk/` in THREE separate processes to refresh -->
# Benchmarks — `internal/service/crypto/jwk`

A JWK is a key's serialised form, and this package's job is to get keys into
and out of it. Nothing here runs on a hot path **by itself** — but one call
does, through a consumer: `internal/service/token`'s JWK Set verifier called
`KeyValue.ECDSAPublic()` on **every authenticated request**, to rebuild a key
that never changes.

So this report was commissioned for one question:

> **What fraction of a JWKS verification is spent re-deriving a key that is
> fixed for the verifier's lifetime — in TIME and in ALLOCATIONS?**

The short answers, before the evidence:

1. **A JWK Set verification spent 8.3 % of its CPU and 28.2 % of its allocated
   objects rebuilding one EC public key.** Not on the signature, not on the
   claims: on `x509.MarshalPKIXPublicKey` producing a DER blob that
   `x509.ParsePKIXPublicKey` re-parsed one line later.
2. **The O(n) `AllByKid` scan, the other hypothesis this report was asked to
   test, is REFUTED as a cost.** It is **one** allocation of 176 B and
   **10.0–10.1 ns per set member** — 0.09 % of a verification at n=1 and 0.54 %
   at n=64. The scan is real, its price is not.
3. **The cost is EC-only, by a factor of twelve to seventeen.** Rebuilding an
   EC key cost 50 allocations; the same operation for Ed25519 cost 3 and for a
   symmetric key 4. The other two families hand back a `bytes.Clone`.
4. **Three instruments priced the same work at 7 128 ns, 8 434 ns and
   12 487 ns**, and only the last one — the profile, taken inside the real
   path — agreed with the end-to-end delta. §7 is that reconciliation, and it
   is the most useful thing in this report.

The fix landed in `internal/service/token`, not here: `NewSetVerifier` now
derives every member's binding once at construction. **Nothing in this package
changed.** Its numbers are what made the case, and its `BenchmarkPKIXRoundTrip`
is what keeps the case checkable.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly. This is a
> memory-ballooned VM: 15 GiB nominal against a balloon the host may reclaim
> mid-run. The *shape* of every result — the ratios, the slopes, the flatness —
> is what travels, not the nanoseconds.

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor |
| CPU cores | 8 |
| RAM | 15 GiB (ballooned VM — see above) |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | `jaimerias-que-tu-te-connect` |
| Git commit | `f02e743` (pre-commit; `jwk_bench_test.go` and the token-side files in the working tree) |
| Generated (UTC) | 2026-09-11 |
| Machine load | `01:51:01 up 18 days, 13:34, load average: 0.43, 0.38, 0.63` at start; `02:14:04 … 1.10, 0.64, 0.73` at end (the tail is this run's own three processes). No other job on the box. |
| Bench wall-clock | `-benchtime=1s -count=3`, three processes; 27 jwk rows + 24 token rows × 2 arms × 9 samples ≈ 15 min, plus the GC experiment in §7 |

### Method

- **Every published number is the median of NINE samples across THREE separate
  processes** — `-count=3` run three times from a single pre-built binary in
  `/dev/shm`. A single-process `-count=3` was demonstrated on this box to report
  a precision it does not have. Three processes also re-randomise map
  iteration, ASLR and the heap's starting shape.
- **Spread is `(max−min)/median` over all nine.** It is under 4 % for 22 of the
  27 rows here and never above 4.8 %.
- **The before/after arms are two SEPARATE BINARIES**, built from the two
  versions of `internal/service/token/jwkbridge.go` and run alternately. §8
  records the control that proves they are genuinely different code: the
  single-key `BenchmarkVerify/ES256/realistic` row, which the change does not
  touch, measures 141 284 ns in one binary and 141 868 ns in the other —
  **0.41 % apart, inside its own 2.0–2.7 % spread** — while the JWKS row moves
  by 8.0 %.
- **pprof came first.** The CPU and allocation profiles in §1 are from separate
  runs of the same benchmark on the same binary; `-memprofilerate=1` is never
  mixed with a CPU profile.
- **Sinks are TYPED, one per shape.** A single `var sink any` boxes every
  result and charges the allocation to the function under test — an error that
  published "1 alloc" for a documented zero-allocation function earlier in this
  campaign.
- **`B/op` counts bytes ALLOCATED, not retained.**

## 1. The profiles, verbatim

Both are of `BenchmarkSetVerify/ES256` in `internal/service/token` — the
consumer, because that is where this package's cost is actually paid.

### 1.1 Allocation, BEFORE — `-memprofilerate=1`, `alloc_objects`, cumulative

```
Type: alloc_objects
Showing nodes accounting for 114054, 26.70% of 427126 total
      flat  flat%   sum%        cum   cum%
         0     0% 0.0021%     420975 98.56%  ...token.(*setVerifier).Verify
         0     0% 0.0021%     187088 43.80%  ...token.policyValue.decodeAndValidate
      2460  0.58%  0.58%     184628 43.23%  ...token.decodeClaims
      7380  1.73%  2.31%     130397 30.53%  ...token.checkNoDuplicateMembers
         0     0%  2.31%     120635 28.24%  ...token.bindECJWK
         0     0%  2.31%     120635 28.24%  ...token.bindJWK
     63922 14.97% 17.27%     108257 25.35%  encoding/json.(*Decoder).Token
         0     0% 17.27%      73857 17.29%  ...crypto/jwk.KeyValue.ECDSAPublic
      2460  0.58% 17.85%      64010 14.99%  ...token.policyValue.parseJWS
      2531  0.59% 18.44%      63204 14.80%  crypto/x509.MarshalPKIXPublicKey
         0     0% 19.02%      50549 11.83%  encoding/asn1.Marshal (inline)
      4991  1.17% 20.18%      50549 11.83%  encoding/asn1.MarshalWithParams
```

and the halves below the top eighteen, extracted by name:

```
      2531  0.59% 37.18%      27841  6.52%  crypto/x509.ParsePKIXPublicKey
         0     0% 47.60%      19744  4.62%  crypto/ecdsa.(*PublicKey).ECDH
      7386  1.73% 63.94%      12310  2.88%  ...crypto/jwk.KeyValue.ecdsaPublicKey
      2462  0.58% 98.84%       2462  0.58%  ...crypto/jwk.Set.AllByKid (inline)
```

**That block is the whole report.** `bindJWK` is **28.24 %** of every
allocated object in a JWK Set verification, and `encoding/asn1.Marshal` alone —
an ASN.1 DER encoder, running per request, to produce bytes re-parsed on the
next line — is **11.83 %**. `AllByKid`, the other suspect, is **0.58 %**.

### 1.2 CPU, BEFORE — `-benchtime=5s`, cumulative

```
Duration: 7.62s, Total samples = 7.86s (103.09%)
      flat  flat%   sum%        cum   cum%
         0     0%     0%      7.67s 97.58%  ...token.(*setVerifier).Verify
         0     0%  0.25%      5.59s 71.12%  ...token.es256Verifying.verify
     0.01s  0.13% 50.25%      1.06s 13.49%  ...token.policyValue.decodeAndValidate
     0.03s  0.38% 51.40%      0.65s  8.27%  ...token.bindJWK
         0     0% 51.40%      0.62s  7.89%  ...token.bindECJWK
```

**8.27 % of CPU**, against a 71.12 % signature. `AllByKid` does not appear at
all: it is below the `cum <= 0.04s` drop threshold.

### 1.3 Both profiles, AFTER

`bindJWK` is **absent from the CPU profile entirely**. In the allocation
profile it survives only as the benchmark's own SETUP:

```
        88 0.021% 92.55%       3470  0.84%  ...token.indexByKid
       138 0.033% 92.58%       3378  0.82%  crypto/x509.MarshalPKIXPublicKey
         0     0% 92.58%       3377  0.82%  ...token.bindJWK
```

**28.24 % → 0.82 %, and the 0.82 % is reached through `indexByKid`** — the
constructor — rather than through `Verify`. The signature's share rose from
71.12 % to **77.66 %** and `decodeAndValidate`'s from 43.80 % to **61.35 %**,
which is the same thing said from the other end: what is left is the work that
was always the point.

## 2. The round trip, decomposed

`BenchmarkPKIXRoundTrip` splits the per-request derivation into the three calls
that make it up. Publishing the halves separately is what makes the total
attributable rather than one opaque figure.

| stage | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| 1 — `KeyValue.ECDSAPublic()` (2 × `big.Int` + `x509.MarshalPKIXPublicKey`) | **3 863** | 1 288 | **30** |
| 2 — `x509.ParsePKIXPublicKey` (the blob from stage 1) | **2 306** | 696 | **11** |
| 3 — `(*ecdsa.PublicKey).ECDH()` (the on-curve check `bindP256Public` runs) | **958.6** | 592 | **8** |
| **sum** | **7 127.6** | **2 576** | **49** |

**Stage 1 costs 1.68× stage 2.** Rendering DER is more expensive than parsing
it — `encoding/asn1.Marshal` walks a struct reflectively — so the round trip is
not two halves of one price, it is a 63/37 split with the *avoidable* direction
the more expensive one.

**Stage 3 is not overhead.** `ECDH()` is the stdlib's on-curve and
non-identity check, which RFC 8725 §3.4 asks for and which
`internal/service/token` runs deliberately. It is 958.6 ns of real validation,
and it moved to construction with the rest rather than being dropped.

## 3. The other two families, for contrast

| accessor | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `KeyValue.ECDSAPublic()` (EC) | **3 870** | 1 288 | **30** |
| `KeyValue.Ed25519Public()` (OKP) | **42.9** | 32 | 1 |
| `KeyValue.Secret()` (oct) | **44.5** | 32 | 1 |

**90× and 87×.** The OKP and oct accessors are a `bytes.Clone` and a length
check; the EC one is an ASN.1 encoder. That asymmetry is not a defect in this
package — PKIX is the shape `service/crypto/ecdsasig` consumes, and RFC 7518
§6.2.1 stores EC keys as bare coordinates, so *somebody* has to build the
SubjectPublicKeyInfo. The finding is that it was being built per request.

It shows up end to end exactly as this table predicts. Against a single-key
verification of the same algorithm, the JWK Set path used to cost:

| algorithm | extra ns | extra allocs |
|---|---:|---:|
| **ES256** | **+13 916** | **+55** |
| EdDSA | +1 870 | +8 |
| HS256 | +1 692 | +9 |

## 4. `AllByKid` — the refuted hypothesis, with its number

`BenchmarkAllByKid` sweeps set size against match POSITION. The position arm is
a **control**, not a slope: the scan has no early exit, so `first` and `last`
must agree at every size.

| n | match=first | match=last | first/last agreement | match=absent | B/op (matching) | allocs |
|---:|---:|---:|---:|---:|---:|---:|
| 1 | 127.9 | 128.3 | 0.31 % | 13.5 | 176 | 1 |
| 4 | 156.3 | 159.3 | 1.9 % | 43.0 | 176 | 1 |
| 16 | 276.8 | 281.0 | 1.5 % | 163.0 | 176 | 1 |
| 64 | 765.3 | 761.9 | 0.44 % | 642.6 | 176 | 1 |

**The control holds** — the two columns agree to 0.31–1.9 %, inside the 2.1–4.8 %
spread — so the scan really is position-independent, and the slope below is a
property of the set size alone.

**The slope is 10.12 ns per member** ((765.3 − 127.9)/63), and the same
regression over the `absent` column gives **9.99 ns** — a 1.3 % agreement
between two independent columns. That is a 176-byte `KeyValue` copied by the
`range` plus a string comparison, which is what ~10 ns buys.

**The intercept is the allocation.** matching − absent is 114.4 / 113.3 / 113.8
/ 122.7 ns across the four sizes: **flat in n**, because there is always exactly
one match and therefore exactly one `append` into a nil slice. `absent`
allocates **0 B**, which is the same fact from the other side.

### The verdict

Two denominators, both named, because mixing them is how a percentage lies. The
**single-key** ES256 verification is 141 284 ns / 134 allocs; the **JWK Set**
one, before the change, is 155 200 ns / 189 allocs.

| | of a single-key verification | of a JWK Set verification |
|---|---:|---:|
| `AllByKid` at n=1 | 127.9 ns = **0.091 %** · 1 alloc = **0.75 %** | **0.082 %** · **0.53 %** |
| `AllByKid` at n=64 | 765.3 ns = **0.54 %** · 1 alloc = **0.75 %** | **0.49 %** · **0.53 %** |
| the PKIX round trip | 12 487 ns = **8.84 %** · 50 allocs = **37.3 %** | **8.05 %** · **26.5 %** |

**H2 — "an O(n) scan with a per-call allocation, per verification" — is
literally true and materially wrong.** Every mechanical claim in it checks out:
the scan is linear, there is no map index, `KeyValue` is 176 bytes, and one
match is one 176 B allocation. It is also a third of a percent. Published with
its number, as a refuted hypothesis should be.

The end-to-end rows say the same thing with a blunter instrument: sweeping the
set from 1 to 64 keys moved a whole verification by **2 215 ns (1.4 %)** before
the change and **783 ns (0.55 %)** after — both smaller than the 0.6–3.1 %
run-to-run spread of the rows themselves. Only the micro-benchmark above can
see this scan at all, which is why it exists.

## 5. What the format itself costs

The per-refresh half of the picture. A JWKS consumer pays these once per key
rotation, not once per request.

| operation | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `Parse` EC | 2 857 | 688 | 9 |
| `Parse` OKP | 1 388 | 208 | 2 |
| `Parse` oct | 1 352 | 208 | 2 |
| `ParseSet` n=1 | 4 497 | 1 057 | 13 |
| `ParseSet` n=16 | 59 840 | 17 399 | 185 |
| `MarshalPublic` EC | 2 073 | 704 | 7 |
| `MarshalPublic` OKP | 1 514 | 528 | 5 |
| `Thumbprint` (RFC 7638) | 721.0 | 416 | 7 |
| `ByKid` (n=16, unambiguous) | 311.4 | 176 | 1 |

**`ParseSet` is linear and its constant is `Parse`.** (59 840 − 4 497)/15 =
**3 690 ns per additional member**, against 2 857 for `Parse` EC alone — the
833 ns difference being the envelope's `json.RawMessage` slicing and the
`append`. Allocations: (185 − 13)/15 = **11.5 per member** against `Parse` EC's
9. Both hold across a 16× range, so there is no hidden quadratic in the set
decoder.

**An EC key costs 2.06× an OKP one to parse** and 1.37× to marshal, for the
reason §3 gives: two coordinates and a point validation against one clone.

`ByKid` costs **30.4 ns more than the `AllByKid` it delegates to** at n=16 — the
`switch` over the match count, plus a call frame the profile shows `AllByKid`
itself does not pay (it is inlined into its callers). Small, and published so
nobody has to guess whether the refusal is free.

## 6. What the token domain bought — the consumer's rows

Reproduced here because this package's numbers are what argued for the change,
and a report that only shows the argument is half a report. The full table is
in `internal/service/token/BENCH.md` §16.

| row | before | after | change |
|---|---:|---:|---:|
| `SetVerify/ES256` ns | 155 200 | **142 713** | **−8.0 %** |
| `SetVerify/ES256` allocs | 189 | **139** | **−26.5 %** |
| `SetVerify/ES256` B | 10 649 | 7 892 | −25.9 % |
| `SetVerifyRotation` (2 candidates) ns | 278 182 | 255 596 | −8.1 % |
| `SetVerifyRotation` allocs | 260 | **160** | **−38.5 %** |
| `SetVerifierConstruction/n=1` ns | 130.9 | 8 752 | +8 621 |
| `SetVerifierConstruction/n=64` ns | 131.4 | 540 050 | +539 919 |

**The break-even is under one request for a one-key set and 44 requests for a
sixty-four-key one** (539 919 ÷ 12 487 ns saved per verification). A JWKS
refresh interval is measured in hours.

## 7. The reconciliation — three instruments, one operation, 1.84×

The component table in §2 sums to **7 127.6 ns**. The end-to-end delta is
**12 487 ns**. That is 75 % more than the components explain, and a table that
contradicts itself arithmetically is noise until the cause is found. It was
chased before anything here was published.

**Four instruments, in order of realism:**

| instrument | ns | share of the whole | allocs |
|---|---:|---:|---:|
| 1. sum of the three components (§2), each timed alone | 7 127.6 | 4.59 % | 49 |
| 2. one whole `bindJWK`, timed alone — `SetVerifierConstruction` slope, (540 050 − 8 752)/63 | 8 434 | 5.43 % | 50.1 |
| 3. `bindJWK` **in situ** — CPU profile, cumulative | **12 953** | **8.27 %** | — |
| 4. end-to-end delta, median of 9 before minus median of 9 after | **12 487** | **8.05 %** | 50 |

**Instruments 3 and 4 agree to 2.7 % as a SHARE of the operation** — 8.27 %
against 8.05 % — and that is the comparison that carries, because instrument 3
is a fraction the profiler measured on its own run while instrument 4 is a
difference of two medians, so only the dimensionless form is directly
comparable. Instrument 2 is 68 % of instrument 4 and instrument 1 is 57 %.

**GC pressure was the first hypothesis and it is REFUTED.** Removing 50
allocations and 2 758 B per operation is exactly the shape that shows up as
collector work charged to wall time, so the arms were re-run at `GOGC=off` and
`GOGC=400`:

| GOGC | before | after | delta |
|---|---:|---:|---:|
| 100 (default) | 155 165 | 142 115 | 13 050 |
| 400 | 152 720 | 140 112 | 12 608 |
| off | 152 610 | 139 445 | 13 165 |

**The delta is invariant** — 12.6 to 13.2 µs, a 4.4 % range, across a setting
spanning "collect often" to "never collect". These are their own nine-sample
medians, run after the main arms; the `GOGC=100` row reproduces the main run's
12 487 to 4.5 %. Whatever inflates the cost, it is not the garbage
collector.

**What is left is locality**, and the instruments are ordered by how much of it
they destroy. Timed alone, `bindJWK` runs against a hot L1 and a tiny live
heap. Timed *inside a verification*, it runs between a P-256 scalar
multiplication — which walks large precomputed point tables — and a JSON decode
that allocates eighty-one times. Same instructions, cold cache, **1.48×**.

**The allocation column does not inflate**: 49, 50.1, 50 across the three
instruments that can report it. An allocation is an allocation wherever it
runs. That asymmetry is the practical lesson, and it decided how the regression
is gated: `TestSetVerificationDoesNotRederiveTheKey` counts **allocations**, not
nanoseconds, because the allocation number is the one that reconciles.

## 8. The cross-checks

Six identities were computed before publication.

1. **The denominator reproduces.** `BenchmarkVerify/ES256/realistic` measures
   **141 284 ns / 7 795 B / 134 allocs** in this campaign against **142 029 ns /
   7 796 B / 134 allocs** published by an independent campaign on a different
   day — **0.52 % apart on time, IDENTICAL on allocations, one byte apart on
   B/op**. EdDSA and HS256 agree to 1.6 % and 0.4 %, likewise.
2. **The control row does not move.** That same single-key row measures
   141 284 ns in the *before* binary and 141 868 ns in the *after* one —
   **0.41 %**, well inside its own 2.0–2.7 % spread — while the JWKS row moves
   8.0 %. The change touches the set path and nothing else, and the numbers say
   so. This is the check that catches an arm accidentally running the same code
   in both halves.
3. **The primitives reproduce.** `ecdsa_p256_verify` measures 110 069 /
   110 406 ns in the two binaries against 109 891 published — 0.16 to 0.47 %.
4. **The surcharge is identical across all three algorithms after the change.**
   `SetVerify` minus `Verify` is **+5 allocations and +95 B** for ES256, EdDSA
   *and* HS256 — three independent key families landing on one number, which is
   what "the family-specific work has moved to construction" predicts. Before,
   the same three columns read +55, +8 and +9. It reproduced across two
   independent nine-sample campaigns, harness refactor in between.
5. **That residual +5 is the `kid` header, not the selection.** The two rows
   compare different tokens: a JWKS token carries a `kid` member the single-key
   one does not. `TestSetVerificationDoesNotRederiveTheKey` removes the
   confound by giving both verifiers the **same** token, and measures the delta
   at **exactly 0 allocations over 200 calls, three runs in a row** (16 200
   against 16 200). Selection is a map read over a slice the constructor
   already built, and it allocates nothing.
6. **The construction sweep is linear in n.** 54 / 204 / 809 / 3 213
   allocations at n = 1/4/16/64 is **50.1 per key** by regression, against the
   **50** the end-to-end delta removed and the **49** §2's components sum to.
   Time: 8 434 ns per key by regression, against 8 752 / 8 233 / 8 408 / 8 437
   computed pointwise.

## 9. Rows discarded, and their causes

**One row-set was discarded, and its cause is now a comment in the harness.**

The first draft of `BenchmarkAllByKid` keyed its sets `"k0"`…`"k63"` and
measured **match=last 43 % slower than match=first** at n=64 (644.7 ns against
451.3 ns). `AllByKid` has no early exit — it walks every member — so the scan
cannot produce that difference, and a control that disagrees with itself is a
broken instrument, not a finding.

The cause was the **kid's length, not the match's position**. Go compares
strings by length first, so looking up the two-octet `"k0"` fails on length
against `"k10"`…`"k63"` — fifty-four of the sixty-four members — and never
reaches a byte comparison, while looking up the three-octet `"k63"` reaches
`memcmp` for all but ten. The harness was measuring its own key-naming scheme.

Fixed-width kids (`benchKid`: `"key-00"`…`"key-63"`) removed it, and the two
columns now agree to 0.04–2.2 %. The same scheme is used on the token side, for
the same reason, and both call sites carry the note.

**One transient is recorded because it did NOT reproduce.** An exploratory arm
measured `BenchmarkPrimitive/ed25519_verify` at a **17.7 %** spread, which would
have been the widest row in either report. The final campaign measures the same
row at 2.4 % and 2.7 % in the two binaries, on identical code. It was the box,
not the benchmark, and it is written down rather than quietly dropped: a reader
who sees a wide spread on that row once should re-run before concluding
anything from it. In the published campaign no row exceeds **4.8 %**.

## 10. What was REFUSED, and the property it would have weakened

**Indexing `jwk.Set` by kid, inside this package.** A `map[string][]KeyValue`
field on `Set` would make `AllByKid` O(1) and remove the 176 B allocation. It
was refused on three grounds, in increasing order of importance:

- The measurement does not justify it. §4 prices the whole scan at 0.09–0.49 %
  of the operation that motivated this report.
- `Set` is an **immutable value**, copied freely — `NewSet`, `Keys`,
  `ParseSet` and every caller pass it by value. A map field is a reference: two
  copies of a `Set` would share one index, and the type's whole contract is
  that they cannot share anything.
- The consumer that needed the index needed it **grouped with something else**
  — the derived binding — and that thing does not belong in a key format at
  all. Putting the index here would have satisfied nobody: `token` would still
  have had to build its own structure to hold the bindings.

The index lives in `internal/service/token`'s `setVerifier`, built once at
construction, and this package is unchanged. That is also why the O(1) lookup
is a **side effect** of the fix and not its motivation — worth saying plainly,
because a reader who takes the map for a performance decision will read §4 and
wonder what was measured.

**Caching the derived key inside `KeyValue`.** A memoised `ECDSAPublic` would
have fixed the symptom without moving anything. It was refused because
`KeyValue` is an immutable value with no pointer identity: a cache field would
be copied with the struct and would either be recomputed per copy (no gain) or
require a pointer to shared mutable state inside a type whose redaction
guarantees rest on having none.

**Nothing cryptographic was touched.** `ECDH()`'s on-curve check (958.6 ns,
§2 stage 3) is the most expensive avoidable-looking call in the round trip and
it is RFC 8725 §3.4's requirement. It moved to construction with everything
else; it did not get cheaper and it did not get skipped.

## 11. Full results

Median of nine samples (three processes × `-count=3`), `-benchtime=1s`. Spread
is `(max−min)/median` across all nine.

| benchmark | ns/op (median of 9) | spread | B/op | allocs/op |
|---|---:|---:|---:|---:|
| `BenchmarkAllByKid/n=1/match=absent` | 13.5 | 2.4 % | 0 | 0 |
| `BenchmarkAllByKid/n=1/match=first` | 127.9 | 2.2 % | 176 | 1 |
| `BenchmarkAllByKid/n=1/match=last` | 128.3 | 4.1 % | 176 | 1 |
| `BenchmarkAllByKid/n=4/match=absent` | 43.0 | 1.7 % | 0 | 0 |
| `BenchmarkAllByKid/n=4/match=first` | 156.3 | 3.1 % | 176 | 1 |
| `BenchmarkAllByKid/n=4/match=last` | 159.3 | 4.8 % | 176 | 1 |
| `BenchmarkAllByKid/n=16/match=absent` | 163.0 | 2.8 % | 0 | 0 |
| `BenchmarkAllByKid/n=16/match=first` | 276.8 | 3.4 % | 176 | 1 |
| `BenchmarkAllByKid/n=16/match=last` | 281.0 | 3.5 % | 176 | 1 |
| `BenchmarkAllByKid/n=64/match=absent` | 642.6 | 2.2 % | 0 | 0 |
| `BenchmarkAllByKid/n=64/match=first` | 765.3 | 4.3 % | 176 | 1 |
| `BenchmarkAllByKid/n=64/match=last` | 761.9 | 2.1 % | 176 | 1 |
| `BenchmarkByKid` | 311.4 | 2.2 % | 176 | 1 |
| `BenchmarkBridgeOut/EC/ECDSAPublic` | 3 870.0 | 4.4 % | 1 288 | 30 |
| `BenchmarkBridgeOut/OKP/Ed25519Public` | 42.9 | 4.6 % | 32 | 1 |
| `BenchmarkBridgeOut/oct/Secret` | 44.5 | 3.1 % | 32 | 1 |
| `BenchmarkPKIXRoundTrip/1_marshal` | 3 863.0 | 1.0 % | 1 288 | 30 |
| `BenchmarkPKIXRoundTrip/2_parse` | 2 306.0 | 3.3 % | 696 | 11 |
| `BenchmarkPKIXRoundTrip/3_ecdh_pointcheck` | 958.6 | 1.4 % | 592 | 8 |
| `BenchmarkParse/EC` | 2 857.0 | 1.4 % | 688 | 9 |
| `BenchmarkParse/OKP` | 1 388.0 | 0.9 % | 208 | 2 |
| `BenchmarkParse/oct` | 1 352.0 | 1.6 % | 208 | 2 |
| `BenchmarkParseSet/n=1` | 4 497.0 | 1.2 % | 1 057 | 13 |
| `BenchmarkParseSet/n=16` | 59 840 | 2.1 % | 17 399 | 185 |
| `BenchmarkMarshalPublic/EC` | 2 073.0 | 1.4 % | 704 | 7 |
| `BenchmarkMarshalPublic/OKP` | 1 514.0 | 1.8 % | 528 | 5 |
| `BenchmarkThumbprint` | 721.0 | 2.7 % | 416 | 7 |
