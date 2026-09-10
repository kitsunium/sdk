# ADR 0066 — third-party compressors: zstd and s2, one library, one package, and a ceiling that cannot be omitted

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0014](0014-sdk-transform-crypto-ports-config-topology.md) §Deferred (the `core/transform` port and the deferred vendor compressors), [ADR 0012](0012-logger-writer-registry.md) (the `third-party/` quarantine policy), [ADR 0022](0022-sdk-codec-hcl.md) + [ADR 0034](0034-hcl-quarantine-rationale-corrected.md) (quarantine, and its mechanism corrected), [ADR 0023](0023-sdk-schema-codecs.md) (opt-in codecs under `third-party/`), [ADR 0018](0018-sdk-cross-platform-portability.md) (the build bar and the `x/sys` ban), [ADR 0030](0030-stdout-is-a-protocol-channel.md) + [ADR 0048](0048-sdk-metrics-otlp-json.md) (what an import may and may not arm), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (clamp vs refuse)
- **Closes**: ADR 0014 §Deferred "zstd / snappy / s2 compressors — DEFER"

## Context

`internal/service/transform` ships gzip, flate and zlib over the standard
library. ADR 0014 deferred the vendor compressors in one line — *"First MR is
stdlib gzip/flate; vendor compressors land as opt-in follow-ups behind their own
imports"* — and named three: **zstd / snappy / s2**. This ADR closes that
deferral.

The ticket that reopened it proposed **zstd, brotli and lz4**, and justified
quarantining them by quoting ADR 0022's reasoning: that `hcl/v2` *downgrades*
`golang.org/x/sys`.

**That justification is dead and must not be repeated.** ADR 0034 retired it:
under minimal version selection the selected version of a module is the
*maximum* required across the graph, so a dependency asking for a lower version
cannot lower a higher requirement that is still present. ADR 0034 replaced it
with two criteria that do apply, and this ADR is the first placement decision
argued against them from scratch:

1. does the dependency **introduce a banned module** (`golang.org/x/sys`, or
   anything that transitively pulls it) into a module that forbids it?
2. does it **impose a dependency graph on consumers who never use the
   integration**?

Criterion 1 is what quarantines HCL. It turns out **not to apply to any of the
three candidates**, which is exactly why ADR 0034 insists a placement argument
show its measurement instead of reciting a rule.

## The measurement

Go 1.27.1, `GOWORK=off`, fresh probe module per library, `CGO_ENABLED=0`.

| | `klauspost/compress` | `andybalholm/brotli` | `pierrec/lz4/v4` |
|---|---|---|---|
| version measured | v1.19.2 (selected) / v1.20.0 | v1.2.3 | v4.1.29 |
| module requires (`go mod graph`) | **none** | `xyproto/randomstring` (test-only; absent from `go list -deps`) | **none** |
| introduces `golang.org/x/sys` | **no** | **no** | **no** |
| cgo (`go list -deps -json` → `CgoFiles`) | **none — pure Go** | none of its own | **none — pure Go** |
| cross-builds on the ADR 0018 matrix | **10 / 10** | 10 / 10 | 10 / 10 |
| binary size delta over a trivial `main` | **+0.55 MiB** | **+2.75 MiB** | +0.08 MiB |
| drags `net/http` into the build graph | no | **yes** (its `http.go`) | no |
| licence | BSD-3-Clause + Apache-2.0 + MIT (upstream mix, permissive) | MIT | BSD-3-Clause |

The `x/sys` question deserves one warning, because the obvious check produces a
false positive. `go list -deps` on the brotli probe prints
`vendor/golang.org/x/sys/cpu` — but every `vendor/…`-prefixed entry there is the
**standard library's own vendored copy**, pulled in because brotli imports
`net/http`. It is not a module requirement and it does not appear in `go.mod` or
`go mod graph`. A grep for `x/sys` over `go list -deps` output would have
rejected brotli for a reason that is not true.

**No candidate is disqualified on dependencies.** So the decision is made on
criterion 2 and on numbers.

### The numbers

4 MiB corpora, `-benchtime=1s`, `-cpu=1`, best of three runs on a contended
8-core VM. Full report in `third-party/transform/BENCH.md`.

| Scheme | JSON ratio | compress MB/s (json / random) | decompress MB/s (json / random) | allocs (comp / decomp) |
|---|---:|---:|---:|---:|
| gzip (stdlib, the reference) | 6.02 | 94 / 1 093 | 231 / 1 272 | 29 / 207 |
| **zstd** fastest | 5.75 | **234** / 955 | **775** / 2 736 | 17 / **1** |
| zstd better | **6.06** | 86 / 447 | 948 / 2 875 | 15 / 1 |
| **s2** | 3.39 | **713** / **3 837** | **1 693** / **5 104** | **1** / **1** |
| brotli level 4 | 6.49 | 33 / 98 | 180 / 814 | — |
| lz4 fast | 3.68 | 316 / 1 997 | 741 / 804 | — / 36 |

