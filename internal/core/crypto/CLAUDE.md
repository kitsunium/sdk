# internal/core/crypto/

## Purpose

Declares the **authenticated-encryption port** of the SDK: the `AEAD` contract,
the redacting `Key` value type, and the process-wide registry mapping an
`Algorithm` (and its 1-byte wire id) to a registered `AEAD`. Peer of
`internal/core/codec` — the registry resolves an `Algorithm` to an `AEAD`
exactly as codec resolves a `Format` to a `Codec` (ADR 0013).

No algorithm bodies and no vendor types live here. Concrete schemes live under
`internal/service/crypto/<algo>/` (stdlib AES-256-GCM today) and, later,
`third-party/x-crypto/*` (XChaCha20-Poly1305), self-registering via a
package-level `var` at import — no `init()`. `pkg/v1/crypto` blank-imports the
stdlib scheme and re-exports `Seal` / `Open` against this package.

Code range: `0.2.4.*` (ADR 0013).

## Contents

| File | Surface |
|---|---|
| `algorithm.go` | `Algorithm` typed string (`String` / `Known`) |
| `key.go`       | `Key` — opaque, redacting 256-bit key (`NewKey` / `Bytes` / `Zeroize`); `KeyLen` |
| `aead.go`      | `AEAD` interface (`Algorithm` / `ID` / `Seal` / `Open`) |
| `registry.go`  | `snapshot.Value`-backed AEAD registry + id index: `Register` / `Lookup` / `Available` |
| `seal.go`      | `Seal` / `Open` dispatch + the frozen box `Version` byte |
| `hasher.go`    | `Hasher` interface (`Algorithm` / `New`) — the **non-authenticated** fingerprint port |
| `hash_registry.go` | second `snapshot.Value`-backed registry mapping an `Algorithm` to a `Hasher`: `RegisterHasher` / `LookupHasher` / `AvailableHashers` + `Sum` / `SumHex` / `NewHash` dispatch |
| `signer.go`    | `Signer` interface (`Algorithm` / `GenerateKey` / `Sign` / `Verify`) — the **digital-signature** port (public-key authenticity) |
| `signer_registry.go` | third `snapshot.Value`-backed registry mapping an `Algorithm` to a `Signer`: `RegisterSigner` / `LookupSigner` / `AvailableSigners` + `GenerateKey` / `Sign` / `Verify` dispatch |
| `deriver.go`   | `Deriver` interface (`Algorithm` / `Derive`) — the **key-separation KDF** port (NOT password stretching) |
| `deriver_registry.go` | fourth `snapshot.Value`-backed registry mapping an `Algorithm` to a `Deriver`: `RegisterDeriver` / `LookupDeriver` / `AvailableDerivers` + `Subkey` dispatch |
| `password_hasher.go` | `PasswordHasher` interface (`Algorithm` / `Hash` / `Verify` / `NeedsRehash`) — the **password-storage** port (deliberately slow, salted, PHC-string) |
| `password_registry.go` | fifth `snapshot.Value`-backed registry: `RegisterPasswordHasher` / `LookupPasswordHasher` / `AvailablePasswordHashers` + `HashPassword` / `VerifyPassword` / `NeedsRehash` dispatch (the latter two resolve the scheme from the PHC id via `phcID`) |
| `mac.go`       | `MAC` interface (`Algorithm` / `Tag` / `Verify` / `New`) — the **keyed detached-authentication** port; a tag IS secret-comparison-sensitive (route through `Verify`, never `==`) |
| `mac_registry.go` | sixth `snapshot.Value`-backed registry mapping an `Algorithm` to a `MAC`: `RegisterMAC` / `LookupMAC` / `AvailableMACs` + `MACTag` / `MACVerify` dispatch (named `MAC*` so they never collide with the signer/verifier verbs) |
| `agreement.go` | `Agreement` interface (`Algorithm` / `GenerateKey` / `Shared`) — the **DH-style key-agreement** port; raw `Shared` output MUST be KDF'd before use |
| `agreement_registry.go` | seventh `snapshot.Value`-backed registry mapping an `Algorithm` to an `Agreement`: `RegisterAgreement` / `LookupAgreement` / `AvailableAgreements` + `GenerateAgreementKey` / `AgreementShared` dispatch (a scheme `Shared` fault wraps into `AgreementFailed`, leaking no key bytes) |
| `stream.go`    | `StreamSealer` interface (`Algorithm` / `Writer` / `Reader`) — the **chunked streaming-AEAD** port with the hold-back contract; truncation surfaces as `StreamTruncated` |
| `stream_registry.go` | eighth `snapshot.Value`-backed registry mapping an `Algorithm` to a `StreamSealer`: `RegisterStreamSealer` / `LookupStreamSealer` / `AvailableStreamSealers` + `SealStream` / `OpenStream` dispatch (a lookup miss reuses the AEAD `UnknownAlgorithm` sentinel — streaming extends the AEAD domain) |
| `codes.go`     | `Code*` constants — range 0.2.4.\* |
| `errors.go`    | `UnknownAlgorithm`, `InvalidKey`, `DecryptionFailed`, `EntropyFailed`, `UnknownHashAlgorithm` (0.2.4.6), `UnknownSignatureAlgorithm` (0.2.4.7), `SigningFailed` (0.2.4.8), `KeyGenerationFailed` (0.2.4.9), `UnknownKDFAlgorithm` (0.2.4.10), `DerivationFailed` (0.2.4.11), `UnknownPasswordAlgorithm` (0.2.4.12), `PasswordHashFailed` (0.2.4.13), `InvalidPasswordHash` (0.2.4.14), `UnknownMACAlgorithm` (0.2.4.15), `UnknownAgreementAlgorithm` (0.2.4.16), `AgreementFailed` (0.2.4.17), `StreamTruncated` (0.2.4.18) |

