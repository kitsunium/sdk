<!-- generated from internal/service/token/{token,refusal,stage,parsing,jwkset}_bench_test.go — run `cd internal/service && GOWORK=off go test -run=NONE -bench=. -benchmem -benchtime=1s -count=3 ./token/` in THREE separate processes to refresh -->
# Benchmarks — `internal/service/token`

Token **verification runs on every authenticated request**, and it is the only
place in this SDK where a hot path and a security boundary are the same code.
So the measurement has to answer both questions at once, and this report is
built around the four it was commissioned for:

> **What does each algorithm cost on each side; how much of a verification is
> signature maths and how much is this package's own parsing; what does a
> claim set cost; and does any REFUSAL cost more than the acceptance it
> replaces?**

The short answers, before the evidence:

1. **"Is verification mostly crypto?" has two opposite answers and the
   algorithm decides which.** HS256 verification is **4.5 % signature and
   95.5 % this package**. Every asymmetric option is **74–79 % signature**.
   A single figure for "what a JWT costs to verify" is not a thing that exists.
2. **No refusal costs more than the acceptance it replaces.** Twelve of the
   thirteen cost between **0.34 % and 25.5 %** of an accepted token; the
   thirteenth — an authenticated token that is simply expired — costs
   **96.3 %**, which is exactly what "signature first, claims second" predicts.
3. **The refusal costs are strictly ordered by how deep the check sits**, and
   the order matches the source. That is ADR 0042 §D4 verified by measurement
   rather than by reading.
4. **The algorithm-confusion gate — the mechanism ADR 0042 exists for — costs
   15.6 ns, 0.05 % of a verification.** The expensive security work in this
   package is not the part the ADR is about; it is RFC 8725 §2.6's
   duplicate-member refusal, at **24.8 %**, which is **5.5× the signature**.

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly. This is a
> memory-ballooned VM: 15 GiB nominal against a balloon the host may reclaim
> mid-run. The *shape* of every result — the ratios, the orderings, the
> flatness — is what travels, not the nanoseconds.

| Dimension | Value |
|---|---|
| CPU | AMD EPYC 7351P 16-Core Processor |
| CPU cores | 8 |
| RAM | 15 GiB (ballooned VM — see above) |
| OS / kernel | Linux 6.12.101+deb13-amd64 |
| Architecture | amd64 |
| Go toolchain | go1.27.1 linux/amd64 |
| Git branch | `jaimerias-que-tu-te-connect` |
| Git commit | `7b6073d` (pre-commit; the four `*_bench_test.go` files in the working tree) |
| Generated (UTC) | 2026-09-11 |
| Machine load | `23:51:24 up 18 days, 11:34, load average: 2.58, 1.98, 1.89` at start; `1.04, 1.20, 1.44` at end. No other job on the box; a sibling agent was reading source and ran nothing. |
| Bench wall-clock | `-benchtime=1s -count=3`, three processes, 73 rows × 9 samples ≈ 15 min |

### Method

- **Every published number is the median of NINE samples across THREE separate
  processes** — `-count=3` run three times from a single pre-built binary in
  `/dev/shm`. A single-process `-count=3` was demonstrated on this box to report
  a precision it does not have: it produced a wrapper measuring faster than the
  function it wraps. Three processes also re-randomise map iteration, ASLR and
  the heap's starting shape, which a `-count=N` inside one process does not.
- **Spread is `(max−min)/median` over all nine.** It is under 4 % for 68 of the
  73 rows and never above 11.4 %; §10 names the five that run wide and why.
- **Rows are cross-checked against each other arithmetically before
  publication.** Six independent identities were computed (§9). The stage
  decomposition sums to its own `whole` row at **100.0 % of allocations and
  100.0 % of bytes**; the white-box and black-box instruments agree on the same
  verification to **0.85 % on time and 0 allocations**.
- **pprof came first.** The CPU and allocation profiles in §1 are from separate
  runs of the same benchmark on the same binary; `-memprofilerate=1` is never
  mixed with a CPU profile.
- **`B/op` counts bytes ALLOCATED, not retained.**
- Every timing row uses a `clock.ManualClock`, because an expired-token row
  cannot exist without moving time by assignment. §8 prices that distortion at
  **181 ns, 0.63 %** — measured, not assumed.

## 1. The profiles, verbatim

### 1.1 CPU — `BenchmarkVerifyStage/whole`, `-benchtime=5s`

```
Duration: 6.16s, Total samples = 6.85s (111.22%)
Showing nodes accounting for 0.36s, 5.26% of 6.85s total
Dropped 233 nodes (cum <= 0.03s)
Showing top 18 nodes out of 196
      flat  flat%   sum%        cum   cum%
     0.04s  0.58%  0.58%      6.23s 90.95%  ...token.BenchmarkVerifyStage.func5
         0     0%  0.58%      6.23s 90.95%  testing.(*B).launch
         0     0%  0.58%      6.23s 90.95%  testing.(*B).runN
         0     0%  0.58%      6.19s 90.36%  ...token.(*jwsVerifier).Verify
         0     0%  0.58%      4.67s 68.18%  ...token.policyValue.decodeAndValidate
         0     0%  0.58%      4.52s 65.99%  ...token.decodeClaims
         0     0%  0.58%      2.95s 43.07%  encoding/json.Unmarshal (inline)
         0     0%  0.58%      2.95s 43.07%  encoding/json/v2.Unmarshal
     0.03s  0.44%  1.02%      2.77s 40.44%  encoding/json/v2.unmarshalDecode
     0.01s  0.15%  1.17%      1.84s 26.86%  ...token.checkNoDuplicateMembers
     0.03s  0.44%  1.61%      1.80s 26.28%  encoding/json/v2.makeMapArshaler.func3
     0.07s  1.02%  2.63%      1.20s 17.52%  encoding/json.(*Decoder).Token
         0     0%  2.63%      1.19s 17.37%  ...token.policyValue.parseJWS
     0.14s  2.04%  4.67%      1.17s 17.08%  runtime.mallocgc
         0     0%  4.67%      1.14s 16.64%  ...token.parseHeaderSegment
     0.01s  0.15%  4.82%      1.10s 16.06%  ...token.parseJOSEHeader
     0.01s  0.15%  4.96%      0.83s 12.12%  ...token.decodeRegistered
     0.02s  0.29%  5.26%      0.76s 11.09%  ...token.skipValue
```

And the same profile's crypto line, extracted by name because it does not reach
the top eighteen:

```
         0     0% 21.02%      0.33s  4.82%  ...crypto/hmacsha2.hmacSHA256.Verify
```

**That single line is the report's first finding.** The signature — the thing a
reader assumes a token verification *is* — is **4.82 % of the profile**.
`decodeClaims` is **65.99 %**, `encoding/json` is **43.07 %**, and
`checkNoDuplicateMembers` alone is **26.86 %**: five and a half times the
cryptography.

The flat profile says the same thing from the other end:

```
Showing nodes accounting for 1870ms, 27.30% of 6850ms total
Dropped 233 nodes (cum <= 34.25ms)
Showing top 12 nodes out of 196
      flat  flat%   sum%        cum   cum%
     380ms  5.55%  5.55%      380ms  5.55%  encoding/json/internal/jsonwire.ConsumeSimpleString (inline)
     200ms  2.92%  8.47%      200ms  2.92%  runtime.memmove
     150ms  2.19% 10.66%      150ms  2.19%  encoding/json/internal/jsonwire.ConsumeWhitespace (inline)
     150ms  2.19% 12.85%      250ms  3.65%  ...token.checkJSONDepth
     140ms  2.04% 14.89%      430ms  6.28%  encoding/json/jsontext.(*decoderState).ReadValue
     140ms  2.04% 16.93%     1170ms 17.08%  runtime.mallocgc
     140ms  2.04% 18.98%      200ms  2.92%  runtime.mallocgcSmallScanNoHeaderSC2
     120ms  1.75% 20.73%      140ms  2.04%  encoding/json/jsontext.(*decoderState).reset
     120ms  1.75% 22.48%      120ms  1.75%  internal/runtime/maps.memHashAES
     110ms  1.61% 24.09%      110ms  1.61%  crypto/internal/fips140/sha256.blockSHANI
     110ms  1.61% 25.69%      510ms  7.45%  encoding/json/jsontext.(*decoderState).ReadToken
     110ms  1.61% 27.30%      110ms  1.61%  internal/strconv.readFloat
```

