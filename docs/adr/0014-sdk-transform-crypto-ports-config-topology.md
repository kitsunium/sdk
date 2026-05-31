# ADR 0014 — The verb wave: transform tier, new crypto ports, config-driven topology

- **Status**: Accepted
- **Date**: 2026-05-30
- **Deciders**: SDK maintainers
- **Related**: ADR 0001 (multi-module layout), ADR 0003 (codec registry — the pattern mirrored), ADR 0005 (dotted-quad codes), ADR 0010 (recycler), ADR 0011 (snapshot.Value), ADR 0012 (writer registry), ADR 0013 (crypto domain — the ports extended here)
- **Amends**:
  - the `internal/core` purpose statement — admits a **fifth** sibling, `transform`, beside `codec` / `writer` / `crypto` / `logger`;
  - ADR 0012 — adds `logger.FromConfig` + the `ConfigDecoder` writer-extension hook, and widens the logger-middleware purpose to admit a **transform-the-bytes** category (encrypt-at-rest, fan-out/spill) beside route / sample / fan-out / failover;
  - ADR 0013 — admits three new intra-domain crypto ports (`MAC`, `Agreement`, `StreamSealer`), the `KeyEnvelope` / `KeyTree` compositions, the streaming-AEAD wire freeze, and the `pkg/v1/hash` non-oracle public-digest verification carve-out;
  - the ADR 0005 §Registry allocation table — new blocks `0.1.5.*` (kernel/batcher), `0.2.5.*` (core/transform), `0.2.4.15`–`0.2.4.19` (core/crypto extension), service `0x1c`–`0x1f`, and `1.1.0.4` (pkg/v1/logger).

## Context

The SDK tells one story: **pick a verb, get every implementation behind it.**
`codec.Marshal(format, v)` is one verb over thirteen wire formats;
`crypto.Seal(key, pt, aad)` is one verb over a frozen self-describing box;
`logger` writers resolve names to `Sink`s through a registry. The consumer
learns the verb once; the SDK absorbs the combinatorial *how*. Every capability
is a dep-light type-alias facade over the strict kernel → core → service →
pkg/v1 stack, with typed dotted-quad errors and heavy deps quarantined under
`third-party/*`.

Three places the verb currently runs out of road:

1. **Bytes at scale.** `Seal` is whole-buffer only — a multi-megabyte batch
   holds two plaintext copies in memory. There is no streaming AEAD, and the
   fastest fingerprint tier (BLAKE3, xxh3) is not wired behind the shipped
   `Hasher` verb.
2. **Bytes you shrink, key, derive, authenticate.** Compression cannot be
   modelled without breaking the frozen `any→[]byte` codec contract; there is
   no detached-MAC verb though `crypto/hmac` is unused; the `Deriver` (HKDF)
   has no upstream key-agreement; the symmetric `Key` has no password-wrapped
   at-rest form; `Subkey` separation is flat, not path-addressed.
3. **Writers that batch / rotate / encrypt / spill / reconfigure.** The writer
   subsystem is AWS-only batching copy-paste with no generic lifecycle, no
   rotating file sink, no encrypt-at-rest seam, and — despite ADR 0012's
   *"config-driven"* title — no string/map-driven topology.

`internal/core/CLAUDE.md` forbids a **fifth** core sibling without first
widening the layer purpose via an ADR (the gate ADR 0012 cleared for `writer`,
ADR 0013 for `crypto`). The new crypto ports are intra-domain (the `crypto`
sibling already exists) so they are an ADR *amendment*, not a new sibling.

**The hard delivery constraint:** all of this lands in **one branch,
`feat/writer-subsystem-v2`, as one MR** — fighting the SDK's one-concern-per-PR
norm head-on. This ADR is the contract that makes a single, build-green-at-every-
commit MR honest rather than reckless: it freezes the wire formats and code
allocations *before* any code, so the fourteen stacked commits each land against
a stated decision rather than smuggling one.

## Decision

### D1 — `core/transform`: a fifth core sibling for byte transforms

Compression is modelled as a **parallel registry**, never a codec `Format`.
Adding `gzip` as a `Format` would break the frozen `any→[]byte` contract and the
append-only Format set. Instead `internal/core/transform` declares a
`Compressor` port and a `snapshot.Value`-backed registry mirroring `codec`:

