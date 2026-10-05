<!-- updated: 2026-10-05T00:00:00Z -->
# pkg/v1/security/

## Purpose

The security family's public facades (ADR 0155). This directory holds no Go
code: it is a prefix, not a package, and there is no
`github.com/kitsunium/sdk/pkg/v1/security` to import. Each member is a package
of the SDK module with its own `CLAUDE.md`, `README.md` (written by
`tools/genindex` from `docs/api`, ADR 0167) and, where it measures something,
`BENCH.md`. A member is imported by its full path —
`github.com/kitsunium/sdk/pkg/v1/security/secret` — and links what it imports
and never this directory or its siblings, because Go links a package's imports
and not its parent (the measurement ADR 0155 records).

## The rule that put them together

A domain belongs here when the question it answers is about a CALLER or a
SECRET, at the application level, rather than about bytes: who the caller is
from one request to the next (`session`, `token`), what that caller may do
(`authz`), and which values must never be disclosed — kept as versions that
rotate (`secret`) or replaced wherever they are shown (`redact`). The schemes
they are built on are the crypto family's (`pkg/v1/crypto` and its children),
and the identity a TLS connection presents is the network domain's (`tlsid`).

## Members

| Package | What it publishes | Aliases onto | README |
|---|---|---|---|
| `authz/` | RBAC and ABAC with no policy language: `NewRBAC` / `NewABAC` / `DenyOverrides` / `Check`, three decisions with `Abstain` the zero (ADR 0057) | `internal/core/security/authz`, `internal/service/security/authz` | `authz/README.md` |
| `redact/` | `New(Config)` → a `Redactor` — the port, since ADR 0160 — whose `Value` / `JSON` / `Text` / `Attrs` replace every secret it recognises within an exact byte bound (ADR 0101) | `internal/core/security/redact`, `internal/service/security/redact` | `redact/README.md` |
| `secret/` | the `Value` no rendering writes down, the versioned stores, `NewKeyring`, `NewRotator` (ADR 0096) and `NewSubjectKeys` (ADR 0142) | `internal/core/security/secret`, `internal/service/security/secret` | `secret/README.md` |
| `session/` | `NewMemoryStore` / `NewFileStore` → a `Store` where `Regenerate` is the only call that binds a subject, and `NewSealer` for the cookie's value (ADR 0045) | `internal/core/security/session`, `internal/service/security/session` | `session/README.md` |
| `token/` | JWT over JWS Compact and PASETO v4.public, one constructor per algorithm, `ParseJWK` / `ParseJWKSet` / `NewJWKSet` (ADR 0042) | `internal/core/security/token`, `internal/service/security/token`, `internal/service/crypto/key/jwk` | `token/README.md` |

Each moved here from `pkg/v1/<name>` in one minor release, with no alias left
at the old path — a clean break, permitted only while the module is v0
(ADR 0155 §4, extending ADR 0040).

## Do NOT

- Put Go code in this directory. A file here would publish a package nobody
  designed, at a path every member would then appear to belong to.
- Hand-edit a member's `README.md`: edit its doc comment and run
  `make docs-readme`, whose `--repository.path` names the member's full path.
- Leave an alias package at an old `pkg/v1/<name>` path. ADR 0155 §4 refuses
  it: it would double the surface for consumers nobody can name.
