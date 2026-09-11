# ADR 0040 — a published data shape may still change, and v0 is the only reason

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Related**: [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (the interface half of this problem), [ADR 0007](0007-sdk-release-and-versioning.md) (bump semantics), [ADR 0017](0017-pkg-bare-module-path.md) (the published module), [ADR 0027](0027-sdk-metrics-domain.md) (the change that triggered this)

## Context

ADR 0039 established that a published **interface** is extended by a sibling,
never by widening, because Go interfaces are structural and downstream doubles
break at compile time with no deprecation window.

Labelled metrics immediately raised the other half. `SnapshotValue` changed from
`map[name]value` to `map[name][]seriesValue` — it had to, because a snapshot
must now carry several series per instrument name. And `pkg/v1/metrics.Snapshot`
is a **type alias** to it, so the change reaches every consumer that reads a
snapshot.

There is no sibling trick here. A concrete data shape has one form; a consumer
that ranges over it breaks when the element type changes. The question is
therefore not *how to avoid it* but *when it is allowed at all*.

The honest answer, this time, is: **because the module is v0.**
`pkg/v0.1.26` is the latest tag, and Go promises nothing across v0 minors
(`go.dev/ref/mod#v0-major`). That is a real licence, and it expires.

## Decision

1. **A breaking change to a published shape is permitted while the module is
   v0**, and the commit that makes it must say so explicitly — naming the
   published alias, the old shape, the new one, and v0 as the reason. "It
   compiles here" is not the reason; nothing downstream is in this repo.

2. **`Release-bump: minor` is the correct trailer for such a change at v0**
   (ADR 0007), not because the change is compatible but because v0 minors carry
   no compatibility promise. The trailer records release mechanics, not
   semantics — so the *semantics* belong in the commit body and here.

3. **Past `pkg/v1.0.0`, this option is gone.** The same edit would then require
   either a new named type alongside the old one, a `pkg/v2` module path
   (ADR 0017 reserves the shape), or the change not happening. This ADR exists
   so that a v1-era contributor who finds the metrics precedent reads the
   condition attached to it rather than only the precedent.

4. **The two halves now have one rule between them.** Before changing anything
   reachable from `pkg/v1`:
   - is it an **interface**? Extend with a sibling — ADR 0039. The version does
     not matter; structural satisfaction breaks callers at any version.
   - is it a **concrete shape**? It may change while v0, loudly and on the
     record. After v1, it may not.

## Consequences

- Every future published-surface change carries an explicit statement of which
  half of the rule it falls under. A commit that changes a `pkg/v1` alias
  silently is now a review defect, not a matter of taste.
- The metrics snapshot change stands as the worked precedent: recorded in
  ADR 0027 §Deferred (which keeps its original "deferred at the time" wording
  and dates the update rather than overwriting it) and in the merge commit.
- **A pre-v1 audit becomes necessary before cutting `pkg/v1.0.0`**: every
  concrete type an alias publishes should be reviewed once, deliberately, while
  changing it is still free. That audit is not scheduled here.

## Why not

- **Forbid breaking published shapes now, ahead of v1.** Rejected: it would
  freeze the design at exactly the moment it is still being learned, and v0
  exists so that this does not happen. The labelled-series shape is better than
  the one it replaces; refusing it to protect a promise nobody has made yet
  trades real quality for imaginary stability.
- **Add `SnapshotValue2` and keep both.** Rejected at v0: two shapes for one
  concept, with the old one carrying no users this repo can name, is the cost
  of a compatibility guarantee that does not exist yet.
- **Treat the alias as internal because the type lives under `internal/`.**
  Rejected — this is the same error ADR 0039 corrects. What is published is the
  shape, and an alias publishes it regardless of where it is declared.

## References

- `pkg/v1/metrics/metrics.go` — `type Snapshot = coremetrics.SnapshotValue`
- ADR 0039 (interfaces), ADR 0007 §bump semantics, ADR 0017 (module path)
- [Go modules reference — v0 major version](https://go.dev/ref/mod#v0-major)
