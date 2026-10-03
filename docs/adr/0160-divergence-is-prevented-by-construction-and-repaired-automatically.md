# ADR 0160 — divergence of replicated state: prevented by construction, detected at fixed indexes, repaired automatically

- **Status**: Proposed — split from ADR 0152 by the owner's decision Q12; its own challenge is pending
- **Date**: 2026-10-03
- **Deciders**: SDK maintainers
- **Depends on**: [ADR 0152](0152-a-write-is-committed-when-a-majority-holds-it-on-disk.md) (the consensus domain whose state this ADR protects)
- **Related**: [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (siblings), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) / [ADR 0035](0035-pp-range-ownership-enforcement.md) (the code range shared with ADR 0152)

## Context

ADR 0152 adds the `consensus` domain. Its challenge ran two review runs. By
the second, most of the defects found were concentrated in one subject:
detecting that two replicas applied the same log and reached different
states, and repairing that automatically. That covers signed digests,
anchors and bases, state replacement, self-withdrawal and Apply
fingerprints. The owner decided to split (Q12). ADR 0152 keeps the Raft
core. This ADR takes the divergence work, as it stood after round 4, with
every decision mark of its origin kept (R1-25, R2-18, R3-10, R3-27, R3-28,
R4-xx). Nothing was deleted in the move.

The owner's decisions that bind this ADR:
- **Q9**: the first consumer's ref table is held in memory, and rebuilt at
  start from a snapshot and the log.
- **Q10**: divergence handling is FULLY AUTOMATIC, with guardrails. Every
  defect is fixed, and the automation is never removed.
- **Q11**: the durable state-machine ports stay in v1 (ADR 0152 D3).

The objections still open from ADR 0152's challenge on this subject are the
input of this ADR's own challenge. They are listed at the end and are NOT
fixed here yet.

## Decision

### D1 — What ADR 0152 offers, and what this ADR adds

ADR 0152 guarantees a deterministic `Apply`. It is a pure transition of
state the state machine owns, depends on no node-local precondition, and is
called in log order on one goroutine (ADR 0152 D3).

It also exposes the extension points this ADR needs (ADR 0152, section
"Divergence prevention: ADR 0160"):
- `Entry.Version`;
- the entry kinds reserved for check and digest entries;
- the checksum chain;
- checksummed snapshots, through `SnapshotMeta`;
- alarms in `SnapshotMeta` and in `Health`;
- frozen siblings discovered by assertion (ADR 0039).

This ADR adds everything below on top. None of it changes how ADR 0152
starts, replicates or snapshots. A node that does not enable this ADR's
checks runs ADR 0152 alone.

### D2 — The hashing sibling and the machine factory

```go
type HashingStateMachine interface { // R2-18, R3-28
	StateMachine
	Hash() [32]byte                         // incremental, at a check index
	FullHash(view Snapshot) ([32]byte, error) // full recomputation over a point-in-time view (R4-17)
	HashVersion() uint32                    // the algorithm's version, carried in check entries
}
// The engine builds fresh state machines for rebuilds, replays and the
// self-test through Config.NewMachine func() StateMachine (R4-16).
```

```go
func Replay(d DataDir, newMachine func() StateMachine) (Report, error)  // checks every validated hash (R4-16)
```

### D3 — State hash, verdict and automatic repair (moved from ADR 0152 D3)

**State hash, decided by the log (R1-25, R2-18, R4-11).** A hash check is an
entry kind. **Digests exist only at deterministic indexes (R4-11).** Every
index where `index % K == 0` is a check index, and a batch is split there by
force, so every node computes `Hash()` on its apply goroutine at exactly the
same state. The leader may also propose an explicit check entry, every
`StateHashEvery` or when an operation asks for one. That entry only starts a
round at the next check index, and never carries a digest (R4-07). This is
the SDK form of the first consumer's per-repository checksum.

**Only self-proposed, signed digests count (R3-10, R4-07).**
- At a check index, each voter proposes its OWN digest entry. Its origin is
  the TLS identity of the connection that forwarded it, or the leader itself
  for the leader's own. A digest written by anyone else for that voter does
  not count.