Three results are worth stating because each reverses a common expectation.

**zstd's win is speed, not ratio.** On log-shaped JSON it is 4.5 % *worse* than
stdlib gzip at `ZstdFastest`, and only edges ahead at `ZstdBetter` — where it
compresses slower than gzip. What it buys is 2.5× gzip's compression throughput,
3.4× its decompression, and **1 allocation to decode against gzip's 207**.

**In Go, lz4 does not decompress faster than zstd.** The received wisdom from
the C implementations is inverted here: 741 MB/s against zstd's 775 on text, and
804 against 2 736 on entropy. `klauspost`'s zstd decoder is assembly-optimised;
`pierrec`'s lz4 is not. lz4's only remaining edge is compression throughput, and
even that is beaten — see D2.

**brotli loses on every performance axis, including to stdlib gzip.** 33 MB/s
compressing is 3.1× slower than gzip; 180 MB/s decompressing is *slower* than
gzip's 231. Its ratio at level 6 (6.81) is the best measured, at roughly seven
times zstd's CPU for four percent of bytes.

## Decision

### D1 — one library: `github.com/klauspost/compress`, giving zstd **and** s2

Taken. It requires **no other module**, is pure Go, needs no cgo, cross-builds
on all ten CI cells, and costs +0.55 MiB of binary. It supplies both schemes the
SDK needs, so the "how many dependencies" question and the "how many algorithms"
question have different answers: **one, and two.**

`zstd` covers ratio-with-speed. `s2` covers latency and throughput: 7.6× gzip's
compression, 7.3× its decompression, one allocation on each side, and 8 bytes of
overhead on 4 MiB of incompressible input.

### D2 — lz4 is refused, and s2 is why

`pierrec/lz4/v4` is clean on every dependency criterion. It is refused on
measurement. Against s2 — which arrives at **zero marginal dependency cost**
inside the library D1 already takes — lz4 wins one axis and loses three:

| | lz4 | s2 |
|---|---:|---:|
| JSON ratio | **3.68** | 3.55 |
| compress, json / random (MB/s) | 316 / 1 997 | **706** / **3 584** |
| decompress, json / random (MB/s) | 741 / 804 | **1 427** / **3 133** |
| allocations per decompress | 36 | **1** |

A 3.7 % ratio advantage does not buy a whole module when the alternative is
roughly twice as fast in both directions and allocates once.

### D3 — brotli is refused, and the trigger to revisit it is named

brotli is the only candidate whose value is a **wire format** rather than a
performance point: `Content-Encoding: br` (RFC 7932) is what every browser puts
in `Accept-Encoding`, and no other scheme can answer it. That argument is real,
and it is currently unusable: **the SDK has no content negotiation anywhere.**
`grep -rn 'Accept-Encoding\|Content-Encoding' internal/ pkg/ third-party/`
returns four hits, all of them prose in `internal/service/transform`'s docs
explaining that HTTP's `deflate` means zlib. There is no negotiator to hook a
`br` encoder to.

Against that, it is 3.1× slower than stdlib gzip to compress and slower than
gzip to decompress, costs **+2.75 MiB of binary**, and drags `net/http` into the
build graph of anything that links it.

Refused. **Revisit when the `net` domain grows `Accept-Encoding` negotiation** —
and when it does, brotli gets its **own package and its own `PP` range**, not a
slot beside zstd, because its 2.75 MiB must not be linked by a consumer who came
for zstd.

### D4 — the ceiling is a required positional parameter, and zero is refused

Every constructor is `New…Compressor(…, maxDecompressedBytes int64)`. The bound
is **not** a functional option, and the package has no `Option` type at all.

The reason is mechanical rather than stylistic: **an option can be omitted, and
a security bound that can be omitted is not mandatory.** A positional parameter
cannot be forgotten, cannot be lost in a refactor that drops a variadic, and
cannot be absent from an example someone copies.

A non-positive value is **refused** at construction (`LIMIT_MISCONFIGURED`,
`0.3.63.4`) — ADR 0031's refuse half, on the same reasoning ADR 0052 used for a
lock TTL. Zero's two natural readings, "no limit" and "refuse everything", are
opposites; a caller who typed it meant one of them, and defaulting silently
grants exactly what the other reader wanted forbidden. So no compressor that
would decompress unbounded ever exists to be called.

`ZstdLevel`, in the same package, takes the other half and **clamps**: every
level round-trips the caller's bytes identically, so a clamped level costs ratio
or CPU and never correctness. Both halves of ADR 0031 sit side by side here on
purpose, and the contrast is the documentation.