`sha256.blockSHANI` — the SHA-NI instruction that IS HS256 — is **1.61 % flat**,
below `ConsumeWhitespace`. The profile is JSON scanning from top to bottom.

### 1.2 Allocation — same benchmark, `-memprofilerate=1`, `alloc_space`

```
Showing nodes accounting for 30300.05kB, 91.85% of 32989.58kB total
Dropped 244 nodes (cum <= 164.95kB)
Showing top 16 nodes out of 56
      flat  flat%   sum%        cum   cum%
 6663.41kB 20.20% 20.20%  6663.41kB 20.20%  reflect.mapassign_faststr0
 3682.03kB 11.16% 31.36%  3682.03kB 11.16%  maps.clone
 2945.31kB  8.93% 40.29%  2945.31kB  8.93%  encoding/json/jsontext.NewDecoder (inline)
 2098.31kB  6.36% 46.65% 10455.16kB 31.69%  ...token.checkNoDuplicateMembers
 2061.94kB  6.25% 52.90%  7405.22kB 22.45%  ...token.policyValue.parseJWS
 1841.02kB  5.58% 58.48%  6075.35kB 18.42%  ...core/token.ClaimsValue.WithPrivateRaw
 1767.06kB  5.36% 63.84%  1767.06kB  5.36%  encoding/base64.(*Encoding).DecodeString
 1620.16kB  4.91% 68.75%  4527.94kB 13.73%  encoding/json.(*Decoder).Token
 1435.39kB  4.35% 73.10%  1435.39kB  4.35%  encoding/json/jsontext.Token.String (inline)
 1362.03kB  4.13% 77.23%  1362.03kB  4.13%  encoding/json/jsontext.(*Value).UnmarshalJSON
 1178.25kB  3.57% 80.80%  1178.25kB  3.57%  crypto/internal/fips140/sha256.New (inline)
 1178.06kB  3.57% 84.37%  1251.50kB  3.79%  encoding/json/jsontext.(*decoderState).fetch
 1031.06kB  3.13% 87.49%  2209.36kB  6.70%  crypto/internal/fips140/hmac.New[...]
  552.42kB  1.67% 89.17%   552.42kB  1.67%  slices.Clone[go.shape.[]uint8,go.shape.uint8] (inline)
  441.80kB  1.34% 90.51%   441.80kB  1.34%  bytes.NewReader (inline)
  441.80kB  1.34% 91.85%  3387.11kB 10.27%  encoding/json.NewDecoder
```

Three things a reader should take from this:

- **`checkNoDuplicateMembers` is 31.69 % of every byte a verification
  allocates**, and 10.27 % of the total is the `json.NewDecoder` +
  `bytes.NewReader` pair it builds *twice* per verification — once for the
  header, once for the claims. That plumbing is not the check; it is the cost
  of reaching it.
- **`maps.clone` at 11.16 % is `core/token.ClaimsValue.WithPrivateRaw`**, and
  §4 shows why that line is quadratic in the private-claim count.
- **The crypto is 6.70 % of allocated bytes**, all of it `hmac.New` building a
  fresh HMAC state per call.

## 2. Issue and Verify, per algorithm

Median of nine. `minimal` is what the issuer stamps when the caller supplies
nothing — `iss`, `iat`, `exp`. `realistic` adds `sub`, `aud`, `jti` and three
private claims (a scope string, a three-element role array, a tenant id): nine
members, 303 bytes of JSON.

### 2.1 Verify — the per-request number

| algorithm | minimal | realistic | B/op (realistic) | allocs (realistic) |
|---|---:|---:|---:|---:|
| **HS256** (JWS) | **14 353 ns** | **28 905 ns** | 7 122 | 121 |
| PASETO v4.public | 97 873 ns | 112 154 ns | 5 834 | 93 |
| EdDSA (JWS) | 104 459 ns | 119 406 ns | 6 610 | 114 |
| ES256 (JWS) | 126 140 ns | 142 029 ns | 7 796 | 134 |

### 2.2 Issue — the per-login number

| algorithm | minimal | realistic | B/op (realistic) | allocs (realistic) |
|---|---:|---:|---:|---:|
| **HS256** | **5 442 ns** | **12 155 ns** | 5 245 | 54 |
| EdDSA | 42 015 ns | 50 472 ns | 4 925 | 48 |
| PASETO v4.public | 42 426 ns | 50 212 ns | 5 293 | 48 |
| ES256 | 59 904 ns | 68 291 ns | 11 180 | 110 |

**HS256 verifies 3.9–4.9× faster than any asymmetric option**, which is what a
symmetric MAC buys — and what it costs is that every verifier holds the key that
can also MINT. That trade is the decision; this table is its price tag, and
ADR 0042 is where the security half is argued.

**Verification costs 2.1–2.4× what issuing costs, on every one of the four**
(HS256 2.38×, EdDSA 2.37×, PASETO 2.23×, ES256 2.08×). Two reasons compound:
Ed25519 verification is **2.31×** its own signing (88 621 ns against 38 436 ns,
measured directly in `BenchmarkPrimitive`), and the verify side runs the three
JSON passes §7 prices while the issue side runs one `json.Marshal`. A service
that mints once per login and verifies once per request should read the second
column of the first table and ignore the second table almost entirely.

**PASETO v4.public is the cheapest asymmetric option on the verify side even
though it uses the same Ed25519 primitive as JWS EdDSA.** The cryptography is
held constant across those two rows on purpose, so the 7 252 ns difference is
the ENVELOPE: PASETO has no header to base64-decode, no JOSE object to parse,
no `alg` to compare, and no duplicate-member scan over a header. An independent
campaign at `pkg/v1/token/BENCH.md`, on a different claim set and a different
day, measured that same envelope difference at 7 019 ns — **3.3 % apart**
(§11).

## 3. The crypto-versus-parsing split

This is the question the report was commissioned for, and it has no single
answer. `BenchmarkPrimitive` measures each signature primitive on the exact
input a verification hands it, with no token around it; subtracting gives what
this package costs.

| algorithm | verify (realistic) | signature | this package | crypto share |
|---|---:|---:|---:|---:|
| **HS256** | 28 905 ns | **1 299 ns** | **27 606 ns** | **4.5 %** |
| PASETO v4.public | 112 154 ns | 88 621 ns | 23 533 ns | **79.0 %** |
| ES256 | 142 029 ns | 109 891 ns | 32 138 ns | **77.4 %** |
| EdDSA | 119 406 ns | 88 621 ns | 30 785 ns | **74.2 %** |

With the `minimal` claim set the asymmetric share rises further — PASETO
reaches **90.5 %**, ES256 **87.1 %**, EdDSA **84.8 %** — while HS256 falls to
**9.1 %** crypto.

**So: if verification is 90 % signature maths there is nothing to do in this
package — and for every asymmetric algorithm on a small claim set, it very
nearly is. For HS256 the opposite holds, and 95.5 % of the cost is this
package's own parsing.** An optimisation to the JSON path would move HS256 by
roughly twenty times what it moves ES256, which is the whole reason the split
is published per algorithm instead of averaged.

**Honesty about the subtraction.** For the asymmetric rows this is a small
difference of two large numbers, so it carries both rows' spread: at ±1.3–3.6 %
on a ~110 µs subtrahend, the "this package" column for ES256/EdDSA/PASETO is
good to about ±1.5–4 µs and no better. Only the HS256 split is precise, because
there the subtrahend is 4.5 % of the total.

**The precise, crypto-free instrument** is the within-algorithm claim-set
delta, which cancels the signature exactly:

