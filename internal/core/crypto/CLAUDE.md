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
| `registry.go`  | `snapshot.Value`-backed registry + id index: `Register` / `Lookup` / `Available` |
| `seal.go`      | `Seal` / `Open` dispatch + the frozen box `Version` byte |
| `codes.go`     | `Code*` constants — range 0.2.4.\* |
| `errors.go`    | `UnknownAlgorithm`, `InvalidKey`, `DecryptionFailed`, `EntropyFailed` |

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