```go
package transform
type Algorithm string // "gzip", "flate" — frozen per scheme like codec.Format
type Compressor interface {
    Algorithm() Algorithm
    Compress(dst, src []byte) ([]byte, error)
    Decompress(dst, src []byte) ([]byte, error)
}
func Register(c Compressor) Compressor
func Lookup(a Algorithm) (Compressor, bool)
```

The `internal/core` purpose statement widens to: *core declares the contracts
for codecs, the logger, log-transport writers, cryptographic schemes, **and byte
transforms***. Compression verbs live at `pkg/v1/codec` as **explicit verbs**,
never a Format mutation:

```go
func MarshalCompressed(f codec.Format, a transform.Algorithm, v any) (box []byte, err error)
func UnmarshalCompressed(box []byte, v any) (err error)
```

**Frozen compressed-frame format** (self-describing, mirrors the AEAD box's
version-byte precedent):

```
[1B magic=0xC7][1B frame-ver=0x01][1B algID][2B innerFormatLen BE][innerFormat bytes][payload]
```

`algID` is frozen per compressor (`gzip=0x01`, `flate=0x02`). `innerFormat` is
the codec Format string used for the inner encode, so `UnmarshalCompressed`
needs no Format argument. A **bounded-decompression guard** (max output size and
max expansion ratio) is mandatory, wired to `CodeCompressedFrameInvalid`, to
defend against decompression bombs. zstd / snappy / s2 are an explicit
follow-up; the first MR ships **stdlib gzip/flate only**, so the `pkg/v1/codec`
module graph stays vendor-free (a test asserts this).

### D2 — three new intra-domain crypto ports (extends ADR 0013)

Each is a new port + registry on the existing `Algorithm` keyspace in
`internal/core/crypto`, with stdlib service emitters and a thin `pkg/v1` facade
— the exact shape of the five ports ADR 0013 already shipped.

**MAC (detached authentication).** Fills the recon #1 gap: the `Hasher` port is
deliberately unkeyed and routes keyed integrity to "AEAD/signatures", yet no
detached-MAC verb exists and `crypto/hmac` is unused.

```go
type MAC interface {
    Algorithm() Algorithm
    Tag(key Key, message []byte) []byte            // total — redacting Key pins KeyLen
    Verify(key Key, message, tag []byte) (ok bool) // hmac.Equal, constant-time
    New(key Key) hash.Hash                          // streaming, parallels Hasher.New
}
```

`Tag`/`Verify` cannot fail (the redacting `Key` rejects bad lengths at
construction), so a **single** lookup-miss code suffices
(`CodeUnknownMACAlgorithm`) — mirroring `CodeUnknownSignatureAlgorithm`. **No
dead `CodeMACFailed`** is registered. `mac.go`'s doc states the inverse of the
Hasher rule: a MAC tag **is** secret-comparison-sensitive — callers route
through `Verify`, never `==`/`bytes.Equal`. Default scheme:
HMAC-SHA256 (`service/crypto/hmacsha2`, stdlib). Facade: `pkg/v1/mac`.

**Agreement (X25519 → HKDF bridge).** Closes the loop: a `Deriver` exists but
nothing establishes the shared secret it consumes. `crypto/ecdh` is stdlib and
unused.

```go
type Agreement interface {
    Algorithm() Algorithm
    GenerateKey() (pub, priv []byte, err error)
    Shared(priv, peerPub []byte) (secret []byte, err error) // raw DH — MUST be HKDF'd
}
// pkg/v1/agree — SharedKey forces HKDF; raw DH is never handed back
func SharedKey(a Algorithm, priv, peerPub []byte, info string) (crypto.Key, error)
```

**Key-hygiene contract:** `SharedKey` returns a redacting `crypto.Key`; the raw
`Shared()` output and the `priv` from `GenerateKey` carry a documented
`Zeroize`-when-done contract. `crypto/ecdh` rejects low-order points;
`CodeAgreementFailed` wraps that via `errs.Wrap`, leaking no key bytes.

**StreamSealer (chunked AEAD).** ADR 0013 deferred streaming AEAD; this turns
the non-goal into a concrete, frozen wire format. The box format (`0x01`) is
untouched.

