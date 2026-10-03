<!-- updated: 2026-10-03T04:05:00Z -->
# internal/core/security/

## Purpose

The security family's contracts (ADR 0155): the ports, the values and the
codes of the security domains that have a core. This directory holds no Go
code: it is a prefix, not a package, and nothing imports
`internal/core/security` itself. Each member is a package of the
`internal/core` module with its own `CLAUDE.md`, and each still follows the
core's rules — interfaces and immutable values, the standard library and the
kernel only (`internal/core/CLAUDE.md`). The engines are the same paths under
`internal/service/security`, and the facades the same paths under
`pkg/v1/security`.

## The rule that put them together

A domain belongs here when the question it answers is about a CALLER or a
SECRET, at the application level, rather than about bytes: who the caller is
from one request to the next (`session`, `token`), what that caller may do
(`authz`), and which values must never be disclosed — kept as versions that
rotate (`secret`) or replaced wherever they are shown (`redact`).

Every one of them is built on the crypto family, and none of them is a scheme:
a scheme turns keys and bytes into bytes and lives in `internal/core/crypto`,
whoever calls it. A transport's identity is not here either: the certificate a
connection presents is the net family's (`internal/core/net`, ADR 0029),
because it is what a server and a client hand each other.

`redact`, the family's fifth domain, has no core package yet — its two codes
are declared by its engine, `internal/service/security/redact` (ADR 0101 §D5).
ADR 0160 §1 gives it one, as `redact/` beside the four below, when the
reorganisation series reaches it.

## Members

| Package | What it declares | Code range | Engine |
|---|---|---|---|
| `authz/` | the `Policy` and `Condition` FUNC ports, the three-valued `Decision` (`Abstain` is the zero), the immutable `RequestValue` and the typed `AttrValue`; no registry (ADR 0057) | `0.2.26.*` | `internal/service/security/authz` |
| `secret/` | the redacting `Value`, the `Store` port frozen at five over never-reused versions, `VersionValue` and the name grammar (ADR 0096); the `SubjectKeyStore` port frozen at five, `SubjectKeyValue` and the subject grammar (ADR 0142) | `0.2.37.*` | `internal/service/security/secret` |
| `session/` | the `Store` port frozen at five with `Sweeper` as a sibling, the `Sealer`, the opaque redacting `ID` and the immutable `SessionValue` (ADR 0045) | `0.2.14.*` | `internal/service/security/session` |
| `token/` | the one-method `Issuer` and `Verifier` ports, the redacting `ClaimsValue` and the closed `Algorithm` enum in which `none` has no representation; no registry, because its key would be the attacker-written `alg` header (ADR 0042) | `0.2.13.*` | `internal/service/security/token` |

A code keeps its value when its package moves (ADR 0160): the four ranges
above are the ones these packages declared before the family existed, and
`codeRangeOwners` names the new directories under the same keys.

## Do NOT

- Put Go code in this directory. A file here would make `security` a package
  of its own, and the family a domain nobody chose.
- File a cryptographic scheme here because a security domain calls it: a
  hash, a MAC, a signature or an AEAD is the crypto family's.
- Renumber a member's codes to match its new path. Nothing derives a code from
  a directory, and a consumer branches on the value (ADR 0160).