- Each digest is SIGNED with the node's key, over
  `(lineage, check index, HashVersion, digest)`. `Apply` verifies the
  signature against the certificate the membership records for that node.
  An unsigned digest, a bad signature, or a tuple that does not match the
  round is invalid and ignored.
- Only one digest per voter per check index counts.
- When a majority of the voters' valid digests agree, `Apply` decides the
  verdict deterministically on every node. `STATE_DIVERGED` names the nodes
  in the minority.
- A simulator case has a malicious leader forge a proof, with its mutation
  (D10).

**What a verdict does, automatically (Q10, R4-08).** The owner's decision is
that divergence handling stays fully automatic. Its guardrails:
- **A named minority node is fenced, then repaired.** It refuses reads,
  reports itself unready, never campaigns, and transfers leadership away if
  it holds it. It repairs itself automatically (below). **Writes stay open**
  for the majority meanwhile.
- `ClearDivergence` only RE-ADMITS a repaired node, and only after its
  `FullHash` matches a majority-validated digest. Issued from a node that the
  alarm names, it needs a second origin (R4-06).
- **An inconclusive verdict** (no majority of agreeing digests), or a verdict
  of an `Apply` bug, refuses writes until an operator acts. No node is blamed.
- Each row of the decision table below names the node it blames, or says
  that it blames none.

**A repair follows a fixed lifecycle (R4-08):**
1. reconstruction in isolation, off the apply goroutine, from a verified
   anchor and the log;
2. replay to a FIXED boundary: a check index with a validated digest;
3. verification: the rebuilt state's `FullHash` must equal that
   majority-validated, self-proposed and signed digest. A node installs a
   state ONLY when this holds;
4. the reconstruction is run TWICE, and both runs must agree before either
   is trusted, so a bit flip during the rebuild cannot install a bad state;
5. atomic replacement. The original state is preserved until its replacement
   has passed step 3;
6. readiness restored, then re-admission through `ClearDivergence`.
A simulator case flips a bit during the rebuild (D10).

**No rebuild storm (R4-09).** A node rebuilds only on a COMMITTED verdict that
names it, or on an inconclusive one, and at most once per check index. A
mismatch seen in the comparison ring only triggers a `CheckStateHash`,
rate-limited per origin. It never triggers a rebuild by itself.

- **Repair stays possible under the alarm (R3-27).** As under `NO_SPACE`,
  `Remove`, `AddLearner`, `Promote`, `TransferLeadership` and
  `ClearDivergence` are still admitted while `STATE_DIVERGED` is raised, and
  so are hash-check, digest and trigger entries (R4-14).
- `Hash()` must be O(1) at the apply point: an incrementally maintained
  digest, not a scan of the whole state (R3-24).
- **The full hash is point-in-time (R4-17).** At a check index, the engine
  captures `Snapshot()` and `Hash()` together on the apply goroutine.
  `FullHash(view)` is then computed off the loop against that view, and
  compared with the captured `Hash()`.

**Divergence prevention (R3-28).** These measures, studied by ten agents and
retained by the owner, make "no majority of agreeing digests" as close to
impossible as the hardware allows, and decidable when it is not. Round 4
corrected each of them (Q10).

1. **A canonical, incremental hash (R4-11).**
   - The digest depends only on the replicated state. It is incremental, and
     versioned: the algorithm's version and the configuration identity travel
     in each comparison. A node that lacks that version declares itself
     unable to verify, which is not a mismatch.
   - A mismatch names the smallest unit the hash can name: for the first
     consumer, the repository (D19).
   - The root is maintained incrementally, as a two-level sum or a
     maintained tree. The real complexity is stated with the implementation:
     O(1) per leaf update for the sum, O(log n) for the root of a maintained
     tree.
   - The sum is computed on `[4]uint64` limbs with `math/bits` carries, in a
     fixed limb and byte order, pinned by known-answer vectors.
   - The weakness is stated: a sum of hashes (AdHash) resists accidental
     divergence, not an adversary who CHOOSES its inputs. That is acceptable,
     because the inputs come from the authenticated log, not from an
     attacker.