```go
type StreamSealer interface {
    Algorithm() Algorithm
    Writer(key Key, dst io.Writer, aad []byte) (io.WriteCloser, error) // Close writes the final marker
    Reader(key Key, src io.Reader, aad []byte) (io.Reader, error)      // EOF only after final-chunk auth
}
// pkg/v1/crypto
func SealStream(dst io.Writer, key Key, aad []byte) (io.WriteCloser, error)
func OpenStream(src io.Reader, key Key, aad []byte) (io.Reader, error)
```

**Frozen streaming-AEAD wire format (version `0x02`, disjoint from box `0x01`):**

```
header : [1B stream-ver=0x02][1B algID][16B random salt]
key    : streamKey = HKDF-SHA256(ikm=key.Bytes(), salt=salt,
                                 info="kitsunium/stream-aead/v1")  -> 32B
chunks : fixed 64 KiB plaintext per chunk (final chunk 0..64 KiB)
         nonce_i (12B) = counter_i (11B big-endian) || flag (1B)
                         flag = 0x01 on the final chunk, else 0x00
         wire_i = AES-256-GCM-Seal(streamKey, nonce_i, plaintext_i, aad) // = ct_i || tag(16)
```

This is the well-studied STREAM construction (Hoang–Reyhanitabar–Rogaway,
as used by `age`): the random per-stream salt makes the deterministic counter
nonce safe even when one `Key` encrypts many streams, and the final-chunk
**flag in the nonce** provides truncation resistance — a truncated stream fails
authentication because the reader reaches EOF without ever decrypting a
flag=`0x01` chunk. **Locked decisions:**

1. `Open`/`OpenStream` reject the other format's lead byte (`0x01`↔`0x02`).
2. **Bespoke frame, documented — not age/libsodium interop.** Half-matching an
   interop format is worse than a clean bespoke one; the construction is
   age-*shaped* but the SDK freezes its own `info` string and header, and the
   KAT vectors are SDK-owned (committed in c7), not borrowed.
3. **stdlib AES-256-GCM, not x-crypto ChaCha** — keeps `pkg/v1/crypto`
   dep-light; the counter-nonce construction is GCM-safe. XChaCha streaming is a
   deferred follow-up, so the dep-light path is provably stdlib-only at merge.
4. **Hold-back semantics:** the `Reader` MUST NOT surface decrypted-but-
   unverified plaintext downstream before the chunk's tag verifies; the
   classic streaming-AEAD footgun is closed by construction (GCM verifies the
   whole chunk before returning plaintext) and by never emitting a chunk's bytes
   until its `Open` succeeds.
5. **Counter overflow** (> 2^88 chunks — unreachable in practice; the 11-byte
   counter is sized so it never wraps for any real stream) is a hard error,
   never a silent wrap.

Merge gate (c7): SDK-owned KATs, a reader fuzz target, and `BENCH.md`.

### D3 — intra-domain crypto compositions

**KeyEnvelope (password-wrapped key, PHC-style `$kenv$` grammar).** Gives the
symmetric `Key` the at-rest representation only signature keys had, composing
three shipped ports (password stretch + HKDF + AEAD `Seal` of `Key.Bytes()`).

```go
func WrapKey(passphrase []byte, dek crypto.Key) (envelope string, err error)
func UnwrapKey(passphrase []byte, envelope string) (dek crypto.Key, err error)
```

**Frozen `$kenv$` grammar:** `$kenv$v=1$kdf=<id>$<b64salt>$aead=<id>$<b64box>`.
**Locked:** (1) envelope logic + the `CodeInvalidKeyEnvelope` sentinel live in
the **service emitter** (`service/crypto/keyenvelope`), not the facade. (2)
**Default KDF is pbkdf2-sha256 (stdlib)**; argon2id is reachable only when the
consumer blank-imports the third-party scheme — the existing `pkg/v1/password`
contract; the KEK resolves via the **registry**, never a direct argon2id import,
so x/crypto never leaks into the dep-light path. (3) Wrong passphrase reuses the
existing non-oracle `CodeDecryptionFailed`; the **new** code is for an
unparseable envelope **only** (no key-validation oracle). (4) The transient KEK
is zeroized on **all** paths including errors; envelope / passphrase / salt
never enter `errs`.

**KeyTree (path-addressed HD derivation).** Pure composition over the registered
HKDF `Deriver` — no new crypto, dep, or core port (reuses `CodeDerivationFailed`).

```go
func NewKeyTree(algo crypto.Algorithm, master crypto.Key) KeyTree
func (t KeyTree) Child(segment string) KeyTree     // no error: validate+append only
func (t KeyTree) DeriveKey() (crypto.Key, error)   // 32-byte AEAD Key at this node
```

