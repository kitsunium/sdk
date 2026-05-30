# ADR 0013 — Crypto domain: authenticated encryption (`Seal` / `Open`)

- **Status**: Accepted
- **Date**: 2026-05-30
- **Deciders**: SDK maintainers
- **Related**: ADR 0001 (multi-module layout), ADR 0003 (codec registry — the pattern mirrored), ADR 0005 (dotted-quad codes), ADR 0011 (snapshot.Value), ADR 0012 (writer registry — the gate precedent)
- **Amends**: the `internal/core` purpose statement (admits a fourth sibling) and the ADR 0005 code-allocation table (new `0.2.4.*` and `1.3.0.*` blocks)

## Context

The SDK has earned its shape twice: `codec.Marshal(format, v)` dispatches over a
`snapshot.Value` registry, and `logger.NewMulti(min, specs…)` resolves named
writers the same way. Both are dep-light type-alias facades over a strict
kernel → core → service → pkg/v1 stack with typed dotted-quad errors and heavy
deps quarantined under `third-party/*`.

There is **zero** cryptography in the tree today. Consumers reaching for
authenticated encryption fall back to assembling `crypto/cipher` + nonce
management by hand — the exact footgun surface (nonce reuse, oracle-y error
handling, naked digest compares, leaked key material) a normed SDK should erase.

`internal/core/CLAUDE.md` forbids a **fourth** sibling beside `codec` / `writer`
/ `logger` without first widening the layer's purpose statement via an ADR —
exactly the gate ADR 0012 cleared for `writer`. This ADR clears it for `crypto`.

## Decision

Add a **crypto domain** modelled verbatim on the codec registry: anyone who
knows `codec.Marshal` already knows `crypto.Seal`.

### Layer layout

```text
internal/core/crypto/        # 0.2.4.* — AEAD interface, redacting Key, registry. No algorithm bodies, no vendor types.
internal/service/crypto/     # stdlib-only schemes (aesgcm today). Dep-free → keeps pkg/v1 dep-light.
third-party/x-crypto/*       # FUTURE — golang.org/x/crypto schemes (XChaCha20-Poly1305, argon2id, …), opt-in blank import.
pkg/v1/crypto/               # 1.3.0.* — Key/Algorithm aliases + Seal/Open/SealAs/NewKey; blank-imports the stdlib default.
```

The `internal/core` purpose statement is widened to: *core declares the
contracts for codecs, the logger, log-transport writers, **and cryptographic
schemes***.

### The AEAD seam — `Seal` / `Open` with a hidden nonce

The flagship ergonomic: **the nonce is generated, embedded, and stripped for
you**. No nonce parameter exists anywhere in the API.

```go
k, _   := crypto.NewKey(key32)            // exactly 32 bytes, copied, redacting
box, _ := crypto.Seal(k, plaintext, aad)  // nonce drawn from crypto/rand, hidden in box
pt, _  := crypto.Open(k, box, aad)         // algorithm read from the box header
```

Wire format (each scheme owns its full framing; the registry dispatches on the
1-byte id): `[1B Version][1B alg-id][nonce][ciphertext || tag]`. `Version`
(`core/crypto.Version`) and per-scheme `alg-id` are **frozen post-v1.0.0**, like
codec `Format` strings — a box sealed today opens tomorrow.

### Hard-to-misuse guarantees (the whole point)

- **No nonce in the API.** `Seal` draws it from `crypto/rand`; `Open` strips it.
  Nonce reuse is structurally impossible.
- **`NewKey` rejects non-32-byte input** with `InvalidKey` — never truncates or
  pads — and copies defensively. `Key.String()/GoString()/%v/%#v` are always
  `<redacted>`; key material never reaches an `errs` `Public`/`Private`/`Fields`.
- **`Open` is non-oracle.** Every failure (short box, unknown id, bad tag, wrong
  key, wrong aad) returns the single `DecryptionFailed`; schemes MUST NOT
  distinguish them.
- **`aad` is authenticated, not encrypted** — bind a ciphertext to its context
  (record id, table, format tag) so a box cannot be replayed elsewhere.

### Default scheme — AES-256-GCM, stdlib, dep-free

`internal/service/crypto/aesgcm` implements the default over `crypto/aes` +
`crypto/cipher` + `crypto/rand` (hardware-accelerated, FIPS-track). It
self-registers via `var AEAD = crypto.Register(aesGCM{})`. `pkg/v1/crypto`
blank-imports it, so the dep-light invariant is literal:

