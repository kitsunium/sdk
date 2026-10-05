<!-- updated: 2026-10-05T12:00:00Z -->
# pkg/v1/crypto/

## Purpose

Stable v1 public facade for authenticated encryption: `Seal` / `Open` with a
hidden nonce. Thin alias + validate-and-delegate layer over
`internal/core/crypto` (ADR 0013). Importing the package activates the default
**AES-256-GCM** scheme (stdlib-only), so `go get`-ing this package pulls **zero**
non-stdlib dependencies — the dep-light invariant.

It is also the root of the crypto family (ADR 0155): the six scheme facades are
its children, one directory per port of `internal/core/crypto` beside the AEAD
this package keeps. A child is a package of its own — importing
`pkg/v1/crypto/hash` links neither this package nor its AES-GCM activators,
because Go links what a package imports and never its parent directory — so the
"siblings, not children" placement ADR 0013, 0014 and 0102 §D5 relied on bought
nothing, and ADR 0155 refuted it by measurement.

Consumer-facing prose lives in the package doc comment (`crypto.go`) and is
rendered to `README.md` by gomarkdoc — edit the doc comment, not the README.

## The family

| Child | Port of `internal/core/crypto` | Engine under `internal/service/crypto` |
|---|---|---|
| `agree/` | `Agreement` — X25519 key agreement | `agree/x25519` |
| `hash/` | `Hasher` — digests, streaming writer and verifying reader | `hash/stdhash` |
| `kdf/` | `Deriver` — HKDF subkeys, and the key tree | `kdf/hkdfsha256`, `kdf/keytree` |
| `mac/` | `MAC` — HMAC-SHA256 tags | `mac/hmacsha2` |
| `password/` | `PasswordHasher` — PBKDF2 PHC strings, and `IsCommon` | `password/pbkdf2pw`, `password/commonpw` |
| `sign/` | `Signer` — Ed25519 and ECDSA P-256 | `sign/ed25519sig`, `sign/ecdsasig` |

This package keeps the AEAD (`aead/aesgcm`, `aead/streamaead`) and the passphrase
envelope (`key/keyenvelope`). The JWK format (`key/jwk`) is published by
`pkg/v1/security/token`, the domain that reads keys from a document. Each child has its own
`CLAUDE.md`, `README.md` and `BENCH.md`; this package's `BENCH.md` carries the
family's choice table the children's link back to.

## Contents

```
crypto.go  — Key alias + Algorithm defined type, KeyLen + AESGCM +
             XChaCha20Poly1305 consts, NewKey, Seal / SealAs / Open +
             SealStream / OpenStream + WrapKey / UnwrapKey; blank-imports the
             aesgcm + streamaead activators, delegates the envelope to
             internal/service/crypto/key/keyenvelope
crypto_external_test.go — facade tests (round trips, non-oracle Open, stream
             vs box versions, redaction, the V104 defined-type check)
crypto_bench_test.go    — the benchmarks BENCH.md is generated from
BENCH.md   — generated cost report; carries the crypto family's choice table
README.md  — generated from the package doc comment (make docs-readme)
```

## Streaming (ADR 0014 §D2)

`SealStream(dst, key, aad)` / `OpenStream(src, key, aad)` wrap an `io.Writer` /
`io.Reader` and process the payload in fixed 64 KiB authenticated chunks under a
disjoint, frozen wire format (stream version `0x02`, the box is `0x01`). The
construction is truncation-resistant (final-chunk flag in the nonce + a random
per-stream salt) and never surfaces a chunk's plaintext before it authenticates.
The streaming scheme (`internal/service/crypto/aead/streamaead`) is **stdlib-only**
(AES-256-GCM + HKDF), so it preserves the dep-light invariant — no x/crypto.
`Close()` on the writer is mandatory: it seals the final chunk. The whole-buffer
`Open` rejects a `0x02` stream and `OpenStream` rejects a `0x01` box.

## Conventions

- **`Key` is an alias, `Algorithm` a defined type.** `Key = corecrypto.Key` is
  identity-equal to the internal model. `Algorithm` is a type of its own over
  `corecrypto.Algorithm`, converted at the call into core, so a hash, MAC or
  signature constant does not compile into `SealAs` (V104).
- **Thin wrappers.** `NewKey` / `Seal` / `SealAs` / `Open` / `SealStream` /
  `OpenStream` delegate to the core dispatcher, `WrapKey` / `UnwrapKey` to the
  `keyenvelope` service (ADR 0014 §D3: PBKDF2-SHA256 KEK, AES-256-GCM box,
  `$kenv$` string); no crypto logic lives here.
- **Default scheme via blank import.** `crypto.go` blank-imports
  `internal/service/crypto/aead/aesgcm`; that is the only thing wiring the default
  algorithm, and it is stdlib-only. `XChaCha20Poly1305` is only a name here:
  the scheme registers when the consumer blank-imports
  `third-party/x-crypto/xchacha` (which alone pulls `golang.org/x/crypto`) —
  never auto-pulled here, to preserve dep-light.
- **The nonce is never in the API.** `Seal` generates and embeds it; `Open`
  strips it. There is no nonce parameter.
- **`Open` is non-oracle.** Every failure returns the same `DecryptionFailed`.

## Dep-light invariant

```sh
# This package must list zero golang.org/x/crypto (or any non-stdlib crypto):
cd pkg/v1 && GOWORK=off go list -deps ./crypto | grep -i 'x/crypto' && echo LEAK || echo "dep-light OK"
# The whole family — this package and its six children — must print nothing:
# no package outside the SDK and the standard library. Do not grep the family
# for 'x/crypto': `sign` reaches crypto/x509, which links the standard
# library's own vendored copy (vendor/golang.org/x/crypto/cryptobyte), and
# that grep reports it as a leak although it is part of the standard library.
cd pkg/v1 && GOWORK=off go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./crypto/... | grep -v '^github.com/kitsunium/sdk/'
# A child never links its parent (ADR 0155) — must print nothing:
cd pkg/v1 && GOWORK=off go list -deps ./crypto/hash | grep -x 'github.com/kitsunium/sdk/pkg/v1/crypto'
```

## Do NOT

- Re-export `errs.Define` / construct `*errs.Error` here — introspect via
  `pkg/v1/errs` accessors.
- Auto-import a vendor-dependent scheme (argon2, XChaCha20) from this package —
  that would break the dep-light guarantee. Each lives behind its own activator.
- Import a child from this package's production code, or this package from a
  child's. The family shares a directory, not a link: a consumer of `hash` must
  not pay for the AEAD, nor a consumer of `Seal` for six registries. A suite may
  compose them — `agree`'s seals with the key it agreed.
- Log `Key.Bytes()` or surface it in an error message.

## Generated

`facade_gen.go` is kit's (ADR 0166): every alias, re-exported constant and variable, and forwarder of this package is declared in the `facade:` of `design/crypto.yaml`, doc comments included, and kit writes it. Where this file names another file as holding one of them, read `facade_gen.go`: the hand-written files keep the package comment and the declarations of their own. Change a re-export, or its doc comment, in the design and run `kit gen` (then `make api` and `make docs-readme`); `make api-check` fails on a `facade_gen.go` edited by hand.

## Verification

```sh
bazel test --config=race //pkg/v1/crypto:crypto_test
# The whole family
bazel test --config=race //pkg/v1/crypto/...
# Fallback
cd pkg/v1 && GOWORK=off go test -race -cover ./crypto/...
```

## Subtree

`agree/`, `hash/`, `kdf/`, `mac/`, `password/`, `sign/` — each documents its own
facade in its `CLAUDE.md`.
