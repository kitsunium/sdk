# third-party/entitlement/

## Purpose

The **ssh implementation of `internal/core/entitlement.Identity`**, plus
enrolment: minting a subject identity locally and turning it into a request the
vendor can act on (ADR 0079).

The mechanism this identity feeds — roster, signature, offline cache,
anti-rollback ratchet, CI seat, version floor — is NOT here. It lives in
`internal/service/entitlement` behind the public `pkg/v1/entitlement` facade,
and importing that facade costs a consumer zero additional modules.

**Lives under `third-party/` (root module)** because `golang.org/x/crypto/ssh`
pulls `golang.org/x/term` and through it **introduces** `golang.org/x/sys` —
**banned SDK-wide**. Measured, not assumed: a probe module importing only
`x/crypto/ssh` requires `x/sys v0.48.0` indirect. Quarantining here mirrors
ADR 0022/0034 (the HCL codec) and ADR 0012 (the AWS writers).

Its error codes are `internal/core/entitlement`'s, range `0.2.35.*`; this
package mints none of its own.

## Contents

| File | Role |
|---|---|
| `sshidentity.go` | `SSHIdentity` — the three port methods over a key directory |
| `sshidentity_compliance.go` | the compile-time proof that it still satisfies the port |
| `discover.go` | which subject this machine is enrolled as, and the refusal to guess |
| `key.go` / `key_unix.go` / `key_windows.go` | load, fingerprint, prove possession, permission checks |
| `enroll.go` | `NewSubjectID`, `GenerateKeyPair`, `IssueURL` |

## What it is NOT

A wall against a determined attacker. A check running on someone else's machine
is removable by definition. It stops casual sharing and makes revocation real
for cooperative installs, and the package doc says so rather than implying
otherwise.

## Why-this-shape

- **Possession is proven against material the user ALREADY has.** A file this
  package invented would need a lifecycle — where it lives, who may read it, what
  happens on rotation — that a user's own key directory already has.
- **`DiscoverSubject` refuses rather than guesses.** Several identities in one
  directory is ambiguous, and picking one would silently decide which licence
  gets verified, which one a rotation overwrites, and which one `status`
  reports. It sorts only so the diagnostic is stable, never to choose.
- **A stray `.pub` must not win.** `usableIdentities` narrows to the subjects
  whose private half is present as a real FILE — a directory or a FIFO named
  `<uuid>.pub` stats without error and would otherwise count as an identity.
- **`GenerateKeyPair` validates the subject BEFORE building any path.** It is
  the only entry point here that WRITES, and a subject like
  `../authorized_keys` wrote both halves outside the caller's directory until
  a review caught it. `TestGenerateKeyPairRefusesAPathTraversal` pins it.
- **The published half is world-readable on purpose.** The roster hands it to
  everyone, so protecting it locally would be theatre. The private half is
  `0600` and `SignerFromFile` refuses anything looser.

## Do NOT

- Reintroduce an origin, a cache directory, an audience or an enrolment URL as a
  package constant. That is `svcent.ProductValue`'s job, and the suite injects a
  product naming a vendor the implementation never mentioned so a reintroduced
  constant fails rather than passes.
- Add the verification mechanism back here. It is `internal/service/entitlement`
  now, and the whole point of ADR 0079 is that a consumer can reach it without
  this module's dependency graph.
- Move this to `internal/service`. The measurement above is why, and it is
  reproducible in one `go mod tidy`.

## Verification

```sh
bazel test --config=race //third-party/entitlement:entitlement_test
go test -race ./third-party/entitlement/...
```