2. **Compared at every check index (R4-11).**
   - AppendEntries and their responses carry the latest
     `(check index, root hash)`.
   - Each node keeps a ring of the last `W` pairs. `W` defaults to 4 096, and
     a value below twice `MaxInflight` batches is refused.
   - An index older than the ring is "cannot compare", never a mismatch, and
     those skipped comparisons are counted.
   - A mismatch triggers a hash check (R4-09). The verdict comes from the
     signed, self-proposed digests above, not from the ring.
   - A faulty LEADER is named by that verdict, and it transfers leadership
     away. If it cannot, the followers that see the difference stop
     acknowledging it, and CheckQuorum deposes it.
3. **A local self-check, and what withdrawing means (R4-10).**
   - Each node periodically compares its incremental hash with `FullHash` on
     a point-in-time view (R4-17), and does so after every `Restore`, on a
     staggered schedule.
   - A difference is a local fault, `HASHER_FAULT`. At start and every hour, a
     known-answer self-test runs SHA-256, CRC-32C, a witness `Apply` built by
     the state-machine factory (R4-16) and the decoders. A failure at start
     refuses to start (`SELF_TEST_FAILED`).
   - **To withdraw** is to stop serving reads and stop applying writes,
     while still voting and still storing the log, with `Health` unready. The
     exits are a restart whose self-test passes, or an operator's clear.
   - **Before withdrawing, a node counts** how many members have already
     withdrawn for the same code. If its own withdrawal would cost the
     majority, it raises a replicated alarm instead of withdrawing.
   - A self-test or `FullHash` failure shared by a MAJORITY is a bug in the
     binary, not hardware. The runbook covers it, including a downgrade while
     the cluster version has not been raised.
4. **Never three behaviours (R4-12).**
   - Each member announces, in the replicated configuration, a **behavioural
     fingerprint**. It is the SHA-256 of the outputs of every supported
     `applyVn` on the golden vectors checked into the product, together with
     the version table of `applyVn`. It is computed at start. It does not use
     `debug.ReadBuildInfo`: the fingerprint is stable across releases that do
     not change `Apply`, and it changes when one does.
   - Only build settings checkable at run time are recorded beside it:
     `runtime.Version()` and the default `GODEBUG`.
   - An admission that would create a THIRD distinct fingerprint is refused
     (`APPLY_FINGERPRINT_REFUSED`). A node's old fingerprint is retired when
     it re-announces.
   - When no leader exists, so no new fingerprint can be admitted — a poison
     entry in the middle of an upgrade — an offline, operator-signed
     fingerprint replacement is recorded in the log, and the cluster can
     restart.
   - The fingerprint guards against mistakes, not attackers, and the text
     says so.
   - The `Apply` logic is selected by the entry's version: frozen `applyVn`
     functions, with golden vectors per version checked in CI. New semantics
     start only at an index fixed by a **gate entry**, which the hash
     includes.
   - **Replay gate:** a new binary replays the latest verified anchor and the
     log tail OFFLINE with `Replay` (R4-16), and must reproduce every
     validated hash before it votes (`BINARY_REPLAY_MISMATCH`).
   - One voter at a time applies to the ROLLING upgrade path. The full-stop
     path makes the replay gate mandatory on every node before it votes
     (R4-20, D19).
5. **Determinism by construction, checked with the standard library
   (R4-22).**
   - `Apply`, `ApplyBatch` and `Hash` live in a package with an allow-list of
     imports.
   - The checker uses no `golang.org/x/tools` and is no separate module. It
     is a stdlib function the PRODUCT runs from its own tests:
     - `go list -deps -json` gives the import closure, which must stay
       inside the allow-list;
     - `go/parser` and `go/types` find goroutines, `select`, floating point,
       `range` over a map, and unstable sorts in the package.
   - The round-3 transitive call-graph analysis is replaced by that
     import-closure rule, on the precedent of `tools/sdkguard`
     (`tools/sdkguard/CLAUDE.md:31-37`).
   - The forbidden list stands: goroutines, channels, `select`, `sync`,
     `runtime`, `unsafe`, `reflect`, `os`, `os/exec`, `time`, `math/rand`,
     `hash/maphash`, floating point, an unsorted `range` over a map, unstable
     `sort.Slice`/`SortFunc`, `unicode` and case folding, writes to package
     variables. Most are forbidden by the import rule, the rest by the AST
     checks.
   - `Apply` sees only its entry.
   - The build is pinned: the toolchain and `godebug` in `go.mod`,
     `GOTOOLCHAIN=local`, `CGO_ENABLED=0`, `-trimpath`. A node refuses to start
     when `GODEBUG` is set.