**Correctness (locked):** (1) **Injective path encoding** — each segment is
length-prefixed in the canonical derivation input, so `Child("a/b").Child("c")`
cannot collide with `Child("a").Child("b/c")`. (2) Derivation model is **re-derive
from master with the full canonical path** as HKDF `info`. (3) Master-secret
ownership across `Child()` is documented (the master `Key` is shared by
reference; `Zeroize` the root owns the lifetime). (4) `DeriveKey()` always
returns a `KeyLen` `crypto.Key`.

### D4 — `pkg/v1/hash` non-oracle public-digest verification carve-out

`pkg/v1/hash/CLAUDE.md` currently forbids any Verify/Equal helper (to avoid a
timing oracle). This ADR permits **public-digest, non-oracle, EOF-typed**
verification only — a content-ID mismatch on a *public* digest reintroduces no
timing hazard:

```go
func NewDigestWriter(a Algorithm, dst io.Writer) (*DigestWriter, error)
func NewVerifyingReader(a Algorithm, src io.Reader, wantHex string) (*VerifyingReader, error)
```

`VerifyingReader` fails the **final** `Read` (EOF) with a typed
`CodeDigestMismatch` iff the digest mismatches — never mid-stream. The sentinel
lives in the **emitter layer** (`service/crypto/stdhash`, `0.2.4.*` block); the
facade only re-introspects via `errs` accessors. BLAKE3 / xxh3 hashers
(`third-party/x-crypto/{blake3,xxh3}`) reuse the existing
`CodeUnknownHashAlgorithm` and carry the same "NOT collision-resistant" warning
the shipped CRC32C / FNV consts carry (xxh3).

### D5 — `logger.FromConfig` + `ConfigDecoder` (capstone)

ADR 0012 is *titled* "config-driven" yet `NewMulti` still takes typed Go
structs. `FromConfig` makes `rotfile` / `encwrite` / `tee` reachable from a
config file with zero Go glue, via a `ConfigDecoder` type-assertion extension
(mirroring codec's `Appender`) — no closed central switch (the anti-pattern that
bit `promote.go`).

```go
type Topology struct { Level string; Writers []WriterEntry }
type WriterEntry struct { Name string; Config map[string]any }
func FromConfig(format codec.Format, raw []byte) (*logger.Logger, error)
// core/writer optional extension, detected by type assertion:
type ConfigDecoder interface { DecodeConfig(map[string]any) (Config, error) }
```

**Dep-light constraint (locked):** `FromConfig` imports ONLY the **core/codec
dispatch surface** (stdlib) and calls `codec.Unmarshal` against codecs the
**consumer already registered**; it MUST NOT blank-import `pkg/v1/codec` or any
service codec — otherwise every `pkg/v1/logger` consumer inherits four vendor
modules. A test asserts `pkg/v1/logger`'s module graph stays vendor-free.
**Secret gate:** S3 / CloudWatch `DecodeConfig` parse credentials from
`map[string]any`; `CodeTopologyInvalid` MUST redact before the map reaches
`Public`/`Private`/`Fields`.

**Logger-middleware purpose widening:** the middleware category admits a
**transform-the-bytes** member alongside route / sample / fan-out / failover —
covering `encwrite` (seal each record via the public `crypto.Seal`, length-
prefixed framing as the default for byte sinks) and `tee` (fan-out with a
spill / dead-letter seam for records *all* primaries reject). `encwrite.Close`
zeroizes the held key + derived per-sink subkey.

### D6 — kernel lifecycle primitives

`kernel/worker` (`Daemon`/`Loop`/`Start`/`Stop`/`Every`) collapses the
byte-identical `stop/stopOnce/done/doneOnce` scaffold across the async drainer
and the s3/cw sinks (three real consumers — clears the ≥2 bar). It emits **no
codes** (pure goroutine control, like `recycler`/`snapshot`). Its contract: a
`Loop` MUST return promptly on `stop`, so the async drainer is rewritten to
`select` on `stop` in the same commit — no latent deadlock ships.