Eight **independent** registries live here, all on one `Algorithm` keyspace: the
**AEAD** (`registry.go`, keyed encryption), **Hasher** (`hash_registry.go`,
public fingerprints), **Signer** (`signer_registry.go`, public-key signatures),
**Deriver** (`deriver_registry.go`, key-separation KDF from a *strong* secret),
**PasswordHasher** (`password_registry.go`, slow salted storage of *human*
passwords), **MAC** (`mac_registry.go`, keyed detached authentication),
**Agreement** (`agreement_registry.go`, DH-style shared-secret establishment),
and **StreamSealer** (`stream_registry.go`, chunked streaming AEAD). They never
mix: a digest carries no secret, `Open`/`Verify`/`MACVerify` are constant-time
or non-oracle, `Subkey` rejects passwords by contract, password hashing is the
one deliberately-slow surface, the raw `Shared` secret is never handed back
un-KDF'd, and `SealStream`/`OpenStream` reuse the AEAD `UnknownAlgorithm` miss
because streaming extends the AEAD domain. `pkg/v1/{crypto,hash,sign,kdf,password,mac,agree}`
re-export these surfaces (the streaming verbs ride `pkg/v1/crypto`).

## Wire format

Every box is self-describing so `Open` needs no algorithm argument:

```
[1B Version][1B alg-id][nonce][ciphertext || tag]
```

`Version` (`seal.go`) is frozen post-v1.0.0; alg-id is frozen per scheme
(AES-256-GCM = `0x01`). The nonce length is scheme-specific (GCM = 12 bytes) and
known from the alg-id, so the reader strips it without a length prefix. Each
`AEAD` owns its **complete** framing — the registry only dispatches on the id.

## Conventions

- **`snapshot.Value`, not `sync.Map`** — schemes register once at import, then
  it is read-many (ADR 0011). `Register` publishes via `Value.Update`
  (mutex-serialised); `Lookup` / `lookupByID` are lock-free.
- **Registration is a package-level `var`, never `init()`** (`KTN-FUNC-NOINIT`):
  `var AEAD = crypto.Register(aesGCM{})` in each scheme package.
- **Idempotent re-registration** of the same scheme is fine; a *distinct* scheme
  claiming a taken `Algorithm` **or** wire id **panics at boot** with the
  dotted-quad code.
- **`Algorithm("")` is the reserved invalid zero value** — `Known()` is false.
- **The nonce never reaches a caller.** `Seal` generates it from `crypto/rand`
  and embeds it; `Open` strips it. No nonce parameter exists anywhere.
- **`Open` is non-oracle.** Every failure — short box, unknown id, bad tag,
  wrong key — returns the single `DecryptionFailed`; schemes MUST NOT
  distinguish them.
- **Secrets redact.** `Key.String()` / `GoString()` are always `<redacted>`;
  `Bytes()` is the only way out and goes straight to the cipher, never a log.

## Do NOT

- Add a `MustRegister` or deregistration API — the registry is append-only and
  `Register` already panics on conflict.
- Differentiate `Open` failures — that builds an oracle.
- Put a scheme implementation or a vendor import here — those live in
  `service/crypto/*` and `third-party/x-crypto/*`.
- Log `Key.Bytes()` or place key material in an `errs` `Public` / `Private` /
  `Fields`.

## Verification

```sh
bazel test --config=race //internal/core/crypto:crypto_test
# Fallback
cd internal/core && GOWORK=off go test -race -cover ./crypto/...
```