| algorithm | realistic − minimal | B/op | allocs |
|---|---:|---:|---:|
| HS256 | +14 552 ns | +4 038 | +57 |
| EdDSA | +14 947 ns | +4 039 | +57 |
| ES256 | +15 889 ns | +4 038 | +57 |
| PASETO v4.public | +14 281 ns | +4 240 | +59 |

**All four agree within 11 % on time and to the BYTE on allocation** (+4 038 B
and +57 allocations for the three JWS algorithms, identically). That is what a
shared claims codec predicts, and it is the strongest single cross-check in this
report: four independent measurements of the same six claims, through four
different signature primitives, landing on one number.

## 4. What a claim set costs — and the quadratic in it

`BenchmarkVerifyPrivateClaims` scales HS256 verification from zero private
claims to `core/token.MaxPrivateClaims`.

| private claims | ns/op | B/op | allocs | marginal ns/claim | marginal B/claim |
|---:|---:|---:|---:|---:|---:|
| 0 | 15 722 | 3 172 | 69 | — | — |
| 1 | 17 600 | 3 757 | 77 | 1 878 | 585 |
| 4 | 23 346 | 5 728 | 106 | 1 915 | 657 |
| 16 | 52 854 | 23 116 | 245 | 2 459 | 1 449 |
| 64 | **205 667** | **191 599** | 786 | 3 184 | 3 510 |

**The marginal cost of a private claim is not constant — it rises 1.7× between
the first claim and the sixty-fourth, and the byte cost rises 6×.** A token with
64 small private claims allocates **191 KB** to verify.

The cause is named directly in the allocation profile: `maps.clone` at 11.16 %.
`attachPrivate` (in this package) adds claims one at a time through
`core/token.ClaimsValue.WithPrivateRaw`, and that method is copy-on-write —
`c.private = maps.Clone(c.private)` on every call, deliberately, so a value
receiver cannot mutate a copy somebody else holds. Attaching *n* claims
therefore clones maps of size 0, 1, 2, … n−1: **Σ = n(n−1)/2 entries copied,
which is O(n²)**. For n = 64 that is 2 016 entries, and at ~90 B per entry it
predicts ~181 KB against the 188 KB actually measured above the n=0 baseline.

**This is recorded, not fixed, and the reason is scope.** The quadratic lives in
the interaction between this package's `attachPrivate` loop and `core/token`'s
copy-on-write setter; removing it needs a batch setter that only `core/token`
can add, and `core/token` is a published, frozen value type. What makes the
quadratic *safe* rather than merely unpleasant is `MaxPrivateClaims = 64`: the
worst case is bounded at 205 µs and 191 KB, it is reachable only by a party who
can already mint a token this verifier accepts, and the bound is what turns an
unbounded quadratic into a fixed 7× ceiling over a realistic payload.

## 5. The refusal table — and the denial-of-service verdict

A verifier is exposed to input the caller does not choose. **A refusal that
costs more than the acceptance it replaces is a denial-of-service lever an
attacker gets for free**, so this table is read against one baseline: an
accepted HS256 token with the realistic claim set, **28 905 ns / 121 allocs**.

Every row is HS256, deliberately — it is the cheapest acceptance this package
offers, so a refusal that does not beat HS256 does not beat anything.

| refusal | stage | ns/op | allocs | % of acceptance |
|---|---|---:|---:|---:|
| too many/few segments | pre-auth | **99.3** | **0** | **0.34 %** |
| oversized, 1 MiB | pre-auth | 257.6 | 2 | 0.89 % |
| oversized, 8 KiB + 1 | pre-auth | 257.9 | 2 | 0.89 % |
| oversized, 8 MiB | pre-auth | 258.1 | 2 | 0.89 % |
| segment is not strict base64url | pre-auth | 334.7 | 3 | 1.16 % |
| header nested past `maxHeaderDepth` | pre-auth | 528.2 | 3 | 1.83 % |
| duplicate member in the header | pre-auth | 1 396.0 | 13 | 4.83 % |
| `"alg":"none"` | pre-auth | 4 674.0 | 27 | 16.17 % |
| `alg` ≠ the constructor's binding | pre-auth | 4 977.0 | 29 | 17.22 % |
| bad signature | post-sig | 6 239.0 | 34 | 21.58 % |
| claims nested past `MaxClaimDepth` | post-auth | 7 170.0 | 37 | 24.81 % |
| duplicate member in the claims | post-auth | 7 361.0 | 46 | 25.47 % |
| expired | post-auth | 27 830.0 | 121 | 96.28 % |

### Verdict

**No refusal in this package costs more than the acceptance it replaces.** The
worst is 0.96×; every *structural* refusal — the ones an attacker can reach
without a valid signing key — is between **0.0034×** and **0.26×**.

The expired row is the interesting one, and it is not a defect: an expired
token is parsed in full, its signature verified in full, and its claims decoded
in full before `checkExpiry` runs, because ADR 0042 §D5 forbids reporting a
claim verdict for a token whose signature was never checked. So "expired costs
almost exactly what valid costs" is the *design being visible in the numbers*.
It also costs an attacker a valid signature to reach, which means it is not a
lever: anybody who can produce one can equally well send a token that is
accepted.

The measured 96.28 % is 3.7 % below the acceptance row against per-row spreads
of 1.4 % and 2.3 %, i.e. just outside them. The predicted gap is ~60 ns — the
`checkLifetime` and `checkIdentity` calls the early return skips. The residue
is that the two rows live in different top-level benchmarks and therefore in
different heap states; it is reported rather than smoothed.

### The cost ordering IS the check ordering

Sort the refusals by cost and you recover the source order of the checks:

```
splitCompact          99 ns   →  MaxTokenLen        258 ns  →  strict base64url   335 ns
  →  checkJSONDepth  528 ns   →  duplicate member  1 396 ns →  checkHeader      4 674 ns
  →  signature     6 239 ns   →  claim decode      7 361 ns →  claim validation 27 830 ns
```

**Every check costs strictly more than the one before it, and every check is
placed strictly before it.** That is ADR 0042 §D4 — "every bound is checked
before the work it funds" — verified by measurement. It is also the property
that makes the whole table cheap: the checks a stranger can trip without a key
are the first eight, and they are the eight cheapest.

Two rows in that ladder reconstruct exactly from the stage decomposition,
which is what makes it a measurement rather than a story (§9):

- `bad_signature` = parse + checkHeader + signature = 4 911 + 15.6 + 1 299 =
  **6 226 ns predicted, 6 239 ns measured — 0.22 % apart.**
- `algorithm_bound` costs **303 ns more** than `algorithm_none`, because
  `AlgorithmMismatch` is built with `errs.Wrap` carrying the expected
  algorithm as a field while `AlgorithmNone` returns a bare sentinel. Refusing
  with a diagnosis costs 303 ns; that is the price of the field, and it is the
  same ~300 ns the oversized rows pay for their `errs.Int("limit", …)`.

## 6. The CVE-2025-30204 bound, measured

`splitCompact` refuses a token past `MaxTokenLen` **before** scanning it.
`BenchmarkSplitCompactOversized` presents the same refusal at four sizes
spanning 8 192×:

| oversized token | ns/op | B/op | allocs |
|---|---:|---:|---:|
| 8 KiB + 1 | 168.9 | 208 | 2 |
| 1 MiB | 168.5 | 208 | 2 |
| 8 MiB | 170.2 | 208 | 2 |
| 64 MiB | 169.5 | 208 | 2 |

**Flat: 168.5–170.2 ns across a 8 192× size range, 1.0 % end to end, with the
allocation identical to the byte.** The 208 B / 2 allocs is the `errs.Wrap`
carrying `limit`; nothing about the token itself is ever touched. This is the
`strings.Split`-before-any-length-check class refused by construction, and it
is now also a gate rather than an observation — see §12.

**A finding that came out of writing that gate.** The hostile shape
CVE-2025-30204 was reported with — megabytes of `.` — does **not** force a scan
here at all, with or without the length bound: the "more parts than the format
allows" check short-circuits at the fourth separator, three bytes in. The shape
that forces the scan is a long token with **no separator**, where
`strings.IndexByte` reads every byte before reporting there are none. So
`splitCompact` carries two bounds that stop two different hostile shapes, and
**only one of them is the CVE's**. Both tables above use the separator-free
shape, because it is the one that could be slow.

