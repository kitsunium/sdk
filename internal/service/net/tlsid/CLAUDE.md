<!-- updated: 2026-10-24T04:00:00Z -->
# internal/service/net/tlsid/

## Purpose

The filesystem half of the network domain's TLS surface (ADR 0029). This package
does **I/O and nothing else**: it reads PEM files and hands the bytes to
`internal/core/net.NewIdentityValue`, which owns every validation rule. That split is
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

Public façade: `pkg/v1/net/tlsid`.

`identity.go` adds ADR 0174's `Identity` (`NewIdentity`, `LoadReloadable`,
`PeerOf`, `Deadline`, `SPKI`): the chain and roots verified on every
handshake before the caller's check, and a file identity reloaded in place.

- `PeerOf` sets `Chain` only from `VerifiedChains`; under the identity's own
  `VerifyConnection` that is empty, never the presented certificates
  (review of PR #301, L1).
- The roots only grow until the process restarts (ADR 0174 D1, L2). A roots
  file that cannot be read is no change; one that names no certificate, an
  empty one included, is counted once and never replaces nor narrows them.
- A look, at most once per `Every`, reads the three files and decides by
  their bytes (SHA-256 per file), never by their times (ADR 0174, amendment
  of 2026-10-24, #633): a copy that keeps times (`cp -p`, `rsync -a`,
  `tar x`) or a coarse filesystem gives a new file an older or the same
  mtime. Unchanged bytes are not parsed again, whatever their times.
- A refused pair is read again at every look, and counted once for the bytes
  refused (L3); a pair caught mid-write is refused, then taken at the next
  look once whole. It is parsed from the very bytes the look compared.

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
- Decide that a reloadable identity's file changed by its metadata — mtime,
  size, inode, ctime —, nor skip a look because a time is not newer. A copy
  that keeps times hides a change from the mtime; on a coarse filesystem an
  in-place rewrite of the same size within the same second hides it from all
  four, and Windows has no inode nor ctime to read. A CA rollover is then
  never trusted, a renewal never served (#633). Compare the bytes.

## Cost — deliberately not benchmarked

**`Load` does no work per request and none per handshake, so it ships a
paragraph instead of a `BENCH.md`.** This section is about `Load` alone; the
`Identity` of `identity.go` is not covered by it, and does work per handshake
(the chain and roots verified before the caller's check) and per look, at most
once per `Every` (the three files read and hashed, parsed only when their
bytes changed), which is not benchmarked here. `Load` calls
`os.ReadFile` at most four times and hands the bytes to
`corenet.NewIdentityValue`. It is invoked once, at wiring time; no internal
caller reaches it on a request path, and `go build -gcflags=-m` reports not one
heap escape in `tlsid.go` (`identity.go` has escapes; it is not on this path). The identity it produces is consumed once, to build
a transport or a listener, and from there every handshake is `crypto/tls` over a
config the SDK never touches again.

Benchmarking `Load` would measure `os.ReadFile` and `x509.ParseCertificate` —
the operating system and the standard library — and print the result under this
package's name. The one number that would genuinely be about certificate
material, how long a PEM bundle takes to parse, belongs to
`core/net.NewIdentityValue` and is paid once per process.

That emptiness is ADR 0029's split working as intended: the filesystem I/O is
here, every validation rule is in `internal/core/net`, so disk-sourced and
memory-sourced material are held to one standard. What remains on this side is
`os.ReadFile` plus four error wraps. There is nothing here to regress.

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
