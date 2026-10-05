<!-- updated: 2026-10-05T00:00:00Z -->
# pkg/v1/crypto/sign/

## Purpose

Public **detached digital-signature** facade (ADR 0013). Generate a keypair,
`Sign` bytes with the private key, `Verify` with the public key. Thin alias +
delegation over `internal/core/crypto`'s Signer registry — zero runtime cost,
no business logic (per `pkg/` convention).

Importing the package blank-imports `internal/service/crypto/sign/ed25519sig` AND
`internal/service/crypto/sign/ecdsasig`, activating both Ed25519 and ECDSA-P256 with
**zero non-stdlib deps**.

## Surface

| Identifier | Role |
|---|---|
| `Algorithm` | defined type over `corecrypto.Algorithm` (stable scheme id) — distinct from the other crypto-family `Algorithm` types, so a hash or MAC constant does not compile into a signature call |
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
- **hash** (`pkg/v1/crypto/hash`) — unkeyed *public* fingerprints; no authentication.

## Conventions

- **`Algorithm` is a defined type, not an alias** — converted to
  `corecrypto.Algorithm` at the call into core, so the signature registry stays
  its own keyspace. The consts are the frozen wire strings (`"ed25519"`,
  `"ecdsa-p256"`).
- **README.md is generated** (`make docs-readme` → `tools/genindex` from the
  committed `docs/api`, ADR 0167). The package comment is in `doc.go`, which
  kit writes from the design (`design/crypto.yaml`): edit the design, run
  `kit gen`, then `make api` and `make docs-readme`; never hand-edit `README.md`.
- **Signatures freeze at v1.0.0.** New scheme consts can be added; existing ones
  cannot move.

## Do NOT

- Log or persist `priv` in the clear; it is the secret half of the keypair.
- Branch on `Verify`'s error to decide validity — validity is the bool; the
  error means "scheme not registered" (blank-import it).
- Hand-author `README.md` — `make docs-readme` writes it from `docs/api` (ADR 0167).

## Verification

```sh
bazel test --config=race //pkg/v1/crypto/sign:sign_test
# Fallback
cd pkg/v1 && GOWORK=off go test -race -cover ./crypto/sign/...
```

## Declarations

`decl_gen.go` is written by kit gen from the design (ADR 0170): the declaration of `Algorithm`. Their methods, constructors and helpers stay hand-written, in the files this document names.
