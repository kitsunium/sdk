<!-- updated: 2026-10-03T04:05:00Z -->
# internal/service/security/

## Purpose

The security family's engines (ADR 0155): the concrete halves of the
contracts under `internal/core/security`, at the same paths. This directory
holds no Go code: it is a prefix, not a package, and nothing imports
`internal/service/security` itself. Each member is a package of the
`internal/service` module with its own `CLAUDE.md`, and each is published by
the facade at the same path under `pkg/v1/security`.

## The rule that put them together

A domain belongs here when the question it answers is about a CALLER or a
SECRET, at the application level, rather than about bytes: who the caller is
from one request to the next (`session`, `token`), what that caller may do
(`authz`), and which values must never be disclosed — kept as versions that
rotate (`secret`) or replaced wherever they are shown (`redact`).

The engines COMPOSE the crypto family and reimplement none of it — `token`
signs with `crypto/mac/hmacsha2` and `crypto/sign/ed25519sig` and reads keys
with `crypto/key/jwk`, `session` seals with `crypto/aead/aesgcm`, `secret`'s
keyring derives and seals with HKDF and AES-GCM. A scheme stays in
`internal/service/crypto` whoever calls it; what is here is the policy built on
it. A transport's identity stays with the network domain (`net/tlsid`,
ADR 0029).

## Members

| Package | What it is | Implements | Code range | Facade |
|---|---|---|---|---|
| `authz/` | the RBAC evaluator over an inverted grant table, the ABAC evaluator over conditions, `DenyOverrides` and `Check` — the one closure where "nobody said Allow" becomes a refusal (ADR 0057) | `core/security/authz` | `0.3.56.*` (construction refusals; every verdict is a core sentinel) | `pkg/v1/security/authz` |
| `redact/` | display redaction of values, JSON documents, texts and log attributes — names, declared fields, URL credentials — within an exact byte bound, never mutating its input (ADR 0101) | none yet — no core counterpart (ADR 0101 §D5; ADR 0160 §1 adds one) | `0.3.73.*` | `pkg/v1/security/redact` |
| `secret/` | memory, environment and sealed file stores, the `Keyring` over one secret's versions, the `Rotator` (ADR 0096); `SubjectKeys`, one data key per subject under a rotating root, destroyed to erase (ADR 0142) | `core/security/secret` | `0.3.68.*` | `pkg/v1/security/secret` |
| `session/` | the memory store, the file store — its directory audited with `kernel/fs/pathchain`, then held as an `os.Root` — and the AEAD cookie sealer (ADR 0045) | `core/security/session` | `0.3.46.*` | `pkg/v1/security/session` |
| `token/` | JWT over JWS Compact Serialization and PASETO v4.public, one constructor per algorithm, `kid` selection from a JWK Set (ADR 0042) | `core/security/token` | `0.3.44.*` | `pkg/v1/security/token` |

Every range above kept its value when its package moved (ADR 0160):
`codeRangeOwners` (`internal/kernel/errs/registry_ownership_external_test.go`)
names the new directories under the same keys, and `//:audit_sources` lists
them by their new labels.

## Do NOT

- Put Go code in this directory. A file here would make `security` a package
  of its own, and the family a domain nobody chose.
- Reimplement a primitive here because a security engine needs it: a scheme
  goes to `internal/service/crypto`, a path rule to `kernel/fs/pathchain`, a
  wait to `kernel/clock`.
- Import one member from another without saying so in both `CLAUDE.md` files:
  today none does, and `secret`'s file store reaches `lock` and `vfs`, not
  `session`.