`kernel/batcher` (`Batcher[T]`, `WeightOf`) consolidates the coalesce / flush /
ticker logic duplicated in `cwsink.go` + `s3sink.go`; both sinks are cut over in
the same commit (or it becomes a third copy). Batcher owns its **own**
`time.Ticker` — it does **not** depend on `kernel/worker` (coupling two new
primitives in one commit is needless risk). It emits `CodeBatcherClosed`,
`CodeBatcherDeliverFailed` in the new `0.1.5.*` block.

## Error-code allocation (amends ADR 0005 §Registry)

The authoritative truth is **real hex** in the live tree, not ADR comment-space.
Verified against the tree at this ADR's date:

```text
kernel/batcher — 0.1.5.*  (kernel emits only ring=0.1.3 today; 0.1.5 free)
  0.1.5.1  CodeBatcherClosed          Add/Flush after Close
  0.1.5.2  CodeBatcherDeliverFailed   the deliver closure returned an error

core/crypto — 0.2.4.* extension (0.2.4.1–0.2.4.14 used; .15 next free)
  0.2.4.15 CodeUnknownMACAlgorithm        Tag/Verify of an unregistered MAC
  0.2.4.16 CodeUnknownAgreementAlgorithm  SharedKey of an unregistered scheme
  0.2.4.17 CodeAgreementFailed            crypto/ecdh rejected the peer point
  0.2.4.18 CodeStreamTruncated            streaming Open hit EOF before the final-flag chunk
  0.2.4.19 CodeInvalidKeyEnvelope         unparseable $kenv$ string (NOT a wrong-pass oracle)
  0.2.4.20 CodeDigestMismatch             VerifyingReader EOF digest mismatch (public, non-oracle)

core/transform — 0.2.5.*  (codec=.2, writer=.3, crypto=.4; .5 free)
  0.2.5.1  CodeUnknownCompressor       Lookup/Decompress of an unregistered algorithm
  0.2.5.2  CodeCompressionFailed       the compressor returned an error
  0.2.5.3  CodeDecompressionFailed     the decompressor returned an error
  0.2.5.4  CodeCompressedFrameInvalid  malformed frame OR decompression-bomb guard tripped

service — layer 0x03 (0x00–0x19 used, gap at 0x0c; 0x1a next free, contiguous)
  0.3.26.* (0x1a) service/transform gzip/flate  CodeGzipFailed/CodeFlateFailed (compress/decompress)
  0.3.27.* (0x1b) service/writer/rotfile        CodeRotFileOpenFailed/RotateFailed/WriteFailed
  0.3.28.* (0x1c) service/logger/middleware/encwrite  CodeEncWriteSealFailed/FramingFailed
  0.3.29.* (0x1d) service/logger/middleware/tee       CodeTeeAllBranchesFailed/SpillFailed

pkg/v1/logger — 1.1.0.* extension
  1.1.0.4  CodeTopologyInvalid         FromConfig got a malformed/credential-bearing topology (redacted)
```

Service emitters that route through a core port (hmacsha2 / x25519 / streamaead
/ keyenvelope) define **no** new codes — they return the core `0.2.4.*`
sentinels, exactly as `ecdsasig` does today.

**Earmark reconciliation (resolves the `pr-05/07/10` conflict).** The superseded
writer-subsystem-v1 plans (`.claude/plans/pr-05/07/10`) earmarked service
`0.3.26`–`0.3.30` for timeout / retry / circuit_breaker / dlq / rate_limit
middlewares. Those plans are **superseded by this wave** (the v1 PRs #39–51 were
closed "workflow restart") and **none of those middlewares were ever
implemented** — no identifier in the live tree occupies that range. This wave
therefore claims the live next-free contiguous block starting at `0x1a`
(= `0.3.26`), which numerically reclaims the abandoned earmark range; because no
`Code…` identifiers were ever defined there, there is no collision. The AST
audit keys on identifier-name uniqueness + literal-Public +
`reason = screamingSnake(varName)` (it does **not** enforce numeric uniqueness),
so the binding guarantee is unique *identifiers*; the numeric allocation here is
the documentary source of truth, recomputed against the live tree at each
implementing commit.

The AST audit (`internal/kernel/errs/registry_external_test.go`) scans
`internal/` + `pkg/` + `third-party/`. Every new emitter package ships a
`filegroup(name = "audit_srcs")` added to root `//:audit_sources`, or its codes
are silently unaudited.

## Consequences

- A fifth core sibling (`transform`) exists; the core purpose statement widens.
- Three new crypto ports, two crypto compositions, and a frozen streaming-AEAD
  wire format extend the crypto domain — all dep-light at the facade.