6. **Deposition on repeated chain refusals (R4-13, moved by Q12).** The
   checksum chain on entries is part of ADR 0152 (D4, R5-05). This ADR keeps
   only its escalation: after N identical refusals at the same index, a
   follower stops answering that leader's heartbeats, so CheckQuorum deposes
   it. N is not fixed yet (open objection, below).
7. **Bases and anchors: cut and verification are decoupled (R4-14).**
   - Snapshots are cut every `SnapshotEvery`, as before. A cut snapshot is a
     **base**, checksummed, and bounds compaction.
   - A snapshot that falls due proposes its own hash check, so
     `StateHashEvery = 0` cannot starve verification.
   - **Anchors** are the verified subset of bases. A base becomes an anchor
     when three things hold: its file passes its checksum, a full
     recomputation equals its recorded hash, and that hash equals one
     validated in the log.
   - When no base has become an anchor for N intervals, the node keeps
     cutting bases and raises an alarm in `Health`. The age of the latest
     anchor is a metric.
   - The full replay interval from the OLDEST recovery anchor still promised
     is retained, with the evidence that verified it. Otherwise an older file
     is not a local fallback, and the text says so.
   - The start-up replay is bounded by the distance to the latest anchor.
     That is about `2 × SnapshotEvery` only while anchors keep being
     verified.
   - **A node whose local anchors ALL fail verification starts with no
     table.** It votes and stores the log, applies nothing, and waits for an
     install from a peer.
   - A simulator case reaches the log quota with no anchor.
   - At any difference, the repair lifecycle above runs. The decision table:

   | Observation | Verdict | Blames |
   |---|---|---|
   | live state ≠ the twice-agreeing rebuild | that node's memory or `Apply` run failed | that node |
   | same content, different hashes | the hasher failed | the node whose hasher disagrees with the validated digest |
   | the rebuilds agree with the validated digest | they are the truth; the others are repaired | each node not matching it |
   | the rebuilds differ on equal entries, on stable nodes | a bug in `Apply` | none: writes refused until an operator acts |

8. **The Git projection stays outside the consensus hash.** Its own digest is
   part of ADR 0152 (D19, R4-25, R5-07).
9. **Tests** (D10 here).
10. **Operations** (D7 here).

**What remains, stated honestly.** With three nodes, one fault at a time is
tolerated. Three different digests are still decidable by the reconstruction
of point 7. The only case with no culprit is a bug in `Apply` that differs
between builds. Points 4 and 5 and the tests block it upstream. If it still
happens, writes are refused and no node is blamed.


### D4 — Anchors on top of checksummed snapshots (moved from ADR 0152 D7, D14, D17)

ADR 0152 cuts snapshots on `SnapshotEvery`, checksums them, and starts a node
from its latest checksummed snapshot plus the log (ADR 0152 R5-01). This ADR
adds **anchors** on top: a snapshot whose state hash a majority-validated
digest confirms. Adding anchors does not change ADR 0152's start-up or
compaction rules, except where stated here.
- From ADR 0152 D7, before the split: **Cut
  and verification are decoupled (R4-14):** every cut is a checksummed base
  that bounds compaction, and a base becomes an anchor when a validated
  digest confirms it (D3, point 7). Only anchors are sent to a peer as a
  verified state.
- From ADR 0152 D7, the install rule, before the split:
  - **an installed snapshot is verified as an anchor only once the verdict
    entries for its index have arrived (R4-29).** Until then it is a base,
    usable for catching up, not a source of repair;
- From ADR 0152 D17, `RestoreSnapshot`, before the split (R4-15): it founds
  the new log with an anchor-validation record, identical on every node. The
  record holds the snapshot's state hash and the `Expected` digest the
  operator confirmed. The restored snapshot is therefore a verified anchor
  from the first entry, and a cold restart can verify it.
