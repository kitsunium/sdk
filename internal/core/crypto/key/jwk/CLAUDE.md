<!-- updated: 2026-10-03T12:00:00Z -->
# internal/core/crypto/key/jwk/

## Purpose

The **codes and sentinels** of the JSON Web Key format (RFC 7517), at the core
path that mirrors `internal/service/crypto/key/jwk` (ADR 0160 §2). The format
itself — parse, render, select by `kid`, cross to the schemes' encodings — is
the service's; this package declares what it refuses with, and nothing else.

Range `0.3.42.*`. It was allocated to the service package and keeps its value
here: `LL = 3` records the layer that ALLOCATED the range, not the directory its
declaration lives in (ADR 0160 §3). It imports `internal/kernel/errs` and
nothing else.

## Surface

| Symbol | Kind | Notes |
|---|---|---|
| `CodeJWKMalformed` … `CodeJWKAmbiguousKid` | `errs.Code` | `0.3.42.1` – `0.3.42.11` |
| `Malformed`, `MissingMember`, `UnsupportedKeyType`, `UnsupportedCurve`, `InvalidEncoding`, `KeyMismatch`, `NoPublicForm`, `NoPrivateMaterial`, `TypeMismatch`, `KeyNotFound`, `AmbiguousKid` | `*errs.Error` | each var's name is its Reason in SCREAMING_SNAKE form (ADR 0020) |

Consumers: `internal/service/crypto/key/jwk` (imported as `corejwk`), and
`pkg/v1/security/token`, whose `CodeJWK*` constants and `JWK*` sentinels alias
these (ADR 0074 — an alias points at the layer that owns the symbol).

## Error codes

| Code | Sentinel | HTTP | Exit |
|---|---|---|---|
| 0.3.42.1 | `Malformed` | 400 | 65 |
| 0.3.42.2 | `MissingMember` | 400 | 65 |
| 0.3.42.3 | `UnsupportedKeyType` | 400 | 65 |
| 0.3.42.4 | `UnsupportedCurve` | 400 | 65 |
| 0.3.42.5 | `InvalidEncoding` | 400 | 65 |
| 0.3.42.6 | `KeyMismatch` | 400 | 65 |
| 0.3.42.7 | `NoPublicForm` | 500 (default) | 70 (default) |
| 0.3.42.8 | `NoPrivateMaterial` | 500 (default) | 70 (default) |
| 0.3.42.9 | `TypeMismatch` | 500 (default) | 70 (default) |
| 0.3.42.10 | `KeyNotFound` | 404 | 65 |
| 0.3.42.11 | `AmbiguousKid` | 400 | 65 |

`0.3.42.12` – `0.3.42.255` reserved. No Public string names a member VALUE: a
JWK carries key material, and an error message is the one place it must never
surface. The Private strings name `service/crypto/key/jwk`, where each
condition is detected, and the rule — never a member's contents.

The two HTTP statuses are named integer constants (`httpBadRequest`,
`httpNotFound`), so this package does not import `net/http` for two numbers.

## Do NOT

- Renumber a code to make its `LL` byte say "core". The value is a wire
  contract, and `codeRangeOwners`' own comment forbids it (ADR 0160 §3).
- Rename a sentinel var: its name is its Reason (ADR 0020), and a Reason is
  what a log query matches on.
- Put the format here — parsing, marshalling, the curve checks. Reading or
  writing a wire format is a mechanism and stays in the service (ADR 0160 §4).
- Quote a member value, a `kid` or key material in a Public or Private string.

## Verification

```sh
bazel test //internal/core/crypto/key/jwk:jwk_test
# Fallback
cd internal/core && GOWORK=off go test -race ./crypto/key/jwk/...
```

`Test_sentinels` pins each sentinel's code and Reason, and that every code
stays inside `0.3.42.*`; the errs AST audits (`//internal/kernel/errs:errs_test`)
see this package through `//:audit_sources`, and `codeRangeOwners` names this
directory as the owner of `0x00_03_2A_00`.
