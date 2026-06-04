# pkg/v1/sign/

## Purpose

Public **detached digital-signature** facade (ADR 0013). Generate a keypair,
`Sign` bytes with the private key, `Verify` with the public key. Thin alias +
delegation over `internal/core/crypto`'s Signer registry — zero runtime cost,
no business logic (per `pkg/` convention).

Importing the package blank-imports `internal/service/crypto/ed25519sig` AND
`internal/service/crypto/ecdsasig`, activating both Ed25519 and ECDSA-P256 with
**zero non-stdlib deps**.

## Surface

| Identifier | Role |
|---|---|
| `Algorithm` | alias of `corecrypto.Algorithm` (stable scheme id) |
| `Ed25519` | the EdDSA-over-Curve25519 scheme const (RFC 8032) — the modern default |
| `ECDSAP256` | ECDSA over NIST P-256 with SHA-256 + ASN.1/DER signatures (JWT `ES256`, X.509, COSE interop) |
| `GenerateKey(a) (pub, priv []byte, error)` | fresh keypair; `priv` is secret |
| `Sign(a, priv, message) ([]byte, error)` | detached signature; bad `priv` → `SigningFailed` |
| `Verify(a, pub, message, sig) (bool, error)` | `(true,nil)` valid · `(false,nil)` invalid · `(false, UnknownSignatureAlgorithm)` unregistered |

## Keys are raw bytes — the private key is secret

Keys/signatures are scheme-specific `[]byte` (Ed25519: 32-byte public, 64-byte
private, 64-byte signature; ECDSA-P256: DER-encoded keys — PKIX public, SEC1
private — and ASN.1/DER signatures). The private key is secret material: hold it like a
password, never log it, zero it when done. `Verify` is constant-time w.r.t. the
signature and **never errors on an invalid signature** — invalid is `(false,
nil)`, so the error channel signals only misconfiguration (unregistered scheme),
never WHY a check failed.

## vs AEAD / hash

- **sign** — authenticity + non-repudiation under a *public* key anyone verifies.
- **crypto** (`pkg/v1/crypto`) — confidentiality + integrity under a *shared secret*.
- **hash** (`pkg/v1/hash`) — unkeyed *public* fingerprints; no authentication.

## Conventions

- **Aliases, not new types** — `Algorithm = corecrypto.Algorithm`; the const is
  the frozen wire string (`"ed25519"`).
- **README.md is generated** (`make docs-readme` → gomarkdoc, ADR 0008). Edit the
  package doc comment in `sign.go`; never hand-edit `README.md`.
- **Signatures freeze at v1.0.0.** New scheme consts can be added; existing ones
  cannot move.

## Do NOT

- Log or persist `priv` in the clear; it is the secret half of the keypair.
- Branch on `Verify`'s error to decide validity — validity is the bool; the
  error means "scheme not registered" (blank-import it).
- Hand-author `README.md` — it is regenerated from the `sign.go` doc comment.

## Verification

```sh
bazel test --config=race //pkg/v1/sign:sign_test
# Fallback
cd pkg/v1 && GOWORK=off go test -race -cover ./sign/...
```

## Accepted audit findings

- Deferred/accepted low+info audit findings (V81, V108) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
