<!-- updated: 2026-09-02T00:00:00Z -->
# internal/service/net/tlsid/

## Purpose

The filesystem half of the network domain's TLS surface (ADR 0029). This package
does **I/O and nothing else**: it reads PEM files and hands the bytes to
`internal/core/net.NewIdentity`, which owns every validation rule. That split is
deliberate — it is what guarantees that material loaded from disk and material
supplied in memory are held to *exactly* the same standard, so neither path can
accept what the other refuses.

## Surface

```go
func Load(p corenet.IdentityFileParams) (corenet.IdentityValue, error)
```

The parameter type lives in `internal/core/net`, beside `IdentityParams`, its
in-memory twin. Both describe the material an identity is built from, which is
core's vocabulary; this package contributes the filesystem I/O and nothing else.
Declaring the on-disk half here would have put one domain concept on both sides
of the layer boundary and left the two free to drift.

Public façade: `pkg/v1/tlsid`.

## Why-this-shape

- **No validation lives here.** `Load` reads four optional files and delegates.
  Adding a rule to this package would immediately create the memory/disk
  divergence the split exists to prevent — put it in `internal/core/net`.
- **"Not configured" and "configured but unreadable" are different answers.** An
  empty path yields nil bytes (the caller did not ask for this material). A
  non-empty path that fails to read, or that reads as **zero bytes**, is
  `TLS_MATERIAL_INVALID`. The empty-file case matters: without it an empty trust
  bundle would reach core indistinguishable from "no bundle", and silently widen
  trust to the platform store — the exact silent downgrade the domain forbids.
- **The path is echoed in the error field; the file contents never are.** A path
  is operator-supplied configuration and naming it is what makes the error
  actionable. Key material is not.
- **`ServerName` is typed optional and is close to mandatory in practice.** Any
  deployment reached through a port forward or a tunnel dials `127.0.0.1` while
  the peer certificate names the real service; the handshake then fails every
  time until `ServerName` overrides the verified name. The field doc says so.
- **`CertFile` without `KeyFile` is refused** (in core). It is always a
  configuration slip, never an intention, and an mTLS client that silently
  degrades to plain TLS is a security hole that reports nothing.

## Error range

None of its own. Every failure is `internal/core/net.TLSMaterialInvalid`
(`0.2.11.15`), wrapped with `field`, `path` and — when there was one — `cause`.
Per ADR 0029 the service layer declares **no** codes.

## Imports allowed

stdlib (`os`) + `internal/kernel/errs` + `internal/core/net`. Never `pkg/*`.

## Do NOT

- Add a validation rule here. It belongs in `internal/core/net`.
- Put file contents into an error field.
- Return a partially-loaded identity when one file failed — a half-configured
  TLS identity is worse than none, because it looks configured.

## Verification

```
bazel test --config=race //internal/service/net/tlsid:tlsid_test
# Fallback:
cd internal/service && GOWORK=off go test -race -cover ./net/tlsid/...
# expected: coverage ~89%
```

## Reference

- ADR 0029 — `docs/adr/0029-sdk-net-domain.md`
- Contract layer — `internal/core/net/CLAUDE.md`