The default the registered singletons carry is **64 MiB**, deliberately tighter
than the 256 MiB backstop the stdlib schemes use. That is arithmetic: DEFLATE's
maximum expansion is roughly 1 032:1, so reaching 256 MiB through gzip costs an
attacker about 254 KiB of upload, while the two fixtures in this package's tests
reach 64 MiB from **13 KiB** (zstd) and **28 bytes** (s2). The number that
matters is what an attacker spends, not what the format is called.

### D5 — the bomb refusal has its own error code, and both refusals were measured

`DECOMPRESSION_LIMIT_EXCEEDED` (`0.3.63.3`) is a **separate** code from
`ZSTD_FAILED` / `S2_FAILED`. "Someone sent bytes we could not parse" and
"someone sent bytes engineered to exhaust this process" are different events for
whoever reads the logs: the first is traffic, the second is an attack signature
worth alerting on. Sharing a code makes the second invisible inside the first,
and an alert built on the shared code fires on every scanner that pokes the
endpoint until nobody looks at it. `TestCorruptInputIsNotReportedAsABomb` pins
the converse.

Where each refusal happens was measured, not assumed:

- **zstd** refuses inside the decoder, from `WithDecoderMaxMemory` — either
  because the frame header declares a window past the ceiling
  (`ErrWindowSizeExceeded`) or because the running decode passes it
  (`ErrDecoderSizeExceeded`). Both are the ceiling speaking and both map to the
  bomb code; reporting the first as a corrupt stream would file the loudest
  signature under the quietest label.
- **s2** refuses in this package, from `s2.DecodedLen` — the block format
  carries its decoded length as a varint header, so the bomb question is
  answered by reading a handful of bytes **before any allocation**.

The sharp case is `TestZstdRefusesMultiFrameBomb`. A zstd stream may carry many
concatenated frames and `DecodeAll` decodes all of them, so a ceiling enforced
**per frame** would accept 64 frames each individually under the bound and
materialise 64× it without ever violating it — the classic way an output bound
is bypassed rather than broken. Measured: 64 frames, 6 912 compressed bytes
claiming 32 MiB against a 1 MiB ceiling, **refused**. The bound is enforced
across the whole call.

A refusal returns `dst` at its original length. `TestBoundaryIsTheDeclaredSize`
drives the edge in both directions, and `TestRegisteredSingletonsAreBounded`
proves the guard is not something a caller must remember to switch on.

### D6 — registering from an import is legitimate here, and the reason is not "the codecs do it"

ADR 0030 refuses an SDK default that writes to `os.Stdout`. ADR 0048 refuses
registering the OTLP/HTTP emitter, one step further: *"arming a network client
from an import is worse than the stdout hazard"*. Both refusals are about an
import **acquiring a resource, or emitting on a channel the process shares**.

A registry insertion is neither. It opens no descriptor, starts no goroutine,
writes nothing, and only makes `transform.Lookup("zstd")` succeed where it
previously failed. The caller still has to ask for the algorithm by name.

The specific risk this package could have carried was checked rather than waved
away, because these schemes — unlike every codec and every stdlib compressor —
register **stateful** singletons holding a shared encoder and decoder. Measured:
building a zstd `Encoder` and `Decoder` acquires **zero goroutines** and 8 272 B
+ 3 792 B of heap, with the window buffers allocated lazily on first use. Had
construction spawned a `GOMAXPROCS`-sized pool at import, the answer would have
been different, and the honest resolution would have been a constructor-only
package with no registered singleton.

Registration uses a package-level `var`, never `init()`, per `core/transform`'s
documented convention.

### D7 — one package, `third-party/transform`, owning `0.3.63.*`

Both schemes come from one library, so they land in one package holding one
`PP` slot — the same shape `internal/service/transform` has for its three stdlib
schemes. Rule 3 allows one package per `MM.LL.PP` and this respects it without
claiming ranges the change does not need.

Codes: `0.3.63.1` `ZSTD_FAILED`, `0.3.63.2` `S2_FAILED`, `0.3.63.3`
`DECOMPRESSION_LIMIT_EXCEEDED`, `0.3.63.4` `LIMIT_MISCONFIGURED`.

### D8 — neither scheme gets a `pkg/v1/codec` frame id

`pkg/v1/codec/compressed.go` freezes a 1-byte `algID` per scheme (`gzip=0x01`,
`flate=0x02`) and exports only `Gzip` and `Flate`. Neither scheme here is added
to that table, so `MarshalCompressed(f, "zstd", v)` returns
`UnknownCompressor` — which is the documented behaviour for an algorithm with no
frame id, and the situation `zlib` has been in since ADR 0014 for the stated
reason that allocating a new id changes a wire format frozen post-v1.0.0 and is
a `pkg/v1` decision, not a scheme package's.