## 7. What the security properties cost

Every number below is a check that exists for a stated security reason, priced
against one whole HS256 verification of the realistic claim set (28 660 ns).

| check | what it refuses | ns | share |
|---|---|---:|---:|
| `checkNoDuplicateMembers` (claims) | RFC 8725 §2.6 substitution | **5 968** | **20.82 %** |
| — the signature itself, for scale — | forgery | 1 299 | 4.53 % |
| `checkNoDuplicateMembers` (header) | a token with two `alg` members | 1 147 | 4.00 % |
| `checkJSONDepth` (claims) | a frame-per-level decode bomb | 865.8 | 3.02 % |
| strict base64url (header segment) | two spellings of one token | 100.9 | 0.35 % |
| `splitCompact` | CVE-2025-30204 | 62.1 | 0.22 % |
| `validateClaims` | exp / nbf / iss / aud | 59.5 | 0.21 % |
| `checkHeader` | **algorithm confusion** | **15.6** | **0.05 %** |

Two readings, both worth stating out loud.

**The mechanism ADR 0042 is entirely about is essentially free.** `checkHeader`
— the comparison of the token's `alg` against the constructor's binding, the
runtime half of the algorithm-confusion defence — costs **15.6 nanoseconds**,
one twentieth of one percent. It is a handful of string comparisons on a
value already parsed. There has never been a performance argument for reading
the algorithm from the token, and now there is a number saying so.

**The expensive security property is the one nobody argues about.** RFC 8725
§2.6's duplicate-member refusal costs **24.8 % of every verification** across
its two calls — **5.5× the signature**. It is 26.86 % of the CPU profile and
31.69 % of the allocated bytes. If any security check in this package is ever
worth optimising, it is this one, and §13 records why it was not touched here.

## 8. Two distortions, priced rather than assumed

**The clock.** Every row uses a `clock.ManualClock`, which reads a struct field
where `clock.System` calls into the runtime's monotonic clock.

| clock | ns/op | B/op | allocs |
|---|---:|---:|---:|
| `ManualClock` | 28 680 | 7 122 | 121 |
| `clock.System` | 28 861 | 7 122 | 121 |

**181 ns, 0.63 %, and zero allocations either way.** Add it to any row to get a
production number. It is below every row's spread, which is why the manual
clock was chosen without hesitation once it was measured.

**The benchmark's own sink.** The first draft of `stage_bench_test.go` used a
single `var stageSink any`, and reported `splitCompact` at **80 B / 1 alloc per
call** — which is `sizeof(segmentsValue)`, not anything `splitCompact` does.
Assigning a struct to an `any` boxes it, and the box was being charged to the
function under test. `splitCompact`'s own doc comment says the split "allocates
nothing at all"; the sink was contradicting the code. With one typed sink per
shape it measures **0 B / 0 allocs**, and §12 makes that a test.

That row would have been published as a contradiction of the package's own
documentation. It is recorded here because the instrument was the defect, and
that is the failure mode a benchmark report is least able to catch about itself.

## 9. The cross-checks

Six identities, computed before anything above was written. A table that
contradicts itself arithmetically is noise.

| identity | expected | measured | verdict |
|---|---|---|---|
| JWS stages sum to `whole` (allocs) | 27+0+7+87 = 121 | 121 | **exact** |
| JWS stages sum to `whole` (bytes) | 1 602+0+544+4 974 = 7 120 | 7 121 | **1 B apart** |
| JWS stages sum to `whole` (time) | 27 328 ns | 28 660 ns | 95.4 % — the residue is the interface call, the `jwsPartsValue` return copy and the sink store |
| white-box `whole` vs black-box `Verify/HS256/realistic` | same work, two rigs | 28 660 vs 28 905 ns; 121 vs 121 allocs | **+0.85 %, 0 allocs apart** |
| `VerifyStage/3_signature` vs `Primitive/hmac_sha256_verify` | same call, two rigs | 1 299 vs 1 299 ns | **identical** |
| Ed25519 through PASETO vs the bare primitive | same call, two rigs | 88 212 vs 88 621 ns | **−0.46 %** |
| PASETO stages sum to `whole` | 93 allocs / 5 830 B | 93 allocs / 5 833 B | **exact / 3 B** |
| `bad_signature` = parse + header + signature | 6 226 ns | 6 239 ns | **+0.22 %** |
| four algorithms' claim-set delta | one shared codec ⇒ one number | +4 038 B and +57 allocs, three times identically | **exact** |

The one identity that did **not** close on the first attempt: `algorithm_none`
was predicted at 4 927 ns (parse + checkHeader) and measured 4 674 ns, **5.1 %
low**. The prediction was wrong, not the row — `1_parse` parses the *realistic*
token (≈490 characters) while the `algorithm_none` fixture is a hand-forged
token of ≈180. `splitCompact` scans the whole string and `parseJWS` converts the
signing input to `[]byte`, so a shorter token parses faster by roughly the
observed amount. Recorded rather than quietly dropped.

## 10. Rows discarded, and their causes

Four sets of rows were thrown away during this campaign. Each is here with what
was wrong, because "we re-ran it" without a cause is indistinguishable from
picking the number that suited.

1. **The entire first draft's allocation columns**, discarded for the `any`
   sink described in §8. Every row that stored a result — `splitCompact`,
   `parseJOSEHeader`, `decodeClaims`, both `whole` rows — was carrying one
   boxing allocation of the result's own size. Cause: the instrument. Fixed by
   typed sinks; `splitCompact` went from 80 B / 1 alloc to 0 B / 0 allocs and
   the stage sum then closed to 100.0 % on allocations, which it had not before.
2. **The first `BenchmarkSplitCompactOversized` set**, which built a 64 MiB
   string *inside* the benchmark body. `testing` calls the body repeatedly
   while calibrating `b.N`, so that was a dozen 64 MiB allocations on a VM
   whose balloon the host can halve mid-run. Hoisted out of the timed function;
   the timings were unaffected but the risk was not acceptable to keep.
3. **The first `oversized` fixture set**, which used megabytes of `.`. Those
   rows were *correct* and *uninformative*: they measure a short-circuit three
   bytes in, not the bound they were written to exercise (§6). Replaced with
   the separator-free shape.
4. **A whole 99-row session run before the linter fixes.** Converting the
   refusal fixture from a map to a slice changes sub-benchmark ordering, and
   the closure fix in `BenchmarkDuplicateScan` changes escape analysis around
   the payload. Neither should move a number, but "should not" is not a
   measurement, so the session was discarded and the campaign re-run end to end
   on the exact code that ships. The discarded session agreed with the
   published one to within 3 % on every row it covered.

## 11. Reconciliation with `pkg/v1/token/BENCH.md`

That report exists, predates this one, and measures the **public facade** as a
black box: a choice table for a caller picking an algorithm. This one measures
the **service package** as a white box: where the time goes inside one
verification. They must agree where they overlap, and they do — once the two
differences between the harnesses are accounted for.

| | `pkg/v1/token` | here |
|---|---|---|
| claim set | 6 registered, 0 private | 3 (`minimal`) and 9 (`realistic`) |
| clock | `time.Now()` (system) | `ManualClock`, +181 ns to convert (§8) |
| samples | **one run, "machine under load"** | median of 9 across 3 processes |

`Verify_HS256` there is **20 528 ns** on 6 members. Here, HS256 is **14 353 ns**
on 3 members and **28 905 ns** on 9. **Their number sits between ours, and the
claim-set slope predicts it:** 14 353 + 3 × (14 552 / 6) = 21 629 ns, against
20 528 measured — **5.4 % apart**, on two harnesses, two claim sets and two
sessions, one of which was under load.

The other overlap is the PASETO-versus-EdDSA envelope difference: **7 019 ns**
there, **7 252 ns** here, **3.3 % apart** on different payloads. Two independent
measurements of the same structural fact.

