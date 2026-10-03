<!-- updated: 2026-10-03T07:14:15Z -->
# internal/core/security/

## Purpose

The security family's contracts (ADR 0155): the ports, the values and the
codes of the five security domains — every one of them has a core (ADR 0160). This directory holds no Go
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

`redact`, the family's fifth domain, shipped as an engine alone (ADR 0101
§D5); ADR 0160 §1 gave it a core, `redact/`, holding the port a test double
implements, the values every implementation writes, and its two codes.

Every code of the family is declared here (ADR 0160 §2): each member holds its
port's verdicts AND the refusals its engine raises, so a domain's codes are in
one place and the engines under `internal/service/security` declare none.

## Members

| Package | What it declares | Code range | Engine |
|---|---|---|---|
| `authz/` | the `Policy` and `Condition` FUNC ports, the three-valued `Decision` (`Abstain` is the zero), the immutable `RequestValue` and the typed `AttrValue`; no registry (ADR 0057) | `0.2.26.*`, `0.3.56.*` | `internal/service/security/authz` |
| `redact/` | the `Redactor` port frozen at five methods, the `DocumentValue` a JSON rendering returns, and the values every implementation writes — `Placeholder`, `Ellipsis`, `MinBytes`, `Unencodable` (ADR 0101, ADR 0160) | `0.3.73.*` | `internal/service/security/redact` |
| `secret/` | the redacting `Value`, the `Store` port frozen at five over never-reused versions, `VersionValue` and the name grammar (ADR 0096); the `SubjectKeyStore` port frozen at five, `SubjectKeyValue` and the subject grammar (ADR 0142) | `0.2.37.*`, `0.3.68.*` | `internal/service/security/secret` |
| `session/` | the `Store` port frozen at five with `Sweeper` as a sibling, the `Sealer`, the opaque redacting `ID` and the immutable `SessionValue` (ADR 0045) | `0.2.14.*`, `0.3.46.*` | `internal/service/security/session` |
| `token/` | the one-method `Issuer` and `Verifier` ports, the redacting `ClaimsValue` and the closed `Algorithm` enum in which `none` has no representation; no registry, because its key would be the attacker-written `alg` header (ADR 0042) | `0.2.13.*`, `0.3.44.*` | `internal/service/security/token` |

A code keeps its value when its declaration moves (ADR 0160): the `0.2.*`
ranges above are the ones these packages declared before the family existed,
and each `0.3.*` range is the one its engine allocated in the service layer —
`LL` records the layer that allocated a range, not the directory its
declaration lives in. `codeRangeOwners` names these directories under the same
keys.

## Do NOT

- Put Go code in this directory. A file here would make `security` a package
  of its own, and the family a domain nobody chose.
- File a cryptographic scheme here because a security domain calls it: a
  hash, a MAC, a signature or an AEAD is the crypto family's.
- Renumber a member's codes to match its new path. Nothing derives a code from
  a directory, and a consumer branches on the value (ADR 0160).