```sh
cd pkg/v1 && GOWORK=off go list -deps ./crypto/... | grep -i 'x/crypto'  # empty
```

Future schemes (XChaCha20-Poly1305, with its 192-bit nonce for high-volume
random-nonce workloads) land under `third-party/x-crypto/*` and activate only
via their own explicit blank import — never auto-pulled into `pkg/v1`.

## Error-code allocation (amends ADR 0005)

```text
core/crypto — 0.2.4.*
  0.2.4.1  CodeDuplicateRegistration  (panic-only, boot)
  0.2.4.2  CodeUnknownAlgorithm       Seal of an unregistered algorithm
  0.2.4.3  CodeInvalidKey             NewKey wrong length          (exit 65 EX_DATAERR)
  0.2.4.4  CodeDecryptionFailed       any Open failure (non-oracle) (http 400, exit 65)
  0.2.4.5  CodeEntropyFailed          crypto/rand fault in Seal
  0.2.4.6  CodeUnknownHashAlgorithm   Sum/SumHex/NewHash of an unregistered hasher
  0.2.4.7  CodeUnknownSignatureAlgorithm  Sign/Verify/GenerateKey of an unregistered signer
  0.2.4.8  CodeSigningFailed          Sign with a malformed private key       (exit 65 EX_DATAERR)
  0.2.4.9  CodeKeyGenerationFailed    crypto/rand fault in GenerateKey
  0.2.4.10 CodeUnknownKDFAlgorithm    Subkey of an unregistered deriver
  0.2.4.11 CodeDerivationFailed       Subkey length above the scheme's maximum (exit 65 EX_DATAERR)
pkg/v1/crypto — 1.3.0.* (reserved; facade adds no sentinels in this cut)
```

`0.2.4.6`–`0.2.4.11` were filled in by the hashing + signature + KDF follow-ons
(see Deferred); they sit in the already-claimed `0.2.4.*` block, so these are
registry syncs (mirrored by the AST audit), not Decision changes.

The AST audit (`internal/kernel/errs/registry_external_test.go`) scans
`internal/` + `pkg/` + `third-party/` (see the ADR-0012 audit fix), so these
sentinels are enforced for Public-is-literal / reason / uniqueness from day one.

## Consequences

- A fourth core sibling exists; the layer purpose statement is widened.
- `pkg/v1/crypto` ships `Seal`/`Open`/`SealAs`/`NewKey` with zero non-stdlib
  deps. The keystone the rest of the crypto tier sits on is in place.
- New `Code` blocks `0.2.4.*` (used) and `1.3.0.*` (reserved) are claimed.

## Why not

- **Expose the nonce / a `cipher.AEAD`-style API.** That is the primary misuse
  surface; hiding the nonce and the algorithm id is the value.
- **Differentiate `Open` errors.** Builds a padding/format oracle.
- **Pull `golang.org/x/crypto` into `pkg/v1`.** Breaks the dep-light guarantee;
  vendor schemes live under `third-party/x-crypto/*` behind opt-in imports.
- **A single mega-package.** The codec/writer precedent (core port + service
  impls + pkg facade) is the proven shape.

## Deferred (follow-on PRs, each just registers a factory)

- XChaCha20-Poly1305 scheme (`third-party/x-crypto/*`). — **shipped**
- Hashing (`Sum`/`SumHex`/`NewHash`, fingerprint — explicitly NOT
  authentication; `Hasher` port + second registry in `core/crypto`,
  stdlib schemes in `service/crypto/stdhash`, facade `pkg/v1/hash`). — **shipped**
- Password storage (PHC strings + upgrade-on-verify, argon2id).
- KDF (HKDF `Subkey` for key separation; `Deriver` port + fourth registry in
  `core/crypto`, stdlib HKDF-SHA256 in `service/crypto/hkdfsha256`, facade
  `pkg/v1/kdf`). — **HKDF shipped**; argon2id password-stretching is its own port.
- Signatures (`Sign`/`Verify`/`GenerateKey`; `Signer` port + third registry in
  `core/crypto`, stdlib Ed25519 in `service/crypto/ed25519sig`, facade
  `pkg/v1/sign`). — **Ed25519 shipped**; ECDSA is the next scheme follow-on.
- Streaming AEAD, encrypt-then-codec, signed-codec (see the feature roadmap).

## References

- Feature roadmap: `.claude/contexts/sdk-feature-ideation.md` (crypto domain section)
- `internal/core/crypto/CLAUDE.md`, `internal/service/crypto/aesgcm/CLAUDE.md`, `pkg/v1/crypto/CLAUDE.md`