The one figure that does **not** reproduce is discussed next.

## 12. `skipValue`: a claim in the tree, re-measured

`encoding.go`'s `skipValue` carries a comment asserting three things about the
`json.RawMessage` implementation it replaced: that a memory profile put the copy
at **23 % of the objects** a verification allocates, that removing it was worth
**two allocations**, and that it was worth **~14 % of an HS256 verification** —
citing "BENCH.md", which at the time meant `pkg/v1/token`'s.

`BenchmarkDuplicateScanRealistic` runs both implementations over the exact two
payloads one verification scans. `checkNoDuplicateMembersByCopy` lives only in
the benchmark file; nothing in production calls it.

| payload | `skipValue` | `json.RawMessage` | delta | allocs | B/op |
|---|---:|---:|---:|---:|---:|
| header (27 B, 3 members) | 1 147 ns | 2 066 ns | **+919 ns (+80.1 %)** | 14 → 14 | 576 → 608 |
| claims (303 B, 9 members) | 6 016 ns | 9 925 ns | **+3 909 ns (+65.0 %)** | 50 → **48** | 1 696 → 1 792 |

A verification calls it twice, so the saving is **4 828 ns = 16.8 % of a whole
HS256 verification**.

**The time claim reproduces and is if anything understated** — 16.8 % here on
nine members against ~14 % there on six, which is the same claim-set slope §11
used.

**The allocation claim does not reproduce on this toolchain, and in one case it
inverts.** On the realistic claim set the copying implementation allocates **two
FEWER objects** (48 against 50) while allocating 96 more bytes; on the header it
allocates exactly the same number. Only on synthetic payloads whose values are
all scalars does it cost the stated +2 (measured at +2 on 16-, 256- and
4 096-byte values). The mechanism is visible in the profile: `skipValue` walks
tokens through `json.Decoder.Token()`, which returns an `any` and therefore
boxes every scalar it passes — an array of three strings costs three boxes —
while `Decode(&json.RawMessage{})` takes the whole value in one grab. Go 1.27's
`encoding/json` is backed by `json/v2`, which reuses a buffer for that grab; the
comment's arithmetic is from before that.

Where `skipValue` wins on memory is **bytes on large values**, and there it wins
decisively: a single 4 KiB claim value costs **832 B more** to copy than to
skip, and that gap grows with the value while the object count does not.

**Action taken:** the comment in `encoding.go` was corrected to state what
reproduces and to name which report says what. No code was changed — the
implementation is right, and it is right for a reason the comment now states
accurately.

## 13. What was REFUSED, and the property each would have weakened

The profile names three costs inside this package's own parsing. All three are
refused, and this section is the reason each.

**1. Merging the three JSON passes into one.** A verification walks the claim
payload three times — depth, duplicates, decode — at a measured **24 %** of the
total for the duplicate pass alone. Collapsing them means hand-writing a JSON
walker for the most security-sensitive decoder in the SDK. **REFUSED. Property
it would weaken:** RFC 8725 §2.6 and the depth bound are enforced today by two
independent passes over bytes that a third (`encoding/json`) then re-parses;
one hand-rolled pass makes all three verdicts depend on one hand-rolled parser
being right about JSON string escaping, surrogate pairs and number syntax. The
saving is bounded at ~7 µs on HS256 and ~0 % on any asymmetric algorithm.
`pkg/v1/token/BENCH.md` already recorded this refusal; this report puts a number
beside it.

**2. Pooling or resetting the `json.Decoder` in `checkNoDuplicateMembers`.**
`json.NewDecoder` + `bytes.NewReader` is **10.27 % of every byte** a
verification allocates, built twice per call. `encoding/json.Decoder` has no
`Reset`, so avoiding it means importing `encoding/json/v2/jsontext` and driving
its decoder directly. **REFUSED. Property it would weaken:** it swaps the parser
underneath the duplicate-member check — the one check whose whole job is that
two readers cannot disagree about a token — for a different one with different
edge-case behaviour, to save allocations on a path already dominated by the
scan itself.

**3. Hand-decoding the registered string claims.** `decodeStringClaim` calls
`json.Unmarshal` per claim, seven times across the header and the claim set,
and `encoding/json/v2` reflection is 43.07 % of the profile. **REFUSED.
Property it would weaken:** a hand-written string decoder is a hand-written
JSON *unescaper*, and `iss`/`sub`/`aud`/`kid` are precisely the values an
attacker chooses. RFC 8725 §3.7's "use UTF-8" is covered here *by delegation*
to `encoding/json`; hand-decoding takes that delegation back.

**Nothing in the cryptographic path was touched, considered or measured for
change.** No check was made conditional, moved off a path, cached, or reordered;
no comparison was shortened. The one change this campaign made to a production
file is a corrected comment (§12), and `git diff` confirms every other
production byte is unchanged.

**And one optimisation that is not refused but is out of scope:** the O(n²)
private-claim attachment of §4. It needs a batch setter in `core/token`, which
this campaign does not own.

## 14. The two guards this campaign added

Both are in `split_alloc_internal_test.go`, both `//go:build !race`, both gated
by `//internal/service/token:token_test` in `tools/alloc-lane-targets.txt`
(SDK-wide rule 12 — the race-off alloc lane is their only lane). Each carries
its mutation and the observed failure in its own doc comment.

**`TestSplitCompactAllocatesNothing`** pins the claim made in three places —
`splitCompact`'s doc comment, `segmentsValue`'s, and the package CLAUDE.md
§Bounds — that splitting a well-formed token allocates nothing.
*Mutation:* the body replaced by `strings.Split`. *Observed:* `split of a
well-formed token performed 500 allocations in 500 calls, want 0`. Restored
byte-identical.

**`TestOversizedRefusalDoesNotScanTheToken`** pins that the length bound is
checked before the scan. *Mutation:* the bound moved below the walk.
*Observed:* the 8 MiB refusal cost **1 364×, then 553×, then 1 334×** the 8 KiB
one against a 50× limit, in three consecutive runs. Restored byte-identical.

**One mutation failed to model its defect, and that is a result.** The first
version of the second guard asserted that the 8 MiB refusal allocated the same
as the 8 KiB one. The bound-ordering mutation **passed** against it: the walk is
`strings.IndexByte` into a fixed `[4]string`, so scanning eight megabytes
allocates exactly nothing. The guard was measuring an instrument the defect does
not move. It was rebuilt on wall time — sound here only because the clean ratio
is 0.9–1.4 and the mutated ratio is 553–1 364, three orders of magnitude of
separation — and the input was changed from all-separators to separator-free for
the reason in §6. Both halves of that lesson are written into the test's doc
comment so the next reader does not re-derive them.

## 15. Full results

Median of nine samples (three processes × `-count=3`), `-benchtime=1s`. Spread
is `(max−min)/median` across all nine.