One consequence should be on the record for whoever revisits it: the frame
layer's decompression-bomb guard bounds expansion at **1 000:1**, a figure
DEFLATE can barely reach and which both schemes here clear by three orders of
magnitude on repetitive data. Framing zstd without revisiting that ratio would
make the guard start refusing legitimate traffic.

## Consequences

- `github.com/klauspost/compress` moves from indirect to **direct** in the root
  `go.mod`, at the version MVS already selected (`v1.19.2`, via
  `clickhouse-go`). No other `go.mod` changes and the selected version of
  nothing changes. `MODULE.bazel`'s `use_repo` block gains
  `com_github_klauspost_compress`.
- **Isolation is verifiable, not asserted.** `GOWORK=off go list -deps ./...`
  run in each of `internal/kernel`, `internal/core`, `internal/service` and
  `pkg` returns **zero** hits for `klauspost`, `andybalholm` or `pierrec`. That
  command is the acceptance test for criterion 2 and belongs in any future
  `third-party/` placement argument.
- `internal/service/transform`'s "Do NOT add a vendor compressor here" entry and
  its "zstd / snappy / s2 are deferred" sentence are now satisfied rather than
  outstanding; both documents are corrected in this change (rule 11).
- The SDK gains two algorithms and no new module in any consumer's graph.
- ADR 0014's deferral of **snappy** is closed by substitution rather than by
  implementation: `s2` reads Snappy streams, and the package deliberately does
  **not** register under the name `"snappy"` (see §Why not).

## Why not

- **Take all three libraries as the ticket proposed.** Rejected on measurement,
  not on principle: lz4 is beaten on three of four axes by a scheme that costs
  no extra module (D2), and brotli has no consumer to serve and costs 2.75 MiB
  to have none (D3). Shipping a scheme nobody can reach for is how a dependency
  budget erodes without a single bad decision.
- **Refuse all three and stay stdlib-only.** Rejected: the decompression column
  is not close. 3.4× and 7.3× gzip's throughput, at 1 allocation against 207, is
  the difference a fleet feels, and `third-party/` exists precisely so that
  difference is available without being imposed.
- **Put the schemes in `internal/service/transform` beside gzip.** Rejected on
  criterion 2. The library introduces no banned module, so criterion 1 would not
  have stopped it — but `internal/service` is required by `pkg`, and `pkg` is
  the module consumers actually take. Adding it there would put a compression
  library in the graph of every consumer who only wanted a logger.
- **Register `s2` under the name `"snappy"`.** Rejected: `s2.Encode` emits
  extensions a Snappy decoder does not read, so the name would promise interop
  the bytes do not honour. That is the mistake HTTP made with `deflate`, which
  `internal/service/transform` documents at length; repeating it with a new name
  in the same domain would be indefensible.
- **Give `s2` a level knob.** Rejected: its reason for existing is throughput,
  and the levels that buy ratio (`EncodeBetter` / `EncodeBest`) land in zstd's
  territory at zstd's cost. A caller who wants that trade should name zstd
  rather than reach it sideways from a scheme chosen for speed.
- **Make the ceiling a functional option with a safe default.** Rejected — D4.
  It would be strictly more ergonomic and strictly less safe.
- **Use one error code for both a corrupt stream and a tripped ceiling.**
  Rejected — D5. The two events have different readers and different responses.
- **Ship zstd dictionaries.** Deferred by name. They are the single biggest
  ratio win available for small homogeneous payloads, and they are also a shared
  mutable artefact that must be versioned with the data it was trained on —
  a domain decision, not a scheme detail.
- **Expose the streaming APIs.** Deferred. `core/transform.Compressor` is a
  whole-buffer port; a streaming compressor is a port change (an ADR 0039
  sibling interface), not a scheme addition, and it would need its own bomb
  story because a stream has no declared output length to check.

## References

- ADR 0034 §Decision.3 (the two placement criteria) · ADR 0022 §Context.1 (the
  retired mechanism) · ADR 0014 §Deferred · ADR 0012 · ADR 0018 · ADR 0030 ·
  ADR 0048 · ADR 0031 · ADR 0052 (the TTL refusal this D4 mirrors)
- `third-party/transform/BENCH.md` — full tables, the choice table, and the
  pprof behind the one optimisation
- `third-party/transform/CLAUDE.md` — surface, conventions, rejected candidates
- [RFC 8878](https://www.rfc-editor.org/rfc/rfc8878) (Zstandard) ·
  [RFC 7932](https://www.rfc-editor.org/rfc/rfc7932) (Brotli, for the refusal) ·
  [Go modules reference — minimal version selection](https://go.dev/ref/mod#minimal-version-selection)
