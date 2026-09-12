# third-party/entitlement/

## Purpose

Machine entitlement: turns local key material plus a vendor-signed roster —
freshly fetched, or the last one this machine authenticated when no origin
answers — into a `GrantValue`, or refuses with one of fifteen typed sentinels
(ADR 0078).

**Lives under `third-party/` (root module), NOT `internal/service`**, because
`golang.org/x/crypto/ssh` pulls `golang.org/x/term` and through it **introduces**
`golang.org/x/sys` — **banned SDK-wide** — into the dep-light service module.
Measured, not assumed: a probe module importing only `x/crypto/ssh` requires
`x/sys v0.48.0` indirect. Quarantining here mirrors ADR 0022/0034 (the HCL
codec) and ADR 0012 (the AWS writers). **Opt-in**: nothing in `pkg/v1` imports
it, so public-API consumers inherit nothing.

Error range `0.3.65.*` (`0x00_03_41_*`).

## What it is NOT

A wall against a determined attacker. A check running on someone else's machine
is removable by definition. It stops casual sharing and makes revocation real
for cooperative installs, and the package doc says so rather than implying
otherwise.

## The trust model has exactly one anchor

`vendor`, the ed25519 public key the consuming binary links in. Everything else
— the roster, the per-subject keys, the host serving them — is untrusted input.
A hostile endpoint can serve whatever it likes; it cannot forge a signature made
with the vendor's private half.

## Why-this-shape

- **One thing on disk: the last bundle authenticated.** Cached bytes go back
  through `ParseBundle` on every read, so the signature is re-checked and a
  frozen copy stops authorising at its own `ExpiresAt`, at most `RosterLifetime`
  after it was signed.
- **The frozen CLOCK is not answered, and cannot be.** Every source of time an
  offline process can read belongs to the party being checked. The ratchet
  (`checkClock`) raises the cost; it does not close the hole. Stated, not
  implied.
- **"Cannot decide" is never "no".** A cold `Verify` reaches the network FIRST;
  only when no origin answers does the cache substitute, inside `currentRoster`,
  so the update floor, the CI seat, the subject match and the grant deadline all
  run unchanged on it. Failure with nothing cached is `RosterUnreachable` — and
  it names the NETWORK, because the network is what failed.
- **`ProductValue` carries everything vendor-specific.** Origins, cache
  directory, OIDC audience, enrolment URL. The source implementation kept all
  four as package constants, which is the only reason a correct implementation
  served exactly one binary.
- **`ProductValue.Validate` is a test that became an API.** The source asserted
  over its own shipped constants that origins are distinct, uniquely named, and
  span at least two HOSTS — because branches on one host share that host's
  outage. Moving the origins to the caller would have moved that property out of
  reach with them.
- **Every method on `ProductValue` tolerates a nil receiver.** A `Service` built
  without a product gets the documented fallbacks rather than a panic on the one
  path that runs when nothing else is working.
- **`ProductValue` methods take a pointer receiver.** 72 bytes — four strings
  and a slice header — and copying all of it to read one field is what
  KTN-VAR-BIGSTRUCT exists to catch.

## Do NOT

- Add a second trust anchor, or a fallback that verifies less.
- Reintroduce an origin, a cache directory, an audience or an enrolment URL as a
  package constant. That is `ProductValue`'s job, and the suite injects a product
  naming a vendor the implementation never mentioned so a reintroduced constant
  fails rather than passes.
- Move this to `internal/service`. The measurement above is why, and it is
  reproducible in one `go mod tidy`.

## Verification

```sh
bazel test --config=race //third-party/entitlement:entitlement_test
go test -race ./third-party/entitlement/...
```