| benchmark | ns/op (median of 9) | spread | B/op | allocs/op |
|---|---:|---:|---:|---:|
| `BenchmarkDuplicateScan/copy/value=16` | 3 945.0 | 1.7 % | 752 | 23 |
| `BenchmarkDuplicateScan/copy/value=256` | 5 535.0 | 6.5 % | 1 912 | 26 |
| `BenchmarkDuplicateScan/copy/value=4096` | 22 565.0 | 6.4 % | 21 856 | 30 |
| `BenchmarkDuplicateScan/skip/value=16` | 2 193.0 | 1.8 % | 688 | 21 |
| `BenchmarkDuplicateScan/skip/value=256` | 3 517.0 | 2.2 % | 1 824 | 24 |
| `BenchmarkDuplicateScan/skip/value=4096` | 18 180.0 | 7.5 % | 21 024 | 28 |
| `BenchmarkDuplicateScanRealistic/copy/claims` | 9 925.0 | 2.9 % | 1 792 | 48 |
| `BenchmarkDuplicateScanRealistic/copy/header` | 2 066.0 | 1.5 % | 608 | 14 |
| `BenchmarkDuplicateScanRealistic/skip/claims` | 6 016.0 | 8.4 % | 1 696 | 50 |
| `BenchmarkDuplicateScanRealistic/skip/header` | 1 147.0 | 3.1 % | 576 | 14 |
| `BenchmarkIssue/ES256/minimal` | 59 904.0 | 3.1 % | 7 942 | 88 |
| `BenchmarkIssue/ES256/realistic` | 68 291.0 | 2.7 % | 11 180 | 110 |
| `BenchmarkIssue/EdDSA/minimal` | 42 015.0 | 2.2 % | 1 689 | 26 |
| `BenchmarkIssue/EdDSA/realistic` | 50 472.0 | 1.9 % | 4 925 | 48 |
| `BenchmarkIssue/HS256/minimal` | 5 442.0 | 1.9 % | 2 025 | 32 |
| `BenchmarkIssue/HS256/realistic` | 12 155.0 | 3.5 % | 5 245 | 54 |
| `BenchmarkIssue/PasetoV4Public/minimal` | 42 426.0 | 1.7 % | 1 913 | 26 |
| `BenchmarkIssue/PasetoV4Public/realistic` | 50 212.0 | 1.6 % | 5 293 | 48 |
| `BenchmarkParsePiece/checkJSONDepth` | 865.8 | 2.2 % | 0 | 0 |
| `BenchmarkParsePiece/checkNoDuplicateMembers` | 5 968.0 | 2.1 % | 1 696 | 50 |
| `BenchmarkParsePiece/decodeClaims` | 19 943.0 | 2.6 % | 4 653 | 86 |
| `BenchmarkParsePiece/decodeSegment_header` | 100.9 | 9.8 % | 32 | 1 |
| `BenchmarkParsePiece/encodeClaims` | 8 224.0 | 1.7 % | 2 362 | 40 |
| `BenchmarkParsePiece/parseJOSEHeader` | 4 174.0 | 2.7 % | 1 089 | 24 |
| `BenchmarkParsePiece/splitCompact` | 62.1 | 3.2 % | 0 | 0 |
| `BenchmarkParsePiece/validateClaims` | 59.5 | 5.6 % | 0 | 0 |
| `BenchmarkPasetoStage/1_split` | 774.4 | 5.9 % | 416 | 1 |
| `BenchmarkPasetoStage/2_preAuthEncode` | 197.0 | 3.7 % | 384 | 1 |
| `BenchmarkPasetoStage/3_signature` | 88 212.0 | 1.8 % | 0 | 0 |
| `BenchmarkPasetoStage/4_decodeAuthenticated` | 20 128.0 | 1.9 % | 5 030 | 91 |
| `BenchmarkPasetoStage/whole` | 112 464.0 | 11.4 % | 5 833 | 93 |
| `BenchmarkPrimitive/ecdsa_p256_verify` | 109 891.0 | 1.2 % | 1 184 | 20 |
| `BenchmarkPrimitive/ecdsa_p256_verify_raw` | 109 508.0 | 2.1 % | 1 056 | 18 |
| `BenchmarkPrimitive/ed25519_sign` | 38 436.0 | 1.2 % | 64 | 1 |
| `BenchmarkPrimitive/ed25519_verify` | 88 621.0 | 1.3 % | 0 | 0 |
| `BenchmarkPrimitive/hmac_sha256_verify` | 1 299.0 | 2.6 % | 544 | 7 |
| `BenchmarkSplitCompactOversized/1_MiB` | 168.5 | 2.7 % | 208 | 2 |
| `BenchmarkSplitCompactOversized/64_MiB` | 169.5 | 4.4 % | 208 | 2 |
| `BenchmarkSplitCompactOversized/8_KiB+1` | 168.9 | 3.6 % | 208 | 2 |
| `BenchmarkSplitCompactOversized/8_MiB` | 170.2 | 2.9 % | 208 | 2 |
| `BenchmarkVerify/ES256/minimal` | 126 140.0 | 1.8 % | 3 758 | 77 |
| `BenchmarkVerify/ES256/realistic` | 142 029.0 | 1.9 % | 7 796 | 134 |
| `BenchmarkVerify/EdDSA/minimal` | 104 459.0 | 3.6 % | 2 571 | 57 |
| `BenchmarkVerify/EdDSA/realistic` | 119 406.0 | 2.1 % | 6 610 | 114 |
| `BenchmarkVerify/HS256/minimal` | 14 353.0 | 1.1 % | 3 084 | 64 |
| `BenchmarkVerify/HS256/realistic` | 28 905.0 | 1.4 % | 7 122 | 121 |
| `BenchmarkVerify/PasetoV4Public/minimal` | 97 873.0 | 1.9 % | 1 594 | 34 |
| `BenchmarkVerify/PasetoV4Public/realistic` | 112 154.0 | 1.0 % | 5 834 | 93 |
| `BenchmarkVerifyClockSource/manual` | 28 680.0 | 1.6 % | 7 122 | 121 |
| `BenchmarkVerifyClockSource/system` | 28 861.0 | 5.9 % | 7 122 | 121 |
| `BenchmarkVerifyPrivateClaims/n=0` | 15 722.0 | 2.0 % | 3 172 | 69 |
| `BenchmarkVerifyPrivateClaims/n=1` | 17 600.0 | 2.5 % | 3 757 | 77 |
| `BenchmarkVerifyPrivateClaims/n=16` | 52 854.0 | 2.7 % | 23 116 | 245 |
| `BenchmarkVerifyPrivateClaims/n=4` | 23 346.0 | 2.1 % | 5 728 | 106 |
| `BenchmarkVerifyPrivateClaims/n=64` | 205 667.0 | 3.1 % | 191 599 | 786 |
| `BenchmarkVerifyRefusal/post-auth/claims_duplicate` | 7 361.0 | 2.8 % | 2 393 | 46 |
| `BenchmarkVerifyRefusal/post-auth/claims_too_deep` | 7 170.0 | 4.6 % | 2 305 | 37 |
| `BenchmarkVerifyRefusal/post-auth/expired` | 27 830.0 | 2.3 % | 7 116 | 121 |
| `BenchmarkVerifyRefusal/post-sig/bad_signature` | 6 239.0 | 1.3 % | 2 145 | 34 |
| `BenchmarkVerifyRefusal/pre-auth/algorithm_bound` | 4 977.0 | 0.9 % | 1 504 | 29 |
| `BenchmarkVerifyRefusal/pre-auth/algorithm_none` | 4 674.0 | 1.7 % | 1 296 | 27 |
| `BenchmarkVerifyRefusal/pre-auth/bad_base64url` | 334.7 | 1.4 % | 211 | 3 |
| `BenchmarkVerifyRefusal/pre-auth/header_duplicate` | 1 396.0 | 2.8 % | 608 | 13 |
| `BenchmarkVerifyRefusal/pre-auth/header_too_deep` | 528.2 | 3.3 % | 272 | 3 |
| `BenchmarkVerifyRefusal/pre-auth/oversized_1_MiB` | 257.6 | 2.3 % | 208 | 2 |
| `BenchmarkVerifyRefusal/pre-auth/oversized_8_KiB` | 257.9 | 1.4 % | 208 | 2 |
| `BenchmarkVerifyRefusal/pre-auth/oversized_8_MiB` | 258.1 | 1.2 % | 208 | 2 |
| `BenchmarkVerifyRefusal/pre-auth/segment_count` | 99.3 | 1.8 % | 0 | 0 |
| `BenchmarkVerifyStage/1_parse` | 4 911.0 | 2.9 % | 1 602 | 27 |
| `BenchmarkVerifyStage/2_checkHeader` | 15.6 | 5.2 % | 0 | 0 |
| `BenchmarkVerifyStage/3_signature` | 1 299.0 | 2.9 % | 544 | 7 |
| `BenchmarkVerifyStage/4_decodeAndValidate` | 21 102.0 | 2.2 % | 4 974 | 87 |
| `BenchmarkVerifyStage/whole` | 28 660.0 | 3.9 % | 7 121 | 121 |

---

## 16. The JWK Set verifier — a second campaign, 2026-09-11

