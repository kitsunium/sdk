# ADR 0078 — entitlement ships under third-party/ because proving key possession brings x/sys with it

- **Status**: Accepted
- **Date**: 2026-09-12
- **Deciders**: SDK maintainers
- **Related**: [ADR 0022](0022-sdk-codec-hcl.md) (the first quarantine), [ADR 0034](0034-hcl-quarantine-rationale-corrected.md) (its mechanism, corrected — and the method used here), [ADR 0012](0012-logger-writer-registry.md) (the vendor-writer quarantine policy), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a refusal is not a degradation), [ADR 0077](0077-a-self-update-is-an-order-of-operations-and-a-product-name-is-not-part-of-it.md) (the versement that precedes this one)

## Context

A product distributed as a binary often needs to know whether the machine
running it is entitled to. `kodflow/ktn-linter`'s `pkg/license` answers that with
a design worth keeping: a vendor-signed roster with a bounded freshness window,
possession of a local key proven rather than asserted, an offline cache that
re-verifies the signature on every read, an anti-rollback clock ratchet, and a CI
seat established from the runner's OIDC provenance rather than from a shared
secret in the repository.

The mechanism is generic. The trust model is stated honestly — it stops casual
sharing and makes revocation real, and it is explicitly not a wall against a
determined attacker, because a check running on someone else's machine is
removable by definition.

Two things stood between it and the SDK.

**The vendor was compiled in.** The publication origins, the cache directory, the
OIDC audience and the enrolment URL were package constants. That is the whole
reason a correct implementation served exactly one binary.

**And `golang.org/x/crypto/ssh` brings `golang.org/x/sys`.** Possession is proven
against the key material the user already has, in OpenSSH format, which the
standard library cannot parse. Measured with a probe module the way ADR 0034
insists — importing nothing but `x/crypto/ssh`:

```
require golang.org/x/crypto v0.57.0
require golang.org/x/sys v0.48.0 // indirect   ← via golang.org/x/term
```

`x/sys` is banned SDK-wide. It is why `proc`'s syscall code is written against
raw stdlib `syscall`, and why `net` reaches `recvmmsg` and `SO_REUSEPORT` the
same way.

## Decision

**`third-party/entitlement`, in the root module.** Not `internal/service`, not
`pkg/v1`.

This is ADR 0022's answer to ADR 0022's question, reached by ADR 0034's method:
measure what a dependency introduces, and quarantine it in the root module when
the answer is `x/sys`. Nothing in `pkg/v1` imports it, so public-API consumers
inherit nothing — the same shape as the AWS writers (ADR 0012) and the HCL codec.

Three decisions inside it:

### 1. `ProductValue` carries every vendor-specific fact

Origins, cache directory name, OIDC audience, enrolment URL. A product that names
none still works: the cache falls back to a package-scoped directory rather than
claiming the cache root, and the audience falls back to a value specific enough
that no other product would mint a token for it — never the empty string, which
some issuers read as "any".

Every method tolerates a nil receiver, because the one code path that runs when
nothing else is working must not be the one that panics.

### 2. `Validate` is a test that became an API

The source implementation asserted, over its own shipped constants, that its
origins were uniquely named, published at distinct URLs, and spanned at least two
distinct HOSTS — because two branches on one host share that host's outage, so a
list that looks redundant can be a single point of failure.

Moving the origins to the caller would have moved that property out of reach with
them. `ProductValue.Validate` is those assertions, callable by any consumer, at
construction or in its own suite, returning every failure rather than the first.

### 3. Fifteen typed sentinels, and "cannot decide" is never "no"

The fourteen the source carried, plus `ProductInvalid`. The distinction that
matters most is preserved verbatim: when no origin answers and nothing is cached,
the refusal is `RosterUnreachable` — and it names the NETWORK rather than the
cache, because the network is what failed. Reporting that as "not entitled" would
turn an outage into a revocation.

## Consequences

- The SDK gains a complete machine-entitlement mechanism, and `pkg/go.mod` is
  untouched. `internal/service/go.mod` and `pkg/go.mod` still contain no `x/sys`
  — checked, not assumed.
- The cost of the quarantine is real and worth naming: a consumer must require
  the ROOT module to reach this package, which also pulls the AWS SDK and the
  other heavy vendor integrations into its graph. That is the price ADR 0012 and
  ADR 0022 already accepted for their subjects.
- The frozen-clock hole is not closed and is documented as open in three places.
  Every source of time an offline process can read belongs to the party being
  checked; the ratchet raises the cost and does not remove the hole.
- The SDK now has an opinion about roster layout: one signed bundle carrying the
  roster and its signature in ONE document, because two documents cannot be
  fetched atomically.

## Breaking changes

None. `entitlement` is new in this change set, in a module nothing else requires.

## Alternatives considered

- **Split it: the stdlib-pure 92% into `pkg/v1`, the SSH identity into
  `third-party/`.** The roster, cache, ratchet, CI seat and roughtime code is
  stdlib-only; only `key.go` and `enroll.go` (327 lines of 4 075) need
  `x/crypto/ssh`. A port in core with the SSH implementation quarantined would
  put the mechanism in the public API and keep `x/sys` out. It is the better
  long-term shape and it is deliberately not what this change does: it doubles
  the work and would have split a versement across two module boundaries on its
  way in, making the result impossible to diff against its source. Recorded here
  so the option is not lost.
- **Re-implement OpenSSH key parsing against the stdlib.** It is a format with a
  specification, and it is also the one piece of this package where a parsing
  bug is a security bug. `x/crypto/ssh` is the reference implementation.
- **Drop SSH identity for a generated keypair the SDK owns.** It removes the
  dependency and the whole design with it: the point is that possession is proven
  against material the user already has and already protects, not against a file
  this package would have to invent a lifecycle for.

## Deferred

- **The split described above.** If a second consumer wants entitlement without
  the root module, that is the change to make.
- **The frozen clock.** Nothing local can answer it. `checkNetworkTime` queries
  signed network time and is advisory and fail-open, because a mandatory time
  source is an availability dependency on a third party for a check that must
  keep working offline.
- **`RoughtimeServers` ships EMPTY.** The mechanism is implemented and tested;
  the server list is a deployment decision and naming one here would make every
  consumer depend on a host the SDK does not operate.

## References

- [ADR 0034](0034-hcl-quarantine-rationale-corrected.md) — measure what a dependency introduces; the probe method used here
- [ADR 0005](0005-sdk-error-codes-dotted-quad.md) — the code range this claims (`0.3.65.*`)
- ed25519 — RFC 8032, https://www.rfc-editor.org/rfc/rfc8032
- GitHub OIDC for Actions — https://docs.github.com/en/actions/deployment/security-hardening-your-deployments/about-security-hardening-with-openid-connect
- Roughtime — https://datatracker.ietf.org/doc/draft-ietf-ntp-roughtime/
