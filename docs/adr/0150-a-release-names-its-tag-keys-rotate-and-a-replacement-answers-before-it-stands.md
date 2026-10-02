# ADR 0150 — a release names its tag, its keys rotate, and a replacement answers before it stands

- **Status**: Proposed
- **Date**: 2026-09-28
- **Deciders**: SDK maintainers
- **Amended by**: [ADR 0158](0158-distribution-mechanisms-are-the-frameworks-not-the-sdks.md) — the domain moves to the framework
- **Amends**: [ADR 0077](0077-a-self-update-is-an-order-of-operations-and-a-product-name-is-not-part-of-it.md) (§Deferred: the older-release replay; the one-way replacement)
- **Related**: [ADR 0091](0091-a-single-trust-anchor-is-a-key-with-no-way-out.md) (an ordered, bounded anchor list), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (the `Link` sibling), [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (the status line, first consumer)

## Context

The platform's plan makes the status line update itself silently (D11): a
daemon, launched by its clients, replacing its own binary with no human to
answer a prompt and no privilege to escalate to. ADR 0077's self-update was
built for a CLI a person runs, and four of its properties do not hold for that
product: one key, so a rotation strands every installation (ADR 0091's
argument, one domain over); a signed manifest that does not name its release,
so a host can replay an older signed release under a newer name (ADR 0077
§Deferred); consent only from an environment variable a daemon's user never
sets; and a one-way replacement, so a release that crashes on start leaves a
status line that is simply gone.

## Decision

1. **Keys.** `Service.WithVendorKeys(keys...)` links an ORDERED list, at most
   four (the tail dropped and logged), and a manifest verifies when ANY key
   verifies it. `WithVendorKey` is the one-element list. A list of broken keys
   is `NO_VENDOR_KEY`, as one broken key always was.
2. **A signature domain.** `Service.WithSignatureDomain(domain)` makes the
   signed bytes `domain || 0x00 || manifest`, so a key the vendor also signs
   other documents with cannot have one read as this product's release; and it
   makes the manifest carry `# tag <tag>` and `# expires <RFC 3339>`, read only
   after the signature verified. Another tag is `SIGNATURE_INVALID`
   (`condition=tag_mismatch`), a past expiry `statement_expired`, a missing
   line `statement_incomplete` — the supply-chain class, where no retry helps.
   Without a domain the historical form verifies unchanged, so ktn-linter's
   published releases keep installing.
3. **A probe.** `Service.WithProbe(args, timeout)` hard-links the running
   binary to `<binary>.prev` before the rename — through a `Link` sibling of
   the frozen `FileSystem` port, so the binary's name is never absent —, runs
   the new binary with `args` within `timeout` (five seconds by default), and
   renames `.prev` back when it does not exit 0: `PROBE_FAILED` (`0.3.66.8`),
   `rolled_back` in its fields.
4. **Consent the product gives.** `Service.WithAutomaticConsent` is the
   product's consent to unrequested upgrades, read by
   `Service.AuthoriseUnattendedUpgrade`; an explicit `<PREFIX>_AUTO_UPGRADE`
   still wins, `0` refusing. `Service.WithoutElevation` forbids the privileged
   replacement whatever `<PREFIX>_ALLOW_SUDO` says. The two stay independent:
   consenting to an upgrade never grants escalation. They are options of the
   Service a product builds rather than fields of `Source`, which says where
   releases come from — and which stays within the 64 bytes a value is passed
   in.

## Consequences / Semantics

- A release pipeline adopting a domain signs `domain + NUL + manifest` and
  writes the two header lines at the top of `checksums.txt`; the checksum
  parser already skips lines that are not `<hex>  <name>`.
- Windows remains refused for the replacement (ADR 0095): a product there
  notifies instead of replacing, which the status line does.

## Breaking changes

None: every addition is an opt-in method of `Service`; no published shape
changes.

## Alternatives considered

- **A signed JSON release statement beside the manifest.** Refused: a second
  signed document is a second thing to fetch, verify and keep in step, where
  two header lines inside the already-signed manifest cost nothing.
- **Rename the running binary aside instead of linking it.** Refused: the
  binary's name is absent between the two renames, and a status line is
  executed several times a second.
- **Consent through the environment only.** Refused: a daemon's user never
  sets it, and D11 is the product's decision, not the operator's.

## Deferred

- A probe on Windows, where the replacement itself is refused.
- A key list inside the signed document (keys announcing their successor).

## Verification

```sh
GOWORK=off go -C internal/service test -race ./selfupdate/
GOWORK=off go -C pkg test ./v1/selfupdate/
```

## References

- The platform's plan, "kit design-first" v7, D11 and §9.