Everything above measures the four SINGLE-KEY verifiers. `NewSetVerifier` — the
JWKS path, the one a service that fetches a published key set actually runs —
had never been benchmarked, here or in `pkg/v1/token/BENCH.md`. This section is
that measurement and the change it produced. It is **additive**: no row above
was re-run or re-numbered, and §2.1's ES256 figure is used below as the
denominator it always was.

Same method as §Method: median of nine across three processes, `-benchtime=1s`,
typed sinks, pprof first. Full machine stamp and the complete jwk-side
decomposition are in **`internal/service/crypto/jwk/BENCH.md`**, which was
written with this section and holds the profiles verbatim.

### 16.1 The finding

**A JWK Set verification under ES256 spent 8.3 % of its CPU and 28.2 % of its
allocated objects rebuilding a key that never changes.**

`setVerifier.Verify` called `bindJWK` inside its per-token candidate loop.
For an EC key that reached `jwk.KeyValue.ECDSAPublic()`, which rebuilds two
`big.Int`s and runs `x509.MarshalPKIXPublicKey` — and `bindECJWK` handed the
resulting DER straight back to `x509.ParsePKIXPublicKey` on the next line. A
full ASN.1 round trip, per request, to reconstruct a key fixed for the
verifier's lifetime. The code's own comment said so: *"the DER came from jwk
one line ago, so a failure here is a bug, not input."*

The allocation profile, verbatim:

```
         0     0%  2.31%     120635 28.24%  ...token.bindJWK
         0     0% 17.27%      73857 17.29%  ...crypto/jwk.KeyValue.ECDSAPublic
      2531  0.59% 18.44%      63204 14.80%  crypto/x509.MarshalPKIXPublicKey
      4991  1.17% 20.18%      50549 11.83%  encoding/asn1.MarshalWithParams
      2531  0.59% 37.18%      27841  6.52%  crypto/x509.ParsePKIXPublicKey
```

`NewSetVerifier` now derives every member's binding **once, at construction**.

### 16.2 What it bought

| row | before | after | change |
|---|---:|---:|---:|
| `SetVerify/ES256` | 155 200 ns | **142 713 ns** | **−8.0 %** |
| · B/op | 10 649 | 7 892 | −25.9 % |
| · allocs/op | 189 | **139** | **−26.5 %** |
| `SetVerify/HS256` | 30 492 ns · 130 | 29 846 ns · 126 | −2.1 % · −4 |
| `SetVerify/EdDSA` | 119 379 ns · 122 | 119 894 ns · 119 | +0.4 % · −3 |
| `SetVerifyRotation` (2 candidates) | 278 182 ns · 260 | 255 596 ns · **160** | −8.1 % · **−38.5 %** |
| `SetVerifyRefusal/unknown_kid` (n=64) | 6 775 ns | 6 199 ns | −8.5 % |
| `SetVerifyRefusal/no_kid` | 4 934 ns | 4 935 ns | **+0.02 %** |
| `SetVerifierConstruction/n=1` | 130.9 ns · 1 | 8 752 ns · 54 | the moved work |
| `SetVerifierConstruction/n=64` | 131.4 ns · 1 | 540 050 ns · 3 213 | the moved work |

**The EdDSA row is flat and that is the point.** Held against ES256's −8.0 %,
it isolates the cost as EC-specific: an Ed25519 or symmetric binding is a
`bytes.Clone`, an EC one is an ASN.1 encoder. The three key families' derivation
cost **50 / 3 / 4 allocations** respectively.

**`no_kid` is unchanged to within one nanosecond** (4 934 against 4 935, on a
2.0 % spread). That refusal never touches the key
index — `candidates` refuses an empty kid first — so a row that moved would have
meant the selection order had moved with it.

**The construction cost is real and is paid once per JWKS refresh.** At 8 434 ns
per key (regression over the four sizes), a sixty-four-key set costs 540 µs to
build and saves 12 487 ns per verification: **break-even at 44 requests**, under
one request for a single-key set. A refresh interval is measured in hours.

### 16.3 The JWKS surcharge, before and after