- From ADR 0152 D14, before the split:
  - **Memory.** A repair holds a second table beside the live one until the
    replacement is verified, so the budget counts TWICE the measured table.
    Repairs are staggered across nodes (R4-18).
  - **Start.** The start sequence inserts the self-test and the verification
    of the latest anchor before the replay (R4-18).
  - **Disk.** Two anchors are kept (R3-28, R4-28).

### D5 — The determinism checker (moved from ADR 0152 D2)

**The determinism checker is a stdlib package (R3-28, R4-22).** The
round-3 analyzer needed `golang.org/x/tools` and a separate module. Both are
withdrawn. `pkg/v1/consensus/detcheck` is a stdlib-only checker built on
`go list -deps -json`, `go/parser` and `go/types` (D3, point 5). The product
calls it from its own tests. A query of `scripts/check-layer-deps.sh` asserts
that no non-test package imports it, so the checker never ships inside a
production binary (R4-29).


### D6 — Versioning of `Apply` (moved from ADR 0152 D18)

An entry's version selects the frozen `applyVn` that applies it. New
semantics begin only at the index of a **gate entry**, and the hash includes
it (R3-28). The behavioural fingerprint, the refusal of a third fingerprint
and the replay gate are in D3 above, point 4.

### D7 — Operations (moved from ADR 0152 D17)

- `CheckStateHash`, and `ClearDivergence`, which re-admits a repaired node
  (D3);
- **Hardware evidence (R3-28).** Each node exports its EDAC/MCE memory and
  machine-check counters when the platform exposes them, and logs its CPU
  model and microcode at start. Whether OVH's hosts use ECC memory is a
  question to ask OVH. A hash fault on a node without ECC is read in that
  light.
- **Withdrawn nodes (R4-10).** The runbook covers a node withdrawn for
  `HASHER_FAULT` or `SELF_TEST_FAILED`: restart it, and if its self-test
  passes it returns; otherwise replace its hardware or its binary. When a
  majority fails the same way, the binary is at fault, and the runbook
  downgrades it while the cluster version has not been raised.
- **Replay gate:** `forgejo cluster inspect --replay` runs `Replay` (D2) on a
  stopped node (R4-16, R4-20).

```go
func (n *Node) CheckStateHash(ctx context.Context, op Operator) (Position, error)
func (n *Node) ClearDivergence(ctx context.Context, op Operator, alarm AlarmID, reinstated []NodeID) error
```

### D8 — Errors and metrics (moved from ADR 0152 D12 and D13)

These codes keep their serials in the `0.2.57.*` range that ADR 0152
allocates to `internal/core/consensus`. ADR 0152 marks them reserved for
this ADR.

| Code | Reason | Meaning |
|---|---|---|
| `0.2.57.14` | `STATE_DIVERGED` | the replicated divergence alarm: it names the minority nodes; writes are refused until an operator acts, while membership changes, transfer and `ClearDivergence` stay admitted (D3 here, R2-18, R3-10, R3-27); with no majority of digests, the reconstruction of D3 decides (R3-28) |
| `0.2.57.21` | `HASHER_FAULT` | the incremental hash differs from a full recomputation: a local fault; the node withdraws on its own, without a vote (D3 here, R3-28) |
| `0.2.57.22` | `BINARY_REPLAY_MISMATCH` | a new binary's offline replay did not reproduce a validated hash; it may not vote (D3 here, R3-28) |
| `0.2.57.23` | `APPLY_FINGERPRINT_REFUSED` | an admission would create a third distinct `Apply` fingerprint (D3 here, R3-28) |
| `0.2.57.24` | `SELF_TEST_FAILED` | a known-answer self-test failed at start or on its hourly run; the node refuses to start or withdraws (D3 here, R3-28) |

| Metric | Kind |
|---|---|
| `consensus.state_hash.mismatches` | counter |
| `consensus.anchor.age`, `consensus.hash.comparisons_skipped` | gauge, counter (R4-11, R4-14) |