- `pkg/v1` gains `mac`, `agree`, and grows `crypto` / `hash` / `kdf` / `codec` /
  `logger`; the dep-light invariant is preserved by module-graph tests on
  `codec` and `logger`.
- The writer subsystem gains generic `worker` / `batcher` kernel primitives, a
  rotating file sink, encrypt-at-rest + tee/spill middlewares, and the
  `FromConfig` capstone.
- New `Code` blocks `0.1.5.*`, `0.2.4.15–.20`, `0.2.5.*`, service `0x1a–0x1d`,
  and `1.1.0.4` are claimed.
- A 14-commit single MR is harder to bisect-revert than 14 PRs; the unit of
  rollback becomes the commit. Commits revert-cascade **down the DAG only**
  (later depend on earlier), never sideways.

## Why not

- **Model compression as a codec `Format`.** Breaks the frozen `any→[]byte`
  contract and the append-only Format set; a parallel registry + explicit verbs
  keep Format frozen.
- **Expose the streaming nonce / a `cipher.Stream`-style API.** Nonce management
  is the misuse surface; the counter+salt construction hides it entirely.
- **Interop with age/libsodium streaming.** Half-matching is worse than a clean,
  documented bespoke frame with SDK-owned KATs.
- **Differentiate streaming `Open` / envelope-unwrap failures.** Builds an
  oracle; wrong-key/wrong-pass stays the single non-oracle `DecryptionFailed`.
- **Register a `CodeMACFailed`.** `Tag`/`Verify` cannot fail; a dead code would
  burden the audit forever.
- **Blank-import `pkg/v1/codec` from `FromConfig`.** Drags four vendor modules
  into the most-imported public package; `FromConfig` uses the stdlib
  core-dispatch surface against consumer-registered codecs only.

## Deferred / killed (rejection on record)

| Idea | Disposition | Reason |
|---|---|---|
| `core/cas` content store | **DEFER** | XL, speculative demand; this wave ships its prerequisites (blake3 hashers, DigestWriter/VerifyingReader) but the store would dominate the MR. |
| zstd / snappy / s2 compressors | **DEFER** | First MR is stdlib gzip/flate; vendor compressors land as opt-in follow-ups behind their own imports. |
| XChaCha streaming AEAD | **DEFER** | Keeps the streaming dep-light path provably stdlib-only at merge; lands under `third-party/x-crypto/*` later. |
| `PromotableCodec` (retire promote.go switch) | **CUT** | Refactor, not feature; footgun only bites at a 6th constrained codec. |
| encrypt-then-codec / sign-then-codec verbs | **CUT** | Encrypt half delivered by `encwrite`; sign half needs canonical bytes the SDK does not yet guarantee. |
| Canonical-encoding verb | **DEFER** | Only justified by signed-codec/CAS, both deferred. |
| Signed-codec (COSE-lite) | **CUT** | Depends on a non-existent canonical codec; self-attested pubkey authenticates nobody. |
| Capability/macaroon token codec | **CUT** | HMAC half delivered by the MAC port; macaroon half is a new auth domain mis-filed as a codec. |
| `kernel/backoff` + retrofits | **CUT** | Fails the ≥2-real-consumers bar honestly; revisit with durable retry. |
| NATS/JetStream writer | **DEFER** | Ship as a standalone PR off `main` after the batcher proves out. |
| AES-GCM-SIV (algID `0x03`) | **CONDITIONAL / likely CUT** | `x/crypto` does not ship it; keep only if re-scoped to SIV-over-vendored-chacha20poly1305, else defer until a vetted dep + RFC 8452 KATs are in hand. Strictly additive last commit or cut. |
| RSA / PS256 / P-384 / P-521 / Ed448 signing | **OUT OF SCOPE** | EC-only signature domain stays EC-only until JWT-interop demand is concrete. |

## References

- Feature synthesis driving this wave: `.claude/contexts/sdk-dream-features-v2.md`
  (the §3.2 commit sequence, §3.3 PP table, §4 risk register, §5 kill list).
- Crypto domain precedent: `docs/adr/0013-sdk-crypto-domain.md`.
- Writer registry precedent: `docs/adr/0012-logger-writer-registry.md`.
- Code layout: `docs/adr/0005-sdk-error-codes-dotted-quad.md`.