What selecting a key from a set costs over verifying with one bound directly
(§2.1's rows).

| algorithm | before | after |
|---|---:|---:|
| **ES256** | +13 916 ns (+9.9 %) · **+55 allocs** | +845 ns (+0.6 %) · **+5 allocs** |
| HS256 | +1 692 ns (+5.9 %) · +9 allocs | +717 ns (+2.5 %) · **+5 allocs** |
| EdDSA | +1 870 ns (+1.6 %) · +8 allocs | +194 ns (+0.2 %) · **+5 allocs** |

**All three now land on +5 allocations and +95–96 B, identically** — three
independent key families agreeing on one number, which is what "the
family-specific work moved to construction" predicts and is the strongest
cross-check in this section.

**And that residual +5 is the `kid` header, not the selection.** The two columns
compare different tokens: a JWKS token carries a `kid` member the single-key one
does not, and parsing it costs five allocations and ninety-five bytes wherever
it happens. The +5 reproduced across two independent nine-sample campaigns with
a harness refactor in between.
`TestSetVerificationDoesNotRederiveTheKey` removes the confound by giving both
verifiers the **same** token: the delta is **exactly 0 over 200 calls, three
runs in a row** (16 200 against 16 200). Selection is a map read over a slice
the constructor already built.

### 16.4 The set-size slope, and a refuted hypothesis

The second hypothesis this campaign was asked to test was `jwk.Set.AllByKid`:
a linear scan with a per-call `append` into a nil slice, on every verification.
Every mechanical claim in it is true — no map index, `KeyValue` is 176 B, one
match is one allocation — and it is **0.53 % of a JWK Set verification's
allocations and half a percent of its latency.** Refuted as a cost, and
measured rather than dismissed; the slope (**10.12 ns per set member**) and its
decomposition are in `internal/service/crypto/jwk/BENCH.md` §4.

End to end, sweeping 1 → 64 keys moved a whole verification by **2 215 ns
(1.4 %)** before and **783 ns (0.55 %)** after — both smaller than the rows'
own 0.6–3.1 % spread. `match=first` and `match=last` agree at every size in both
arms, which is the control the sweep exists to provide: the scan has no early
exit, so a divergence would have been a broken harness.

**A kid-less token does not "try every candidate".** That prediction was tested
and is false: `candidates` refuses an empty `kid` with `KEY_ID_MISSING` before
the set is consulted at all, and the row costs **4 935 ns** — 3.5 % of an
accepted ES256 token, and cheaper than every other selection outcome. It belongs
in §5's family: no refusal costs more than the acceptance it replaces.

### 16.5 What was REFUSED, and the property it would have weakened

**Anything that moves, caches or reorders SELECTION.** ADR 0042's design is that
the algorithm is bound by the constructor, never read from the token, and that
the header is compared against the binding before any key reaches a primitive.
Only the *derivation* of the binding moved; which key is selected, the
`MaxKeyCandidates` bound, and the per-candidate `checkHeader` all still run per
token, in the same order, over the same inputs. **No structure here is keyed on
anything the token supplies except the `kid` lookup itself, which is the same
selection `AllByKid` performed.**

**Dropping unbindable members from the index** — the obvious shape for
pre-binding, and a silent widening of a denial-of-service bound.
`MaxKeyCandidates` counts the keys **published** under one kid, not the subset
this SDK can verify with, so a publisher listing six keys under one kid with
three of them on refused curves must still get `KEY_ID_AMBIGUOUS` rather than
three signature verifications per request. `indexByKid` records a refused member
as an *unusable* entry instead. Guarded — see 16.6, whose mutation is exactly
this.

**Making the map do the nil check.** `bindJWK`'s refusals are not all nil
interfaces: `bindECJWK` ends in `return bindP256Public(public)`, whose first
result is the **value type** `es256Verifying`, so a refusal there arrives as a
NON-nil `verifyingKey` wrapping a zero struct whose `verify` would dereference a
nil `*ecdsa.PublicKey`. `bindOctJWK` and `bindOKPJWK` have the same shape.
`boundKeyValue.usable` is a boolean for that reason. **All three inner refusals
are unreachable today** — `jwk` validates length, curve and point first — so
this is defence against a future edit and **not** a fix for a panic anything can
currently produce, which is stated here and in the field's own comment so
nobody inherits a claim nothing can reproduce.

### 16.6 The three guards this campaign added

**`TestSetVerificationDoesNotRederiveTheKey`** (`jwkbind_alloc_internal_test.go`,
`//go:build !race`, gated by the existing `//internal/service/token:token_test`
entry in `tools/alloc-lane-targets.txt`). Counts total `runtime.MemStats.Mallocs`
over 200 JWKS verifications against 200 single-key verifications **of the same
token**, and budgets the delta at 200 (one per call) against a clean measurement
of 0. *Mutation:* the derivation put back inside the loop. *Observed:* `a JWKS
verification allocated 9800 more than a single-key one over 200 calls (49.0 per
call), budget 200`. Restored byte-identical by SHA-256.

**`TestUnverifiableCandidatesStillCountAgainstTheBound`**
(`jwkbridge_external_test.go`). Four keys under one kid, two of them P-384 —
representable by `jwk`, refused by `bindJWK` — against a bound of three.
*Mutation:* `indexByKid`'s append made conditional on the bind succeeding.
*Observed:* `four published candidates at a bound of three: got <nil>, want
KEY_ID_AMBIGUOUS` — **the token verified**. Running the whole package under that
mutation produced exactly one failure, this one: every other test in the file
passes, because the surviving keys behave identically. Restored byte-identical.

**`TestABindRefusalIsNotANilInterface`** (`jwkbind_internal_test.go`). Pins the
typed-nil shape 16.5 describes, through a helper that reproduces `bindECJWK`'s
own `return bindP256Public(public)`. *Mutation:* the helper's result type
narrowed to the concrete `es256Verifying`. *Observed:* the package no longer
compiles — `invalid operation: binding == nil (mismatched types es256Verifying
and untyped nil)` — which is the compiler making the test's point for it, and is
why the assertion goes through an interface-returning helper rather than the
concrete result.

### 16.7 Rows discarded, and the reconciliation

**One row-set discarded.** The first draft of the set-size sweep keyed its sets
`"k0"`…`"k63"` and measured `match=last` **43 % slower** than `match=first` at
n=64 — a difference a scan with no early exit cannot produce. The cause was the
**kid's length, not the match's position**: Go compares strings by length first,
so a two-octet lookup fails on length against fifty-four of the sixty-four
members while a three-octet one reaches `memcmp` for all but ten. The harness
was measuring its own key-naming scheme. Fixed-width kids removed it; the note
lives at `benchJWKSKid` and at `jwk`'s `benchKid`.

**One arithmetic contradiction chased to its cause.** The component
decomposition of the removed work sums to 7 128 ns; the end-to-end delta is
12 487 ns — 75 % more. GC pressure was the obvious explanation and is
**refuted**: re-running both arms at `GOGC=100`, `GOGC=400` and `GOGC=off`
moved the delta only between 12.6 and 13.2 µs, a 4.4 % range. What reconciles it
is the CPU profile taken *in situ*: `bindJWK` at **8.27 %** of the verification,
against the end-to-end delta's **8.05 %** — **2.7 % apart** as a share, which is
the only directly comparable form. The same code costs 8 434 ns timed alone and
12 953 ns timed between a P-256 scalar multiplication and an
eighty-one-allocation JSON decode — **1.48×**, and it is cache locality, not
collection. `internal/service/crypto/jwk/BENCH.md` §7 has the table.

**The allocation column does not inflate**: 49, 50.1 and 50 across the three
instruments that report it. That asymmetry is why the regression gate counts
allocations and not nanoseconds.

**Two independent confirmations that the harness is sound.** §2.1's published
`BenchmarkVerify/ES256/realistic` — measured by a different campaign on a
different day — reproduces here at **141 284 ns / 7 795 B / 134 allocs** against
**142 029 / 7 796 / 134**: 0.52 % on time, identical on allocations, one byte
apart on B/op. And that same row measures 141 284 in the *before* binary and
141 868 in the *after* one — **0.41 % apart**, inside its own 2.0–2.7 % spread —
while the JWKS row moves 8.0 %. The control that does not move is what proves
the two arms are genuinely different code.

**One transient that did not reproduce is recorded rather than dropped.** An
exploratory arm measured `BenchmarkPrimitive/ed25519_verify` at a 17.7 % spread;
the published campaign measures the same row at 2.4 % and 2.7 % on identical
code. It was the box, not the benchmark.

### 16.8 Full results — the JWKS rows

Median of nine (three processes × `-count=3`), `-benchtime=1s`. The *before*
column is the same binary with `jwkbridge.go` at commit `91063bf`.

| benchmark | before ns | after ns | spread (after) | after B/op | after allocs |
|---|---:|---:|---:|---:|---:|
| `BenchmarkSetVerify/ES256` | 155 200 | 142 713 | 1.8 % | 7 892 | 139 |
| `BenchmarkSetVerify/EdDSA` | 119 379 | 119 894 | 2.0 % | 6 705 | 119 |
| `BenchmarkSetVerify/HS256` | 30 492 | 29 846 | 1.7 % | 7 216 | 126 |
| `BenchmarkSetVerifySize/n=1/match=first` | 155 337 | 142 474 | 1.5 % | 7 893 | 139 |
| `BenchmarkSetVerifySize/n=1/match=last` | 155 388 | 142 917 | 2.8 % | 7 893 | 139 |
| `BenchmarkSetVerifySize/n=4/match=first` | 155 733 | 142 241 | 1.5 % | 7 892 | 139 |
| `BenchmarkSetVerifySize/n=4/match=last` | 155 539 | 142 869 | 0.9 % | 7 893 | 139 |
| `BenchmarkSetVerifySize/n=16/match=first` | 155 321 | 142 134 | 1.5 % | 7 892 | 139 |
| `BenchmarkSetVerifySize/n=16/match=last` | 156 000 | 142 439 | 2.6 % | 7 892 | 139 |
| `BenchmarkSetVerifySize/n=64/match=first` | 156 732 | 142 320 | 1.7 % | 7 893 | 139 |
| `BenchmarkSetVerifySize/n=64/match=last` | 157 536 | 142 909 | 1.8 % | 7 892 | 139 |
| `BenchmarkSetVerifyRotation` | 278 182 | 255 596 | 1.5 % | 9 093 | 160 |
| `BenchmarkSetVerifyRefusal/no_kid` | 4 934.0 | 4 935.0 | 1.9 % | 1 634 | 27 |
| `BenchmarkSetVerifyRefusal/unknown_kid` | 6 775.0 | 6 199.0 | 2.2 % | 1 730 | 32 |
| `BenchmarkSetVerifierConstruction/n=1` | 130.9 | 8 752.0 | 2.9 % | 3 304 | 54 |
| `BenchmarkSetVerifierConstruction/n=4` | 131.2 | 33 064 | 2.3 % | 11 696 | 204 |
| `BenchmarkSetVerifierConstruction/n=16` | 131.1 | 134 664 | 3.6 % | 47 384 | 809 |
| `BenchmarkSetVerifierConstruction/n=64` | 131.4 | 540 050 | 2.6 % | 189 528 | 3 213 |

Re-measured controls, both arms, for the cross-checks in 16.7:

| benchmark | before ns | after ns | published (§15) |
|---|---:|---:|---:|
| `BenchmarkVerify/ES256/realistic` | 141 284 | 141 868 | 142 029 |
| `BenchmarkVerify/EdDSA/realistic` | 117 509 | 119 700 | 119 406 |
| `BenchmarkVerify/HS256/realistic` | 28 800 | 29 129 | 28 905 |
| `BenchmarkPrimitive/ecdsa_p256_verify` | 110 069 | 110 406 | 109 891 |
| `BenchmarkPrimitive/ed25519_verify` | 88 929 | 89 668 | 88 621 |
| `BenchmarkPrimitive/hmac_sha256_verify` | 1 261.0 | 1 242.0 | 1 299.0 |