### D9 — Configuration (moved from ADR 0152 D14)

```go
	NewMachine          func() StateMachine // fresh instances for rebuilds, Replay and the self-test (R4-16)
	HashCheckEvery      uint64     // K: a check index every K entries, batches split there (R4-11)
	HashRing            int        // W: comparison pairs kept (R4-11)
	StateHashEvery      time.Duration        // 0: hash checks only on request
```

| Field (R4-11) | Owner | Default | Validation |
|---|---|---|---|
| `HashCheckEvery` (K) | engine | none yet; to be set from measurement | refuse zero |
| `HashRing` (W) | engine | 4 096 | refuse below twice `MaxInflight` batches |


### D10 — Tests (moved from ADR 0152 D15)

**Divergence tests (R3-28).**
- A **state-hash oracle** runs after every `Apply` in the simulator: every
  node's hash at a common index must agree.
- A **non-determinism mutation lane** injects, one at a time:
  - an unsorted `range` over a map;
  - a read of the clock;
  - a field left out of the hash;
  - a `Restore` that drops tombstones;
  - a skipped entry;
  - a wrong incremental hash.
  Each must be caught, and the verdict must name the right node and index.
- `Apply` is **fuzzed** against a reference model, and two instances against
  each other.
- **Bits are flipped** in the state, in snapshots and in the hasher's input,
  including on two nodes within the same window.
- **Differential replay across processes**, varying `GOMAXPROCS`, `-race`,
  `GODEBUG`, amd64 and arm64, the current Go and the next: every replay must
  produce the same hashes.
- A real run ends with a **full diff of the tables** of every node.

Mutations and scenarios moved from ADR 0152:
- a proof entry that carries digests, and a leader that forges one (R4-07);
- a digest counted without its signature, or proposed by another node
  (R4-07);
- a bit flipped during a rebuild, which must not be installed (R4-08);
- the log at its quota with no verified anchor (R4-14);

## Consequences / Semantics

- ADR 0152 can be reviewed, accepted and implemented without this ADR. This
  ADR cannot be implemented without ADR 0152.
- Divergence handling is automatic (Q10). An operator acts only on an
  inconclusive verdict or an `Apply` bug, where writes are refused and no
  node is blamed.
- The error codes and metrics above leave ADR 0152 and are reserved there.

## Breaking changes

None. Nothing is implemented yet.

## Deferred

- Every open objection below, as the input of this ADR's own challenge.

## Open objections from the 0152 challenge

These objections were raised against ADR 0152 in round 5 of its challenge
(review run 2, round 2) on the subject that moved here. Q12 makes them the
input of this ADR's own challenge, unfixed. The topic given beside each id
comes from the round-5 triage, where it says one. The full text is in that
round's reviewer reports.

| Id | Topic named in the triage |
|---|---|
| B3 | signed self-proposed digests, signing keys, key rotation (`RotateKey`), signature domain separation |
| M5-2 | the same |
| R5-A2 | the same |
| V9 | the same |
| R5-A3 | anchors, anchor validation, re-derivation decision table, automatic repair lifecycle, the `NewMachine` factory, the replay gate, repair through `Restore` on the live instance |
| M5-1 | the same |
| M5-6 | the same |
| V2 | the same |
| V10 | the same |
| V11 | the same |
| M10 | self-withdrawal and its guard |
| M5-4 | the same |
| V13 | the same |
| V15 | the self-test and known-answer vectors |
| M8 | the behavioural fingerprint, `ApplyVectors`, never three fingerprints, offline fingerprint override |
| V14 | the same |
| Go-1 | the determinism checker and its import rule |
| V16 | the same |
| M5-3 | not named in the triage's list of moved topics |
| V6, V7, V8 | not named individually (V6 is also cited by ADR 0152's R5-01) |
| Go-2, Go-3, Go-4, Go-5 | not named individually |
| interpretations | the values of K (`HashCheckEvery`), W (`HashRing`) and the two N of D3 points 6 and 7, left symbolic in R4 |

## References

- ADR 0152, D3, D7, D12, D13, D14, D15, D17 and D18 before the split, at
  commit `328e4580`, and its challenge triages R1 to R5.
