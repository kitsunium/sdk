# ADR 0152 — consensus domain (`consensus`): a write is committed when a majority holds it on disk, and the log is compacted from the first version

- **Status**: Accepted on 2026-10-13 by the owner, on R9 as written after review run 3. The SDK stays public. Its companion ADR 0174 is accepted the same day; ADR 0173, split from it, keeps its own challenge and stays proposed.
- **Date**: 2026-10-02
- **Revised**: 2026-10-12 (R7), 2026-10-13 (R8 and R9), see *Revision history*
- **Accepted**: 2026-10-13
- **Deciders**: SDK maintainers
- **Related**: [ADR 0052](0052-sdk-lock-domain.md) (fencing, and the guarantee a lock does NOT make — this domain is the first in the SDK that can make it across machines), [ADR 0056](0056-sdk-vfs-domain.md) (publication by rename, and the streaming writer it deferred), [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md) (the clock port every timer here runs on), [ADR 0029](0029-sdk-net-domain.md) (the stream connection port and the TLS identity the transport rides), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (frozen ports, siblings), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (clamp or refuse), [ADR 0011](0011-kernel-snapshot-primitive.md) (withdrawn as the snapshot tool, D3, R1-14), [ADR 0068](0068-layer-firewall-is-a-checked-graph.md) (the layers), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (which layer owns a type), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) / [ADR 0035](0035-pp-range-ownership-enforcement.md) (the code ranges), [ADR 0120](0120-a-state-machine-keeps-an-agenda-not-a-sweep.md) (a different "state machine" — see D3), [ADR 0148](0148-a-private-socket-is-gated-by-its-directory-and-the-kernel-names-the-peer.md) (the first consumer's proposal socket, D19); **Split into** [ADR 0173, *divergence of replicated state: prevented by construction, detected at fixed indexes, repaired automatically*](0173-divergence-is-prevented-by-construction-and-repaired-automatically.md) (divergence prevention and automatic repair, Q12). The number 0173 is reserved and the ADR is cited by its title until it merges (R7-00d, R7-03). **Depends on** the TLS sibling ADR of D4, number 0174 reserved, which must be Accepted before this one (R3-17). The pre-handshake admission sibling in `net/server` (R7-26) is folded into it (R7-00d). **Follows** the SDK reorganisation and the design rules: [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md) (the charter), [ADR 0155](0155-every-layer-groups-its-packages-by-family-and-a-path-may-move-while-v0.md) (the `app` family), [ADR 0156](0156-the-public-module-links-the-standard-library-and-nothing-else.md) (stdlib only), [ADR 0157](0157-one-module-per-vendor-released-with-the-sdk.md) (zstd stays in its vendor module), [ADR 0158](0158-distribution-mechanisms-are-the-frameworks-not-the-sdks.md) (no distribution mechanism here), [ADR 0159](0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md) (the kernel), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md) (every service has a core, codes declared in the core, a wire format lives in the service), [ADR 0161](0161-an-untyped-error-fails-the-build.md) (typed errors), [ADR 0162](0162-the-sdk-is-one-module-and-a-release-is-one-tag.md) (one module, one tag), [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md) (the design file), [ADR 0164](0164-an-error-code-is-the-designs-and-kit-writes-it.md) (codes), [ADR 0165](0165-a-performance-contract-is-the-designs-kit-measures-what-the-compiler-decides-and-a-test-holds-the-rest.md) (budgets), [ADR 0166](0166-a-facade-is-the-designs-and-a-forwarders-form-is-measured.md) (the facade), [ADR 0167](0167-a-doc-comment-is-the-designs-and-a-readme-is-written-from-docs-api.md) (doc comments), [ADR 0168](0168-a-declaration-is-the-designs-and-a-body-is-the-codes.md) (declarations and bodies), [ADR 0169](0169-the-sdk-is-rebuilt-from-its-design-in-a-copy-and-a-generation-is-held-to-the-whole-inline-set.md) (rebuilt from the design) (R7-01). **First consumer's deployment** (outside this repository): making.codes ADR 0006 and ADR 0015 (D20, R7-46)

## Context

The SDK has no consensus. `agree` is key agreement, `lock` excludes goroutines
of one process or processes of one machine (ADR 0052 D6), and ADR 0052 D11
sends any lock that spans machines to `third-party/`, where nothing has been
written. A program that must agree with two other machines on the order of its
writes has nothing to import.

The first consumer has that problem exactly. A replicator makes a Forgejo
deployment active on three nodes — Warsaw (WAW), Frankfurt (FRA), Roubaix
(RBX), measured round trips WAW–FRA 22.8 ms, WAW–RBX 27.3 ms, FRA–RBX 9.6 ms.
It replicates the EFFECTS of Git writes rather than the requests. The objects
are written to a majority from the `pre-receive` hook. The reference
transaction `{repository, [(ref, expected SHA, expected revision, new SHA)]}`
is proposed to a replicated log from the `reference-transaction` hook, in its
`prepared` phase. Every node applies the log in order to a replicated **ref
table**, a pure state the log orders, with a per-repository checksum to detect
divergence. **Git is an asynchronous projection of that table** on each node,
with its own durable cursor. A request that follows a write carries the log
position the write committed at, and the node serving it waits until its
projection of that repository has reached it. This is the owner's decision
Q1 of the challenge, recorded in D3 and D19 (R1-01).
The decision to keep this log in a consensus group written from scratch,
rather than in a PostgreSQL table or an existing library, was taken by the
owner on 2026-10-02; this ADR proposes where that code lives and what it
must do before anyone depends on it.

The owner has written a Raft before. **raft-sql-poc** is a laboratory in
Node.js with no dependency: about 11 800 lines, a Raft core (`src/raft.js`,
1 640 lines) with PreVote, CheckQuorum, ReadIndex, leases, snapshots,
single-server membership changes and leadership transfer, a shared
segmented WAL with group commit (`src/storage.js`), a multi-Raft layer with
one group per shard (`src/shard.js`, `src/host.js`), a fixed-layout binary
wire format (`src/wire.js`) over TCP with a per-machine identity
(`src/transport.js`, `src/secure.js`), 169 assertions and a README of
measurements and of the bugs it found in itself. It was read in full for
this ADR. Where it measured a better answer than this ADR's first draft,
the decision below is amended and says so with the measurement, marked
**from raft-sql-poc**. Where this ADR keeps its own answer, the section
*raft-sql-poc (owner's prior Raft)* says why, with the file and line.

The owner's Go system at Halys, **IWFS**, was read next, at `origin/master`
`aec79ab`. Its consensus is Raft-like but kept in memory, with no local
WAL. It was read for ideas only, and none of its code is copied. Its
amendments are marked **from IWFS**. The section *IWFS (owner's Go Raft at
Halys)* lists what is taken, what is not, and a three-column comparison
that says, wherever the two prior arts disagree, which one is right.

The cautionary precedent is recent. GitLab built Raft into Gitaly on
`etcd/raft`, one group per partition, and abandoned it in 2026: the design
document was marked rejected on 2026-09-09 and the `raftmgr` package was
deleted. The code before deletion was read for this ADR. What it never
finished is the list this ADR treats as v1, not as follow-up:

- **No snapshot and no compaction.** `raftmgr/replica.go` says it in a
  comment: "The current implementation does not include Raft snapshotting or
  log compaction. This means the log will grow indefinitely until manually
  truncated."
- **No read path.** Neither ReadIndex nor a lease; reads went to the local
  replica with no linearizability claim.
- **No routing to the leader.** Proposal forwarding was disabled on purpose,
  because the transaction was VALIDATED at the proposer against its local
  state and a forwarded proposal would have been validated against a state the
  leader did not have. That is the deeper lesson: a check made before the log
  orders a write is a check against a state that may already be stale.
- **No PreVote and no CheckQuorum** in the `raft.Config` it built, and an
  election timeout of 200 ms × 20 ticks = 4 s by default.
- **No quiescing of idle groups, no heartbeat coalescing, no partition
  tests** — the cost of one group per partition, paid before the group itself
  was finished.

The `raftmgr` directory alone held 3 858 lines of production Go and 6 530 of
tests at deletion; with the write-ahead log it was built on, the reported total
is about 14 000. That is the order of magnitude of what this domain proposes,
and Gitaly's history is the evidence that the hard part is not the part a
textbook covers first.

## Decision

Add **`consensus`** as a new domain of the `app` family (ADR 0155), with a
core and a service (ADR 0160 §1): `internal/core/app/consensus`,
`internal/service/app/consensus` and the facade `pkg/v1/app/consensus`
(R7-01). Both code ranges, `0.2.57.*` and `0.3.92.*`, are declared in the core
package (ADR 0160 §2, R7-02). The domain is designed in
`design/app/consensus.yaml`, and kit writes its codes, ports, facade and doc
comments (ADR 0163, R7-00a). It lives inside the one SDK module (ADR 0162,
R7-00b). One algorithm ships, Raft, and there is **no registry**.

**The SDK is a library and nothing else (Q4, R2-36).** It ships no
`package main`, no daemon, no CLI, no admin HTTP route and no admin handler.
Every operation, online or offline, is a Go function. A product that embeds
the domain owns its process, its command line, its administration interface,
its authorization and its audit. For the first consumer, that product is
Forgejo itself (Q5, Q6, Q7, D17, D19). No `framework/consensus` package or module ships
in v1, because Forgejo is not a kit application (R8-37).

### D1 — The name says the capability, the constructor says the algorithm

The domain is named for what a caller gets — agreement on an order — not for
the algorithm that produces it, the way `lock` is not called `flock` and
`token` is not called `jwt`. The algorithm is named once, where it is chosen:
`consensus.NewRaft`. Rejected names:

- **`raft`** names an implementation.
- **`replica`** names a participant, not the capability, and reads as a
  database read replica — the asynchronous copy this domain exists NOT to be.
- **`agree`** is taken by key agreement (ADR 0014), and a reader meeting
  `agree.Propose` beside `agree.SharedKey` would be right to be confused.

**What is and is not algorithm-neutral (R1-28).** The first draft claimed
that every port spoke terms and indexes that Multi-Paxos would speak as
well. That overstated it. Only the CONSUMER port, `Replicated` (D14), is
neutral: `Propose`, `Barrier`, `WaitApplied`, `Status`. The storage and
transport ports — `LogStore`, `SnapshotStore`, `Transport` — and their
values are Raft's: a hard state of term, vote and commit, a log with Raft's
truncation rules, Raft's messages. A second engine would bring its own,
and the package says so instead of pretending otherwise.

No registry, for ADR 0052 D10's reason sharpened: an engine resolved from a
configuration string is a place where a typo selects different safety
properties with no failure at the moment of the swap.

### D2 — Placement, design file and allowed dependencies (R7-00a, R7-00b, R7-01, R7-02)

| Layer | Package | Holds | May import |
|---|---|---|---|
| core | `internal/core/app/consensus` | every type a port speaks or that implementing a published port requires (ADR 0074, R2-32): `NodeID`, `Incarnation`, `ClusterID`, `LogIdentity`, `GroupID`, `Position`, `Entry`, `EntryKind`, `HardState`, `Stamp`, `Member`, `Membership`, `Message`, `AuthenticatedPeer`, `SnapshotMeta`, `SnapshotSink`, `SnapshotHandle`, `InstallRequest`, `InstallOffer`, `InstallAccept`, `Token`, `Versions`, `TimingContract`, `Status`, `StopReason`. The ports `Replicated`, `StateMachine`, `Snapshot`, `LogStore`, `LogReader`, `SnapshotStore`, `Transport`. The frozen siblings `DurableStateMachine`, `BatchingStateMachine`, `UnreachableReporter`, `LeadershipWatcher`. The codes and sentinels of BOTH ranges, `0.2.57.*` and `0.3.92.*` (ADR 0160 §2, R7-02) | kernel only (`errs`, `clock`) |
| service | `internal/service/app/consensus` | the Raft engine (a pure step function, its event loop and its storage actor), the segmented file log with its `_linux.go` I/O, the snapshot store, the mTLS transport, the wire codecs of `Message`, `SnapshotMeta` and `Token` (ADR 0160 §4, R7-05), the offline functions over the shipped layout, the in-memory log and transport doubles, the simulator's fault models (test-only) | kernel. Core `app/consensus`, `app/lock`, `net`, `proc`, `observe/metrics`, `observe/trace`, `observe/logger`, `data/transform`. Service `app/lock` (the data-directory lock, D11), `net/server` (the inbound stream groups), `net/tlsid` (the identity, D4), `data/transform` (flate and gzip, bounded) |
| pkg | `pkg/v1/app/consensus` | the facade, written by kit into `facade_gen.go` from the design's `facade:` section (ADR 0166): aliases for every type needed to implement a published port (R2-32), `NewRaft`, `Bootstrap`, `Init`, `OpenDataDir`, the offline functions `Recover`, `RestoreSnapshot`, `Inspect` and `ClearQuarantine`, the codecs `AppendMessage` and `ParseMessage` and their `SnapshotMeta` and `Token` pairs (R7-05), the adapters' constructors, the sentinels | its own lower layers, sibling `pkg/v1/*` |

The domain imports neither `data/vfs` (D4 says why) nor
`kernel/concur/batcher` (D6 says why). `proc` is imported for
`UnsupportedPlatform` only (Deferred).

**The design file (R7-00a).** One file, `design/app/consensus.yaml`, is
written on the model of `design/app/lock.yaml`, under `apiVersion:
kit.kitsunium/design/v2`. It holds three packages: the core, the service and
`pkg/v1`. Each carries its `codes:`, `sentinels:`, `ports:`, `types:`,
`functions:` and `doc:`, and `pkg/v1` its `facade:`. `design/sdk.yaml` gains
the two ranges under `codes.ranges`, `0.2.57` and `0.3.92`, both owned by
`internal/core/app/consensus`, under its `placement: core-at-engine-path`.
kit writes `codes_gen.go`, `design_gen.go` (the ports), `facade_gen.go`,
`doc.go` and the api pins (ADR 0163 §2, ADR 0164, ADR 0166, ADR 0167). The
code writes the bodies, the concrete types and the test fixtures (ADR 0168).
Nothing else is hand-declared. There is no hand-written `codes.go`, no
hand-maintained `codeRangeOwners` entry (kit writes it into
`internal/kernel/errs/codes_gen_test.go` from `design/sdk.yaml`), and no
gomarkdoc README. The surface is closed (`surface: closed`), so every
exported symbol of D14 is in the design file, or `make api-check` fails.

**What stays hand-maintained, named (R7-02).** The core package's
`audit_srcs` filegroup and its line in `//:audit_sources` (`BUILD.bazel`),
which the errs AST audit reads. The regenerated `docs/error-codes.yaml`
(`make error-codes`). The alloc lane's hand-written targets
(`tools/alloc-lane-targets.txt`). The values never change: `LL` records the
layer that allocated a range, so `0.3.92.*` keeps its `3` although the core
declares it (ADR 0160 §3).

**One module, the standard library only (R7-00b, ADR 0156, ADR 0162).** The
domain lives in the SDK module, `github.com/kitsunium/sdk`, and ships with its
one tag. It requires no module outside the standard library: no `go.etcd.io`,
no `hashicorp/raft`, no `golang.org/x/*`. The vendor modules do not change.
zstd already exists in `third-party/transform` (range `0.3.63`), and no
package under `internal/`, `pkg/` or `framework/` may import it (ADR 0157).
The v1 snapshot codec is therefore a stdlib bounded codec, flate or gzip
through `data/transform`'s `BoundedDecompressor`. zstd reaches a product only
through the product's own adapter, and only once the vendor module adds
`DecompressBounded` in its own change (D7, R7-06).

**The external compile test (R2-32, R7-00e, R7-11).** A test in the `e2e`
module implements every published port from `pkg/v1/app/consensus` alone. It
runs with `GOWORK=off` in the go-build lanes, not under Bazel, so a missing
alias fails in CI rather than in a consumer's build. The facade also carries
`TestItLinksNoModuleOutsideTheSDK`, which fails when the facade's import
closure leaves the SDK module (R7-01). There is no `tools/` row and no `cmd/`
row: the SDK has no `package main` (Q4, R2-36).

The service-to-service edges are lateral and permitted (`internal/CLAUDE.md`).
Nothing is added to the kernel: the step function is generic only to Raft, so
it fails rule 1's second half.

**Two declared siblings outside this domain (R1-23, R2-20, R7-26).**
- The TLS sibling in `net/tlsid`, in the three parts D4 lists. Its ADR is
  number 0174, reserved, under the working title *a TLS identity is checked
  on every handshake and reloaded in place*.
- A pre-handshake admission hook in `net/server`, keyed on the remote
  address before any TLS byte is read (D4). It is folded into ADR 0174
  (R7-00d).
That one ADR is cited by title until it merges, and it must be Accepted
before this one.

### D3 — The caller's state machine: a pure transition, a frozen port, durable siblings

```go
type StateMachine interface {
	Apply(e Entry) any
	Snapshot() (Snapshot, error)
	Restore(r io.Reader, meta SnapshotMeta) error
}
type Snapshot interface {
	WriteTo(w io.Writer) (int64, error)
	Release()
}
// Frozen siblings, discovered by assertion (ADR 0039).
type DurableStateMachine interface { StateMachine; AppliedPosition() (LogIdentity, Position, error) }
type BatchingStateMachine interface { StateMachine; ApplyBatch(es []Entry, results []any) }
```

**`Apply` is a pure transition of state the state machine owns (R1-01).**
It changes nothing outside that state: no `git update-ref`, no file, no
network call, no webhook. And it depends on no precondition that can differ
between nodes: not the content of a local Git repository, not the time, not
the presence of an object on disk. The first draft let the first consumer's
`Apply` run the compare-and-swap against each node's Git refs, which made
the outcome depend on local state the log does not order. That is the
Gitaly lesson of Context turned on this ADR's own consumer. Per the owner's
decision (Q1), the first consumer replicates a **ref table** it owns, and Git
becomes an asynchronous projection of that table (D19).

`Apply` is called for every committed `EntryKind` `Command`, in log order, on
ONE goroutine, exactly once per entry per incarnation of the state, and must
be **deterministic**: the same entries in the same order produce the same
state on every node. It returns a value, delivered only to the `Propose` that
created the entry on the node that created it (`Propose` returns it typed,
D14). It returns no error, and that is the design: a command a state machine
refuses — an expected value that does not match — is an ANSWER, carried in
the result, and it is the same answer on every node because it is computed
on the same table. An apply that cannot complete is not a refusal; the state
machine must stop, because a node that skips an entry has silently forked.
`Apply` panicking is treated the same: the node stops with
`STATE_MACHINE_FAILED` and does not apply further. **That entry IS committed
(R1-13):** `STATE_MACHINE_FAILED` never means "not written", and the doc
comment says so.

raft-sql-poc is the counter-example this rule exists for. Its core treats
every exception `apply` throws as deterministic and part of the output
(`raft.js:1109-1117`), and its SQLite state machine throws
"changeset conflict on apply: replica state diverged" (`sqlstore.js:83`).
A divergence the replica has DETECTED therefore reaches the proposer as an
ordinary `SQL_ERROR` while the replica keeps applying on a forked state.

**The applied position is durable with the effects (R1-02).** A state machine
that keeps its state on disk implements `DurableStateMachine`. It persists the
`Position` of the last applied entry ATOMICALLY with the state that entry
produced — in the same transaction or the same published file — and returns
it from `AppliedPosition`. On open, the engine resumes applying after that
position. It skips `Restore` when the state machine is already at or past the
snapshot's position, because restoring an older snapshot over newer applied
state rolls the state back. A state machine that does not implement the
sibling is treated as empty at every start, and the engine restores the
latest snapshot and replays the log. The simulator crashes between an effect
and its position, in both orders, and a mutation that shifts the persisted
position by one must be caught (D15).

**Durability is declared, not guessed (R2-09).**
- `Config.Durability` says whether the state machine is durable. A value that
  disagrees with whether the state machine implements `DurableStateMachine`
  is refused with `CONSENSUS_MISCONFIGURED`.
- `AppliedPosition` returns a `LogIdentity` with the position: the
  `ClusterID` and a log epoch that `Recover` and `RestoreSnapshot` bump.
  An applied position from another log identity is not trusted. The engine
  forces a `Restore` when a snapshot of the current identity exists, and
  refuses with `DATA_DIR_MISMATCH` when none does.
- On restart, the durable applied position is reconciled with the lazily
  written commit index. The applied index is a validated lower bound for
  commit: commit starts at the larger of the two, and an applied position
  beyond the log's last index is `DATA_DIR_MISMATCH`.
- `Restore` persists the snapshot's position and log identity atomically with
  the restored state.

**Lineage across a recovery (R3-01).** A `LogIdentity` carries a lineage: the
chain of `(ClusterID, log epoch)` it descends from. `Recover` records the old
identity and the last index it kept. The engine accepts an applied position
of the PREDECESSOR identity when its index is at or below that bound and its
term matches the kept log's term at that index. Otherwise the state machine
must be restored. When the recovered node has a snapshot, `Recover` re-labels
that snapshot with the new identity. When it has none, `Recover` writes the
new identity and the bound in one durable record before anything else, so a
crash leaves either the old directory or the fully re-labelled one. A
simulator case runs `Recover` under a durable state machine and crashes it at
each step.

**Engine state has its own recovery pass (R3-21).** The engine's own
replicated state — timing contract, cluster version, alarms, retired IDs —
is rebuilt on restart by a pass over the committed log up to the commit
boundary. That pass runs even when the state machine's applied position lets
the engine skip application commands, so a durable state machine ahead of
the snapshot cannot make the engine forget an alarm or a retired ID.

**Batching apply.** A state machine that implements `BatchingStateMachine`
receives committed entries in batches, with one result slot per entry, so a
durable state machine can make a batch durable in one write (R1-28).

**Snapshots need a point-in-time read store (R1-14).** `Snapshot` is called on
the apply goroutine and must be cheap: it captures a view of the state at the
current applied position and returns. `WriteTo` then runs on the snapshot
goroutine while `Apply` continues, and `Release` frees the view. That
requires a state machine whose store can serve a consistent read of an older
version while it takes newer writes: a copy-on-write structure, an MVCC
store, or a database read transaction. The first draft named ADR 0011's
`Value[T]` as "the intended tool". That reference is withdrawn: a
copy-on-write `Value` that copies the whole state on write makes every
`Apply` cost O(state). The cost model is in the package `CLAUDE.md`. A view
costs at most O(changes since the cut) in memory while `WriteTo` runs, and
`Snapshot` itself is O(1). A state machine that cannot meet it must say so,
and the engine then cuts snapshots with apply paused, as raft-sql-poc does.
It declares so with `Config.SnapshotPausesApply` (R2-34). The engine then
schedules the cut and accounts for the pause in its apply-lag bound, instead
of discovering it.
`Restore` cancels an in-flight `WriteTo` and waits for its `Release` before it
replaces the state, and it is serialised with `Apply`.

**Divergence prevention: ADR 0173 (Q12).** Detecting that replicas which
applied the same log reached different states, and repairing that
automatically, moved to ADR 0173, *divergence of replicated state: prevented
by construction, detected at fixed indexes, repaired automatically*, by the
owner's decision Q12, with every
related decision of rounds 3 to 5. ADR 0152 keeps only the contract it
offers:
- **A deterministic `Apply`**, as this section states: a pure transition of
  state the state machine owns, no node-local precondition, log order, one
  goroutine.
- **ADR 0173 is a cluster capability, activated all or nothing (R6-04).**
  - A range of entry kinds is reserved for it. Until `RaiseClusterVersion`
    raises the cluster to the version that enables ADR 0173, an entry of
    that range is `VERSION_UNSUPPORTED`.
  - After the raise, ADR 0173 applies those entries only beyond the gate
    index of the raise. A node running ADR 0152 alone therefore never meets
    one before the gate.
  - `Message` carries no field of ADR 0173. Since R7 the core `Message` is a
    plain value and the transport carries ENCODED frames (D4, R7-05). ADR 0173
    adds `CheckIndex` and `RootHash` as fields of version 2 of the frame
    layout, which the service's `AppendMessage` and `ParseMessage` encode and
    decode. A v1 node refuses a v2 frame at the handshake's version
    negotiation, never by skipping its fields (D18).
  - `SnapshotMeta` has a reserved alarm field that ADR 0152 carries without
    interpreting it.
- **The internal hook points ADR 0173 gets (R6-04).** They are internal to
  the engine, not published API:
  - a per-node gate on three permissions: may campaign, may serve reads, may
    acknowledge;
  - batch boundaries imposed at given indexes;
  - self-proposed control entries, their origin stamped from TLS;
  - a floor on log retention, which compaction respects;
  - **a snapshot pin (R7-38):** `pin(id string) (unpin func(), err error)`,
    internal to the engine. Retention (D7) never evicts a pinned snapshot.
    A pin holds one of the two retained slots of D14. When both retained
    snapshots are pinned, the next cut is written into the cut slot and
    replaces the oldest pin only once validated (D7, R8-22). The log
    still never passes `LogReserveBytes`: reaching it raises the no-space
    alarm (D17).
  - state replacement through `Restore`;
  - a pre-vote gate slot at step 4 of the start sequence (D14).
- **What may delay what at start (R6-04).** Nothing beyond the checksums
  blocks LOADING or REPLAY. Only ADR 0173, through its gate slot, may delay
  the VOTE.
- `Entry.Version`, the entry checksum chain of D4, checksummed snapshots and
  the frozen siblings of ADR 0039 are the other extension points.
- The error codes and metrics of ADR 0173 keep their serials in this ADR's
  range, which reserves them (D12).
- **Where ADR 0173's code lives (R7-04, R7-00e).** Its codes are declared in
  the SAME package, `internal/core/app/consensus`, and the same design file,
  because a range has one owner (ADR 0035). Its determinism checker is not a
  `pkg/v1` path. When it is a module, it is an auxiliary module listed under
  `auxiliary:` in `design/sdk.yaml`, outside `go.work`, stdlib-only, with its
  own `CLAUDE.md`, as `tools/sdkguard` is.
Nothing in ADR 0152 waits for ADR 0173, and a node can run without it.

The name collides on purpose with the textbook and must not be confused with
ADR 0120's `statemachine`, which moves entities between declared states on a
timer. That domain decides WHAT state an entity is in; this port is HOW a
deterministic program is replicated. Both package comments say so.

### D4 — The other ports: log, snapshots, transport, all frozen (ADR 0039)

```go
type LogReader interface {
	// Entries returns entries in [lo, hi), at most maxBytes, and always at
	// least one entry when lo < hi, even above maxBytes. lo below first is
	// COMPACTED. hi above last+1 is an impossible answer, and stops the node.
	Entries(lo, hi uint64, maxBytes int, dst []Entry) ([]Entry, error)
	// Term answers for first-1 too, from the term kept at compaction.
	Term(index uint64) (uint64, error)
	// Bounds of an empty log after a snapshot at s: (s+1, s) (R7-11).
	Bounds() (first, last uint64)
}
type LogStore interface {
	LogReader
	// Append buffers entries and, when hs is not nil, a hard state, in ONE
	// record batch. Sync makes everything appended so far durable.
	Append(entries []Entry, hs *HardState) error
	Sync() error
	TruncateSuffix(from uint64) error      // writes ONE durable truncate record, nothing else (R7-12, R8-01, R8-03)
	CompactPrefix(through uint64) error    // drop what a snapshot covers
	HardState() (HardState, error)
	Stamp() (Stamp, error)                 // ClusterID, NodeID, Incarnation (D8)
	WriteStamp(Stamp) error                // durable on return
	Close() error
}
type SnapshotSink interface { // R3-16, R4-21
	io.Writer                        // receives the ENCODED chunks; checks each chunk's integrity
	Commit(final SnapshotMeta) error // final carries the SHA-256 the ENGINE computed over the DECODED stream
	Abort() error
}
type SnapshotHandle interface { // pinned until Close (R3-16)
	io.ReaderAt
	Meta() SnapshotMeta
	Close() error
}
type SnapshotStore interface { // R7-07
	// Create opens the spool of id. When a spool of that id already exists,
	// it is returned with the ENCODED offset durably spooled so far (R8-21).
	Create(id string, meta SnapshotMeta) (sink SnapshotSink, spooled int64, err error)
	List() ([]SnapshotMeta, error)          // complete snapshots, newest first
	Open(id string) (SnapshotHandle, error) // pinned until Close
	Remove(id string) error                 // refused while a handle is open
}
// Latest, a convenience over List and Open, is a service function
// re-exported by the facade, not a port method (R7-07, R8-36).

// AuthenticatedPeer is what the TLS handshake proved, never what a frame
// claims (R7-05). The certificate proves the slot and the key. The ID and the
// incarnation are the member the transport admitted on that key's SPKI
// (R8-07). A peer that is not yet a member, during a JOIN, has ID 0 and only
// its Slot and SPKI set (R8-09).
type AuthenticatedPeer struct {
	ID          NodeID
	Incarnation Incarnation
	Slot        string   // the slot the certificate's URI SAN names (R7-41)
	SPKI        [32]byte // SHA-256 of the certificate's SubjectPublicKeyInfo (R8-07)
}
type Transport interface {
	// Send hands over an ENCODED frame (AppendMessage). It never blocks and
	// may drop. The transport owns frame after the call (R7-05, R7-11).
	Send(to NodeID, frame []byte)
	// Stream sends a snapshot from the offset the receiver accepted (R7-08).
	// open returns an io.ReadCloser at an ENCODED offset on a chunk boundary; Stream closes it (R3-16).
	Stream(ctx context.Context, offer InstallOffer, open func(offset int64) (io.ReadCloser, error)) error
	// deliver gets the encoded frame and the peer TLS authenticated. frame is
	// valid until deliver returns. The engine copies what it keeps.
	// offer is answered on the event loop, which alone knows the term, the
	// leader and the commit index (R8-21).
	Bind(group GroupID,
		deliver func(from AuthenticatedPeer, frame []byte),
		offer func(from AuthenticatedPeer, o InstallOffer) InstallAccept,
		install func(InstallRequest) error,
		// join is answered OFF the event loop, under a deadline, because
		// it calls the product's JoinAuthorizer. The transport writes the
		// answer on the same connection (R9-06).
		join func(ctx context.Context, from AuthenticatedPeer, r JoinRequest) (Position, errs.Code)) error
	SetMembership(group GroupID, admitted []Member) // per group; the transport computes the union (R2-31)
}
type UnreachableReporter interface { Unreachable(func(NodeID)) } // sibling (R1-11)
// Joiner is the sibling a custom transport implements to carry a JOIN (R8-09).
// The shipped transport implements it.
type Joiner interface { Join(ctx context.Context, cfg JoinConfig) (Position, error) }
// JoinAuthorizer is the PRODUCT's port. A member calls it before it turns a
// JOIN into an AddLearner. It decides whether the slot's previous member is
// fenced, which only the product can know, and returns the Operator to record
// (R8-09).
type JoinAuthorizer interface { Authorize(ctx context.Context, r JoinRequest) (Operator, error) }

// The install handshake (R7-08), on the snapshot connection.
type InstallOffer struct { // sender to receiver
	Term       uint64
	SnapshotID string       // equal to Meta.ID (R8-21)
	Meta       SnapshotMeta // ID, index, term, encoded and decoded sizes, chunk size, SHA-256, codec id
}
type InstallAccept struct { // receiver to sender
	SnapshotID string
	Offset     int64 // the chunk-aligned ENCODED offset to resume from, 0 for a fresh spool
	Refused    errs.Code // zero, or why: SNAPSHOT_TOO_LARGE, a stale term, an id already held (idempotent success)
}
type InstallRequest struct { // what the transport hands the engine once bytes flow
	Ctx        context.Context // ends with the connection (R8-37)
	From       AuthenticatedPeer
	Offer      InstallOffer
	Offset     int64     // the accepted offset
	Body       io.Reader // the encoded stream from Offset, ending with the connection
}
```

**Snapshot ports, completed (R8-21).**
- `SnapshotMeta` carries its `ID`, so a spool, an offer and a handle name the
  same snapshot.
- The receiver's resume point comes from the store: `Create` on an existing
  spool returns it with its durably spooled offset.
- **Chunk framing.** Each encoded chunk is `encoded length (u32) | decoded
  length (u32) | CRC-32C of the encoded bytes (u32) | bytes`, little-endian.
  The sink checks each chunk's CRC as it arrives, and the engine checks the
  SHA-256 over the decoded stream at `Commit`.
- `InstallOffer` and `InstallAccept` are core VALUES. Their wire encoding is
  private to the shipped transport. A custom transport chooses its own and
  calls the `offer` callback with the decoded values.

**The install flow is an offer, an accept, then a stream (R7-08).** The
leader sends `InstallOffer`. The receiver checks the term, the leader, its own
`MaxSnapshotBytes` and what it has durably spooled for that id, and answers
`InstallAccept` with the offset it resumes from, or a refusal. The sender
streams from that offset. A custom transport carries the same three steps, and
a test in the `e2e` module installs a snapshot through a custom transport,
breaks the connection mid-stream and resumes it.

**The LogStore contract (R1-08, R2-04).**
- Term, vote and entries travel in one `Append` and become durable under one
  `Sync`. A commit-only change of the hard state is written lazily and never
  makes a reply wait for a sync.
- **A persisted commit index never passes the last SYNCED index (R7-13).**
  The lazy commit record and the commit in a segment's checkpoint are both
  clamped, when written, to the last index the storage actor knows is synced.
  Otherwise a crash could leave a durable commit index above entries that
  were never durable.
- **Readers see appended entries before they are synced.** The leader sends
  entries to its followers before its own `Sync` returns (D6), so `LogReader`
  must return them as soon as `Append` returns.
- The store allows concurrent readers and exactly one writer, the storage
  actor of D5b.
- **The step function never does I/O.** It reads an in-memory view of the
  unsynced and recent entries (D6, recent-entries window). A read below that
  window is an asynchronous fetch on a READER goroutine of its own, never the
  storage actor, whose queue holds writes only. Its result comes back as an
  event (R8-06).
- `Entry.Data` is immutable once appended: the store and the engine never
  write into it, and a caller that hands bytes to `Propose` gives up
  ownership of them. Bytes returned by `Entries` stay valid until the caller
  reuses `dst` or until a `CompactPrefix` covering them returns, whichever is
  first. A caller that keeps them longer copies them.
- **Expected outcomes are not failures.** An index below the first retained
  one is `COMPACTED`, and an entry that is not yet available is reported as
  such. Both are answers the engine acts on. An I/O error, a checksum failure
  or an impossible answer stops the node: a log that cannot be read cannot be
  trusted to agree with itself.
- `deliver` must not block (R2-08). Inbound messages go into two bounded
  queues, one for control frames and one for data, so a flood of entries
  never starves a vote. Each queue is bounded in BYTES (64 MiB for data,
  provisional) as well as in count. Control frames also have their own small
  per-frame size cap and byte bound, far below the data limits, so an
  oversized "control" frame is refused at its header (R3-20). Forwarded
  administrative entries and hash-check trigger entries travel on the
  control connection, and their size is held under the 64 KiB control cap
  (R4-29). When a queue is full the transport drops
  the message and counts it, as Raft tolerates. The node's total memory
  budget, these queues included, is stated in D14. The contract that carries the safety of the whole domain
is `Sync` and `WriteStamp`: when they return nil, what they cover survives a
power loss.

**The shipped log** is a directory of segment files, each record
`length | CRC-32C | version | batch sequence | end-of-batch flag | term |
index | kind | body checksum | previous header checksum | payload`,
little-endian (the batch fields are R7-12's, the record kinds R8-01's, and
the entry checksum chain is below),
written with `write(2)` into the current segment and made durable once per
batch. **The hard state is a record of the log, from raft-sql-poc.** The
first draft kept term and vote in a separate file published by rename, which
costs two device round trips per vote (ADR 0056's measurement). The POC
writes a hard-state record into the current batch instead, made durable by
the same `fdatasync` as the entries, and writes the commit index lazily so
that a heartbeat which only advances it never makes an acknowledgement wait
for a sync (`storage.js:361-370`). Every segment opens with a checkpoint
record holding the hard state, and segments are deleted only as a prefix:
together the two rules let compaction delete old segments without losing a
vote or resurrecting an entry a later record truncated (`storage.js:35-37`,
`storage.js:507-591`). Adopted.

**The entry checksum chain (R4-13, R5-05).**
- The proposer computes only the checksum of the entry's body.
- The LEADER computes the chain value at its serialized append point: the
  checksum of the previous entry's header, chained.
- Both checksums are CRC-32C: they detect corruption, not an adversary. A
  member that holds the cluster's key can forge an entry whose chain
  matches; the chain protects against a damaged or misplaced entry, and
  the transport's mTLS against an outsider (review of [PR #301](https://github.com/kitsunium/sdk/pull/301), L9).
- Both are verified when the entry is appended and again before `Apply`. A
  mismatch at append refuses the acknowledgement, and the leader resends. A
  mismatch found before `Apply` re-fetches the entry from a peer, or stops
  the node. It never skips the entry.
- **At a snapshot boundary the chain is carried, never reseeded (R5-05).**
  `SnapshotMeta` holds the authenticated chain tip at the snapshot's index,
  and the bytes of the retained suffix are preserved as written. A fixed
  seed is used only at genesis, or after a replicated chain reset.
- **The engine state a snapshot carries is the one at its index ([issue
  #402](https://github.com/kitsunium/sdk/issues/402)).** Like the
  configuration and the chain tip, the timing contract, the cluster version
  and its generation, and the no-space alarm in `SnapshotMeta` are those the
  committed entries through the cut index left, never those standing when
  the cut is made. The engine applies its own entries at commit, ahead of
  the applied index a cut is made at; metadata read from the current state
  named a contract committed past the cut, and a node that installed that
  snapshot and was then truncated back carried a contract whose entry it
  never applied. The step function keeps one record per engine entry that
  changed the state above its log's base (`EngineAt`), folded into the base
  as the log compacts. Deferring the cut while an engine entry sits between
  the applied index and the commit was the alternative; it was not taken,
  because a steady stream of alarms or contracts would starve the cut, and
  `SaveSnapshot` would refuse for a reason the operator cannot act on.
- **No silent loop (R6-05, R7-19).** After N identical chain refusals at the
  same index, a follower stops answering that leader's heartbeats and
  appends for the rest of the term, and no longer takes them as contact
  ([issue #508](https://github.com/kitsunium/sdk/issues/508)): answered,
  each rejection counted as contact for the leader's CheckQuorum and
  triggered an immediate resend, and the refusing followers, their timers
  re-armed by every append, never campaigned. N defaults to 3, provisional,
  and zero is refused. Each
  refusal is counted, and the follower reports itself unready in `Health`,
  naming the leader and the index, while it lasts.
  - **One refusing follower does not depose the leader (R7-19).** R6 said
    CheckQuorum deposes it. That holds only when a MAJORITY refuses. With one
    refusing follower of three, the leader still hears from the other, and
    keeps leading. The refusal is then reported, through `Health` and the
    alert of D13, and the refusing node stays out of the commit quorum until
    the term ends.

**Durability, as the kernel gives it (R1-09, R7-22).** On Linux,
`segment_linux.go` calls `fdatasync(2)`. `fallocate(2)` stays OFF: segments
are zero-filled. If it is adopted later, recovery must know unwritten extents,
which read as zeros although nothing was written there. On darwin,
`os.File.Sync` issues `F_FULLFSYNC`, which is stronger than `fdatasync`, and
slower (R8-06). Elsewhere, `os.File.Sync` is the fallback, and it is stated
as the weaker or slower path it is on each platform. A segment has a fixed size, 64 MiB (provisional),
zero-filled at creation, so a `fdatasync` never has a file size to publish. The lifecycle is crash-consistent
and spelled out:

1. create `seg-N.tmp` and zero-fill it to its full size (R2-05, R7-22).
2. write the checkpoint record at its head and `fsync` the file (R2-05);
3. rename it to `seg-N`, then `fsync` the directory;
4. append records, with `fdatasync` per batch;
5. on rotation, the next segment goes through 1 to 3 before the current one
   is sealed;
6. delete only a prefix of segments, after the checkpoint that replaces them
   is durable, and `fsync` the directory after the unlinks.

**What a recovery may delete, and what it must refuse (R2-05, R3-08).**
- Only an INCOMPLETE `.tmp` is deleted: a file that never reached step 3.
- A published segment whose checkpoint is invalid is corruption, not an
  interrupted rotation. It is quarantined. Round 2's reading, "no valid
  checkpoint means an interrupted rotation", was only true before the rename,
  and the rename is now after the checkpoint's sync.
- **A sealed segment is read up to its sealed length (R9-02).** Its
  activation record names that length. Bytes beyond it are ignored.
- **Continuity is judged only on the effective log (R8-01, R9-02)**, after
  the truncate records are applied, never on raw indexes.
- **A zero header at the end of a segment that is not the last** is
  classified by the decision procedure below, like any other hole. R7's
  separate rule for it is withdrawn.
- The simulator zeroes a synced header sector in the middle of a segment. It
  tests a corrupt checkpoint separately from a crash before publication.

**The next segment is prepared in the background, never published early
(R3-24, R4-01).**
- It is zero-filled while the current one fills, as a `.tmp`, in a
  "prepared" state distinct from every active segment. It is never a
  published later segment, which recovery would read as later data.
- Its checkpoint is written at ACTIVATION, with the CURRENT hard state, not
  the hard state of the moment it was prepared.
- An activation record seals the previous segment, with its used length and
  the boundary it hands over.
- Recovery keeps the highest term it has seen in any checkpoint or record,
  then the last vote record of that term (R5-05). It ignores a segment holding only a checkpoint as "later data".
- A simulator case crashes with a prepared segment present, and the safety
  oracle checks vote uniqueness.

**Tail repair is narrow (R1-10, R2-01).** The first draft truncated any record
whose CRC failed at the tail. That repairs too much: a bit flip in a SYNCED
record at the tail is corruption of acknowledged data, not a torn write. Only
two shapes are truncated:

- a short read, where the file ends inside a record;
- a torn record of the highest, unfinished batch, as the decision procedure
  below defines it (R8-02, R9-03).

**The tear model is stated (R4-02).** The supported model is crash-only: power
loss or a process crash, not a device that rewrites old sectors. A zeroed
sector proves that a record was never acknowledged ONLY when it lies inside
the last unsynced write window, which the writer records as its sync
watermark. A zeroed sector before the watermark is damage to synced data,
and is quarantined.

**The sync watermark is implicit in the write order, and batches are numbered
(R5-02, R6-03, R7-12, R8-01 to R8-04).** Round 5 wrote an explicit watermark
record. R6 replaced it with the ordering the storage actor already keeps: it
writes batch k, syncs it, and only then writes batch k+1. R7 made that
ordering readable on disk. R8 writes the recovery as one procedure, because
R7's rules contradicted each other.
- **Every record header carries its batch sequence number and an
  end-of-batch flag.** Without them a record cannot say which batch it
  belongs to, and the implicit form does not hold.
- **Proof:** any valid record of batch k+1 proves that batch k was synced.
- **Truncation is an explicit record (R8-01).** `TruncateSuffix(from)` writes
  ONE record of kind `truncate`, carrying `from`, in the current batch, made
  durable by that batch's sync. That record is its only effect. It rewrites
  nothing and zeroes nothing (R8-03).
- **Continuity is judged on the EFFECTIVE log (R8-01).** Recovery applies
  every truncate record in order, then checks the indexes. A record whose
  index is lower than its predecessor's is valid only when a truncate record
  with `from` equal to that index precedes it. Any other lower index is
  `LOG_CORRUPT`. A simulator case truncates across a segment boundary, then
  restarts.
- **The decision procedure (R8-02).** Recovery scans every segment and
  classifies before it changes anything.
  1. A HOLE is a short read, a zero header, or a record whose CRC fails.
  2. A hole is a **torn tail** when both hold: the record before it belongs
     to the highest batch present, or ends a batch with its end-of-batch
     flag. And every valid record after it carries that highest batch
     sequence.
     **Batch sequences are contiguous (R9-01).** A hole after batch b
     followed by batch b+2, or by any batch other than b or b+1, is
     quarantined, never truncated. A test zeroes the header of batch k,
     which holds one acknowledged entry, writes a batch k+1 that holds only
     a hard state, and must see a quarantine.
  3. **Whether the highest batch completed decides what damage means
     (R9-03).** The tear signature is checked per 512 B chunk.
     - When the highest batch's end-of-batch record is NOT valid, that batch
       never completed, so it was never synced and never acknowledged. Any
       damage inside it, zeros or garbage, is a tear. Garbage is what a torn
       write through dm-crypt leaves.
     - When its end-of-batch record IS valid, the batch may have been
       acknowledged. A bad CRC inside it is quarantined, never truncated.
     - **The end record is written last, on its own
       ([#417](https://github.com/kitsunium/sdk/issues/417)).** A batch is
       made durable in two steps: every record before the last, then its
       `fdatasync`; then the last record, which carries the end-of-batch
       flag, then its `fdatasync`. With one `fdatasync` per batch a power
       cut DURING it could persist any subset of the batch's dirty pages
       (neither the page cache, the block layer nor the disk orders the
       pages of one file, ALICE, OSDI 2014): the end record's page without
       an earlier one's, a batch never acknowledged, quarantined, and two
       nodes cut together waiting for an operator. Telling such a tear by
       zeroed sectors does not work through dm-crypt, which leaves random
       bytes. Written last, a valid end record proves that the rest of its
       batch was durable before it.
     - **The residual, stated:** a device that loses a chunk of a batch
       after reporting that chunk's sync, then persists the end record, is
       outside the crash-only model. That case is quarantined, so it loses
       availability, never data.
     - **A second residual, stated (review of [PR #301](https://github.com/kitsunium/sdk/pull/301), L7, found again by the strict review, [#422](https://github.com/kitsunium/sdk/issues/422)):** damage to
       the END record of the highest batch AFTER its sync returned (bit
       rot, a misdirected write) cannot be told from a batch that never
       completed. That batch is truncated, and with it what it acknowledged,
       a vote included. The log assumes a medium that does not corrupt
       synced data silently: dm-crypt detects nothing, so detection after
       the sync belongs to the device or to dm-integrity, and a node that
       loses a synced batch this way relies on the other replicas.
     A crash test runs through dm-crypt, as the nodes do.
  4. A torn tail is truncated at the hole. Every other hole is quarantined:
     a valid record of a lower batch after it, a bad CRC that is not a tear
     pattern, a gap in the effective indexes.
  The reason for point 3: in the highest batch, an acknowledged record can
  exist once that batch's sync returned. A rule that truncated any damage
  there would delete acknowledged data (codex, R8-02).
- **What recovery trusts is synced first ([#414](https://github.com/kitsunium/sdk/issues/414)).**
  A process killed between a batch's write and its `fdatasync` leaves the
  batch in the page cache, end record included, and a restart in the same
  boot reads it as complete. Before the node serves anything, recovery
  `fdatasync`s every published segment and syncs the log directory, the
  data directory and the snapshot directory, as PostgreSQL does before
  crash recovery (`SyncDataDirectory`). Without it, a Ready that writes
  nothing new acknowledges entries and votes no device holds, and a power
  cut within the write-back delay loses them. A write-back that failed after
  the process died is reported to the fresh descriptor (errseq, Linux 4.13):
  the open fails with `STORAGE_FAILED`, never a log trusted from pages the
  disk refused. A directory the node creates (the data directory, `log/`,
  `snap/`) has its parent synced, and the audit file's directory is synced
  after each line ([#422](https://github.com/kitsunium/sdk/issues/422)):
  only ext4 and XFS publish a new entry through their journal anyway.
- **Zeroing only after classification, only for a torn tail (R8-03).** When
  step 4 truncates a torn tail, recovery zeroes the rest of that segment and
  syncs it, as etcd's `ZeroToEnd` does (`wal.go:505`, `wal.go:562-570`),
  before any new record is written. Any other outcome writes the quarantine
  marker FIRST, and modifies nothing. The hot path never zeroes: only
  recovery does, and only one tail. A mutation that zeroes before
  classifying must fail.
- **A batch never straddles a rotation (R8-04).** Rotation happens only
  between batches, after the previous batch's sync-done. The seal record of
  segment N is written and synced in N before the first write to N+1. A
  crash test at rotation persists a random subset of the unsynced pages.
  Recovery that finds N sealed and no N+1 only publishes N+1, whose
  checkpoint takes the batch after the seal's: it never seals N again,
  because a second seal is invisible to the next recovery and leaves a hole
  in the batch sequence (review of [PR #301](https://github.com/kitsunium/sdk/pull/301), H2; the dm-crypt lane cuts in
  that window and reopens twice).
- **Nothing else changes.** D5b's sync-done still means "data durable".
  D16 counts two `fdatasync` per batch on each side since
  [#417](https://github.com/kitsunium/sdk/issues/417), the fallback stated
  below: the end record waits for the rest of its batch.
- *Tests:* crashes between a batch's write, its sync, the next batch's write
  and the acknowledgement, in every order the kernel allows. A shorter
  overwrite, then a crash. A crash that persists a RANDOM SUBSET of the
  unsynced 4 KiB pages and 512 B sectors, not only a prefix (R7-12). A bad
  CRC on a complete record of the highest batch, which must quarantine
  (R8-02).
- **Fallback, stated in advance:** if the batch fields are not adopted, or
  review shows the implicit form unsafe, the explicit watermark record of
  round 5 comes back, and D16 is rewritten with two `fsync` per batch. The
  second half was taken by [#417](https://github.com/kitsunium/sdk/issues/417):
  the batch fields stay, and the end record is synced after the rest of its
  batch.

Both are writes that were never acknowledged, because acknowledgement follows
the sync. A node that truncated one of them **keeps voting and acknowledging**,
as etcd does. Round 1 forbade it, and that rule is retracted (R2-01): with
three nodes power-cut together, it would have left no node able to vote.

Any other CRC failure, at the tail or not, is `LOG_CORRUPT`, fail-closed.
Before anything is truncated or moved, the node writes and syncs a
**quarantine marker** in the data directory, naming the segment and offset,
and it keeps the damaged bytes untouched. A node that finds the marker
refuses to start (`LOG_QUARANTINED`) until an operator has inspected the
directory and cleared it, through `Inspect` and the product's own procedure
(D17). **`ClearQuarantine` allows exactly two exits (R3-09):**
1. **WipeAndRejoin (R4-03):** `Remove` the old `NodeID`, then `Init` under a
   NEW `NodeID`, which returns that `(NodeID, Incarnation)`. A voter's
   incarnation is never swapped in place, and a mutation that does so must
   fail;
2. **TruncateAndStaySilent (R4-03):** truncate at the damage, and PERSIST,
   before returning, a silence condition: no vote and no acknowledgement
   until a leader of a term at least equal to the node's own has rewritten
   its log past the truncation point AND has committed an entry in its
   current term. **Its cost, stated (R5-13):** with one of the two other
   nodes down, no leader can exist, and the node stays silent until the
   third returns. **The condition is persisted before the truncation
   ([#418](https://github.com/kitsunium/sdk/issues/418)):** its term, the
   highest any readable record names, is read before anything changes and
   written durably first, never below a condition a crashed run already
   persisted. A rerun after a crash past the truncation reads a log whose
   dropped records no longer name their term; a lower term would let the
   node vote twice in the term of a vote the truncation dropped.
There is no third exit that keeps the damaged node voting.

**Product commands (R5-12).** The first consumer exposes both exits as
`forgejo cluster quarantine clear --exit wipe|truncate`. The wipe exit is
split in two: offline, wipe and `Init` under a NEW `NodeID`, which the
command prints; online, `Remove(old)` through the site administration.
With TWO nodes quarantined, the runbook is `Recover` on the healthy one,
with the other two `Init`ed as learners (D17).

The simulator flips bits in synced records, not only in the unsynced
tail. A liveness scenario cuts power on all three nodes at once, with torn
tails, and the cluster must elect and commit again (D15).

A failing `fsync` stops the node (`STORAGE_FAILED`) and is never retried. The
PostgreSQL "fsyncgate" of 2018 established that a retried `fsync` can return
success after the kernel has dropped the dirty pages the first one failed to
write; a node that continues after one is a node whose acknowledged entries
may not exist. raft-sql-poc does not retry either, but it does not stop: after
a failed `fsync` it records the error and never syncs again
(`storage.js:489-495`), so every later acknowledgement waits forever. That is
safe and silent. Here the node stops and says so.

**After `STORAGE_FAILED`, the node refuses to start until the machine
reboots (R7-14, R8-05).** The page cache may still hold pages the device
never wrote, and an ordinary read would return them. R7 offered three
remedies. R8 keeps one rule.
- `STORAGE_FAILED` writes its marker in TWO places (R9-05): the data
  directory, and a boot-scoped tmpfs path. Each holds the kernel's
  `/proc/sys/kernel/random/boot_id` and the offset of the last fully synced
  batch.
- `NewRaft` refuses to start while EITHER marker carries the current
  `boot_id`. After a reboot the page cache is gone, and the node starts.
- When neither marker can be written, the node exits with a distinct code,
  73, which the guide adds to `RestartPreventExitStatus`.
- **The guarantee under Nomad** is that at least one marker survives the
  allocation's restart: the data directory is the host volume, and the
  tmpfs path is on the host.
- **Fail closed before any mutation.** After a storage failure, nothing
  writes to the log or the snapshots until the marker check has passed.
- The `O_DIRECT` re-read is dropped. On these nodes the data directory sits
  on a buffered loop device over the LUKS image, so `O_DIRECT` on the file
  does not bypass every cache.
- Copying the trusted prefix into a fresh, synced segment is allowed, after
  the reboot. The prefix ends at the last fully synced batch the marker
  records.
- `Config.RuntimeDir` is withdrawn.
A test fails `fdatasync`, restarts in the same boot and is refused, then
restarts with another `boot_id` and starts.

**Each stop has its own exit code and its own marker (R7-14, R7-44, R8-35).**
The exit codes are declared on the sentinels in the design file, with
`errs.WithExitCode`, as `app/lock` does. `StopReason.ExitCode()` returns
`errs.ExitCodeOf` of the stop's error. Final stops:
`STORAGE_FAILED` 74, `LOG_QUARANTINED` 65, `STATE_MACHINE_FAILED` 69,
`VERSION_UNSUPPORTED` 76, and 73 when no `STORAGE_FAILED` marker could be
written (R9-05). A stop that a restart may cure, such as a lost
quorum at `Close`, exits 75 (`EX_TEMPFAIL`). None uses 70, the errs default,
so a code without an option never looks like a consensus stop.
- `STORAGE_FAILED` writes the marker above.
- `STATE_MACHINE_FAILED`, `LOG_QUARANTINED` and `VERSION_UNSUPPORTED` write a
  persistent marker in the data directory, which only an operator clears.
- `NewRaft` refuses to start while a marker applies. A supervisor's
  exit-code filter is not enough: Nomad's `restart` block has none.
- The consumer guide sets `RestartPreventExitStatus=74 65 69 76 73` under
  systemd, and a crash-loop alert (D13).

**Not on `vfs`.** The `vfs` write half is frozen at whole-file verbs plus
`WriteAtomic(name, data []byte)` (ADR 0056); it has no append, no handle and
no `Sync`, and ADR 0056 deferred streaming writes as "a different port with a
different atomicity story". A segmented log is that different story, so it is
its own port. The snapshot store applies ADR 0056's publication rules — a
temporary in the target's directory, `fsync` of the file, `rename`, `fsync` of
the directory — to a STREAM, which `WriteAtomic` cannot take without holding
a whole snapshot in memory. It is the second consumer that would justify a
streaming publication sibling in `vfs`; that sibling is deferred to its own
ADR rather than decided here.

**The shipped transport** rides ADR 0029. Inbound is stream groups on
`pkg/v1/net/server` with `server.TLS(identity)` and a `ConnHandler`. Outbound
dials with the same `tlsid.Identity`'s `ClientConfig()` (the HTTP client of
`pkg/v1/net/client` is not a stream dialer).

**Identity is checked in both directions, from the certificate (R1-23, R2-20,
R2-21).**
- `server.TLS` and `tlsid` expose neither the peer's certificate nor a
  verification hook today. The SDK gains ONE declared sibling (ADR 0039) in
  three parts:
  1. a `VerifyConnection` option: a fail-closed gate that runs on every
     handshake, resumed sessions included;
  2. the verified peer identity, bound to the connection and handed to the
     handler after the handshake;
  3. a reloadable identity source (`GetCertificate` and
     `GetClientCertificate`). A reload validates that the key matches the
     certificate. It refuses a certificate whose SAN differs from the
     current one, keeps the old identity when it fails, and counts the
     failures. During a CA rollover it trusts the union of the old and new
     roots. A connection is closed when its peer certificate reaches
     `NotAfter`, and at a maximum age, so a revoked or expired identity
     cannot ride an old connection.
- **The dialer verifies the chain first (R2-21).** It runs a full x509
  verification — roots and extended key usage — before it checks the URI
  SAN. It never sets `InsecureSkipVerify` without doing that verification
  itself. A mutation that drops the chain check must fail the tests. Node
  certificates carry both `serverAuth` and `clientAuth`.
- **A certificate names a SLOT, not a node (R7-41, revises R2-40).** It
  carries exactly ONE URI SAN, `kitsunium-consensus://<cluster>/slot/<slot>`,
  in a byte-exact canonical form: lowercase scheme, no user, no port, no
  query, no fragment, no trailing slash, the cluster id in lowercase hex and
  the slot in `[a-z0-9-]{1,32}` (R7-25). Any other form, or a second URI SAN,
  is `PEER_REJECTED`.
  - The `NodeID` is a random 64-bit value drawn at `Init`. One that collides
    with the replicated retired set is refused and redrawn.
  - The `AddLearner` entry binds the `NodeID` to its slot, its stamp, its
    incarnation and **its key (R8-07)**: the SHA-256 of the SubjectPublicKeyInfo
    of the TLS key the host generated. Admission checks that the
    certificate's slot equals the member's slot AND that its SPKI equals the
    member's pinned SPKI. The handshake checks the incarnation.
  - **Renewal reuses the key (R8-07).** A renewed certificate carries the
    same key, so the pin holds. A second `NodeID` of the same slot, during a
    replacement, is admitted only under its own pinned key. A certificate for
    the slot with another key is `PEER_REJECTED`, even when it is valid. A
    mutation that admits a claimed ID under another key must fail.
  - **One key, one member (review of [PR #301](https://github.com/kitsunium/sdk/pull/301), M5).** `AddLearner`,
    `Bootstrap`, `Recover` and `RestoreSnapshot` refuse a member whose key
    another member already holds, and a transport given two members under
    one key admits that key for neither: admission goes by key, so two
    members under one key could speak as each other.
  - The dialer checks the listener's SAN, and the listener checks the
    dialer's. A wrong cluster or slot is `PEER_REJECTED`.
  - The transport takes its own cluster and slot from its own certificate's
    SAN. `NewRaft` refuses a `Config.Cluster` that differs from the SAN, and a
    `Config.ID` that differs from the stamp (R3-24).
  - R2-40 read the node id from the SAN. That rule is withdrawn. The slot
    form must be frozen before v1, because the SAN is a wire contract with
    the issuing Vault roles (D20).
- **The engine checks the sender (R7-05).** The transport hands the engine
  the encoded frame and the `AuthenticatedPeer`. The engine parses the frame
  and drops, and counts, a frame whose `From` differs from `from.ID`. A
  mutation in which the engine trusts the frame's `From` must fail.
- `TimeoutNow` carries the sender's term. It is accepted only from the leader
  of the receiver's current term, and only when that term equals the
  receiver's own (R2-13). A mutation that drops the term check must fail.
- The cluster and group in the first frame are a guard against
  misconfiguration, not an authentication: the certificate already decided.
- An identity with nil `RootCAs`, or a listener without
  `RequireAndVerifyClientCert`, is refused at construction. The deployment
  trusts only the consensus intermediate, as both `RootCAs` and `ClientCAs`
  (D20, R7-25). TLS session tickets are disabled by the `tlsid` sibling, so a
  resumed session never skips the certificate check, and a mutation that
  re-enables them must fail (R7-26).

**Admission follows the membership, from raft-sql-poc, fed by the engine
(R1-16, R2-31).** The transport admits only the `NodeID`s and incarnations of
the admission set: the union of the latest appended membership and the latest
committed one, rebuilt when a truncation removes a membership entry. The
engine pushes each group's admitted members and their addresses through
`SetMembership(group, admitted)`, and the transport computes the union over
its groups.
The static map given at construction is only a seed for the first contact.
A `Remove` that commits cuts that node's connections and refuses its
handshakes from then on. Admission is keyed on the member's pinned SPKI as
well as its slot (R8-07). The POC learned that draining a machine is not
removing it: with a credential every machine shared, a removed machine could
come back (README bug 8). Its fix revokes the removed machine's key on every
peer (`shard.js:796-803`, `transport.js:110-120`). Here a `NodeID` is never
reused (D8), and a retired ID is refused even with a valid certificate.

**A stale voter admits the leader a member vouches for, for replication
only (amendment of 2026-10-23, #542, arbitrated by the owner on
2026-10-22).** Admission from the local view alone left a voter that was
down while a member was admitted, promoted and handed the leadership unable
to hear that leader, for as long as it led: the members it knew were
followers and sent it nothing. From protocol 2:
- A follower that follows a live leader and receives a PreVote from a
  member of its configuration whose last index is behind its commit answers
  with `MsgLeaderRecord`: the leader's member record (ID, slot, SPKI,
  address), read from its COMMITTED configuration. Only at cluster version 2.
- The voter that receives it, knowing no live leader, takes it only from a
  member of its own configuration, at a term at least its own, for a pinned
  key, and for an ID it neither lists nor retired. It adds the record to its
  admission set, so the transport admits that key at the handshake, which
  still verifies the chain to the cluster CA.
- From that leader it accepts appends, heartbeats and snapshot installs
  only: a vote request, a transfer, a read or a forward is dropped. It never
  sends that leader a vote request: elections use its own configuration.
- The provisional admission ends when one of its configurations lists the
  leader (it is then admitted as any member) or retires it, or after four
  maximum election timeouts.
- Threat model: an admitted member can vouch for another member's key to a
  peer that has no leader, for replication only. Raft is crash-fault
  tolerant here, a compromised member can already forge far more, and the
  key must still be one the cluster CA signed.

**Limits TLS does not set (R1-24, from raft-sql-poc).**
- A frame's announced length is checked against `MaxFrameBytes` before
  anything is allocated.
- A snapshot is inflated no further than `MaxSnapshotBytes` and the size its
  metadata announces, whichever is smaller (R1-15), before its hash is checked
  (`raft.js:1272-1289`).
- A connection that has not finished its handshake within `HandshakeTimeout`
  is closed. The default is 1 s, and a larger value is refused.
- **Admission before the handshake is a `net/server` sibling (R2-22,
  R7-26).** `net/server` gains a pre-handshake admission hook keyed on the
  remote address, run before any TLS byte is read. It caps unauthenticated
  connections per IPv4 address and per IPv6 /64, and reserves slots for
  members, keyed on the IP addresses the committed members' addresses resolve
  to. `SetMembership` refreshes the reserved slots. A member address that
  does not resolve is refused, never given an unkeyed slot. The hook is
  declared in ADR 0174 (D2).
- **Exposure is the consumer's (R2-37).** The SDK has no allow-list setting.
  The listener binds to the address it is given, and nothing else. The
  consumer guide says that a deployment binds it to a private interface
  only. The first consumer's binding is in D19 (R3-24).
All of these are `Config` fields with an owner, a default and a validation
(D14, R1-29).

**Three connections per peer and direction (R1-12, R2-06).** One is for control:
votes, PreVotes, heartbeats, their replies and `TimeoutNow`. One is for data:
entries and their acknowledgements. One is for snapshot streams. The first
draft's priority lane inside one connection was not enough: a frame already
written into a TCP send buffer is behind every byte queued before it, and no
lane in user space reorders the kernel's buffer. **From IWFS:** under CPU
starvation, IWFS's PreVote fan-out saturated the shared mesh client, and
elections never settled (`01-shared-log-single-writer.md` §7.1). IWFS
answered by backing elections off up to 30 s. This ADR removes the cause
instead. Data writes are bounded by a write deadline and a per-peer byte cap.
A write that misses its deadline closes the data connection and reports the
peer through `UnreachableReporter` (R2-06).
The event loop never waits on storage or on a socket (D5b). A test measures
heartbeat latency while the data pipe is saturated and the disk is slow
(D15).

**A pre-admission JOIN, the one frame a non-member may send (R7-42, R8-09).**
A node that holds a valid certificate for a slot but is not yet a member may
open ONE control connection to any member.
- **Frame kinds (R8-09).** `JOIN{ClusterID, NodeID, Incarnation, slot}` from
  the joiner. `JOIN_ACK{Position}` or `JOIN_REFUSED{code}` from the member.
  Nothing else is accepted from a non-member.
- **The slot comes from the certificate.** A `JOIN` whose slot differs from
  the certificate's is `JOIN_REFUSED`. The SPKI comes from the handshake.
  The transport hands the engine an `AuthenticatedPeer` with ID 0, the slot
  and the SPKI.
- **The product decides the fence.** The member calls
  `Config.JoinAuthorizer.Authorize` with the `JoinRequest`. Only the product
  knows whether the slot's previous member is fenced, from Nomad and its own
  state (D19). It returns the `Operator`, built ONLY from authenticated
  parts: `automation:<slot>/<SPKI prefix>/<new NodeID>`. With no authorizer,
  every `JOIN` is refused.
- The member then proposes the `AddLearner`, with the pinned SPKI, and
  answers `JOIN_ACK` once it is appended.
- **Checked against every member of the slot (R8-11).** A slot that still
  has a live member, voter or learner, is refused, and raises the
  `WipeAndRejoin` alarm.
- It is rate-limited to one per slot per election timeout, counted, and
  logged at `Warn`.
- It never promotes. `Promote` still needs the `PromoteLag` gate and the
  object coverage of D17.
- **Published surface (R8-09).** `Join(ctx, JoinConfig)` on the shipped
  transport. The `Joiner` sibling for a custom transport. The
  `JoinAuthorizer` port for the product. All of them are in
  `design/app/consensus.yaml`.
- **The answer is authenticated (review of [PR #301](https://github.com/kitsunium/sdk/pull/301), M4).** The answer
  installs the peers the joiner admits, so a certificate of the cluster is
  not enough to give it. `JoinConfig.Pins` carries the members' keys from
  the operator; an empty list refuses the JOIN with
  `TRANSPORT_MISCONFIGURED`. An answer counts only from a peer presenting
  one of them, and only when the membership it returns lists that peer
  under that key.

**Batching on the wire, from raft-sql-poc.** Every frame queued for a peer
on one connection during one turn of its sender leaves in one write
(`transport.js:1-8`). Each per-peer queue is bounded and drops when full,
because Raft retransmits what matters and an unbounded queue turns an outage
into memory exhaustion (`transport.js:294-296`, 64 MiB in the POC).

**Why TLS and not the POC's handshake.** The POC wrote a SIGMA handshake of
its own over Ed25519, X25519 and AES-256-GCM (`secure.js:1-60`). Its README
lists two cryptographic bugs it found in its own earlier versions: a
`SHA256(key ‖ msg)` MAC open to length extension, and a static key whose
nonce counter restarted at zero (bugs 5 and 6). TLS 1.3 gives the same
properties from the standard library: a signed transcript, ephemeral keys,
record sequence numbers. The SDK does not write a handshake. Frames are
`length | version | type | group | body`, hand-encoded; TLS gives the
integrity, so the wire carries no checksum of its own.

**Entries are bytes.** The log record, the frames and `Entry` are
hand-written fixed layouts, not `codec` formats: the codec is selected by a
`Format` string at run time, and two nodes configured with different strings
would write different bytes for the same entry, which is a fork. A caller
encodes its command however it likes — through `codec` if it wants — and hands
`[]byte` to `Propose`.

### D5 — Elections: PreVote and CheckQuorum are on and cannot be turned off

- **PreVote** (Ongaro, thesis §9.6). A node whose election timer fires first
  asks whether it COULD win, without incrementing its term. A node cut off by
  a partition therefore cannot return with an inflated term and depose a
  healthy leader. On a WAN, partial partitions are routine, not exotic.
- **CheckQuorum** (thesis §6.2). A leader that has not heard from a majority
  within an election timeout steps down. Without it, a leader isolated with
  its clients keeps accepting proposals that will never commit, and the lease
  read of D9 is unsound.
- **A follower that heard from a live leader within the minimum election
  timeout refuses votes** (thesis §4.2.3), in RequestVote as in PreVote,
  unless the request carries the leadership-transfer flag (D8). This is the
  vote-side half of CheckQuorum and what makes leases safe.

Neither is a configuration option. Both exist to prevent failures that only
appear under partition, which is where nobody tests a disabled flag.

**The leader refuses votes too (R7-19).** A leader that heard from a majority
within the minimum election timeout refuses a RequestVote and a PreVote of a
higher term, unless the request carries the transfer flag it issued itself.
Otherwise a node that missed heartbeats could depose a leader whose lease
still holds.

**The election restriction is stated (R7-16).** A node grants a vote, and a
PreVote, only to a candidate whose log is at least as up to date as its own: a
higher last term, or the same last term and a last index at least as high
(thesis §3.6.1, paper §5.4.1). The transfer flag lifts the live-leader refusal
ONLY. It never lifts this rule. Three mutations must fail: the check dropped
from RequestVote, dropped from PreVote, and skipped under the transfer flag.

**A node that starts refuses votes for one election timeout, from
raft-sql-poc.** It behaves as if it had just heard from a leader
(`raft.js:205-213`). Otherwise a node restarted inside a lease window votes
at once, and a second leader can be elected while the first still serves
lease reads. The POC checks for a live leader in PreVote only
(`raft.js:524-531` against `raft.js:588-620`). That holds until an election
skips PreVote, and a leadership transfer does (`raft.js:563`,
`raft.js:827`).

**A refusal never moves a term, and a stuck node is answered (R1-18).**
- A RequestVote refused under the live-leader rule does not raise the
  refuser's term. Raising it would depose the live leader the refusal exists
  to protect.
- A node stuck at a higher term — partitioned, it campaigned and was refused
  PreVotes, or it raised its term before PreVote existed in its version —
  receives AppendEntries of a lower term. It answers them with its own term so
  that the leader learns of it, instead of ignoring them in silence. The leader
  then steps down, and a PreVote round decides. This is etcd's rule
  (`raft.go:1133-1154`).
- The scenario "PreVote granted, RequestVote refused, partition heals" is a
  named simulator case (D15).

**Timings, defaults for this RTT matrix:** heartbeat every 100 ms; election
timeout randomized in [1 s, 2 s). A zero is clamped to the default (ADR 0031,
clamp half: there is one sensible order of magnitude); a heartbeat not below
a fifth of the minimum election timeout is refused (`CONSENSUS_MISCONFIGURED`),
because that is the configuration in which a single delayed heartbeat starts
an election. These timings are the initial proposal of the timing contract;
once committed, the contract entry of D9 rules on every member (R2-14).

### D5b — The event loop, the storage actor, and what waits for what (R1-07)

The engine is one event loop that owns the step function. Nothing else
touches the step function's state. Storage is not a call the step function
makes. It is a message to a **local storage actor**, modelled inside the step
function the way etcd models `MsgStorageAppend` and `MsgStorageApply`. The
actor's completions come back as input events: "appended through index i at
term t", "synced through index i", "hard state durable". The step function
acts on durability only when such an event arrives.

| Effect | Waits for |
|---|---|
| reply to RequestVote (granted) | the term and vote durable |
| reply to PreVote | nothing: a PreVote's prospective term and vote are never persisted (R2-03) |
| counting the leader's own vote for itself | its term and self-vote durable |
| reply to AppendEntries (success) | the entries and any term change durable |
| the leader counting its own match index | its own sync-done event for that index |
| reply to InstallSnapshot (done) | the snapshot and its stamp durable |
| sending a request at a new term | the term durable |
| `Apply` of an entry | its commit known; its own durability on this node |
| reporting `Propose` success | the entry applied locally |

The leader's write and the sends still run in parallel (thesis §10.2.1). The
table says what each side may count, not what it must wait for before
sending.

**A completion names the write it completes (R2-02).** Every storage request
carries an ordered write-sequence id, plus the term and index of its last
entry. A completion carries the same. A completion for a write that a later
truncation or a new term has superseded is stale and ignored, as etcd does
(`log_unstable.go:138-158`, `rawnode.go:266-300`). Without that, a sync-done
for entries a new leader has since replaced would count them as durable.
When the storage actor's queue is full, the event loop coalesces its pending
writes into the next request. It never blocks on the queue.

Goroutines, each with a bounded queue:
- the event loop;
- the storage actor;
- the apply goroutine;
- one sender per peer and per connection (D4);
- the snapshot goroutine.

`Status` is published through an `atomic.Pointer` after each turn, so reading
it never takes a lock the loop holds.

**Shutdown (R2-38, R8-37).** `Close` and `HardClose` are idempotent. `Close`
returns only after every goroutine above has exited. The storage actor owns
the final `fdatasync`: on `Close` it syncs what it holds before it exits.
`HardClose` stops without syncing, which is what a crash test needs, and
returns within `HardCloseTimeout` even when a goroutine is stuck in
`fdatasync`. That goroutine is abandoned, counted, and the directory lock
stays held until the process exits (D14).

### D6 — Replication: pipelined, batched with no linger timer, leader writes in parallel

- **Pipeline.** Per follower, the leader tracks a `Progress` (match, next,
  state `probe` / `replicate` / `snapshot`). In `replicate` it sends the next
  `AppendEntries` without waiting for the previous answer, up to
  `MaxInflight` messages (default 64) or `MaxInflightBytes`; a rejection drops
  the follower back to `probe`.
- **The window always recovers (R1-11).**
  - `match` only grows.
  - **Rejections are handled per state (R2-07).** In `probe`, a rejection
    counts only when it answers the probe outstanding at that moment, by its
    index. In `replicate`, a rejection drops the follower to `probe` only when
    it answers an append still in the window. Anything else is stale and
    ignored.
  - When the window is full, the heartbeat carries an empty AppendEntries at
    `next`, with `prevLogIndex = next − 1` and its term (R2-07), so a lost
    acknowledgement cannot stall the follower forever.
  - **A heartbeat never commits past what the follower holds (R2-06).** The
    commit index a heartbeat carries is `min(follower match, leader commit)`,
    as etcd does (`raft.go:696-709`). A heartbeat on the control connection can
    overtake an append on the data connection, and a raw leader commit would
    then point past the follower's log.
  - The transport reports an unreachable peer through `UnreachableReporter`,
    and the leader moves that progress to `probe`.
  - `MaxInflightBytes` below `MaxBatchBytes` is refused at construction,
    because one batch could then never be sent.
- **Batching without a timer.** The leader loop takes EVERY proposal waiting
  when it wakes, appends them as one batch, issues one `Sync`, and sends one
  `AppendEntries` per follower, bounded by `MaxBatchBytes` (default 1 MiB).
  Proposals arriving during that `Sync` form the next batch. The batch grows
  with load and shrinks to one entry at rest, so latency at low load is not
  inflated by a linger delay — the reason `internal/kernel/concur/batcher`'s
  `FlushEvery` shape is not used. **Confirmed by raft-sql-poc:**
  batching per turn of the event loop with no timer (`raft.js:870-874`,
  `storage.js:447-454`) gave 101 to 143 writes per `fsync` under load, and
  7 000 to 9 400 durable writes/s against 184 to 216/s with one `fsync` per
  write (macOS, `F_FULLFSYNC` at 4.6 to 5.4 ms).
- **An empty `AppendEntries` is sent only as a heartbeat or when it carries
  a new commit index** (`raft.js:918-924`). With proposal coalescing, this
  took the POC from 198 to 8.1 RPCs per log entry.
- **The leader's own write runs in parallel with the sends** (thesis §10.2.1):
  an entry is committed when a majority has it durably, and the leader is
  counted only once its own sync-done event arrives (D5b).
- **Followers acknowledge after their sync**, never before (D5b).
- **A new leader commits a no-op entry of its term** before it serves a read
  or accepts a membership change (thesis §6.4, §4.1).
- **Three rules the POC learned as safety bugs** are stated here rather than
  left to the implementation. All three are silent on the happy path and end
  in divergence (README bugs 1 to 3).
  - A follower advances its commit index to at most
    `min(leaderCommit, prevLogIndex + len(entries))`, the prefix this message
    verified, never the end of its log.
  - Its acknowledgement reports that verified prefix, never `lastIndex`
    (`raft.js:1020-1032`).
  - A node that steps down within the same term keeps its vote
    (`raft.js:711-718`).
- **The pipeline stays bounded.** The POC pipelines optimistically with no
  bound on what is in flight (`raft.js:937-939`), relying on the transport
  queue's cap. This ADR keeps `MaxInflight`: on a 27 ms link an unbounded
  window turns one slow follower into megabytes of retransmission.

**Forwarded proposals have a submission identity (R1-13).** A follower
forwards a proposal to the leader over the data connection, with a
submission id made of the follower's `NodeID`, its incarnation and a
sequence number. **The sequence never repeats across restarts (R7-23,
R8-37):** its high 32 bits are a random epoch drawn at each start of the
node, and its low 32 bits count within that epoch. Two starts of one node
draw the same epoch with probability 2^-32 per pair, and the incarnation in
the id already separates two lives of one directory. A product that cannot
accept that probability persists a counter range instead. **Before the low
32 bits run out, the node reserves a fresh persisted range or draws a new
epoch (R9-17).** A test exhausts the counter while the leader still retains
an old answer, and the next submission must not match it. A leader that still holds an answer for an
id from before the restart can never match it to a new submission. A read's
context is drawn from the same ids: a counter restarting at 1 matched a
new incarnation's `Barrier` to the answer a leader still held for the
previous one, at a commit index below a write acknowledged since ([issue
#510](https://github.com/kitsunium/sdk/issues/510)). The follower tracks each submission through four states:

1. **unsent** — not written to the connection;
2. **possibly accepted** — written, no answer yet;
3. **appended** — the leader answered with the `Position` it appended at;
4. **committed** — that position committed with that term.

A submission that fails while unsent is `NOT_LEADER` and is safe to retry.
Once it is possibly accepted, any failure is `OUTCOME_UNKNOWN`. The only
exception is a rejection from the leader that correlates with the submission
id and says nothing was appended. The forward acknowledgement carries the
`Position`. **`ENTRY_SUPERSEDED` is answered only when a COMMITTED entry at
that position has another term (R7-15)**, that is an entry at an index at or
below the local commit index. A RETAINED but uncommitted entry with another
term is `OUTCOME_UNKNOWN`: it could itself be truncated, and the original
could still win. When the position is compacted or unknown, the answer is
`OUTCOME_UNKNOWN`. A mutation that answers `ENTRY_SUPERSEDED` on a retained
uncommitted conflict must fail. **From IWFS:** a follower dials the leader the moment it
learns of it and opens the forwarding connection before the first proposal
(`src/internal/cluster/election.go:805-807`). Without that, IWFS's first
forwarded write after an election exceeded its wait
(`docs/src/design/2026-09-10-point-acceptation-commit.md` §3).

**Outcomes.** `Propose` answers in one of these ways:
- success: a lineage-scoped `Token` (its `Position` and `LogIdentity`, R4-27) and the `Apply` result, once applied locally;
- `NOT_LEADER`: certainly not appended, with the known leader readable through
  `consensus.LeaderHint`;
- **Every outcome is an ORIGIN (R7-10).** Each one is a fresh error built on
  its own sentinel, its cause kept as a field or through `errors.Join`, never
  a wrap of the cause. So `errs.CodeOf` yields `OUTCOME_UNKNOWN` or
  `NOT_LEADER`, not the code of a broken connection underneath. The
  `uint64` values an error carries, the leader's `NodeID` and a `Token`,
  travel in its fields as strings (decimal, and the token's hex encoding),
  read back with `LeaderHint` and `TokenOf`. A test calls `CodeOf` on every
  exit path of `Propose`.
- `OVERLOADED`: refused before append;
- `ENTRY_SUPERSEDED`: certainly not committed, because another entry is committed at its position (R7-15).
- `OUTCOME_UNKNOWN`: appended, or possibly delivered, and the answer was lost
  to a leadership change, a context's end or a broken connection. The entry
  may still commit.

**Never retry blindly after `OUTCOME_UNKNOWN` (R1-04).** The first draft said a
compare-and-swap makes a duplicate harmless. That is false under ABA: a ref
moved A→B, then back B→A by another writer, accepts a replayed A→B a second
time. The `pkg/v1` doc comment says: after `OUTCOME_UNKNOWN`, read the state
(through `Barrier`) and decide, or use client sessions (Deferred). The first
consumer's ref table carries a per-ref revision that defeats ABA (D19).

**A context that ends after the append is `OUTCOME_UNKNOWN` too, from
raft-sql-poc.** Its cause is `ctx.Err()`, but the code is not
`ctx.Err()` alone. The POC's `COMMIT_TIMEOUT` (`raft.js:850-858`) is this
case under another name. A context that ends before the append returns
`ctx.Err()`, as everywhere in the SDK. "Before the append" is decided by a
compare-and-swap on the proposal's stage: the caller abandons an unsent
proposal or the event loop claims it, never both, and an abandoned proposal
is dropped unappended ([issue #519](https://github.com/kitsunium/sdk/issues/519)).

**Refused before anything is appended, from IWFS.** When the leader's pending
proposals exceed `MaxPendingBytes`, `Propose` answers `OVERLOADED` and
appends nothing, so a retry is safe. IWFS refuses a full queue before
acceptance (`0x0032`), never after. The SDK does not take IWFS's admission on
host CPU and memory thresholds: the leader's own pending bytes are the only
signal.

**Apply backpressure (R1-14).** The gap between commit and applied is bounded
by `MaxApplyLagBytes`. When it is reached, the leader stops admitting
proposals with `OVERLOADED` until apply catches up, and a follower stops
reading new entries from its log into memory. The engine keeps a window of
recent entries in memory, bounded by `RecentEntriesBytes` (D14, R2-40), so
the apply goroutine and slow followers do not read the disk for what was just
written.

**Forwarded commands meet the same bounds (review of [PR #301](https://github.com/kitsunium/sdk/pull/301), M2).** A
command a follower forwards is checked by the leader before it is appended,
as a local one is: above the leader's `MaxEntryBytes` it is refused with
`ENTRY_TOO_LARGE`; while the forwarded commands appended in this term and
not committed reach `MaxPendingBytes`, or while apply backpressure holds, it
is refused with `OVERLOADED`. Nothing is appended in either case.

### D7 — Snapshots and compaction ship in v1

- **Trigger:** after `SnapshotEvery` applied entries (default 16 384) or
  `SnapshotEveryBytes` of log (default 64 MiB), whichever comes first. **Snapshots follow the core model (R5-01):**
  - every cut is checksummed: a SHA-256 per chunk of the file, and one over
    the decoded stream;
  - a node STARTS from its latest snapshot whose checksums pass, plus the log
    after it, as etcd and hashicorp/raft do;
  - **the latest TWO complete snapshots are retained (R6-02).** A snapshot
    whose checksum fails is not used, and the node falls back to the older
    one, which is now always retained (R6-13). The log is kept back to the
    older snapshot;
  - **a node whose two snapshots both fail** starts with no table. It keeps
    its term, vote and commit index, votes and stores the log (R6-13), and
    applies nothing until an install. **It asks for one (R7-18):** it answers
    the leader's next append, and every heartbeat, with an explicit "need
    snapshot" rejection, so the leader moves it to the snapshot path at
    once: a leader with nothing to append to it sends only heartbeats, and
    the node held no state for as long as the cluster stayed quiet
    ([#549](https://github.com/kitsunium/sdk/issues/549)). It is exempt from the
    install's freshness rule up to the leader's snapshot index, so an install
    at or below its own commit index is accepted. Its may-campaign gate is
    closed while it holds no table, so it can vote but never lead. A test
    corrupts every snapshot and cold-restarts (R6-02).
    **Its base term is never guessed ([#415](https://github.com/kitsunium/sdk/issues/415)).**
    It votes on the term of its log's compaction point, so that term must be
    the snapshot's. An install past the end of the log compacts the store
    to an index it holds no entry for: the engine passes the term it holds
    there, and the store refuses a compaction past its end without one (it
    used to record 0, and the node granted its vote to any candidate whose
    last term was 1 or more). At a start with no table the base takes the
    term of the snapshot metadata at that index when the log answers 0, and
    the node refuses to start when neither knows it.
  - **integrity checks beyond the checksum never gate start-up.** ADR 0173
    may add anchors on top of these snapshots without changing that rule.
- **Cut:** `StateMachine.Snapshot` on the apply goroutine; `WriteTo` streams
  into `SnapshotStore.Create` off it, with a SHA-256 of the stream recorded in
  `SnapshotMeta` beside `Position`, `Membership`, the snapshot format version
  and the compression id (R1-22). Only one snapshot is in flight; a trigger
  during one is coalesced.
- **A snapshot's failed write or sync stops the node, as the log's does
  (D4, [#404](https://github.com/kitsunium/sdk/issues/404)).** The snapshot
  directory sits on the node's one disk, whose page cache may now lie about
  the log as well. Every path that publishes a snapshot obeys it: a cut on
  its trigger, a coalesced or requested one (a peer that needs a snapshot the
  leader has not cut), written aside or on the apply goroutine
  (`SnapshotPausesApply`), an operator's `SaveSnapshot`, and an install,
  whose spool's `fsync`, rename or `.meta` publication fails. The node stops
  with `STORAGE_FAILED` and never retries; an install still answers the
  leader with that error. Any other failure of a cut (the machine's
  `Snapshot`, its encoding) is logged and the next cut tries again, and any
  other failure of an install leaves the leader to offer again.
- **The engine's own replicated state rides in the snapshot (R2-12).**
  `SnapshotMeta` is versioned and carries the committed cluster version, the
  committed timing contract, the no-space alarm, a reserved alarm field
  carried uninterpreted for ADR 0173 (R6-04), and the set of retired IDs. A node that installs a snapshot restores them before it
  admits any peer, so a node rebuilt from a snapshot cannot forget a retired
  ID or an alarm.
- **Compaction bound (R1-14, R6-02):** after the snapshot is committed to
  disk, `CompactPrefix` drops the log through
  `min(older retained snapshot.Index, applied) − SnapshotTrailing` (default
  4 096), computed
  with saturating arithmetic so a small log never underflows. The term of the
  last compacted entry is kept, so the consistency check on the first
  retained entry still has a `prevLogTerm`. A follower whose `next` falls
  below the first retained index gets `COMPACTED` internally, and the leader
  switches it to the snapshot path.
- **A transfer in flight defers, and does not get cancelled (R6-01).**
  - While a snapshot is being sent to a peer, the next cut and the
    replacement of the snapshot it reads are DEFERRED, and compaction is
    pinned, as etcd skips compaction while a snapshot is in flight
    (`server.go:2140-2141`).
  - The transfer is cancelled only when the log reaches `LogReserveBytes`.
  - On the receiving side, an install from a higher term, or of a newer
    snapshot id, replaces a stale receive spool.
  - A simulator case makes a transfer last longer than the cut interval,
    under write load, and the follower must still catch up.
- **Install (R1-15, R2-10):**
  - only from the leader of the receiver's current term;
  - the `InstallRequest` carries the sender's authenticated identity and
    incarnation (from TLS, not from the frame), the RPC's term, a snapshot id
    and a context that ends with the connection;
  - the receiver bounds the announced size by its own `MaxSnapshotBytes`
    before it spools a byte, and spools to a temporary in the snapshot
    directory;
  - the RECEIVER chooses the resume offset, from what it has durably spooled.
    `Transport.Stream` takes a function that opens the snapshot at an offset,
    so the sender can resume where the receiver says;
  - **freshness is checked again on the event loop, right before the install
    is accepted.** A snapshot whose index is not beyond the receiver's commit
    index is refused, even from the same leader in the same term. Then the
    sender's term, the SHA-256 and the size are checked. The check runs at
    the offer AND again once the body has arrived, before the snapshot is
    published: a new leader may have moved the receiver past it meanwhile.
    The apply goroutine never restores a snapshot the machine has already
    applied past (review of [PR #301](https://github.com/kitsunium/sdk/pull/301), H3);
  - **the step function moves onto an install only while it is still
    ahead of it ([#416](https://github.com/kitsunium/sdk/issues/416)).** The
    restore can take seconds, and a new leader may make the receiver commit
    past the snapshot meanwhile, or elect it. Its log and its engine state
    then already hold everything the snapshot holds, and the effects of the
    engine entries committed past it: a leader, or a node with a table whose
    commit reached the snapshot's index, ignores the install, which would
    roll the cluster version, the timing contract and the no-space alarm
    back and let its next cut carry the regression. A node with no table
    still installs, and the effects of the entries it holds committed past
    the snapshot are applied again over the snapshot's state;
  - `Restore` is serialised with `Apply`;
  - **the install is recorded durably, step by step.** An installation record
    names the snapshot and the step reached: spooled and synced, published,
    stamp and hard state written, restored. Recovery reads it and finishes or
    abandons the install. A crash at any step leaves either the old base or
    the new one, never a mix;
  - **a node with no table is exempt from the freshness rule, never from
    its own log (found by the simulator's snapshot schedule while fixing the
    review of [PR #301](https://github.com/kitsunium/sdk/pull/301)).** A snapshot below the base its log continues is
    refused with `COMPACTED`, even with no table: installing it would drop
    durable entries the node acknowledged, committed ones included, and
    leave its commit above its log. The leader then cuts a newer snapshot;
  - the log suffix after the snapshot's index is kept only if the entry at
    the boundary has the snapshot's term and index. Otherwise the whole log is
    replaced. The install acknowledges the snapshot's index, never the end of
    a kept suffix: nothing has verified that suffix against the leader's log,
    and the next append does (review of [PR #301](https://github.com/kitsunium/sdk/pull/301), H1).
  - **Resumption, exactly (R3-16).**
    - The leader streams from a `SnapshotHandle` that `Open` pins, with
      random access, until the transfer completes, so compaction cannot delete
      the snapshot mid-transfer.
    - The offset is a count of ENCODED bytes and must fall on a chunk
      boundary. Any other offset is refused.
    - On resume, the receiver rebuilds its running SHA-256: from a saved hash
      state (`encoding.BinaryMarshaler`), or by re-hashing the decoded prefix
      it has spooled.
    - A spool is bound to its snapshot id, so a resume never appends to
      another snapshot's spool.
    - An install of a snapshot the receiver already holds gets an idempotent
      success, and the leader moves that peer back to `probe`.
    - Decompression is bounded per chunk BEFORE the output is allocated. A
      codec that cannot bound its output before decoding is refused as a
      snapshot codec.
  SHA-256 here is integrity, not authentication: the authentication is the
  TLS identity of the sender.
- **The leader knows each peer's limit (R2-30).** Each peer announces its
  `MaxSnapshotBytes` in the handshake. When the state approaches the smallest
  limit among the members, the leader reports it in `Health` and raises an
  alarm, before a snapshot exists that some peer could never install.
- **Retention:** the previous snapshot is kept until the new one is durable,
  so a crash during publication leaves one valid snapshot. A snapshot that
  ADR 0173 has pinned is never evicted (D3, R7-38).
- **Rotation with two pinned anchors (R8-22).** When both retained
  snapshots are pinned, a candidate is still written, into the slot
  reserved for a cut. Once ADR 0173 validates it, the candidate atomically
  takes over the OLDEST anchor's pin, and only then is that old anchor
  removed. The four-slot bound of D14 holds throughout.
- **Compression, from raft-sql-poc, bounded (R7-06, R7-00b).** The snapshot
  stream passes through `Config.SnapshotCodec`, typed
  `transform.BoundedDecompressor`, the core port whose `DecompressBounded`
  bounds the output before it allocates it. flate from
  `internal/service/data/transform` is the default, and gzip is the other
  stdlib choice. zstd is NOT a v1 codec: `third-party/transform`'s zstd has no
  `DecompressBounded`, and no SDK package may import a vendor module. It
  becomes possible only when the vendor module adds `DecompressBounded` in its
  own change, and a product passes it through its own adapter. The
  compression id is recorded in `SnapshotMeta`. **The compressed format is chunked (R2-11).** The stream is a
  sequence of chunks, each compressed on its own and preceded by its encoded
  and decoded lengths. The SHA-256 covers the decoded stream. The decoded
  length is bounded per chunk while decoding, and the running total by
  `MaxSnapshotBytes`, so a hostile or corrupt chunk fails at its own size, not
  after it has filled memory. A resumed transfer restarts at a chunk
  boundary. The POC measured ÷10.8 with zstd level 3 on a realistic
  state. Adding a machine cost 41 to 43 KB of snapshot instead of 204 to 205 KB
  of replayed log. The POC also compresses once per snapshot and sends every
  lagging follower the same bytes (`raft.js:1176-1194`). Messages are not
  compressed: under encryption, compressing attacker-influenced data with
  other data leaks through the ciphertext's length (CRIME/BREACH).

The POC cuts a snapshot synchronously on the apply path and holds it whole in
memory (`raft.js:1151-1159`, `storage.js:397-431`). This ADR keeps the
point-in-time cut and the streamed write (D3): the first consumer's state
grows with `refs/pull/*`, and a pause in apply is a pause in every write.

For the first consumer the snapshot is the ref table, with its revisions and
the per-repository checksums, not Git objects. A node restoring it does not
serve reads until the objects its refs name are present and verified (D19).

### D8 — Membership: one server at a time, through learners

Single-server changes (thesis §4.1). Joint consensus (§4.3) is not
implemented in v1:

- With three voters, every change a deployment needs — add, remove, replace —
  decomposes into one-at-a-time steps. Joint consensus buys arbitrary changes
  in one step; nothing here asks for that, and it is the larger and less
  tested path in every implementation that has both.
- The known defect of single-server changes (Ongaro, raft-dev, 2015: a
  change proposed by a leader that has not yet committed an entry of its own
  term can produce two majorities) is closed by the rule of D6: no membership
  entry before the no-op of the term is committed.
- One change at a time: a second proposal while one is uncommitted is
  `MEMBERSHIP_CHANGE_PENDING`. Removing the last voter, or a node not in the
  membership, is `MEMBERSHIP_INVALID`.
- **A floor on voters (R7-27).** `Config.MinVoters` (default 3, the first
  consumer's value) is replicated with the timing contract. A `Remove` that
  would leave fewer voters is `MEMBERSHIP_INVALID`. Only the offline, forced
  path of `Recover` (D17) goes below it. A replacement therefore adds before
  it removes (D17, D19).
- **Strict reconfiguration (R2-27).** `Remove`, `AddLearner` and
  `UpdateAddress` are refused with `MEMBERSHIP_INVALID` when the voter set
  that results has no majority acknowledging the leader NOW. This is etcd's
  strict reconfiguration check: a change that would leave the cluster unable
  to commit its own reversal is never proposed.
- **An administrative operation can be submitted on ANY node (Q8, R3-03).**
  A leader is an engine detail, not the cluster's administrator. A membership
  change, a transfer, a contract change or any other operation of D17 is a
  proposal like any other.
  - Submitted on a follower, the engine forwards it to the leader over the
    authenticated CONTROL connection, with its `Operator`.
  - The leader re-validates it against its own state: strict reconfiguration,
    one change at a time, the term's no-op.
  - Its outcome follows D6: a submission id, and `OUTCOME_UNKNOWN` if the
    reply is lost.
  - The ORIGIN recorded in the entry is the TLS identity of the forwarding
    connection, never a field the forwarder wrote.
  - `TransferLeadership` is forwarded the same way.
  The round-2 rule "never forwarded, `NOT_LEADER`" is withdrawn. Each entry
  carries the operator identity the caller provides, for the product's audit
  (D17).

**The configuration is a function of the log (R1-16).**
- Every membership entry records its index. A configuration takes effect on
  a node when it is APPENDED, not when it commits.
- A truncation that removes a membership entry rolls the active
  configuration back to the latest one still in the log.
- On restart, the configuration is rebuilt from the snapshot's membership
  and the membership entries after it.
- A snapshot always records the configuration valid at its boundary index,
  never a later appended one.
- The transport's admission set is the union of the latest appended
  configuration and the latest committed one, pushed by the engine (D4).
- An **address change** is its own operation, `UpdateAddress`, a membership
  entry that changes no vote. Addresses come from the committed membership.
  The static address map of the configuration is a seed only.

**Learners (R1-16).**
- `AddLearner` adds a non-voting member that receives the log and snapshots
  but counts in no majority.
- A learner never campaigns. It DOES answer votes as a voter would, because
  the moment it is promoted, through an entry it may already have appended,
  it is one (R2-17, etcd `raft.go:1222-1239`, `TestLearnerCanVote`). Every
  no-vote rule applies to it as to a voter, and the rules compose by refusal:
  an unstamped directory, a joining state, the restart window and a live
  leader each refuse on their own, and any one refusal wins.
- An empty learner — a new machine with no log — is admitted in a
  **joining** state. It is authenticated by its certificate and its
  incarnation, but it is not yet counted anywhere. **It is admitted only once
  an `AddLearner` naming its `(NodeID, Incarnation, slot)` is appended (R2-23,
  R7-41).** Two paths produce that entry. An operator reads the incarnation
  on the new machine with the product's offline `Inspect` (for Forgejo,
  `forgejo cluster inspect`) and gives it to `AddLearner`. Or the new node
  sends the pre-admission `JOIN` of D4, which a member turns into the same
  entry under the automation rules of D19 (R7-42). The incarnation is an
  identifier, not a secret (R2-36).
- `Promote` makes a learner a voter. It is refused until two things hold:
  the learner has shown recent DURABLE progress, acknowledged entries within
  `PromoteLag` (default 1 024) of the commit index within the last election
  timeout; and the prospective configuration still has a reachable majority
  among the nodes that are acknowledging now. A voter added while it is
  behind, or into a cluster that could not then form a majority, makes the
  cluster unavailable.

Replacing a node is therefore: learner, catch up, promote (four voters,
majority three), remove the old node. The POC checks catch-up in its
orchestrator, outside the engine (`shard.js:662-676`). Here `Promote` refuses
a lagging learner itself, so no caller can forget. As in the POC
(`shard.js:707-716`), removing the current leader first transfers leadership.
During that window a majority of four spans at least two sites; the operator
guide says so. The two runbooks — a DEAD node and a LIVE node to replace —
are part of D17.

**A node ID is never reused, and the engine enforces it (R1-17).** A node
whose disk was lost and that rejoined under its old ID could vote twice in
one term — once before the loss, once after — and elect two leaders.
- The engine writes the stamp `(ClusterID, NodeID, Incarnation)` through
  `LogStore.WriteStamp` and verifies it in `NewRaft`. A mismatch is
  `DATA_DIR_MISMATCH`.
- The incarnation is a random 128-bit value drawn when the stamp is first
  written. It travels in `Member` and in the transport handshake, so a
  wiped machine that kept its certificate and its `NodeID` is still a
  different incarnation, and it is refused.
- A data directory with no stamp does not vote and does not acknowledge. It
  can only join as an empty learner.
- The set of removed IDs is replicated, part of the membership. `AddLearner`
  and `Promote` refuse a retired ID with `NODE_ID_RETIRED`, and so does the
  transport.
- **Bootstrap (R2-24, R7-36, R7-43).** It is an explicit call in two phases.
  First `Init` runs on every founding node and prints its
  `NodeID:Incarnation:slot:SPKI:address` (R9-04). Then `Bootstrap` runs on every founding node
  with the IDENTICAL list `--members id:inc:slot:spki:addr,...`. The list
  is compared and written in NodeID order, so its order on the command line
  does not matter: entry 1 is byte-identical on every founder, as the chain
  requires (review of [PR #301](https://github.com/kitsunium/sdk/pull/301), M1). The founders also pass the same initial
  timing contract, which entry 1 carries.
  **`Bootstrap`, `Recover` and `RestoreSnapshot` require a non-zero SPKI for
  every member (R9-04).** An empty pin is refused, so no member is ever
  admitted on its slot alone. A RUNNING server
  answers bootstrap probes, so a founder can tell "already founded with this
  list" from "unreachable". Automation never bootstraps: the join path only
  joins (D19). Bootstrap runs as a one-shot batch job, by an operator.
  - It first writes a **bootstrap-pending** stamp with a founding id, derived
    from the member list, so that a crash or a retry finds where it stopped.
  - Each phase — stamp, peer contact, founding configuration — is
    idempotent, and a rerun with the same list resumes.
  - A peer that does not answer is retryable, not a refusal.
  - It refuses with `BOOTSTRAP_REFUSED` only a peer that already holds log
    entries, or that is bootstrap-pending with a different member list.
  - During bootstrap, the transport admits exactly the bootstrap member list
    and nothing else (R2-23).
  A node opening an empty directory does NOT form a cluster on its own: "a
  wiped node bootstraps itself" is how a second cluster with the same name is
  born.
- **Bootstrap is resumable and safe to run in parallel (R3-02).** An identical,
  already completed founding operation is accepted as success. A conflicting
  history is refused. The simulator crashes each founder after each phase.
  `NewRaft` never bootstraps: a running server only opens what an offline tool
  has founded or initialised.
- **`Init` prepares a joining node, purely offline (R3-02, R7-41, R8-10).**
  `Init(dataDir, cluster, slot)` draws a random `NodeID`, writes a joining
  stamp and returns the `NodeID` and the incarnation, which the operator, or
  the `JOIN` of D4, gives to `AddLearner` (D17). It contacts no peer and
  takes no retired list. The cluster refuses a reused or retired id at
  `JOIN` or `AddLearner`, with `JOIN_REFUSED` or `NODE_ID_RETIRED`. Only
  then may `Redraw` draw a new id, and only while the stamp is still joining
  and the log is empty.

**Leadership transfer.** `TransferLeadership(ctx, to)` stops accepting
proposals, brings `to` up to date, and sends it `TimeoutNow`. `to` campaigns
at once, its vote requests flagged so D5's live-leader refusal does not
apply. **The flag is valid for one campaign only (R7-17, R8-37):** it
carries the issuer and the issuer's term. A voter honours it only when the
issuer is the leader it knows for that term, and the request's term equals
that term plus one. A later
campaign of the same node, at any other term, carries no flag. A mutation
that honours the flag at another term must fail. It
fails with `TRANSFER_FAILED` after one election timeout and the old leader
resumes — **without its lease for the rest of the term** (R1-19, D9).

**A transfer loses nothing in flight, from raft-sql-poc.** From the moment it
starts, `TransferLeadership` refuses new proposals with `NOT_LEADER`, which is
retryable since nothing was appended. It sends `TimeoutNow` only once every
entry already proposed is COMMITTED (`raft.js:789-809`, `raft.js:841-846`).
The POC's first version stepped down with entries in flight and answered them
"outcome unknown", and a retrying client applied them twice (README bug 7).
The POC measured 10 ms for a transfer, against at least one election timeout
for an election. `PreferredLeaders` in the configuration names the nodes a
leader transfers to after an election when it is not one of them — here FRA
and RBX, 9.6 ms apart, so a commit costs ~10 ms of network instead of ~23.
The leader judges them at every tick, not only when it is elected: at that
moment no peer has acknowledged anything yet, so a check made only then never
transferred (#559). It hands over to the first preferred voter that holds the
commit, and does not try again before `ElectionTimeoutMax` after a transfer it
started.

### D9 — Reads: ReadIndex by default, a lease kept with every correction (Q2, R1-19)

`Barrier(ctx)` returns once the local state machine reflects every write
committed before the call, and returns the applied index:

- **ReadIndex** (default, and it stays the default (R3-24); thesis §6.4).
  - The leader records its commit index and confirms that it is still leader
    with a heartbeat round acknowledged by a majority.
  - **Each read is confirmed by a round started after the read arrived.**
    Concurrent reads share such a round. A read never rides a round that was
    already in flight when it arrived: acknowledgements to heartbeats sent
    before the read prove nothing about leadership at the read's start.
  - **The round needs a majority of the configuration that was current when
    it started (R2-15).** A configuration change during the round restarts
    it. This is etcd's read-only queue (`read_only.go:56-101`).
  - The leader then waits until applied reaches the recorded index and
    answers. A follower asks the leader for the index and waits locally.
  - raft-sql-poc measured 3 quorum rounds for 2 000 concurrent linearizable
    reads, at the price of −13 % for strictly sequential reads, each of which
    then pays its own round.
  - Cost: one round trip to the nearest peer, from the leader.
- **Lease** (`Reads: ReadLease`, opt-in; kept in v1 by the owner's decision,
  Q2). The leader serves without the round while its lease holds. The lease
  starts at the SEND instant of a heartbeat round a majority acknowledged, and
  it lasts `ElectionTimeoutMin × (1 − MaxClockDrift)`, measured on the injected
  `clock.Clock`'s monotonic `Since`. It is sound only under four conditions:
  - (a) no node's clock RATE differs from another's by more than
    `MaxClockDrift`. Absolute agreement is not needed.
  - (b) the vote refusal of D5 holds on every member, in RequestVote as in
    PreVote, and a restarted node keeps its refusal window.
  - (c) **after any transfer ATTEMPT, the lease is dropped for the rest of the
    term**, whether or not the transfer succeeds. A transfer elects without
    waiting for the timeout the lease relies on, and a `TimeoutNow` already in
    flight cannot be recalled. The first draft dropped the lease only while
    the transfer ran.
  - (d) **the timing contract is committed, and only the committed one counts
    (R2-14).**
    - `HeartbeatInterval`, `ElectionTimeoutMin`, `ElectionTimeoutMax` and
      `MaxClockDrift` form the timing contract. The committed contract entry
      is its ONLY authority. A node's own configuration is merely the
      initial proposal, used at `Bootstrap`.
    - **The founding entry is generation 1's contract entry
      ([#421](https://github.com/kitsunium/sdk/issues/421)).** Entry 1 carries
      the contract `Bootstrap` proposed. Appended, it widens the vote window
      and names generation 1's contract for the lease; committed, it is the
      contract in force unless a later generation already is. A snapshot's
      contract is adopted at an equal generation too: a node's own
      configuration used to rule generation 1 on every node, and two nodes
      started with different values timed one generation differently.
    - **A different applied contract version is never grounds for refusal
      (R3-06).** It is normal during a contract change, and it feeds the
      lease minimum below. The handshake refuses only a contract it cannot
      parse, with `VERSION_UNSUPPORTED`. A simulator case partitions a
      follower during a contract change.
    - Changing the contract is an explicit operation, `ProposeTimingContract`,
      that proposes a new contract entry (D17).
    - From the moment a new contract is proposed, the lease is dropped until
      that entry is committed and one maximum election timeout of the new and
      old contracts has passed.
    - Heartbeat acknowledgements carry the contract version the follower has
      applied. The lease is computed from the minimum over the acknowledging
      majority, never from the leader's newer view.
    - **The vote-refusal and restart windows are the LONGEST that could
      apply (R3-07):** the maximum `ElectionTimeoutMin` over every contract
      present in the snapshot or the log, committed or not, and never below
      the one the node last reported. A shorter window could let a node vote
      while a leader under the longer contract still holds its lease. A
      mutation that takes the window from the committed contract alone must
      fail. **The generation a follower reports is the one its durable
      committed entries put in force, the one a restart rebuilds, and a node
      never campaigns inside its own refusal window** ([issue
      #521](https://github.com/kitsunium/sdk/issues/521)): it reported the
      generation applied in memory, and after a crash fell back to the older,
      shorter contract and voted or campaigned inside the lease it had
      granted. An acknowledgement of a generation the leader does not know
      grants no lease, rather than the shortest contract it knows ([issue
      #522](https://github.com/kitsunium/sdk/issues/522)).
    - Validation: `0 < MaxClockDrift < 1` and finite, and the cross-field rules
      of D14 are checked after defaults are applied.

  **Instants, not ticks (R7-20).** The driver takes `clock.Now()` just before
  it hands a heartbeat to the transport, and passes that instant into the
  step function with the send. The lease and CheckQuorum are both checked
  with `Since` on such instants at serve time, never counted in ticks.
  Nothing on that path goes through `UTC`, `In` or `Round`, which strip
  Go's monotonic reading. The driver records each loop turn's latency
  (`consensus_loop_turn_seconds`, D13). **A turn longer than the drift budget
  drops the lease** for the rest of the term. The drift budget is
  `ElectionTimeoutMin × MaxClockDrift` (R8-37): a Forgejo garbage-collection
  pause, in the same process, is exactly that.

  The lease is used only when the leader has applied its commit index and
  that index is past the no-op of its term (`raft.js:1427-1432`).
  `MaxClockDrift` has no default: `ReadLease` with a zero drift is refused at
  construction (ADR 0031, refuse half). `Bootstrap` completes and refuses the
  founding contract as `NewNode` completes and refuses `InitialTiming`, so the
  contract that rules every node is the one `NewNode` validated, and no lease
  is held under a contract whose drift is not in (0, 1) ([issue
  #523](https://github.com/kitsunium/sdk/issues/523)). Each condition has its mutation test
  (D15).

  What a lease does NOT guarantee is stated as loudly as ADR 0052 D5 states it
  for fences (R2-39). A leader process paused AFTER its lease check and BEFORE
  its answer — a GC pause, a VM steal, `SIGSTOP` — serves a read that may be
  stale. So does a clock whose RATE strays beyond `MaxClockDrift`, from a VM
  migration, a host that throttles its timer, or a suspended guest whose
  monotonic clock stopped while others ran. ReadIndex has no such window,
  which is why it stays the default. For the linearizability checker,
  `Barrier` followed by the local read is ONE logical read, with the
  invocation at `Barrier`'s call and the response at the read's end.

  **Two gaps in raft-sql-poc's lease, which this ADR closes.** It measures the
  lease on `Date.now()` (`raft.js:1495-1506`), a wall clock that NTP can
  step. It also keeps the lease while a transfer runs (`raft.js:756-787` does
  not clear it), and the transfer's `RequestVote` skips the live-leader check.
  The POC also offers a `leader` read with no confirmation. This ADR does not:
  it is exactly the read a deposed leader serves stale.

- **`WaitApplied(ctx, index)`** waits until the local node has applied
  `index`. It gives read-your-writes and monotonic reads to a client that
  carries its index; it is NOT a linearizable read for a client that does not.
  For the first consumer, applied is not enough: a Git read waits on the
  projection watermark of its repository (D19).

### D10 — Fencing: an entry's position identifies a command, not an authority (R1-20)

`Entry.Position{Term, Index}` is handed to `Apply`. `Index` is a fencing
token in ADR 0052 D5's sense: unique, strictly increasing over the whole
group's life, across terms, and identical on every node.

**The term of an entry is not current authority.** The first draft said a
side effect "tagged with its term can be refused by a receiver that has seen
a higher one". That conflates two identities:
- the **command identity**, which is the entry's `Position`. It is the same
  on every node and forever, and it says WHICH command a side effect belongs
  to.
- the **dispatch authority**, which says WHO may perform the side effect now.
  That belongs to the dispatcher's own current leadership. It is not the
  term written in a committed entry, which a deposed leader and the new one
  both read.

A side effect — a webhook, a CI trigger — is therefore written as a row of an
**outbox** in the replicated state by `Apply`. The current leader alone
dispatches pending rows, and each row carries its command identity. The
receiver deduplicates on that identity. A deposed leader that dispatches a
row late produces a duplicate the receiver drops, never a second effect.

**Positions are scoped by lineage (R3-22).** A `Position` alone is not unique
across a `Recover` or a `RestoreSnapshot`: the new log restarts its own
history. Client positions (the read-after-write cookie) and fences are
therefore scoped by the `LogIdentity` they come from. A token of another
lineage is refused with `LINEAGE_MISMATCH`. At a disaster recovery, the
fence transition is defined: a resource that accepted fences of the old
lineage accepts the new one only after the operator records the change, and
from then on refuses the old lineage.

**Outbox identities survive a recovery (R5-10, R6-07).** An outbox item that
survives `Recover` keeps its ORIGINAL command identity, the one it was
committed with. Only commands committed after the recovery carry the new
lineage.
- **Each dispatch carries two things.** The original command identity, which
  the receiver deduplicates on. And the dispatcher's CURRENT `LogIdentity`
  and term, which is its authority, and which the receiver fences on.
- After the switch, a receiver refuses any dispatcher of the old lineage,
  even with a command identity it has never seen.
- `Apply` receives the lineage-scoped command token of its entry, so a
  retained entry keeps its identity across the recovery. **The token is the
  one `Entry` carries, built by the engine from the entry's position and the
  `LogIdentity` that committed it (R7-09).** For an entry kept by `Recover`,
  that is the PREDECESSOR identity whenever its index is at or below the last
  kept index the lineage records. Its layout is fixed in D14.
- A test has an old-cluster node dispatch after the switch, and the
  dispatch must be refused.

This is the answer to ADR 0052 D11. A lock over this domain is a state
machine whose acquire entry's `Index` is the lease's `Fence()`; it needs no
third-party system, so it belongs in the SDK, and it inherits D11's sentence
unweakened. The `lock.Locker` adapter itself is deferred.

### D11 — One group in v1, an API that does not forbid more

- Every `Message` and frame carries a `GroupID`; v1 runs exactly one group,
  `1`. A v2 that multiplexes groups on one `Transport` changes no wire format.
- `Transport.Bind` is keyed by group, and one transport serves a process.
- `Node` is per group; nothing is process-global except the transport.
- **`OpenDataDir` owns the data-directory lock (R1-21, R3-24).** It takes the
  lock through `lock.NewFileLocker` (ADR 0052) BEFORE it reads, scans, repairs
  or truncates anything, and `DataDir.Close` releases it. **Path discipline
  (R7-11):** it opens the directory once with `os.OpenRoot`, and resolves every
  file through that root, so no link leads out of it, as
  `internal/service/data/vfs` already does. A second process
  opening the same directory fails at once with `DATA_DIR_LOCKED`, before
  either can touch the tail.
  **The data directory is on a local filesystem, stated
  ([#422](https://github.com/kitsunium/sdk/issues/422)).** On NFS, Linux
  emulates `flock` with POSIX locks, which belong to the process: two
  `DataDir` of one process on the same directory would no longer exclude
  each other, and the durability rules above assume a local disk. No check
  can tell every network filesystem apart, so this is a deployment rule,
  not a refusal.
- **Who closes what (R3-18).** A `Node` never closes what it was handed: the
  `DataDir`, its stores and the transport belong to the caller. `DataDir.Close`
  is idempotent, and refuses while a `Node` built on it is open. `Recover` and
  `Inspect` refuse a `DataDir` that a live `Node` holds.

Heartbeat coalescing and quiescing idle groups — the two things a
group-per-repository design needs and Gitaly never built — are NOT in v1,
because one group has nothing to coalesce. raft-sql-poc built both and
measured them, and they are the v2 specification. ONE heartbeat frame per
pair of machines carries every quiescent group, delta-encoded: 4 bytes for a
group whose term and commit the receiver already confirmed, and a 52-byte
"all clear" acknowledgement (`host.js:1-27`, `raft.js:1351-1368`). At 256
shards on 6 machines that took idle traffic from 10 240 to 300 to 320
messages/s (÷32 to ÷34). ONE WAL per machine is shared by all its groups.
The `LogStore` port does not forbid it: a shared store hands out per-group
views whose `Sync` is the same call. The POC also measured the cost: with 8
shards at replication 3 on 6 machines, losing 2 machines left 4 of 8 shards
unavailable.

### D12 — Errors

Both ranges are declared in `design/app/consensus.yaml`, under the core
package `internal/core/app/consensus`, and added to `design/sdk.yaml`'s
`codes.ranges` in the implementing change. kit writes `codes_gen.go` and the
`codeRangeOwners` entries from them (ADR 0164, R7-00a, R7-02).

Core `0.2.57.*` (`0x00_02_39_*`):

| Code | Reason | Meaning |
|---|---|---|
| `0.2.57.1` | `CONSENSUS_MISCONFIGURED` | refused at construction: a bound missing or out of range (D14), a timing rule (D5), a lease without drift (D9) |
| `0.2.57.2` | `NOT_LEADER` | certainly not appended; the known leader in `Fields`, read with `LeaderHint` |
| `0.2.57.3` | `OUTCOME_UNKNOWN` | appended or possibly delivered, then the answer was lost — may still commit; a context's end is its cause (D6) |
| `0.2.57.4` | `LEADERSHIP_UNCONFIRMED` | a barrier's round did not reach a majority in time |
| `0.2.57.5` | `MEMBERSHIP_CHANGE_PENDING` | one change at a time |
| `0.2.57.6` | `MEMBERSHIP_INVALID` | unknown node, last voter, a removal below `MinVoters` (R7-27), promotion without durable progress or a prospective majority |
| `0.2.57.7` | `TRANSFER_FAILED` | the target did not win within one election timeout |
| `0.2.57.8` | `ENTRY_TOO_LARGE` | a command above `MaxEntryBytes` |
| `0.2.57.9` | `COMPACTED` | an index below the first retained one; internally, the switch to the snapshot path |
| `0.2.57.10` | `STATE_MACHINE_FAILED` | `Apply` panicked; the entry IS committed, and the node stopped applying |
| `0.2.57.11` | `NODE_STOPPED` | the node was closed or stopped itself |
| `0.2.57.12` | `ENTRY_SUPERSEDED` | a COMMITTED entry at the proposal's index has another term: certainly not committed (D6, from IWFS, R7-15) |
| `0.2.57.13` | `OVERLOADED` | refused before append: pending bytes or apply lag over their bound (D6) |
| `0.2.57.14` | reserved | `STATE_DIVERGED`, moved to ADR 0173 with its meaning (Q12) |
| `0.2.57.15` | `VERSION_UNSUPPORTED` | an entry kind, record, frame or snapshot version this node does not know; the node stops (D18, R1-22) |
| `0.2.57.16` | `NODE_ID_RETIRED` | a removed `NodeID` offered again (D8, R1-17) |
| `0.2.57.17` | `NO_SPACE` | the replicated no-space alarm is raised: writes are refused until it is cleared; the alarm, its clearing, membership changes, `TransferLeadership` and the control entries of D3 are still admitted (D17, R2-29, R6-13) |
| `0.2.57.18` | `RESULT_TYPE_MISMATCH` | `ProposeAs` got an `Apply` result of another type; the entry IS committed, and the error carries its `Token` (D14, R2-19, R5-14, R6-13) |
| `0.2.57.19` | `PAYLOAD_VERSION_REFUSED` | a proposal's payload version is above what some member announced; refused before append, retryable after the upgrade (D18, R2-16) |
| `0.2.57.20` | `LINEAGE_MISMATCH` | a position, outbox identity or fence from another `LogIdentity` lineage (D10, R3-22) |
| `0.2.57.21` | reserved | `HASHER_FAULT`, moved to ADR 0173 with its meaning (Q12) |
| `0.2.57.22` | reserved | `BINARY_REPLAY_MISMATCH`, moved to ADR 0173 with its meaning (Q12) |
| `0.2.57.23` | reserved | `APPLY_FINGERPRINT_REFUSED`, moved to ADR 0173 with its meaning (Q12) |
| `0.2.57.24` | reserved | `SELF_TEST_FAILED`, moved to ADR 0173 with its meaning (Q12) |
| `0.2.57.25` | `ADMIN_PRECONDITION_FAILED` | a clearing or raising operation named an alarm, version or contract that is not the current one (D17, R4-06) |

**Reserved serials (R6-13, R7-04).** The five serials reserved for ADR 0173
are declared in the same design file and the same package,
`internal/core/app/consensus`, when ADR 0173 is implemented. That keeps the
errs registry's two rules: one owner per `MM.LL.PP` range (ADR 0035), and one
`Define` per value. ADR 0152's design declares none of them.

Service `0.3.92.*` (`0x00_03_5C_*`), declared in the CORE package too. `LL`
stays `3`, the layer that allocated the range (ADR 0160 §3, R7-02):

| Code | Reason | Meaning |
|---|---|---|
| `0.3.92.1` | `LOG_CORRUPT` | a CRC failure that is not a short read or a zeroed-sector tear, or a gap in the segments; never repaired (D4) |
| `0.3.92.2` | `HARD_STATE_CORRUPT` | the hard-state record or the checkpoint holding it is unreadable; never defaulted (R1-30) |
| `0.3.92.3` | `SNAPSHOT_CORRUPT` | a snapshot's SHA-256 or size does not match its metadata |
| `0.3.92.4` | `STORAGE_FAILED` | a write, `fsync` or read failed; the node stops, no retry |
| `0.3.92.5` | `DATA_DIR_LOCKED` | another process holds the data directory |
| `0.3.92.6` | `DATA_DIR_MISMATCH` | the stamp names another cluster, node or incarnation |
| `0.3.92.7` | `PEER_REJECTED` | the certificate's single URI SAN is not canonical, or does not name the expected cluster and slot, or the incarnation is not admitted (R7-25, R7-41) |
| `0.3.92.8` | `CLUSTER_MISMATCH` | a peer's first frame names another cluster or group (a misconfiguration guard) |
| `0.3.92.9` | `FRAME_INVALID` | a frame that does not parse or exceeds `MaxFrameBytes`; the connection closes |
| `0.3.92.10` | `SNAPSHOT_TOO_LARGE` | an announced snapshot above the local `MaxSnapshotBytes`; refused before spooling (D7) |
| `0.3.92.11` | `BOOTSTRAP_REFUSED` | a listed peer is already stamped, or the member lists differ (D8) |
| `0.3.92.12` | `TRANSPORT_MISCONFIGURED` | an identity with nil `RootCAs`, a listener without client-certificate verification, or a limit out of range (D4) |
| `0.3.92.13` | `RECOVER_REFUSED` | `Recover` on a directory that is not stopped and locked, without a new `ClusterID`, without the typed confirmation, without a durable fence for every old voter, or while an old seed answers and no force is given (D17, R2-25, R7-24) |
| `0.3.92.14` | `LOG_QUARANTINED` | the data directory carries a quarantine marker from a `LOG_CORRUPT`; the node refuses to start until it is cleared (D4, R2-01) |
| `0.3.92.15` | `RESTORE_REFUSED` | `RestoreSnapshot` into a non-empty directory, with a zero expected digest, without the typed confirmation, or with a digest or bound that does not match (D17, R2-26, R3-15) |
| `0.3.92.16` | `DATA_DIR_IN_USE` | `DataDir.Close`, `Recover` or `Inspect` while a live `Node` holds the directory (D11, R3-18) |
| `0.3.92.17` | `JOIN_REFUSED` | a pre-admission `JOIN` for a slot whose member is not fenced, over its rate, or with a retired or reused id (D4, R7-42) |
| `0.3.92.18` | `STOP_MARKER_PRESENT` | `NewRaft` found a stop marker, and names its reason. It is cleared by a reboot (`STORAGE_FAILED`) or by an operator (D4, R7-44) |

### D13 — Observability (R1-25, R7-37)

Through `metrics` (ADR 0044), attributed by `group`. The names are part of
the contract and are listed here so that dashboards and alerts can be
written before the code. **They are spelled as Prometheus exposes them
(R7-37):** underscores, a `_total` suffix on counters, units in the name.

| Metric | Kind |
|---|---|
| `consensus_role`, `consensus_term`, `consensus_leader_changes_total` | gauge, gauge, counter |
| `consensus_commit_latency_seconds`, `consensus_apply_latency_seconds` | histograms |
| `consensus_fsync_latency_seconds`, `consensus_batch_entries`, `consensus_batch_bytes` | histograms |
| `consensus_apply_lag_bytes`, `consensus_apply_lag_entries` | gauges |
| `consensus_peer_match_lag_entries`, `consensus_peer_inflight`, `consensus_peer_unreachable` | gauges by peer |
| `consensus_elections_started_total`, `consensus_prevotes_lost_total` | counters |
| `consensus_snapshot_duration_seconds`, `consensus_snapshot_bytes`, `consensus_snapshot_installs_total` | histogram, histogram, counter |
| `consensus_log_bytes`, `consensus_log_quota_bytes`, `consensus_nospace` | gauges |
| `consensus_tail_truncated_total`, `consensus_frames_dropped_total` | counters by reason |
| `consensus_barrier_latency_seconds` | histogram by mode |
| `consensus_failure_tolerance` | gauge: how many voters can fail now with a majority left |
| `consensus_tls_cert_expiry_seconds` | gauge per identity |
| `consensus_has_leader`, `consensus_peer_last_contact_seconds`, `consensus_peer_rtt_seconds` | gauge, gauge by peer, histogram by peer (R2-28) |
| `consensus_proposals_failed_total`, `consensus_proposals_pending` | counter by code, gauge (R2-28) |
| `consensus_fs_free_bytes`, `consensus_cluster_version`, `consensus_protocol_version` | gauges, `fs_free` by declared filesystem (R2-28, R7-33) |
| `consensus_tls_reload_failures_total`, `consensus_tls_peer_cert_expiry_seconds` | counter, gauge by peer (R2-28) |
| `consensus_loop_turn_seconds` | histogram (R7-20) |
| `consensus_chain_refusals_total`, `consensus_snapshot_pins` | counter by leader, gauge (R7-19, R7-38) |
| `consensus_stopped` | gauge by `StopReason`, set before the process exits (R7-37) |

**The product pushes stop reasons (R7-37).** A stopped node exits, so a
scrape may never see `consensus_stopped`. The product sends the
`StopReason` and its exit code to its alert path before exiting, and the
supervisor's crash-loop alert covers the rest.

**Mandatory alert rules (R7-37).** The consumer guide ships them with these
thresholds, provisional like D16's numbers:

| Alert | Condition |
|---|---|
| no leader | `consensus_has_leader == 0` for 10 s |
| failure tolerance lost | `consensus_failure_tolerance < 1` for 1 min |
| node stopped | any `consensus_stopped`, or the pushed stop reason, or 3 restarts in 10 min |
| fsync slow | p99 of `consensus_fsync_latency_seconds` above 50 ms for 10 min |
| apply lag | `consensus_apply_lag_bytes` above half of `MaxApplyLagBytes` for 5 min |
| no space | `consensus_nospace == 1`, or any `consensus_fs_free_bytes` under twice the reserve |
| certificate | `consensus_tls_cert_expiry_seconds` under 7 days, or any reload failure |
| peer unreachable | `consensus_peer_unreachable == 1` for 1 min |
| chain refusals | `consensus_chain_refusals_total` increasing |
| loop turn | p99 of `consensus_loop_turn_seconds` above the drift budget for 5 min |
| snapshot pin | `consensus_snapshot_pins` held longer than the pin TTL of D19 |

**A leader that makes no progress steps aside
([#554](https://github.com/kitsunium/sdk/issues/554)).** CheckQuorum
measures contact, not progress: a leader whose fdatasync never returns, or
whose `Apply` blocks, kept heartbeating and leading while every write and
read entering at it waited. At every tick the leader watches its durable
index against its log and its applied index against its commit; when
either has trailed without moving for `ElectionTimeoutMax`, it transfers
leadership to the voter with the highest acknowledged index (D8). A slow
fdatasync that moves within the bound changes nothing.

`Node.Health()` returns a structured verdict an orchestrator can poll: role,
leader known — on a node that does not lead, a leader that answered one of
its reads within `ElectionTimeoutMax`, a probe read sent every half minimum
election timeout keeping it fresh in a quiet cluster; a heartbeat only says
the leader reaches the node, not that it hears it, and a follower whose
answers were lost, or a learner cut off from the voters, kept its leader
and reported ready on a frozen commit
([#541](https://github.com/kitsunium/sdk/issues/541),
[#540](https://github.com/kitsunium/sdk/issues/540)) — quorum reachable,
applied up to the commit (once caught up, a lag of at most 64 entries
counts as caught up while the applied index moved within
`ElectionTimeoutMin`: a published status pairs a new commit with the
applied index of the turn before, and the strict comparison flapped a
healthy node under load,
[#545](https://github.com/kitsunium/sdk/issues/545)), apply lag over its bound, every current alarm
with its `AlarmID` and the nodes it names (R5-09), the current timing
contract and cluster version, so the product can fill an operation's
preconditions, certificate expiry under its threshold, the smallest peer
snapshot limit approached (R2-30), and **`Stopped` with its `StopReason`**
when the node has stopped itself (R2-28). A `LeadershipWatcher` sibling
notifies leadership changes, so the product does not poll for them. **Each
`Watch(ctx)` call returns its OWN channel (R7-11), unsubscribed and closed
when `ctx` ends (R8-36).** It has capacity one and holds
the latest value: the loop replaces an unread value without blocking. Every
channel is closed on `Close` (R3-19). Through `trace` (ADR 0051): one span
per `Propose` from submission to local apply, with events at append, commit
and apply; the trace context travels in the forwarding frame only — never in
the log, which must not grow with telemetry. Through `logger`: role and term
changes, membership changes, snapshots and truncations at `Info`; nothing per
entry.

### D14 — Public surface (sketch, signatures only)

```go
package consensus // pkg/v1/app/consensus, written by kit from design/app/consensus.yaml (ADR 0166)

// Every type needed to implement a published port is aliased here (R2-32).
type (
	NodeID         = coreconsensus.NodeID      // uint64, never 0, never reused
	Incarnation    = coreconsensus.Incarnation // [16]byte, drawn at first stamp; not a secret
	ClusterID      = coreconsensus.ClusterID
	LogIdentity    = coreconsensus.LogIdentity // ClusterID + log epoch + lineage (R2-09, R3-01)
	GroupID        = coreconsensus.GroupID
	Position       = coreconsensus.Position    // struct{ Term, Index uint64 }
	Entry          = coreconsensus.Entry       // Position, Kind, Version, BodySum, ChainSum, Data []byte (immutable) (R4-21), and its Token is set in memory by the engine, never encoded in the log (R8-36)
	Token          = coreconsensus.Token       // comparable {Cluster, Epoch, Term, Index} (R4-27, R5-14), 41 bytes encoded, D14 (R7-09)
	EntryKind      = coreconsensus.EntryKind
	HardState      = coreconsensus.HardState
	Stamp          = coreconsensus.Stamp
	Message        = coreconsensus.Message     // a plain value: Version, Group, From, Term, Kind, Entries. No ADR 0173 field (R6-04). Encoded by the service's AppendMessage, never by a method (R7-05)
	AuthenticatedPeer = coreconsensus.AuthenticatedPeer // R7-05
	InstallOffer   = coreconsensus.InstallOffer  // R7-08
	InstallAccept  = coreconsensus.InstallAccept // R7-08
	StopReason     = coreconsensus.StopReason    // ExitCode() returns errs.ExitCodeOf (R7-14, R8-35)
	JoinConfig     = coreconsensus.JoinConfig    // the member addresses to try, the stamp, the members' keys (R8-09, M4)
	JoinRequest    = coreconsensus.JoinRequest   // slot and SPKI from TLS, the new NodeID and incarnation (R8-09)
	Joiner         = coreconsensus.Joiner        // sibling for custom transports (R8-09)
	JoinAuthorizer = coreconsensus.JoinAuthorizer // the product's port (R8-09)
	FencedAttestation = coreconsensus.FencedAttestation // {old voter, method, one signed receipt per NAS} (R8-20, R8-36, R9-09)
	SnapshotMeta   = coreconsensus.SnapshotMeta
	SnapshotSink   = coreconsensus.SnapshotSink
	InstallRequest = coreconsensus.InstallRequest
	TimingContract = coreconsensus.TimingContract
	Versions       = coreconsensus.Versions
	Member         = coreconsensus.Member     // ID, Incarnation, Slot, SPKI, Address, Voter, Joining (R8-07, R8-36)
	Membership     = coreconsensus.Membership // the members at a Position, the retired IDs
	Operator       = coreconsensus.Operator   // opaque id, printable ASCII 0x21-0x7E, at most 128 bytes, no personal data (R2-36, R3-24, R7-27, R8-09)
	SnapshotHandle = coreconsensus.SnapshotHandle // R3-16
	Status         = coreconsensus.Status     // Role, Term, Leader, Commit, Applied, versions (R2-32)

	Replicated           = coreconsensus.Replicated // the consumer port (R1-28)
	StateMachine         = coreconsensus.StateMachine
	DurableStateMachine  = coreconsensus.DurableStateMachine
	BatchingStateMachine = coreconsensus.BatchingStateMachine
	Snapshot             = coreconsensus.Snapshot
	LogReader            = coreconsensus.LogReader
	LogStore             = coreconsensus.LogStore
	SnapshotStore        = coreconsensus.SnapshotStore
	Transport            = coreconsensus.Transport
	UnreachableReporter  = coreconsensus.UnreachableReporter
	LeadershipWatcher    = coreconsensus.LeadershipWatcher // Watch(ctx) <-chan Status, unsubscribed when ctx ends (R2-28, R8-36)

	Health     = svcconsensus.Health   // includes Stopped, StopReason
	ReadMode   = svcconsensus.ReadMode // ReadIndex (zero value) | ReadLease
	Durability = svcconsensus.Durability
	Config     = svcconsensus.Config
	TransportConfig = svcconsensus.TransportConfig // the D2 and D14 lists agree (R7-11)
	Node       = svcconsensus.Node
	Report     = svcconsensus.Report
	DataDir    = svcconsensus.DataDir
	QuarantineExit = svcconsensus.QuarantineExit // WipeAndRejoin | TruncateAndStaySilent (R3-09)
	RecoverOptions = svcconsensus.RecoverOptions // Force, Confirm, Operator, Joining, FencedAttestation, WitnessKeys (R2-25, R3-02, R4-04, R9-08)
	RestoreOptions = svcconsensus.RestoreOptions // Expected (non-zero), Confirm, Operator, MaxEncoded, MaxDecoded, Codecs (R3-15)
	AlarmID        = coreconsensus.AlarmID       // the index that raised an alarm (R4-06)
	Precondition   = coreconsensus.Precondition  // {LogIdentity, AlarmID or contract/version generation}; from Health/Status (R6-10)
)

// The consumer port, in core: the only algorithm-neutral interface (D1).
type Replicated interface {
	Propose(ctx context.Context, data []byte) (Token, any, error) // lineage-scoped (R4-27)
	Barrier(ctx context.Context) (Token, error)
	WaitApplied(ctx context.Context, t Token) error // LINEAGE_MISMATCH for another lineage (R4-27)
	Status() Status
}

// ProposeAs types the Apply result (R2-19). A result of another dynamic type
// is RESULT_TYPE_MISMATCH, carrying the committed Token: the entry IS
// committed. A nil result is the zero R (R5-14). The engine keeps the
// lineage chain; a Token names only its own cluster and epoch.
func ProposeAs[R any](ctx context.Context, r Replicated, data []byte) (Token, R, error)

type Config struct { // svcconsensus.Config, shown for its fields; every bound is in the table below
	Cluster             ClusterID
	Group               GroupID
	ID                  NodeID
	Machine             StateMachine
	Durability          Durability // must agree with DurableStateMachine (R2-09); kept in v1 (Q11)
	Log                 LogStore
	Snapshots           SnapshotStore
	Transport           Transport
	Clock               clock.Timed
	InitialTiming       TimingContract // proposed at Bootstrap only; the committed one rules (R2-14)
	MaxBatchBytes, MaxInflight, MaxInflightBytes, MaxEntryBytes int
	MaxPendingBytes, MaxApplyLagBytes, RecentEntriesBytes      int64
	SnapshotEvery, SnapshotTrailing, PromoteLag                uint64
	SnapshotEveryBytes, MaxSnapshotBytes, MaxLogBytes, LogReserveBytes int64
	SnapshotCodec       transform.BoundedDecompressor // core/data/transform port, flate by default, id recorded (R7-06)
	SnapshotPausesApply bool                 // the state machine cannot cut cheaply (R2-34)
	MinVoters           int                  // Remove refused below it (R7-27)
	JoinAuthorizer      JoinAuthorizer       // the product's fence decision for a JOIN (R8-09)
	HardCloseTimeout    time.Duration        // HardClose returns within it (R7-11)
	Reads               ReadMode
	PreferredLeaders    []NodeID
	Meter               metrics.Meter
	Tracer              trace.Tracer
	Logger              logger.Logger
}

type TransportConfig struct {
	Identity                tlsid.ReloadableIdentity // the TLS sibling ADR's source, cluster and slot from its URI SAN (R3-17, R7-41)
	ListenAddress           string         // bound as given, e.g. a private interface (R2-37)
	Seeds                   map[NodeID]string
	MaxFrameBytes           int
	HandshakeTimeout        time.Duration // ≤ 1 s
	MaxUnauthenticatedPerIP int           // per IPv4 address and per IPv6 /64 (R2-22)
	ReservedMemberSlots     int
	PeerQueueBytes          int64
	InboundDataBytes        int64 // R2-08
	InboundControlMessages  int
	MaxControlFrameBytes    int   // R3-20
	InboundControlBytes     int64 // R3-20
	MaxConnectionAge        time.Duration // R2-20
	ForwardedAdminBytes     int64 // forwarded administrative entries, bounded below the votes' share (R7-37)
	MaxJoinPerSlot          int   // JOIN frames per slot per election timeout (R7-42)
}

func Bootstrap(ctx context.Context, cfg Config, members []Member) error
func NewRaft(cfg Config) (*Node, error)

// The shipped layout (R2-33): offline functions work on it, and only on it.
// A DataDir value wraps a pointer, so copies share one lock and one Close (R4-29).
func OpenDataDir(dir string) (DataDir, error) // takes the directory lock first (D11)
func (d DataDir) Log() LogStore
func (d DataDir) Snapshots() SnapshotStore
func (d DataDir) Close() error // idempotent

// Offline, on a stopped node, called by the product's own commands (D17).
func Init(d DataDir, cluster ClusterID, slot string) (NodeID, Incarnation, error) // offline only, random NodeID, joining stamp, idempotent (R3-02, R4-29, R7-41, R8-10)
func Redraw(d DataDir) (NodeID, error) // only while the stamp is joining and the log is empty, after a JOIN_REFUSED for a reused or retired id (R8-10)
func Recover(d DataDir, newCluster ClusterID, members []Member, opt RecoverOptions) error
func RestoreSnapshot(d DataDir, r io.Reader, newCluster ClusterID, members []Member, opt RestoreOptions) (SnapshotMeta, error) // runs without a Node (R3-15)
func Inspect(d DataDir) (Report, error) // read-only
func ClearQuarantine(d DataDir, exit QuarantineExit, op Operator) error // wipe-and-rejoin, or truncate-and-stay-silent (R3-09)

func NewTCPTransport(cfg TransportConfig) (Transport, error) // also a Joiner (R8-09)
func Latest(s SnapshotStore) (SnapshotHandle, error)        // the service's, re-exported (R8-36)
func Lane(frame []byte) (FrameLane, error)                     // control or data, from the frozen header (R8-36)
func NewMemoryLog() LogStore // tests
func LeaderHint(err error) (NodeID, bool) // from the error's field, a decimal string (R7-10)
func TokenOf(err error) (Token, bool)     // from the error's field, hex of the 41-byte encoding (R7-10)

// The wire codecs are the service's (ADR 0160 §4), re-exported here (R7-05).
func AppendMessage(dst []byte, m *Message) []byte
func ParseMessage(frame []byte, m *Message) error // FRAME_INVALID, VERSION_UNSUPPORTED
func AppendSnapshotMeta(dst []byte, m *SnapshotMeta) []byte
func ParseSnapshotMeta(b []byte, m *SnapshotMeta) error
func AppendToken(dst []byte, t Token) []byte
func ParseToken(b []byte) (Token, error)

// Online operations, called by the product's administration (D17, R2-36).
// Every one takes the operator identity the caller vouches for, can be
// called on ANY node (forwarded to the leader, Q8, R3-03), writes an entry,
// and is logged at Warn (R3-14).
func (n *Node) Propose(ctx context.Context, data []byte) (Token, any, error)
func (n *Node) Barrier(ctx context.Context) (Token, error)
func (n *Node) WaitApplied(ctx context.Context, t Token) error
func (n *Node) AddLearner(ctx context.Context, op Operator, m Member) error
func (n *Node) Promote(ctx context.Context, op Operator, id NodeID) error
func (n *Node) Remove(ctx context.Context, op Operator, id NodeID) error
func (n *Node) UpdateAddress(ctx context.Context, op Operator, id NodeID, addr string) error
func (n *Node) DenyPin(ctx context.Context, op Operator, id NodeID) error // withdraws the member's SPKI pin (R9-18)
func (n *Node) TransferLeadership(ctx context.Context, op Operator, to NodeID) error
// Each clearing or raising operation names what it acts on, as a
// lineage-scoped Precondition that Health and Status return and that the
// forwarding envelope carries; Apply refuses a mismatch with
// ADMIN_PRECONDITION_FAILED (R4-06, R5-09, R6-10).
func (n *Node) ProposeTimingContract(ctx context.Context, op Operator, pre Precondition, c TimingContract) error
func (n *Node) RaiseClusterVersion(ctx context.Context, op Operator, pre Precondition, v uint32) error
func (n *Node) ClearNoSpace(ctx context.Context, op Operator, pre Precondition) error
func (n *Node) SaveSnapshot(ctx context.Context, op Operator, w io.Writer) (SnapshotMeta, error) // R3-14
func (n *Node) Membership() Membership
func (n *Node) Status() Status
func (n *Node) Health() Health
func (n *Node) Close(ctx context.Context) error // transfers leadership first; joins every goroutine (R2-38)
func (n *Node) HardClose() error                // stops now, no transfer, no final sync, bounded by HardCloseTimeout (R7-11)
```

**The token's layout is fixed (R7-09).** One version byte (`1`), the
`ClusterID` (16 bytes), then the epoch, the term and the index as three
`uint64`, big-endian: 41 bytes. A parser refuses another length or version.
A `ClusterID` is 16 bytes everywhere it is encoded.

**Methods are the design's (R8-36).** `Node` and `DataDir` are service
types declared in the design file with `decl: true`, and their methods are
listed there, so `surface: closed` holds them (ADR 0168).

**`Lane` and the frozen header (R8-36, R9-07).** `Lane(frame)` reads only
the frame header, frozen in D18, and says whether the frame belongs on the
control or the data connection. The lane is carried in the high bits of
`type`, so a frame type a newer version adds still routes. A custom transport uses it to keep votes
off the data pipe.

**`HardClose` is bounded (R7-11).** It returns within `HardCloseTimeout`
(default 5 s). It does not join a goroutine stuck in `fdatasync`: that
goroutine is abandoned and counted, and the data-directory lock stays held
until the process exits, so nothing else can open the directory meanwhile.

`Config`, `Node`, `Health`, `DataDir` and `TransportConfig` belong to the
engine and are aliased from service. Every type a port speaks, `Status` included, lives in core
(ADR 0074, R2-32). The frozen ports are the interfaces above; every later
capability is a sibling discovered by assertion (ADR 0039). `Operator` is an
opaque identity — a name and an id — that the product provides and vouches
for. The SDK records it in the entry and never authenticates it (R2-36).
**Custom stores get no offline tooling (R2-33).** `Recover`,
`RestoreSnapshot` and `Inspect` work on `DataDir`, the shipped layout. A
consumer that brings its own `LogStore` and `SnapshotStore` brings its own
offline tools.

**Every bound has an owner, a default and a validation (R1-29).** "Clamp" is
ADR 0031's clamp half: a zero becomes the default. "Refuse" is its refuse
half: `CONSENSUS_MISCONFIGURED`, or `TRANSPORT_MISCONFIGURED` for the
transport. The cross-field rules are checked after defaults are applied
(R2-14).

| Field | Owner | Default | Validation |
|---|---|---|---|
| `InitialTiming.HeartbeatInterval` | engine | 100 ms | clamp; refuse if not < `ElectionTimeoutMin`/5 |
| `InitialTiming.ElectionTimeoutMin`, `…Max` | engine | 1 s, 2 s | clamp; refuse if Max ≤ Min |
| `InitialTiming.MaxClockDrift` | engine | none | with `ReadLease`: refuse unless finite and `0 < d < 1` (R2-14) |
| `Durability` | engine | none | refuse if it disagrees with the state machine's siblings (R2-09) |
| `MaxBatchBytes` | engine | 1 MiB | clamp; refuse if it plus 1 KiB of framing is above the shipped transport's `MaxFrameBytes` (review M2) |
| `MaxInflight`, `MaxInflightBytes` | engine | 64, 8 MiB | clamp; refuse if `MaxInflightBytes` < `MaxBatchBytes` |
| `MaxEntryBytes` | engine | 1 MiB | clamp; refuse if > `MaxBatchBytes` |
| `MaxPendingBytes` | engine | 64 MiB | clamp; refuse if < `MaxEntryBytes` (#557) |
| `MaxApplyLagBytes` | engine | 256 MiB | clamp |
| `RecentEntriesBytes` | engine | 32 MiB | clamp (R2-40) |
| `SnapshotEvery`, `SnapshotEveryBytes` | engine | 16 384, 64 MiB | clamp |
| `SnapshotTrailing` | engine | 4 096 | clamp |
| `MaxSnapshotBytes` | engine, enforced by receiver | none; 1 GiB recommended for the three VPS (R2-29) | refuse zero; refuse if < `SnapshotEveryBytes` |
| `MaxLogBytes` | log | none; 1 GiB recommended for the three VPS (R2-29) | refuse zero |
| `LogReserveBytes` | log | 256 MiB | clamp; refuse if ≥ `MaxLogBytes` |
| `SnapshotCodec` | engine | flate | must be a `BoundedDecompressor`, recorded in `SnapshotMeta` (R7-06) |
| `MinVoters` | engine | 3 | clamp, and refuse above the founding voter count at `Bootstrap` (R7-27) |
| `JoinAuthorizer` | engine | none | without it, every JOIN is refused (R8-09) |
| `HardCloseTimeout` | engine | 5 s | clamp (R7-11) |
| `PromoteLag` | engine | 1 024 | clamp |
| `MaxFrameBytes` | transport | 16 MiB | clamp; refuse if < `MaxBatchBytes` + header |
| `HandshakeTimeout` | transport | 1 s | clamp; refuse > 1 s |
| `MaxUnauthenticatedPerIP`, `ReservedMemberSlots` | transport | 4, 8 | clamp |
| `PeerQueueBytes` | transport | 64 MiB | clamp |
| `InboundDataBytes`, `InboundControlMessages` | transport | 64 MiB, 4 096 | clamp (R2-08) |
| `MaxConnectionAge` | transport | 24 h | clamp (R2-20) |
| `ForwardedAdminBytes` | transport | 64 KiB | clamp, and refuse ≥ `InboundControlBytes` / 4, so forwarded administration never crowds out votes (R7-37) |
| `MaxJoinPerSlot` | transport | 1 | clamp (R7-42) |
| `MaxControlFrameBytes`, `InboundControlBytes` | transport | 64 KiB, 1 MiB | clamp; refuse a control cap ≥ the data cap; control frames are bounded in size and bytes as well as in count (R3-20, R3-27) |

**Memory and disk budgets (R2-08, R2-29).**
- The node's memory budget is the sum of these bounds: `MaxPendingBytes`,
  `MaxApplyLagBytes`, `RecentEntriesBytes`, `MaxInflightBytes` per peer,
  `PeerQueueBytes` per peer and connection, and `InboundDataBytes`. With the
  defaults and two peers, that is under 1 GiB. `NewRaft` reports the total,
  so a product can check it against its own limit.
- **Snapshot slots are bounded and enforced, and the disk reservation is
  their actual maximum (R4-28, R5-01, R6-02).** At most FOUR snapshots exist
  at once: the two retained, one being written, and one being received.
  - *Admission:* no cut starts and no install is accepted while its slot is
    taken.
  - *Replacement:* the oldest retained snapshot is deleted once a new one is
    durable, unless a transfer in flight reads it, in which case the
    replacement waits (R6-01).
  - *Transfer cancellation:* only when the log reaches `LogReserveBytes`
    (R6-01).

  The reservation is therefore `MaxLogBytes + 4 × MaxSnapshotBytes +
  LogReserveBytes`. On the three VPS, each with one shared 100 GB disk, the
  recommended values give about **5.3 GiB**. D17 uses the same figure. All
  values are provisional, like D16's numbers.
- **Inside Forgejo (R3-24).** The replicator shares Forgejo's heap. The
  product sets `GOMEMLIMIT` with headroom for the engine, the ref table and
  the projector, computed from this budget with the MEASURED table (R5-15).
  Round 3's 0.8 GiB was an estimate made before the table was measured. The
  headroom exists so the garbage collector does not discover the budget by
  running out. `GOMEMLIMIT` is a SOFT limit: it makes the
  collector work harder, it does not refuse an allocation. The budget is
  what keeps the process inside it (R4-18).
- **A state machine held in memory (R3-29, R4-18).** When the state machine
  is not durable, as for the first consumer (Q9), the budget is: the engine,
  plus the measured table, its tombstones, the decoding buffers and the
  snapshot versions retained by open views, plus the first consumer's
  capped event queue (D19, R8-23). ADR 0173 adds a second table
  while a repair runs.
- **The start sequence is fixed (R4-18, R5-01):**
  1. open the data directory under its lock;
  2. load the latest snapshot whose checksums pass;
  3. replay the log to the commit boundary;
  4. the pre-vote gate slot, empty unless ADR 0173 is active (R6-04);
  5. vote;
  6. serve.
  The start-up replay is bounded by `SnapshotEvery` plus the entries applied
  since the last cut. The restart time is a D16 target, measured on the real
  ref count.

The other defaults are provisional too, and are revised from the simulator
and the VPS measurements before the first release.
### D15 — Testing: a deterministic simulator first, real clusters second (R1-26)

The engine is split so that it can be tested by simulation: a **step
function** with no goroutine, no I/O and no clock — `Tick()`, `Step(*Message)`,
`Propose(...)`, storage-completion events (D5b), and a `Ready()` that returns
what to persist, what to send and what to apply — and a **driver** that
performs those effects. Every DECISION is in the step function. Safety also
rests on the driver keeping its obligations — the ordering table of D5b, the
storage contract of D4 — and those are tested on their own, under `synctest`
and over the fault-injecting filesystem (R3-24).

**The simulator** (test-only, under the service package) runs N step functions
on ONE goroutine against a virtual clock (`clock.ManualClock`) and an event
queue, so a run is a pure function of its seed:

- a network that delays every message by a draw from the measured RTT matrix
  (9.6 / 22.8 / 27.3 ms, with jitter), drops, duplicates and reorders it with
  seeded probabilities, and applies partition schedules — total, asymmetric
  (A hears B, B does not hear A), and the "bridge" where one node sees both
  halves;
- a disk model that distinguishes WRITTEN bytes from SYNCED bytes: a crash
  drops everything written since the last `Sync`, tears the last unsynced
  record at a **4 KiB** sector boundary (R1-30), and flips bits in SYNCED
  records too, at the tail and before it (R1-10);
- crash and restart at any event, including between a follower's `Sync` and
  its acknowledgement, between a leader's append and its `Sync`, and between
  a state machine's effect and its applied position (R1-02);
- clock-rate skew per node, inside and outside `MaxClockDrift`, including a
  node whose monotonic clock stops while it is suspended (R2-39).

**Constructed schedules, not seed sweeps alone.** Every bug this ADR cites is
a named, hand-built schedule that reaches its state in a few dozen events.
Random seeds then explore around them. A seed sweep that never builds the
schedule finds the rare bug by luck.

**Safety and liveness oracles are separate (R2-39).** The safety oracle runs
after every event and never tolerates a violation, even briefly. It checks:
- **vote uniqueness:** no node votes for two candidates in one term, across
  restarts;
- **term monotonicity:** a node's term never decreases, on disk or in memory;
- at most one leader per term;
- Log Matching on CONTENT, not on `(lastIndex, lastTerm)` summaries (IWFS's
  J20);
- Leader Completeness;
- **agreement at common indexes:** wherever two nodes have both applied index
  i, they applied the same entry;
- **preservation of the acknowledged (R3-23):** every `Propose` that returned
  success stays in the COMMITTED history from then on, and every replica that
  reaches its index applied exactly that entry. A replica that lags is not a
  violation: lagging is judged only by the liveness oracle, under its
  preconditions. A follower partitioned away for the whole run is a valid
  history, and the corpus includes it;
- applied positions never go backwards across a restart.

The liveness oracle is separate, and checks progress only in the windows
where the schedule allows it. Its main property is **eventual convergence**:
after a partition heals, within so many election timeouts, every live node
applies the same prefix. One named liveness scenario cuts power on all three
nodes at once, with torn tails on each, and requires an election and a commit
afterwards (R2-01).

**Linearizability.** Clients of a key-value state machine record
`invoke` / `return` pairs with virtual timestamps, through `Propose`,
`Barrier` + local read, and lease reads. The history is checked with the Wing
& Gong search with Lowe's memoization, partitioned per key (the algorithm
Porcupine and Knossos implement). It is written in the test tree — about
three hundred lines, stdlib — rather than imported: the SDK has implemented a
specification before importing a library twice already (the metrics data
model, OTLP/JSON). **The checker is validated on known histories first:** a
corpus of small histories hand-labelled linearizable or not, including the
classic stale-read and lost-update shapes, must be classified correctly.
**A negative control is mandatory:** a run with lease reads and clock skew
beyond `MaxClockDrift` must produce a history the checker REJECTS. A checker
never seen failing is not a checker.

**One mutation per cited bug.** Behind test-only switches, the simulator
reintroduces each of these, and the suite must fail on each:
- an acknowledgement sent before its sync (raft-sql-poc's
  `unsafeAckBeforeSync`, `raft.js:77-80`, lost 50 to 139 of 300 acknowledged
  writes);
- the three core bugs of D6: same-term vote clearing, commit beyond the
  verified prefix, acknowledging `lastIndex`;
- a lease kept after a transfer;
- the live-leader refusal in PreVote only;
- a membership change before the term's no-op;
- committing an older term's entry by count (Figure 8);
- no vote refusal after a restart;
- a learner that does not answer votes;
- a stuck higher-term node ignored (R1-18);
- the applied position persisted off by one (R1-02);
- `TimeoutNow` accepted without the term check (R2-13);
- a stale storage completion counted (R2-02);
- the dialer's chain verification dropped, leaving only the SAN check
  (R2-21);
- a heartbeat commit not clamped to the follower's match (R2-06);
- a chain value computed by the proposer instead of the leader, and an entry
  skipped on a chain mismatch (R4-13);
- a voter's incarnation swapped in place (R4-03);
- each administrative precondition ignored (R4-06).
- the engine trusting a frame's `From` over the transport's peer (R7-05).
- `ENTRY_SUPERSEDED` on a retained uncommitted conflict (R7-15).
- the election restriction dropped from RequestVote, from PreVote, and under
  the transfer flag (R7-16).
- the transfer flag honoured at another term (R7-17).
- TLS session tickets re-enabled (R7-26).
- a persisted commit index above the last synced index (R7-13).
The transport's identity, admission and size checks get the same treatment.

**Named scenarios.**
- From IWFS's defects (`01-shared-log-single-writer.md` §7.1):
  - every node starts at the same instant, and a split vote must not replay
    in the same term;
  - a partition heals, and a follower that missed committed entries must
    converge through the leader's log repair alone;
  - control traffic is starved, and elections must still settle;
  - concurrent acquisitions, and a fence must not advance on a lost one.
- From R1-18: PreVote granted, RequestVote refused, partition heals.

**The driver is tested too (R7-21).** The goroutines of D5b run under
`testing/synctest`, so their timers are virtual, and under `-race`, but ONLY
over the in-memory doubles: `synctest` does not virtualise a real disk or a
real socket. The real-store fault lane and the loopback mTLS lane use real
time. The loopback mTLS lane is the one that covers "a write that misses its
deadline closes the data connection" (D4).
Step-function behaviour is also pinned by **datadriven scripts**: text files
of inputs and expected outputs, in the style of etcd's raft tests, which make
a regression readable in review. Their reader is written in the test tree,
stdlib only, not imported (R7-11).

**Scenarios added in review run 3 (R7).**
- a shorter overwrite, then a crash, and a crash persisting a random subset
  of unsynced 4 KiB pages and 512 B sectors (R7-12).
- `fdatasync` fails, then the node restarts, and must distrust the tail
  (R7-14).
- both snapshots corrupt: the node asks for a snapshot and never campaigns
  (R7-18).
- an old pair restarted after `Recover` is refused (R7-24).
- all three data directories empty: no cluster forms on its own (R7-43).
- a JOIN for a slot whose voter is live raises the alarm and joins nothing
  (R7-42).

**Scenarios added in review run 2 (R4).**
- a crash with a prepared segment present, under the vote-uniqueness oracle
  (R4-01);
- a zeroed sector before the sync watermark, which must quarantine (R4-02);
- a restore of three nodes followed by a cold restart of all three (R4-15);
- all three nodes restarted with a scheduled `gc`, under the object-closure
  oracle (R4-19).

**Fuzzing.** The frame decoder, the log record decoder, the snapshot metadata
decoder, the tail scan and the first consumer's object-channel framing
(R3-13) are fuzz targets. A crash or a hang is a failure,
and the corpus is checked in.

**A real store under faults.** Besides the disk model, a lane runs the real
segmented `LogStore` over a fault-injecting filesystem shim. The shim fails
`write`, `fdatasync`, `rename` and directory `fsync` at chosen points and
tears files at 4 KiB boundaries. The lane checks recovery against the same
oracles.

**Lanes (rule 12).** A short simulation (the constructed schedules and a
few hundred seeds) runs in `bazel test //...`. A long one
(`make test-consensus-soak`, tens of thousands of seeds) is a named lane,
declared in the package `CLAUDE.md` and run on a schedule in CI. A failure
prints the seed, and `CONSENSUS_SIM_SEED=<n>` replays it bit for bit. A real
three-process test over loopback mTLS with injected latency runs in the
normal suite. The heartbeat-latency test of D4 runs with a saturated data
pipe and a slowed disk. The `tc netem` reproduction of the three-site matrix
on three containers belongs to the first consumer's repository; IWFS's
chaos suite is its shape, with these oracles rather than IWFS's.

**No test sleeps.** Every timer of the step function and of the
simulator is on the injected clock, with ADR 0052's wall-clock AST audit
copied over and its own detecting negative test. The two real-time lanes
(R7-21) wait on conditions with deadlines, never on a fixed sleep.

**Confirmed by raft-sql-poc, in its own words.** Its tests are chosen
scenarios on real timers, with no seed to replay, and its README lists "no
deterministic simulation test nor linearizability checker" among what still
separates it from a product. What it did prove is the shape of the power-loss
test: cut power on every machine at once, right after the last
acknowledgement, and count. It got 300 of 300 acknowledged writes back in
simulation, and 622 of 622 over encrypted TCP to disk (`storage.js:601-615`).

**Benchmarks and budgets (R7-00c, ADR 0165).** The allocation budgets below
go into `design/sdk.yaml`'s `budgets:`, each naming its `fn:` (the step
function's `Step`, the record and frame codecs, `Node.Propose`) and its
fixture (R8-36). The three-node `Propose` row runs in the simulator under
`synctest`, over the in-memory doubles, and kit
measures them in the race-off lane (`tools/alloc-lane-targets.txt`, rule 12).
The absolute numbers of D16, `fdatasync` p99, commit p99 and entries per
second, are NEVER design budgets. They go into a hand-run `BENCH.md` (rule 9)
that `make bench` writes. That target runs only the codec's generator today,
and the implementing change extends it. Only benchstat deltas are compared
across changes.

| Benchmark | Budget |
|---|---|
| step function, leader handling one `AppendEntries` response | 0 allocs/op |
| follower handling `AppendEntries` of 64 entries, memory log | ≤ 1 alloc/op amortized (the entry slice), 0 per entry |
| encode / decode of a log record and a frame | 0 allocs/op |
| `Propose` → applied, 3 in-process nodes, memory log and transport | ≤ 4 allocs/op |
| segmented log, `Append` 64 × 256 B + one `Sync` | reported per device, as ADR 0056's `BENCH.md` reports publication |
| first consumer's table checksum, one update, at 10³ to 10⁶ repositories | constant time across sizes (R5-07) |

**The implementing change's verifiers (R7-00f).** Each acceptance criterion
of the change that builds this domain is checked by one of these, all run
without network: `make api-check`, `bash scripts/check-layer-deps.sh`,
`bash scripts/pre-commit/check-core-symmetry.sh`,
`bash scripts/pre-commit/check-error-codes-drift.sh`,
`bash scripts/pre-commit/check-domain-docs.sh`, `make guard`, `make test`,
`make test-alloc`, and a search of the diff for `package main`, which must
find nothing. No dedicated script exists for the last one.

### D16 — Performance targets: provisional, derived, then measured (R1-27)

Every number in this section is **provisional**. None is kept until two
measurements exist:
- the simulator's distributions for failover and commit latency, run with the
  RTT matrix;
- `fdatasync` p50 and p99 on each of the three VPS, measured under their real
  background load: Consul, Nomad and Vault running. An idle VPS measures a
  disk nobody will use.

The first draft assumed 1–3 ms. That assumption is withdrawn until measured.

**What the targets rest on (R7-22).**
- The RTTs of Context are MEANS. The p99 RTT of each pair is to be measured,
  and the targets below are hypotheses built on the means until then.
- `fdatasync` on the OVH VPS is durable only if the hypervisor honours the
  guest's FLUSH down to stable storage. That is NOT verified. It is to be
  confirmed with OVH, and until then a power loss of the host is a risk this
  ADR names rather than covers.
- A pre-release test cuts power on all three nodes at once, under load, and
  counts acknowledged writes, as raft-sql-poc did (D15). On VPS, "power"
  means a hard reset from the provider's console.
- A `Barrier` served from a follower includes that follower's RTT to the
  leader.
- A segment is 64 MiB (D4).
- **Backfill time per node** is measured on the real repositories, because
  it sets the tolerance-0 window of a replacement and of a planned reinstall
  (D17, R8-32).
- **Dispatcher throughput (R8-28).** Events are applied to SQL in batches,
  one SQL transaction per batch. The target is to keep up with the
  sustained commit rate above with a lag under 5 s at p99, provisional.

Commit latency from a leader is `max(fsync_leader, RTT_nearest + fsync_peer)`
plus processing:

| Leader | Proposal from | Expected quorum commit |
|---|---|---|
| FRA or RBX | the leader's node | ≈ 9.6 ms + fsync |
| FRA or RBX | the other of the pair | + 9.6 ms forward |
| FRA or RBX | WAW | + ~23–27 ms forward |
| WAW (avoided by D8) | anywhere | ≈ 22.8 ms + fsync, + forward |

**Three latencies are reported separately, never summed into one:**
- quorum commit: the proposer learns its entry committed;
- local apply: the ref table on the proposing node reflects it;
- projection: Git on a given node reflects it (D19).
A read-after-write on the first consumer waits on the third.

Provisional targets, to be confirmed or revised from the two measurements and
recorded in `BENCH.md`:

- p99 quorum commit from an FRA or RBX leader ≤ 2 × the nearest RTT + 2 ×
  the measured fsync p99, at 1 000 proposals/s;
- sustained ≥ 10 000 entries/s of 256 B committed with two `fdatasync` per
  batch, the end record after the rest ([#417](https://github.com/kitsunium/sdk/issues/417));
- failover (leader killed, then first commit under the new leader): **p99 ≤
  3.5 s, provisional**. The final number is derived from the simulator's
  distribution with the chosen timeouts, not chosen first. The first draft's
  2.5 s did not account for detection, PreVote, the vote round and the
  no-op commit on a 27 ms link;
- `Barrier` in ReadIndex mode from the leader ≈ one nearest RTT, and from a
  follower one RTT to the leader more (R7-22).
- **restart time** — process start to `Health` ready, on the real ref count —
  is a target to set once the table's size and the replay rate are measured
  (R4-18).

**A realistic workload, not 256-byte entries alone.** The benchmark replays a
Git consumer's shape:
- ref transactions of 1 to N refs;
- bursts of `refs/pull/*` updates;
- a few large pushes among many small ones;
- the projection running behind.

**A floor, from raft-sql-poc.** The POC ran in one Node.js process on macOS
with a real `F_FULLFSYNC` of 4.6 to 5.4 ms. It measured 7 000 to 9 400 durable
writes/s, replicated 3 times on 4 shards, and 5 300 to 5 700 over TCP with
AES-256-GCM to disk on 8 shards across 4 machines. That is evidence the
throughput target is reachable, not a target.

### D17 — Operations ship in v1, as Go functions the product calls (Q5, Q7, R2-36)

The owner's round-1 decision Q3 put every operating tool in v1. Round 2's
Q4–Q7 keep that scope and move the surface. **The SDK ships the operations as
Go functions, and nothing that faces an operator.** No admin handler, no HTTP
route, no CLI. The product that embeds the domain exposes them through its
own administration, with its own accounts, roles, authorization and audit.
For the first consumer, that is Forgejo's site administration online, and
these subcommands of the same `forgejo` binary, whose version is therefore
the node's by construction (R4-29, R5-12): `forgejo cluster bootstrap`,
`init`, `recover`, `restore`, `inspect` (with `--replay`, ADR 0173),
`snapshot save`, `reproject`, `quarantine clear`, `objects
verify|backfill` and `reconcile` (R6-09). On Nomad, each offline subcommand
runs as a batch job pinned to the slot's host volume, with the server's
allocation stopped (R7-34). **Every offline subcommand ALSO runs outside
Nomad (R8-18):** `docker run --network host -v <slot volume>` from the
pinned image, the allocation stopped. **That image is cached on each node by
digest, and also kept in the CI's zot and on the NAS (R9-20)**, never only
in the forge it serves, which may be the thing being recovered. The `Recover` and restore runbooks use
that path. **No runbook depends on Nomad's quorum**: the Nomad servers are
these same three VPS.

**Online administration stays web-only (Q14, R7-32).** There is no online
CLI. Forgejo's site administration is the only online surface. **The
residual risk is stated:** when the site administration is unavailable, no
online operation can be called, not even `TransferLeadership` or
`ClearNoSpace`. The cluster keeps committing ordinary proposals unless the
no-space alarm is raised, in which case it refuses them until the alarm is
cleared, which needs the administration (R8-33). The offline subcommands
still work on a stopped node. The break-glass local administrator of the
recovery runbook is the way back into the administration.

**Online operations** (D14), each taking the `Operator` the caller vouches
for. The SDK writes that identity into the membership or administrative entry
it proposes, so the product's audit and the log agree (R2-36):
- `AddLearner`, `Promote`, `Remove`, `UpdateAddress`, under the strict
  reconfiguration check of D8 (R2-27);
- `TransferLeadership`;
- `ProposeTimingContract` (D9) and `RaiseClusterVersion` (D18);
- `ClearNoSpace`;
- **Every clearing or raising operation names what it acts on (R4-06).**
  `ClearNoSpace` names the alarm, by the index that raised it, and so does
  `ClearDivergence` in ADR 0173.
  `ProposeTimingContract` and `RaiseClusterVersion` name the contract or
  version they expect to replace. `Apply` refuses a mismatch with
  `ADMIN_PRECONDITION_FAILED`, so a stale click in an administration page
  cannot clear an alarm raised since. **A precondition includes the
  `LogIdentity` (R5-09):** an alarm id, an expected contract generation or an
  expected cluster-version generation is meaningful only in its lineage. It
  is validated before proposing and again in `Apply`. Each operation has its
  mutation test (D15);
- `SaveSnapshot`, `Status`, `Health`, `Membership`, and the
  `LeadershipWatcher` sibling.
Every one of them can be submitted on ANY node: the engine forwards it to
the leader over the control connection, and its outcome follows D6 (Q8,
R3-03). Every call is logged at `Warn` with its `Operator` (R3-14).
`TransferLeadership` writes an entry, and `SaveSnapshot` takes an `Operator`
(R3-14). An `Operator` is an opaque identifier of bounded length, not a
name or an email address: the log is replicated and kept, and it must not
hold personal data (R3-24).

**Offline operations**, public functions over `DataDir` (R2-33), on a stopped
node, under the directory lock.

- **`Recover` after a lost majority (R2-25).**
  - It writes a forced configuration and a NEW `ClusterID`, and bumps the log
    epoch (R2-09), so no node of the old cluster can talk to the recovered one
    by mistake.
  - **It keeps every entry the node holds whose chain verifies, not only
    those up to the commit index it persisted ([issue
    #399](https://github.com/kitsunium/sdk/issues/399), amending the review of
    [PR #301](https://github.com/kitsunium/sdk/pull/301), L8).** The commit
    index is written lazily: a follower records a commit one round after its
    leader acknowledged it, and a batch caps the commit it carries. An entry
    past the persisted commit may therefore be a write the old cluster
    committed and acknowledged, and cutting it there lost that write. The old
    cluster is fenced, so the new history adopts the persisted log and then
    every entry past its commit whose `BodySum` and `ChainSum` verify from the
    chain at that commit, with a term that never decreases; it cuts only at
    the first broken link (a mismatch, or a record that no longer parses),
    and the configuration whose old voters must be fenced is the one of the
    last entry kept. The audit record is the report: `kept`, the persisted
    `commit` and how many entries past it were `adopted`, and the stamp's
    `KeptIndex` is the last entry kept. A token of the predecessor at or
    below it is applied only when its term is the one kept at its index:
    `WaitApplied` compares it, and answers `LINEAGE_MISMATCH` for a write
    the adopted suffix replaced or whose term the node can no longer read
    ([issue #509](https://github.com/kitsunium/sdk/issues/509)). **The owner's objective is zero loss
    of an acknowledged write, and the trade-off is stated:** an entry past the
    commit may also be one the old cluster never committed — its client was
    told `ENTRY_SUPERSEDED`, or never answered — and it is applied in the new
    history. That is a duplicate, never a loss, and an owner rejects it by
    precondition (forgejo-raft through `ExpectRev`). The runbook picks the
    survivor with the highest `(LastTerm, Last)` `Inspect` reports, compared
    in that order (amended 2026-10-22, [issue
    #534](https://github.com/kitsunium/sdk/issues/534)); a write no
    survivor received is one no choice of survivor can keep.
  - **An interrupted `Recover` never starts under the old cluster, and is
    completed by running it again ([issue
    #405](https://github.com/kitsunium/sdk/issues/405)).** `Recover` makes
    two durable writes: the forced configuration entry, then the new stamp.
    Between them the directory holds the NEW members' configuration under
    the OLD cluster's stamp, and a start there ran that configuration in the
    old cluster, beside voters fenced only for the new one. So the order is:
    the audit record; the stop marker `STOP-RECOVER`, written and synced,
    which names the new cluster and the last index kept; the forced entry;
    the new stamp; then the marker's removal and a directory sync.
    - While the marker is there, a start is refused (`STOP_MARKER_PRESENT`,
      as every stop marker is, D4), and its detail names the command to run:
      `Recover` again, to the cluster the marker names.
    - A `Recover` to another cluster is refused (`RECOVER_REFUSED`): the
      witnesses' receipts were signed for the first one.
    - A `Recover` to the same cluster, while the stamp is still the old
      cluster's, RESUMES: every precondition and every fence is checked
      again, against the configuration at the index the marker kept — never
      against the interrupted forced entry, which would read the new members
      as the old voters and ask for no fence — and the log is cut back to
      that index, the forced entry written again, then the stamp. The audit
      record says `resumed`.
    - A `Recover` to the same cluster once the new stamp is durable only
      removes the marker, confirmed and audited (`completed`); nothing else
      is decided again.
    - Inverting the two writes is not the answer: a new stamp over a log
      without its forced entry starts the new cluster on the OLD
      configuration, the one `Recover` was run to replace, and nothing on
      disk would say a `Recover` was left half done.
  - **The probe cannot detect a partition, and the text says so (R4-04).** A
    seed cut off from this node still runs, and can still serve. So
    `Recover` requires an operator attestation,
    `RecoverOptions.FencedAttestation`, with one entry per old voter: the
    node, and the method that FENCED it. **Only methods that stop the old
    majority from serving count (R5-11):**
    - stopped, or powered off;
    - cut by nftables from the two homes' access and the WireGuard access,
      AND from each other. There is no Traefik or Cloudflare edge in front
      of the management (R8-33).
    Certificate revocation is not a method: the TLS sibling ADR does not
    check revocation. It becomes one only if that ADR adds revocation
    checking. The attestation is written into the audit record.
  - **A fence must outlive a power cycle: the witness (Q17, R7-24, R8-20).**
    "Stopped" and "powered off" count only with a DURABLE fence, because two
    old voters that boot again are a majority of the old cluster.
    - **The authority is a witness on the NAS (Q17, Q19).** Both NAS
      (making.codes ADR 0010 and ADR 0015) hold a record of superseded
      lineages, and each answers with a SIGNED statement.
    - **What it blocks is Forgejo only (Q20, R9-11).** The check runs in the
      product before `OpenDataDir`, and as a prestart task of each of the
      three Forgejo groups. Nomad, Consul and Vault start normally. R8's
      unit before `nomad.service` is withdrawn.
    - **Both NAS are asked, and it fails CLOSED (R9-09).** The check refuses
      when ANY answering NAS says the lineage is superseded. It passes only
      when at least one NAS answers "clear" and none says otherwise. With no
      answer, it refuses. An operator may override it at boot, and the
      override is logged. The override secret is held in 1Password, like
      the LUKS recovery passphrase.
    - **Receipts are signed (R9-08).** A receipt is an Ed25519 signature by
      the NAS's witness key over (old cluster id, new cluster id, voter).
      `RecoverOptions.WitnessKeys` holds the two public keys, and `Recover`
      verifies every receipt with the standard library.
    - **Recover writes first, and needs both NAS (R9-09).** The superseded
      lineage is written to BOTH witnesses BEFORE the survivor serves.
      `RecoverOptions.FencedAttestation` carries, for each old voter, a
      receipt from each NAS, and `Recover` refuses with one missing
      (`RECOVER_REFUSED`).
    - **Who writes and who reads (Q19, R9-10).** Only the operator writes a
      superseded lineage, on the NAS, from the homes. The VPS only read,
      over WireGuard, through a dated, read-only exception of the network
      matrix, management to backup on the witness port (D20).
    - The Consul key `consensus/<old cluster id>/superseded` stays as a
      SECOND, non-authoritative signal, read by the product before it opens
      the data directory.
    - The consensus intermediate of `pki-consensus` is rotated whenever an
      old node could still reach Vault, so old leaves cannot renew.
    - Ansible writes a persistent stop marker (R7-44) into each old data
      directory it reaches.
    The old nodes are also cut from PostgreSQL (their `pg_hba` entries and
    role) and from Garage (their keys). **The test "old pair restarted after
    `Recover` is refused" runs with the old pair's Consul in MAJORITY**, so
    it proves the witness, not the Consul key.
  - **Consul ACL (R8-14).** Only the operator token may write
    `consensus/*/superseded`. The product's token reads it. Forgejo's Nomad
    token is read-only: `node:read`, and `read-job` on its own job.
  - The replacement machine is kept stopped until `Recover` has run.
  - It refuses while any seed of the old cluster still answers. A force
    option overrides that refusal, and the override is logged. **"Answers" is
    defined (R3-27):** when the old cluster's certificate still exists, a
    seed answers if a TLS handshake with it succeeds under the old cluster
    identity. When that certificate no longer exists, a seed answers if a TCP
    connection to its consensus port succeeds. The second test is coarser,
    and errs toward refusing, and the report says which test was used.
  - It requires a typed confirmation: the product asks the operator to type
    the new cluster id.
  - It writes an audit record into the data directory: who, when, from which
    position, and the new membership.
  - It records the old `LogIdentity` and the last index it kept, as the
    lineage of the new identity (R3-01).
  - It may name nodes already stamped as joining (by `Init`), so the
    recovered cluster can grow back to three voters (R3-02). **They enter as
    LEARNERS, and are promoted through the normal `Promote` catch-up, never
    as empty voters (R4-04).** `Init` for them takes the NEW `ClusterID`,
    chosen before `Recover` runs.
  - **Which node:** the one with the highest `(lastLogTerm, lastLogIndex)`,
    compared in that order, not "the highest committed index", which no
    surviving node can know for certain.
  - **What it costs, stated:** the chosen node's whole log tail becomes
    committed, entries no majority ever held included. Writes acknowledged
    by the old majority and held only on the lost nodes are lost.
  - **Runbook order (R3-04, R7-25, R7-31, R8-18, R8-33):** Vault, then
    Consul, then Nomad, each by its own lost-quorum procedure (the
    `peers.json` recovery of their Raft storage). Then the certificates,
    then PostgreSQL under Patroni, then `Recover`, then Forgejo, then the
    site administration. Each step needs the ones before it: certificates
    for new node IDs need Vault, and Forgejo's administration needs its
    database. When the database or the administration IdP is not back yet,
    a break-glass local administrator (`forgejo admin user create`) gets the
    operator into the site administration. `Recover` itself runs through
    the `docker run` path, never through Nomad.
- **`RestoreSnapshot` from a backup (R2-26, R3-15).**
  - It is run on EVERY node of the new cluster, with the identical snapshot,
    the same new `ClusterID` and the same member list. Every node gets a new
    `NodeID` and incarnation.
  - It takes `RestoreOptions`. The expected SHA-256 is required and a zero
    digest is refused. The round-2 "or a signature" is removed. It also takes
    the typed confirmation, the `Operator`, the encoded and decoded size
    bounds, and the codecs it accepts.
  - It writes the same audit record as `Recover`, refuses with
    `RESTORE_REFUSED`, and is tested without a running `Node`.
  - The restored snapshot's `Expected` digest, confirmed by the operator, is
    recorded in the audit record. **The operator reads it from the
    authenticated restic repository**, where the backup role stored it beside
    the snapshot, or checks it against a Vault transit signature, never from
    a copy that travelled outside (R7-37). A test restores three nodes, then
    cold-restarts all three (R4-15). The anchor-validation record that R4-15
    also wrote moves to ADR 0173 (Q12).
  - Backups are encrypted at rest; the product's backup path says how.
  - **The product's command (R3-26, R4-05).** For the first consumer,
    `forgejo cluster snapshot save` reaches the live server through the ADR
    0148 private socket, and nothing else (R5-15). It never goes through
    `/api/internal/manager`, and no function of this section is added to
    Forgejo's `private.Routes()`. A test asserts that `/api/internal` has no
    cluster route.
  - It writes the snapshot and its SHA-256 side by side. The
    making.codes backup role calls it before it backs up the repositories, as
    it already does for `consul`, `vault` and `nomad snapshot save`.
  - **Backup order: PostgreSQL first, then the ref snapshot, then the
    repositories and objects (R7-30, revises R3-26).** The gap left by the
    order is then always refs AHEAD of SQL, which reconciliation rebuilds.
    R3-26 took the database after the snapshot, which leaves SQL ahead of
    refs, the gap that cannot be rebuilt.
  - **A named PostgreSQL backup producer (R7-30).** `pg_dump`, or a base
    backup with WAL archiving, with a bounded window between it and the ref
    snapshot. The backup manifest records four components: the PostgreSQL
    LSN or dump time, the ref snapshot's position, the repository backup,
    and Garage's state.
  - The local backup file is NOT counted in the data directory's 5.3 GiB
    budget (D14, R4-28, R6-02). The operator guide says where it goes and how much it needs
    (R3-26).
  - For the first consumer, `SaveSnapshot` is taken BEFORE the Git
    repositories are backed up. The refs then name objects the repository
    backup is sure to hold. **The snapshot's object closure stays pinned
    until the repository backup is verified**, and a backup manifest records
    which snapshot pairs with which repository backup (R3-13).
  - **Reconciliation at restore is MANDATORY (R4-26, R5-08).** The SQL
    backup, the ref snapshot and the repository backup are never atomic
    together. Pausing repository creations, renames and deletions during
    the backup only narrows the gaps; it is optional. The restore runbook
    names a step for each mismatch:
    - refs ahead of SQL: a ref the table holds for a repository or pull
      request the database does not know;
    - SQL ahead of refs: the database names a ref or a merge the table lacks;
    - repository lifecycle: a repository created, renamed or deleted between
      the three backups.
    Vault comes first in the restore runbook, for the new node certificates.
  - **The tool (R6-09).** `forgejo cluster reconcile --dry-run|--apply`
    enumerates every mismatch between the table, SQL and the repositories,
    by category, and applies the named step for each. It is tested in a
    restore followed by a cold restart.
  - **What reconcile can and cannot rebuild (R7-30).** It classifies SQL in
    two. REGENERABLE rows, such as branches, are rebuilt from the table
    (`SyncRepoBranches`). NON-REGENERABLE rows, such as a pull request's
    merge state, `commit_status` and releases, are reported, and the tool
    outputs the pull requests to demote or to merge again.
  - **Git refs newer than the snapshot (R7-30).** A ref in the repository
    backup that is newer than the ref snapshot is not trusted: the table is
    the authority, the projector sets the ref back, and its objects are kept
    until gc decides on them.
  - **After every PostgreSQL promotion, reconcile runs by itself (Q15,
    R7-31)**, in `--apply` mode for regenerable rows and `--dry-run` for the
    rest, which it reports.
- **`Inspect`** reports a data directory read-only: its stamp and
  incarnation, segments, hard state, snapshot metadata, versions, quarantine
  marker (R2-01) and the first corruption found.

**Disk (R2-29).**
- The data directory's whole budget — `MaxLogBytes + 4 × MaxSnapshotBytes +
  LogReserveBytes` (D14, R4-28) — is what the product reserves.
- The no-space alarm is raised by the quota, and ALSO by the filesystem's
  free space (`statfs`) falling under the reserve. A full disk shared with Git
  must raise it as surely as the quota does.
- While the alarm is committed, the leader refuses ordinary proposals with
  `NO_SPACE`, and keeps admitting the alarm itself, `ClearNoSpace`,
  membership changes, `TransferLeadership`, and the control entries of D3
  (the check and digest kinds ADR 0173 uses), so the cluster can still be
  repaired (R5-12). Snapshots and
  compaction keep running.
- A FOLLOWER that enters its reserve forwards the alarm proposal to the leader,
  like any other proposal (R3-24). The node with the full disk is often not
  the leader.
- **Every backing filesystem is watched (R7-33).** The product declares
  each filesystem the node depends on: the data directory, the filesystem
  that holds the LUKS image, and the repositories. The alarm watches `statfs`
  on each one. The LUKS image is fully allocated (`fallocate` of the image
  file, which R7-22 does not forbid since it is not the log) and sized from a
  written budget that counts every co-tenant (D20, R8-31). The object channel refuses a
  push before any filesystem reaches its reserve. Quotas are optional:
  `statfs` and the reservations are sufficient, and the guide says so.

**Other operations.**
- **Poison entry.** A committed entry that crashes every `Apply` stops every
  node, by D3. The runbook: `Inspect` the entry, fix the state machine, deploy,
  restart. The log is never edited.
- **Certificates.** The transport reloads its identity through the TLS
  sibling of D4 (R2-20). The expiry and reload-failure metrics of D13 make an
  expiring certificate visible before it bites. **Lifetime and alerting
  (R3-27):** node certificates are issued for at least 30 days, and an alert
  fires when any has less than 7 days left. Renewal depends on Vault: a Vault
  outage longer than the remaining lifetime stops the transport, and with it
  the cluster. The operator guide states that coupling, and Vault's
  availability is part of the consensus's availability budget.
  **Issuance and renewal (R7-25).**
  - Certificates come from a dedicated `pki-consensus` mount with its own
    intermediate, through ONE Vault role and policy per slot, whose
    `allowed_uri_sans` is the slot's exact URI, never a glob (D20).
  - The certificate and its key are rendered into ONE file, replaced
    atomically, so a reload never pairs a new certificate with an old key.
  - Renewal starts at two thirds of the lifetime, with a per-node jitter. A
    test checks that the three nodes' `NotAfter` values differ.
  - **The renewer is named (R8-13).** It runs in process. It calls
    `pki-consensus/sign/<slot>` with a CSR made from the host key, so the key
    never leaves the host volume. It authenticates with the allocation's
    workload JWT, fetched again before the JWT's TTL ends. The role is set
    with `require_cn=false`, since the certificate carries a URI SAN only.
    **That setting is lab-gated (R9-21)**, like the JWT claims and the slot
    placement: it is proven in the lab on the Vault version deployed before
    v1.
  - A restart never needs Vault while the certificate is valid. Only a new
    key needs a new certificate, which happens when an allocation starts on
    an empty host volume (D19).
  - **A break-glass certificate per slot, over the PINNED key (R9-04).** At
    `Init`, the node writes a CSR from its host key. The certificate issued
    from that CSR is stored offline, and issued again at every replacement,
    since a new member has a new key. A certificate over another key would
    be refused by the pin. A test stops Vault, lets the certificate expire,
    installs the break-glass one, and the node is admitted.
- **Key compromise, executable with three voters (R2-36, R7-25, R8-12).**
  1. Fence the credential at once, through `DenyPin(ctx, op, id)` (R9-18).
     It is an authenticated, replicated membership entry that withdraws the
     member's SPKI pin (R8-07). When it applies, every node closes that
     member's connections and refuses its handshakes and any `JOIN` under
     that key. It is refused for the leader itself, and while the voters
     that answer, the target left out, are not a majority, and the member
     stays admitted until the entry commits: with one voter of three down,
     denying the other live voter cut the acknowledgement the entry needed
     ([issue #530](https://github.com/kitsunium/sdk/issues/530)). The slot's Vault auth and role are revoked, so the key cannot
     be renewed. **The old voter still counts in the quorum until it is
     removed**: it can no longer acknowledge, so `failure_tolerance` is 0.
  2. Add the replacement as a learner under a NEW `NodeID` and a new key,
     backfill its objects, promote it, then `Remove` the old ID. The window
     at tolerance 0 lasts from step 1 to the promotion.
  3. If no quorum is available for step 1, the alternative is the forced
     path of `Recover`, with the witness fence, and it is stated as such.
  A retired ID is refused forever (D8). The incarnation is an identifier, not
  a secret, and rotating it proves nothing.
- **Two replacement runbooks, both add before they remove (R7-27, R7-40).**
  - A DEAD node: once it is FENCED (D19), add a new `NodeID` as a learner,
    backfill its objects, verify them, promote, then remove the old one.
    Round 2's "remove it first" is withdrawn: it would go below `MinVoters`.
    **The gate, stated (R8-38).** The backfill happens while the new node is
    a LEARNER, so the majority is still 2 of 3 and the two live voters form
    it. The four-voter window lasts from the commit of `Promote` to the
    commit of `Remove`. In it the majority is 3 of 4, so every live voter
    must acknowledge each step. `consensus_failure_tolerance` is 0 from the
    death to the end of the window. That is the gate for a dead voter. The
    `failure_tolerance >= 1` gate of R7-40 applies to the LIVE replacement.
  - A LIVE node: add the new one as a learner, catch up, promote, transfer
    leadership away from the old one if it leads, remove the old one.
  - **A planned reinstall (R8-32).** making.codes ADR 0015 reinstalls a VPS
    to update its system. Before the drain, `forgejo cluster objects verify`
    runs on the two voters that stay, so they alone hold every object the
    table names. The node is then drained, reinstalled and replaced as a
    DEAD node. The window at tolerance 0 is stated: from the drain to the
    `Remove` commit. Its length is dominated by the backfill time per node,
    which D16 measures.
  `consensus.failure_tolerance` tells the operator, before each step, how many
  voters can still fail. **Before `Promote` and before `Remove`, the procedure
  verifies durable OBJECT coverage on the prospective membership (R3-13):** a
  majority of the voters that would remain must hold every object the table
  references. Otherwise the change could leave committed refs whose objects
  exist on a minority only. The first consumer runs it with
  `forgejo cluster objects verify` over every repository, and fills any gap
  with `forgejo cluster objects backfill` (R5-12).

### D18 — Versioning (R1-22)

Every persisted or transmitted structure carries a version (R1-22, R2-16):
- each frame;
- each log record;
- `SnapshotMeta` and the snapshot format;
- the state machine's payload, through a versioned proposal envelope:
  `Entry.Version` is set by the proposer and checked before append.

The rules:
- **Handshake.** Each side announces the protocol range it speaks. The
  connection uses the highest common version, or is refused.
- **The frame header is frozen (R8-36, R9-07).** `length (u32) | version
  (u8) | type (u8) | group (u64)`, little-endian, the same in every protocol
  version. The two high bits of `type` are the lane: `00` control, `01`
  data, `10` snapshot, `11` reserved. `Lane` reads them, so a transport
  routes a frame of a type or a version it does not parse.
- **Payload versions are announced and replicated (R2-16, R3-11).** Every
  member — voters, learners and joining nodes — announces the range of payload
  versions its state machine supports, and the snapshot codecs it can read.
  The ranges are stored in the replicated membership, so a leader elected
  later knows them without asking. A member whose range is unknown counts as
  supporting only the lowest version. The proposal envelope is versioned and
  has a public input path, so a product can set the version it proposes. A
  node exposes its local capabilities through `Status`. Compatibility of
  persisted data — log records and snapshots written by older versions — is
  tracked SEPARATELY from the negotiated protocol floor (R6-12). Raising the
  floor changes what nodes speak. It does not retire a decoder. A storage
  decoder is retired only after a crash-safe migration has rewritten every
  retained log record and snapshot that needed it. A test raises the floor,
  then restarts a node on old storage. The leader refuses a proposal whose version is above the
  minimum announced, with `PAYLOAD_VERSION_REFUSED`, before anything is
  appended, so the refusal is retryable once every member is upgraded.
- **Cluster version.** A committed cluster-version entry records the protocol
  version every member must speak. **Raising it is an explicit operation,
  `RaiseClusterVersion`, never automatic (R2-16).** The point of no return is
  the operator's decision, through the product's administration, not
  something the leader does on its own because every voter happens to support
  it. The operation is refused while any member does not. Protocol version
  1 has no member announcing more than 1, so the engine refuses any raise
  above its own `ProtocolMax`, and a node that finds such a raise committed
  stops (review of [PR #301](https://github.com/kitsunium/sdk/pull/301), M3). Members announce their range in the
  version that first raises it. Protocol 2 is that version: see the amendment of 2026-10-23.
- **Timing contract.** It has its own committed entry and its own version (D9,
  R2-14).
- **Unknown means stop.** An entry kind, record version, frame version or
  snapshot version a node does not know stops that node with
  `VERSION_UNSUPPORTED`. It is never skipped: skipping an entry is a fork.
  A follower appends and acknowledges the entries before an unknown kind,
  never the unknown one, and stops; an unknown kind already in the log
  stops the node when it reaches apply (review of [PR #301](https://github.com/kitsunium/sdk/pull/301), M3).
- **Apply version gate.** An entry whose `Version` is above what the local
  state machine supports stops the node the same way, before `Apply` is
  called. The entry's version SELECTS the frozen code path that applies it.
  The `Apply` fingerprint, the gate entry and the replay gate are in ADR 0173
  (Q12).
- **A new payload version waits for the floor (R7-36).** A proposal of
  payload version 2 is admitted only after the protocol floor, the committed
  cluster version, has risen to a version every member announced it
  supports. The downgrade rule below is the same rule read backwards.
- **Downgrade.** A node may run an older binary only while the committed
  cluster version is within its range. Raising the cluster version is the
  point of no return, and the operator guide says so.
- **Compression.** The snapshot codec id is recorded, so a snapshot written
  with a codec a node does not know is refused cleanly by that node. v1
  ships flate and gzip only (R7-06).

### D19 — The first consumer's contract: Forgejo, in process (outside the SDK, recorded here) (Q6, R2-35)

The SDK does not implement the replicator. The replicator — the consensus
engine, the ref table and the Git projector — runs INSIDE the Forgejo
process, embedding `pkg/v1/app/consensus` (Q6). These rules bind it, because they
came out of the challenge of this ADR, and they decide whether the SDK is
being used soundly.

- **A replicated ref table (Q1, R1-01).** The state machine is a ref table:
  `(repository, ref) → (SHA, revision)`, with tombstones for deleted refs.
  **`HEAD` and every symbolic ref are in the table too (R5-06)**, as
  `(repository, ref) → (target ref name, revision)`. They are versioned with
  the payload, and carried in snapshots. The projector writes them like any
  other ref. No other authority decides a repository's default branch.
  `Apply` is pure: it validates and updates the table, nothing else.
- **The table is held in memory (Q9, R3-05, R3-29).** It is never in
  Forgejo's shared SQL database: a shared database would give three replicas
  one copy, and its own failover would decide what the log already decided.
  And it has no mutable store on disk at all. At start it is rebuilt from the
  latest snapshot whose checksums pass, and the log after it (R5-01). On disk
  there are only the log and the snapshots, both immutable and checksummed.
  - The first consumer declares `Durability = false`. The durable applied
    position (R1-02, R2-09) and the lineage of applied positions (R3-01) are
    therefore not used by it. They stay in the SDK for consumers whose state
    is durable.
  - The table serves a point-in-time read for snapshots, as D3 requires.
  - Its RAM and the start-up replay count in D14's budget, and are to be
    measured.
- **The table's checksum (R3-28 point 1, R5-07).** It stays in ADR 0152,
  because the projection verifier below uses it. The consensus digest check
  built on it is ADR 0173's.
  - A **leaf** is the SHA-256 of a fixed, length-prefixed, big-endian
    encoding. **The encoding is versioned and discriminated (R6-11):** a
    direct ref carries the object format and the binary SHA; a symbolic ref
    carries its target ref name; a tombstone carries neither. All three
    carry the hash algorithm's version, the repository, the ref and the
    revision. Nothing local to the node enters it.
  - A **repository's hash** is the sum of its leaves modulo 2^256, so an
    update costs one subtraction and one addition.
  - **The root is maintained incrementally (R5-07, R6-11).** It is the sum
    modulo 2^256 of `SHA-256(len(repository id) ‖ repository id ‖ repository
    hash)` over every repository. The repository id is length-prefixed, so
    no two ids can collide by concatenation. A repository without refs
    contributes nothing. The sums are computed on four 64-bit limbs, least
    significant first, each encoded big-endian, pinned by known-answer
    vectors. The caveat stands: a sum of hashes resists accidental
    divergence, not a chosen-input adversary, which the authenticated log
    keeps out. An update changes one term: O(1), whatever the number of
    repositories. The round-3 root, a hash over the sorted list of
    repository hashes, cost O(n) per update. A benchmark against the
    repository count pins the claim (D15).
  - A mismatch names the repository.
- **The projector is the ONLY authority over Git (R2-35).**
  - Each node runs a projector that makes the local repositories match the
    table.
  - The write git itself performs after the `reference-transaction` hook
    returns is a **pre-write**. The projector reconciles it: if the table
    agrees, nothing is left to do; if it does not, the projector sets the ref
    back to the table's value.
  - **Projection comes from STATE, not from replay.** For every repository
    with pending changes, the projector sets each ref to the table's
    `(SHA, revision)`, and deletes refs whose table entry is a tombstone. It
    writes the repository's durable cursor only AFTER the refs. Projecting
    twice is harmless: the operation is idempotent.
  - **Per-repository watermark.** It means "every entry up to applied index N
    that touches this repository is projected". When nothing is pending for a
    repository, its watermark advances to the applied index. A
    read-after-write waits on this watermark, not on `WaitApplied`.
  - **It also waits for the SQL cursor (R8-24).** A page that reads SQL
    derived from the write, a branch list or a pull request, waits until the
    dispatcher's cursor has passed the write's position. When that wait
    times out, the answer says the page may be behind, and never serves it
    as current.
  - The projector retries when another git process holds a ref lock.
  - **Certifying a watermark is serialised with git's own transactions
    (R3-12).** A native git transaction can publish a ref after the projector
    has read it, including on the path where the ref already matched, which is
    an ABA. The projector tracks native publications in flight, waits for
    them to complete, and reconciles them before it certifies a repository's
    watermark.
  - **How native git transactions are tracked (R4-24, R5-04).**
    - *One identity per transaction (R6-06).* A 128-bit nonce from
      `crypto/rand`, bound to `(repository, refs)`, is set in the
      environment by **Forgejo's git launch wrapper on EVERY git
      invocation** — pushes, merges, the API, pull requests and AGit
      included — and never by the hook. Every phase of the hook reads it, so
      `prepared`, `committed` and `aborted` of one git transaction share it.
      `prepared` refuses a transaction that carries no nonce.
    - *The owning git process is identified (R6-06)* by walking up from the
      hook's parent to the git process, and is supervised with
      `pidfd_open`, independently of the hook's connections, which come and
      go between phases. When no nonce correlates, a fallback correlates on
      `(repository, refs, old and new values)` plus the pidfd.
    - *Registration is atomic with certification.* The hook registers in
      `prepared`. The projector certifies a watermark under the same lock.
    - *Bounded lifetime (R6-06).* A registration lives at most git's
      ref-lock timeout plus a margin. When it expires, certification of
      that repository stays FENCED until the transaction has completed, or
      its process is confirmed terminated and unable to publish. Only then
      is the repository reconciled from the table.
    - *Caps.* Registrations are capped per repository and in total. Above
      the cap, the hook refuses the transaction before git publishes
      anything.
    - *Explicit cleanup.* When the hook itself rejects a transaction, it
      deregisters it before answering git.
    - *Restart fencing* is kept: after a restart, no watermark is certified
      until every repository has been reconciled once.
    - *Reconciliation:* the ref is set to the table's value (R2-35).
    - *Tests:* git killed between `prepared` and `committed`; a hook
      rejection; a registration that expires; the replicator restarted with
      transactions in flight.
  - The projector's own git writes bypass the replication hook, so it never
    proposes what it is projecting.
  - After log compaction, the projector rebuilds from the table, never from
    the log. A node far behind therefore needs only the table.
  - **Paused during the start-up replay (R4-19, R5-03).** The projector and
    git's garbage collection stay FENCED until the node has applied a commit
    boundary CONFIRMED BY A QUORUM after the restart (a `Barrier` that
    succeeds). The commit index persisted at shutdown is only a lower bound:
    entries may have committed elsewhere while the node was down. Until that
    barrier, durable object pins are kept for everything a pending or
    uncertain entry could name. A repository whose cursor is AHEAD of the
    table waits; the projector never "reconciles" it backwards. A simulator
    case crashes all three nodes at once with a scheduled `gc`, under an
    oracle that checks the object closure.
- **The hook proposes in `prepared`.** The `preparing` reservation of the
  design document is removed (A10): nothing is ordered before the entry
  itself.
- **A whole Git transaction is one entry (R1-03).** A ref transaction touching
  several refs is ONE entry, and `Apply` validates every expected value before
  it changes any.
- **Revisions against ABA (R1-04).** Each ref carries a revision that only
  grows. A proposal states the expected SHA and the expected revision. After
  `OUTCOME_UNKNOWN`, the replicator reads the table and never retries blindly.
- **The Git projection has its own digest, outside the consensus hash (R3-28
  point 8).**
  - **The projection has its own digest (R4-25).** It hashes the observable
    Git state: the refs listed by `for-each-ref` and `HEAD`. A deleted ref is
    its absence, never a tombstone. `HEAD` and every symbolic ref are
    represented by their target name, with a defined encoding. It is
    compared with a projection of the table's digest computed under the SAME
    rules — tombstones dropped, symbolic refs as names — never with the
    consensus digest directly.
  - A local checker computes it under the projector's lock.
  - A difference triggers an automatic reprojection. An operator can force
    one with `forgejo cluster reproject <repo>`.
  - **Every transfer of objects between nodes uses the object channel
    (R6-08)**, in both directions: fetching a missing object, a backfill, a
    restored node getting ready. It runs on the node's management service
    address (D20, R7-28), with the same admission,
    path check, quarantine, `git index-pack --strict` and budget. Never git
    over a public endpoint with a cluster-wide credential. A test asserts
    that the projector has no HTTP or SSH git remote.
  - The projector owns garbage collection: `gc.auto=0`,
    `maintenance.auto=false` and `receive.autogc=false`, so no git process
    prunes behind its back.
  - Each repository keeps a projection stamp (lineage, watermark, checksum).
    A disk restored from backup carries a stale stamp, which forces a
    reprojection.
- **Objects before refs (R1-05, R2-35).**
  - **Object coverage is bound to a configuration (R4-23).** A durability
    receipt for a push names the configuration under which its ref was
    admitted. A reconfiguration is serialised with pending pushes. A push
    still pending when a reconfiguration is proposed has its object closure
    revalidated on the prospective membership before that change commits.
  - The objects a ref update needs are written and `fsync`ed on a majority
    before the ref entry is proposed.
  - **A pending push is retained durably (R3-13).** Its objects are recorded
    as retained BEFORE receipt is acknowledged. They stay retained through an
    `OUTCOME_UNKNOWN`, and are released only on a definite rejection or once
    authoritative retention covers them.
  - **The object channel is admitted like the log (R3-13).** It uses the same
    admission and retired-ID checks, and the sender stamped from TLS. It
    checks the repository path lexically, and refuses a repository unknown to
    Forgejo's database BEFORE touching the disk. It receives into a
    quarantine directory, runs `git index-pack --strict`, and only then
    migrates the objects. Its per-peer byte budget counts against the
    `NO_SPACE` reserve, and its framing is a fuzz target (D15).
  - Retention: `git gc` must keep the reachable closure of the
    AUTHORITATIVE refs — the table's, not the repository's — plus every
    object that committed-but-unprojected work needs.
  - Reads of a repository are gated on object readiness: a node restored from
    a snapshot, or behind on objects, does not serve a repository until every
    object its table refs name is present and verified.
- **Input validation (R1-06, R2-35).**
  - `Apply` refuses, deterministically, a repository path that escapes the
    repository root. Paths are normalised lexically only, never by asking the
    filesystem, which may differ between nodes.
  - Ref names are validated by a pure Go implementation of
    `git check-ref-format`'s rules, pinned to the payload version (D18). A
    `git` binary of another version on another node therefore cannot change
    the verdict.
  - **Directory and file conflicts are refused (R7-35).** `Apply` refuses,
    deterministically, a ref whose name is a directory prefix of an existing
    ref (`refs/heads/a` when `refs/heads/a/b` exists), or the reverse, as git
    does. Tombstones do not count as existing. Otherwise the table would hold
    a pair that no node's projection can write.
  - The proposal endpoint is in process. The hook reaches it over a `0600`
    Unix socket owned by the Git user (ADR 0148).
  - **The SSH path (R7-37).** `forgejo serv` registers the transaction's
    nonce over the same ADR 0148 socket before it runs git. The nonce is a
    correlation id, not an authenticator: the socket's peer credentials are
    the authentication. It is set LAST in git's environment, so nothing a
    user controls overrides it. A test disconnects the client while the
    transaction is `prepared`, and the registration must expire as the
    bounded-lifetime rule says.
- **`DISABLE_GIT_HOOKS = true` is mandatory and checked at start (R2-35).** A
  user-supplied server-side hook would let a repository owner run code inside
  the replication path. The node refuses to start the replicator without it.
- **Administration is Forgejo's (Q5, Q7, Q8).** The site administration calls
  the online functions of D17 with the administrator as `Operator`, on
  whichever node the load balancer chose (R3-03). **What Forgejo must add
  (R7-27):**
  - a cluster-operator permission, distinct from site administrator.
  - no REST route and no API-token path to any D17 operation.
  - a step-up authentication through the administration IdP of making.codes
    ADR 0014 (Authentik, WebAuthn) before each D17 operation, implemented
    and tested in Forgejo (R8-33).
  - the `Operator` taken from the authenticated principal BEFORE any API
    sudo is applied, so an impersonated user is never the recorded operator. The subcommands of the
  `forgejo` binary are every one D17 lists (R5-12): `cluster bootstrap
  --members`, `cluster init --cluster --node-id` (which prints the
  incarnation), `recover`, `restore`, `inspect`, `snapshot save`,
  `reproject`, `quarantine clear`, `objects verify|backfill` and `reconcile`
  (R6-09). The Forgejo server never calls `Bootstrap`.
- **No internal route reaches the engine (R3-14).** `Propose` and every
  function of D17 are never exposed under Forgejo's `/api/internal`. The hook
  reaches the replicator only through the ADR 0148 socket. Traefik denies
  `/api/internal` at the edge. A destructive operation in the site
  administration requires a site administrator who has just re-authenticated.
- **Exposure (R2-37, R3-24, R7-28).** The consensus and object listeners
  bind to the node's management service address, the inventory's
  `wireguard_ip`. That address follows the `wg0` to `wg2` migration of the
  networks by tier, and the listeners follow it. A dedicated nftables input
  rule accepts the consensus port and the object port ONLY from the members'
  addresses. An address migration is an `UpdateAddress` per node, one node at
  a time, with the nftables rule widened to both addresses for its duration
  (runbook in D20). The settings this names are listed in D20.
- **Memory (R3-24, R5-15).** Forgejo runs with `GOMEMLIMIT` leaving the
  replicator the headroom D14 computes from the measured table. `GOMEMLIMIT` is soft (R4-18).
- **Resource limits and stop (R7-37).**
  - Forgejo has a hard memory limit of its own: `MemoryHigh` and
    `MemoryMax` under systemd, `memory` and `memory_max` under Nomad.
    Consul and Vault get a negative `OOMScoreAdjust`, so the kernel's OOM
    killer takes Forgejo before the control plane.
  - The backup's scratch space is not on a tmpfs.
  - The stop timeout (`TimeoutStopSec`, or Nomad's `kill_timeout`) is at
    least a leadership transfer plus Forgejo's `GRACEFUL_HAMMER_TIME`, so
    `Close` can transfer before it is killed.
  - The unit that runs Forgejo, or the Nomad client, carries
    `RequiresMountsFor=/opt/secure`, the LUKS mount.
- **Pins expire (R7-37).** Every object pin, a backup's or a push's, has a
  TTL. A pin past its TTL raises an alarm (D13), and is never released
  silently.
- **Ready means ready (R4-18).** Forgejo's health endpoint reports ready only
  when `Health` is ready: the table is rebuilt, the projector has caught up
  and the objects are present. The load balancer routes nothing to a node
  before that.
- **Outbox identities across a lineage (R4-29, R5-10).** Items that survive a
  `Recover` keep their original identity. Only new commands carry the new
  lineage (D10). The guide tells receivers to expect both.
- **Two upgrade procedures (R3-25).**
  - A Forgejo release WITHOUT a schema migration is rolled: one node at a
    time, leadership transferred away first.
  - A release WITH a schema migration stops all three nodes, upgrades them,
    and restarts them. The consensus survives a full stop, because its LOG is
    durable. The table is rebuilt from the log (R4-29). **The migration runs
    once (R7-36):** `AUTO_MIGRATION=false` on all three, then `forgejo
    migrate` once, on one node, against PostgreSQL. It needs no quorum.
    Whether any Forgejo migration writes refs is NOT verified. If one does,
    it must go through the hook path like any other ref write, and the
    release that carries it is flagged in the upgrade runbook.
  - **Rolling uses a read-only status endpoint (R7-37, R7-45).** Each step
    waits on the node's gate of the Nomad section below, never on a
    write.
  - **On the full-stop path, ADR 0152 relies on the payload-version gate of
    D18 and on gate entries (R4-20, R6-04).** A new release's new semantics
    start only at a gate entry, and a proposal above any member's announced
    payload version is refused before append. `forgejo cluster inspect
    --replay`, a mandatory replay before voting, is an ADDITION of ADR 0173,
    not part of this ADR's runbook. "One voter at a time" belongs to the
    rolling path only.
  - In both cases `RaiseClusterVersion` is called only once all three nodes
    run the new release (D18).
- **Snapshot backup (R3-26, R4-05, R7-30).** `forgejo cluster snapshot save`
  writes the ref snapshot and its SHA-256 side by side, through the ADR 0148
  socket, never `/api/internal/manager`. The Ansible backup role runs the
  PostgreSQL backup first, then this, then the repository backup (D17).
- **SQL is derived from the log, by a log consumer (R7-29).** Forgejo's
  post-receive work (branch rows, push actions, webhooks, pull request
  updates) is NOT driven by the node-local hooks, and the node-local leveldb
  `push_update` queue is not relied on.
  - `Apply` records, in the table, a small deterministic event per ref
    update: repository, ref, old and new SHA, revision, pusher id, and the
    time carried IN the entry, never read from a clock.
  - ONE dispatcher, on the CURRENT leader, runs the post-receive work for
    each event, idempotently, keyed by its log position. On gaining
    leadership it rebuilds its delivery queue from the table's events past
    the cursor (R8-25).
  - **It waits for the leader's projection (R8-27).** The dispatcher
    handles an event only once the leader's watermark for that repository
    has reached it, so the SQL it writes never names a ref Git lacks.
  - **Events are batched (R8-28):** many events per SQL transaction.
  - **The cursor is (lineage, position) (R8-29)**, committed in the SAME SQL
    transaction as the work it covers, as a compare-and-set on the previous
    value (R8-25). At a lineage change, after `Recover` or a restore, the
    cursor of the old lineage is closed, reconcile runs, and the cursor
    restarts at the new lineage's first position. **Before the switch, the
    retained predecessor events are drained from their saved cursor
    (R9-19)**, under their original identities. A test runs `Recover` with a
    predecessor event not yet dispatched, and exactly one SQL task results.
  - A webhook's `hook_task.uuid` is derived from (lineage, position, hook
    id), so a replay after a failover creates no second task. **This needs a
    Forgejo patch**, since Forgejo draws that uuid at random today (R8-25).
  - Branch rows carry the log revision, and an update applies only when its
    revision is newer.
  - **Event lifecycle (R8-23).** An event stays in the table until an ACK
    entry trims it. The dispatcher proposes that entry with the cursor it
    READ BACK from SQL after the commit is durable on the synchronous
    standby. Trimming lags that cursor by a configured margin, and never
    passes the cursor of the last backup. **The PostgreSQL backup records its
    cursor in a row the replicator reads (R9-14).** When that backup lags
    beyond a bound, trimming proceeds anyway. The manifest then marks the
    backup "not replayable", and restoring it requires reconcile in its
    non-regenerable mode. The alert fires on that lag bound, before the queue
    cap is near. The queue is capped in bytes,
    counted in D14's memory budget. At the cap, pushes are refused before
    their ref entry is proposed, and a metric and an alert say so.
  - **Unconsumed events pin their objects (R8-27).** The old and new SHAs of
    every event not yet trimmed are pinned against gc.
  - An alert fires when the dispatcher's lag passes its target (D16, R8-28).
  - **Which post-receive step runs where (R9-15).**

    | Step | Where |
    |---|---|
    | permission and protected-branch checks, push options parsing, AGit's pull request creation and the URL it prints | synchronous, at the hook, before the ref entry is proposed |
    | branch rows, push actions, webhooks, CI triggers, pull request updates and their merge checks | the log consumer |

    When a step runs in the consumer, the event carries what it needs: the
    push options, the trigger, and the pull request id. AGit's pull request
    URL is printed at the hook from the id it reserved there. Forgejo's
    `Merge()` waits for the cursor of its OWN event before it answers.
  - **Reconcile waits for projection (R8-26).** `reconcile --apply` acts on a
    repository only once its watermark has reached the table. Otherwise the
    repository is deferred to the next pass.
- **Forgejo's database and ADR 0014's (Q18, R8-30, R9-16).** making.codes
  ADR 0014 proposes PostgreSQL 18 under Patroni on the same three nodes, for
  Authentik. By the owner's decision Q18, Forgejo uses the SAME Patroni
  cluster, with its own database and role. Its memory counts in the same
  per-node budget, beside ADR 0014's 1.2 to 2 GiB estimate.
  - Forgejo's backups are per database: `pg_dump -d forgejo`. A
    cluster-wide restore is a separate disaster case, because it rewinds
    Authentik too.
  - Each role has a `CONNECTION LIMIT`, Forgejo's migrations set a
    `lock_timeout`, and the shared connection budget is written in D20.
- **PostgreSQL under Patroni on Consul is a prerequisite (Q15, R7-31).**
  Failover is automatic and picks the replica with the highest LSN. The
  primary may move to FRA or RBX. The guide gives the matrix of a
  PostgreSQL failover against a consensus leader change, and its effect on
  the consumer's cursor: a promoted replica that lost the last commits
  re-runs them from its cursor, idempotently. `forgejo cluster reconcile`
  runs after every promotion (D17).
- **Partial availability, stated (R7-31).**
  - PostgreSQL down, quorum up: a push is refused cleanly BEFORE its ref
    entry is proposed. No push answers 500 after its ref committed.
  - Quorum lost, PostgreSQL up: every SQL write that depends on a ref is
    refused, and reads stay served.
- **The replicator starts without PostgreSQL (Q14, R7-32).** The consensus
  engine, the table and the projector start and stay up independently of
  Forgejo's database initialisation. Forgejo's start-up path that ends the
  process when the database is unreachable is not on the replicator's path.
  The dispatcher of R7-29 waits for the database, and the consensus does
  not.

#### Deployment on Nomad (Q13, Q16, R7-34, R7-39 to R7-43, R7-45, R8-08, R8-11, R8-15 to R8-19)

The owner's decision Q13 runs Forgejo as ONE Nomad job across the three VPS.
R7-39 to R7-43 hardened it, and R8 corrects what did not hold on the real
Nomad and Vault.

- **One job, three task groups, each pinned to a node (R7-41, R8-08).** The
  three VPS are ONE Nomad datacenter, `mngt-dc1`, and a client declares its
  own datacenter, so the datacenter is never used for identity or
  placement. Each group is pinned on `${node.unique.id}` of its VPS, has its
  own `host_volume`, and runs a distinctly named task: `forgejo-waw`,
  `forgejo-fra`, `forgejo-rbx`. Each group is a SLOT.
- **Nomad never moves a member by itself (R7-40, R8-15).**
  - `reschedule { attempts = 0, unlimited = false }`.
  - `disconnect { replace = false }`. `stop_on_client_after` is NOT set.
    The Nomad servers are these same three VPS, so a server outage the
    runbooks allow, a full stop for an upgrade, would otherwise stop
    Forgejo on every client. Without it, a disconnected client keeps its
    allocation running, and the consensus decides, not Nomad. The R7-39
    silence delay is therefore measured by the consensus leader alone.
  - An alert fires when a slot stays without a running allocation longer
    than that delay.
- **A slot on a replacement VPS (R8-16).** With `replace = false`, Nomad
  places nothing by itself. The runbook purges the old node
  (`nomad node purge`), updates the group's `${node.unique.id}` pin to the
  new node, and submits the job. The group is then placed on the new node.
  This step is proven in a lab before v1.
- **Keys and certificates (R7-41, R8-08).** The private key is generated on
  the host, inside the host volume, and never enters Nomad Variables. Only
  the certificate is issued, by the slot's Vault role, through a separate
  JWT auth mount `jwt-consensus`. Each slot's role binds the default
  workload-identity claims `nomad_namespace`, `nomad_job_id` and
  `nomad_task` exactly, for example `forgejo` and `forgejo-waw` (R9-13). Each
  role sets its own `user_claim`, so the three slots never share a Vault
  entity alias. The datacenter claim is never bound.
  D20 adds the matching check to `scripts/workload-jobs.test.sh` and a lab
  replay of the two defects of making.codes ADR 0005's revision of
  2026-10-01.
- **"Empty" is not "new" (R7-39, R8-34).** An allocation initialises a fresh
  node ONLY when all three hold:
  - a provisioning marker exists on the REAL mount, holding a value Ansible
    wrote: the LUKS volume's UUID and its creation time, checked against the
    UUID in the LUKS header.
  - `/opt/secure` is a mountpoint, so the LUKS volume is open.
  - the leader reports the slot's old ID silent for at least N minutes.
  The marker is necessary, not sufficient: it proves the volume is the one
  Ansible prepared, not that its previous member is fenced. Otherwise the
  node refuses, reports `Health` unready, and alerts. The Nomad client
  starts after the LUKS mount.
- **The join path only joins (R7-42, R7-43).** The new node runs `Init`,
  then sends the `JOIN` of D4 to any member. No member answering means wait
  and report unready, never `Bootstrap`. `Bootstrap` stays the explicit
  two-phase step of D8, run as a one-shot batch job. `Recover` is never
  reached from automation.
- **The fence decision is Forgejo's (R8-09).** Forgejo's `JoinAuthorizer`
  reads Nomad, with its read-only token (D17), to decide that the slot's
  previous member is fenced: silent for the stated time, AND drained or
  purged from Nomad, or attested by an operator. "Rescheduled" is never a
  fence.
- **Replacing a member is ordered, and one at a time (R7-40, R8-11).**
  - Order: `AddLearner` new, backfill objects, verify them, `Promote`,
    `Remove` old (D17, R8-38).
  - One replacement at a time, cluster-wide, held by a replicated marker.
    **The marker has an owner and an exit (R8-11):** it names the learner
    it was taken for, and it expires when that learner is fenced. A dead
    learner is removed by automation FIRST, before a new one is added.
  - Never forced. A strict-reconfiguration refusal is surfaced, never
    retried around.
  - A slot that still has a live member and an empty data directory raises
    the `WipeAndRejoin` alarm, and never rejoins silently.
- **A returning allocation with a retired ID (R8-11)** wipes its directory
  only under the R7-39 conditions AND a quorum-confirmed answer that its ID
  is retired. It RENAMES the directory to a dated name, kept N days, and
  never deletes it. It then rejoins as a new node.
- **Rolling update, one job, site by site (Q16, R8-19, R9-12).**
  - **No generic submitter touches this job.** The job carries `meta {
    rollout = "script" }`, and every generic submitter, the management's
    `pull` role and the NAS executor, refuses a job that carries it. The
    jobs test checks the refusal.
  - A rollout script changes the image of ONE group. It runs `nomad job
    plan`, and refuses unless exactly one group shows a destructive update
    and the others none. It submits with `-check-index` from that plan, then
    waits for that slot's Consul gate: caught up, promoted, and
    `failure_tolerance >= 1`. Then it moves to the next site.
  - Nomad restarts only the group whose task changed.
  - Each group's `update` block has `max_parallel = 1`. `auto_revert` is OFF
    for any release that raises the payload version, and on for the others.
  - **Nomad does not order groups**, so the script is the guarantee, not
    the job file. The script, its plan check included, is proven in a lab
    before v1.
  - The gate is read-only and is the R7-45 one: the node reports `Health`
    ready, the cluster's `failure_tolerance` is at least 1, and this node
    acknowledges the leader. A learner is never ready.
- **Stop timeout (R8-17).** The job's `kill_timeout` covers a transfer plus
  `GRACEFUL_HAMMER_TIME` (R7-37). The clients' `max_kill_timeout` is raised
  in `nomad.hcl.j2` to at least that, and a test asserts `kill_timeout <=
  max_kill_timeout`.
- **Placement of the job (R7-46, R8-31).** Forgejo runs on the management VPS
  in network mode `host`, bound to `wireguard_ip`. That needs making.codes
  ADR 0006 amended, since its management domain holds no applicative
  service. making.codes ADR 0015 already places "Forgejo and its
  equivalents" on the management, but its P5 (an OS update is a reinstall)
  and its sentence on a single Forgejo instance must be amended beside ADR
  0006.

### D20 — Infrastructure prerequisites, in making.codes (R7-25, R7-28, R7-31, R7-34, R7-39, R7-41, R7-46, R8-08, R8-14, R8-17, R8-20, R8-31, R9-10, R9-12, R9-13, R9-16, R9-20)

None of these exists today. Each lives in the making.codes repository, in a
change of its own, and this ADR does not pretend otherwise. The first
consumer may not start before all of them are in place.

| Prerequisite | Where it lives in making.codes | State on 2026-10-13 |
|---|---|---|
| PostgreSQL under Patroni, Consul as its DCS, automatic failover to the highest LSN | a Patroni role under `domains/management/ansible/roles/`, joined to the Consul cluster | absent: no Patroni role exists |
| Vault mount `pki-consensus` with its own intermediate, ONE role and policy per slot with exact `allowed_uri_sans` | `domains/management/ansible/playbooks/post-bootstrap.yml`, beside the existing PKI tasks, and the task list of `scripts/lab-vault-from-zero.sh` | absent |
| A JWT auth role per slot, bound to the job id and the task name, never the datacenter | the same playbook | absent (R8-08) |
| nftables input rule: consensus and object ports from member addresses only | `domains/management/ansible/roles/common/templates/nftables.conf.j2` | absent |
| the listeners' address, `wireguard_ip`, on `wg2` after the migration | `domains/management/ansible/inventory/group_vars/all/networks.yml` (the networks by tier) | the migration is written and not converged, `wg0` today |
| the provisioning marker on the real mount, holding the LUKS UUID and its creation time, and the Nomad client ordered after the LUKS mount (`/opt/secure`) | the `luks` and `nomad` roles | absent (R8-34) |
| the Nomad job, three task groups, each pinned on `${node.unique.id}`, distinct task names, a host volume each | a new `domains/management/jobs/` directory (none exists), or the management repository of making.codes ADR 0015 | absent |
| a separate JWT auth mount `jwt-consensus`, roles binding `nomad_namespace`, `nomad_job_id` and `nomad_task`, a `user_claim` per slot (R9-13) | `post-bootstrap.yml`, and a check in `scripts/workload-jobs.test.sh`, with a lab replay of ADR 0005's two defects of 2026-10-01 | absent (R8-08) |
| `max_kill_timeout` raised on the three clients | `domains/management/ansible/roles/nomad/templates/nomad.hcl.j2` | absent: the template does not set it (R8-17) |
| the Recover witness on both NAS, each with an Ed25519 witness key, written only by the operator from the homes | the NAS seed (making.codes ADR 0015) | absent (R8-20, R9-08) |
| a dated, READ-ONLY exception of the network matrix: management to backup on the witness port, over WireGuard | `group_vars/all/networks.yml` (`networks_flows`, `temporary:` with its date) and making.codes ADR 0006 and ADR 0010 revised in the same change (Q19) | absent (R9-10) |
| the Forgejo groups' prestart witness check, and the boot override held in 1Password | the job, and a 1Password item beside the LUKS recovery passphrase | absent (R9-09, R9-11) |
| the `pull` role and the NAS executor refuse a job with `meta.rollout = "script"`, checked by the jobs test | the `pull` role, the NAS executor, `scripts/workload-jobs.test.sh` | absent (R9-12) |
| the shared PostgreSQL connection budget, a `CONNECTION LIMIT` per role | the Patroni role | absent (R9-16) |
| the Forgejo image by digest on each node, in the CI's zot and on the NAS | the job and the NAS seed | absent (R9-20) |
| Consul ACL: operator-only write on `consensus/*/superseded`, product read, a read-only Nomad token for Forgejo | `post-bootstrap.yml` and `domains/management/policies/` | absent (R8-14) |
| the LUKS image fully allocated, resized from a written co-tenant budget: every forge repository, consensus 5.3 GiB, PostgreSQL, Garage, Consul, Vault, Nomad | the `luks` role and a written budget beside it | absent (R8-31) |
| a metrics scraper, a rule evaluator and an alert channel reachable from the management | none exists today. The alerts of D13 have nowhere to run | absent (R8-31) |
| making.codes ADR 0015 amended: P5 and its single-instance Forgejo sentence | `docs/adr/0015-nas-graine-et-domaines-pilotes.md`, beside the ADR 0006 amendment | not written (R8-31) |
| the management domain allowed to run Forgejo | an amendment of making.codes ADR 0006, citing ADR 0015 | not written |
| the backup order PostgreSQL, ref snapshot, repositories | the `backup` role | absent. The role backs up Consul, Vault and Nomad today |
| the ports, proposed 7480 (consensus) and 7481 (objects) | the nftables rule and the job | to be checked against the port matrix in that change |



## Consequences / Semantics

- **The SDK gains a domain that can make ADR 0052's missing guarantee**:
  agreement across machines, with a fence a resource can check.
- **Every commit costs one network round trip and one batch's two
  `fdatasync`** on two of three nodes ([#417](https://github.com/kitsunium/sdk/issues/417)). There is no asynchronous mode; a caller that wants one does not
  want this domain.
- **A minority is read-only and says so.** A node cut off from the majority
  fails `Propose` with `NOT_LEADER` or `OUTCOME_UNKNOWN`, fails `Barrier` with
  `LEADERSHIP_UNCONFIRMED`, and can still answer `WaitApplied` for what it has.
- **The ports are frozen on first release.** The interfaces aliased by
  `pkg/v1`; growth is by siblings (ADR 0039), each frozen by a named
  compile-time test as ADR 0052 does.
- **Stopping is the answer to storage failure, corruption, divergence and an
  unknown version.** A node does not limp on; operators restore it with the
  tools of D17.
- **v1 carries the operations, as functions (Q4–Q7, R2-36).** Recovery,
  backup, quota, state checks, versioning and every membership operation are
  in v1, as Go functions. The SDK ships no `package main`, no admin handler,
  no HTTP route and no CLI: the product embedding the domain owns them. The
  milestones are adjusted accordingly.
- **Two declared siblings outside this domain (R2-20, R7-26):** the
  three-part TLS sibling of D4, in `net/tlsid`, and the pre-handshake
  admission hook in `net/server`. Both are in ADR 0174, reserved. It must
  be Accepted before this one, because
  `TransportConfig.Identity` is its type (R3-17).
- **The domain is designed (R7-00a).** `design/app/consensus.yaml` and two
  ranges in `design/sdk.yaml` come first, kit writes the rest of the
  declarations, and the code writes the bodies.
- **The first consumer needs infrastructure that does not exist yet (D20).**
- **Administration is any-node (Q8, R3-03).** No product has to find the
  leader to administer the cluster.
- **This version is rewritten after the final round, and not re-reviewed
  (R9).** Review run 3 reached its cap of three rounds. On R8, every
  panelist answered "nothing blocks" but one external reviewer, whose
  blocking point R9-01 answers. R9 applies that round's objections, and no
  reviewer has read R9. The history is in *Revision history*.
- **Forgejo's start after a Recover depends on the NAS (Q17, Q20, R8-20,
  R9-11).** Its witness check fails closed when no NAS answers, as LUKS
  already does through Tang. Nomad, Consul and Vault do not wait on it.
- The root `CLAUDE.md` domain list, the design file, the two ranges of
  `design/sdk.yaml` and what kit writes from them change in the implementing
  change set, not in this one.

## Breaking changes

None. `consensus` is a new domain; nothing existing changes.

## Alternatives considered

### Why not `hashicorp/raft`

The owner's reasoning: the library hides the log behind its own storage
interfaces and its own `FSM` contract, its snapshot and transport layers carry
assumptions (a snapshot is a file the library manages; the network transport
is its own msgpack protocol) that the SDK's ports, mTLS identity and error
model would have to be wrapped around rather than built from, and the SDK
would ship a dependency in `pkg/v1` that no other domain needs. It would also
leave the hardest knowledge — what a correct snapshot and membership change
require — in someone else's code, which is the knowledge the owner wants the
SDK to hold.

### Why not `etcd/raft` (`go.etcd.io/raft/v3`)

The best-engineered option, and the design this ADR borrows most from (the
step function and `Ready`, `Progress`, PreVote and CheckQuorum as flags). The
owner's reasoning: it is a library of the PROTOCOL only — storage, transport,
snapshots and membership plumbing are the caller's — so adopting it saves the
part that is best documented and leaves exactly the part Gitaly did not
finish on top of it. Gitaly is the evidence: it used `etcd/raft` and still
shipped no snapshot, no compaction and no read path.

### Why not dragonboat

Multi-group, fast, and complete — and large, opinionated about its own
storage engine (its own log DB), and with a single primary maintainer. The
owner declined the dependency for the same reasons as above, sharpened: v1 is
one group, and the multi-group machinery would be carried unused.

### Why not a compare-and-swap table in PostgreSQL

`UPDATE refs SET sha = $new WHERE repo = $r AND ref = $f AND sha = $old`,
synchronously replicated, with Patroni electing the primary. It writes no
consensus code and has the same network cost — and it was the design
document's initial recommendation. Rejected by the owner because it makes the
Git layer's availability that of a PostgreSQL primary and its failover, ties
the replicator to a database it otherwise does not need for refs, and is not
something the SDK can offer to other consumers.

### Why not Consul KV

Consul is already deployed and its KV has check-and-set. Rejected because the
replicator's log would then be Consul's Raft — a shared control-plane cluster
whose availability, upgrade cadence and quotas are set for other purposes —
and because a CAS per ref is not an ordered log: peers would have to reconstruct
an order from watches, which Consul does not promise for multi-key histories.

### Why not joint consensus in v1

See D8: it covers changes three voters never need, and it is the less
exercised path. Deferred, not refused.

### Why not one group per repository from the start

It is what Gitaly built, and the multi-group costs — heartbeat coalescing,
quiescing, routing, thousands of logs to compact — are what it never finished.
One group orders every ref update of the deployment; at the first consumer's
write rate that is not a bottleneck, and D11 keeps the wire and API open.

## Deferred

- **Multi-group (v2)**: groups multiplexed on one transport, heartbeat
  coalescing between node pairs, quiescing idle groups, and a routing table.
  Its own ADR, after v1 has run in production.
- **Joint consensus**, for changes of more than one voter at once.
- **A `lock.Locker` over this domain** (D10), answering ADR 0052 D11 inside
  the SDK.
- **Client sessions for exactly-once commands** (thesis §6.3): a client ID and
  sequence number in the entry so that a proposal retried after
  `OUTCOME_UNKNOWN` applies once. **Specified from IWFS:** the session also
  keeps a digest of the command. The same identifier with the same digest
  joins the pending proposal or returns the known answer. The same
  identifier with a different digest is refused, never applied (IWFS: same
  identifier and FNV-64a content give the same answer, different content
  gives `0x0031`). Until then, a caller never retries blindly after
  `OUTCOME_UNKNOWN` (D6, R1-04): a compare-and-swap does not make a duplicate
  harmless under ABA.
- **A streaming publication sibling in `vfs`** (D4), with the snapshot store
  as its second consumer.
- **Witness / non-data voters**, which would let two data nodes and a cheap
  third site form a majority.
- **A Windows backend for the segmented log**: `FlushFileBuffers` semantics
  and preallocation differ; refused with `proc.UnsupportedPlatform` until
  measured, as ADR 0052 D8 did for `LockFileEx`.
- **zstd snapshots (R7-06, R7-00b)**, once `third-party/transform` adds
  `DecompressBounded` in its own change, through a product-side adapter.
- **`fallocate` for segments (R7-22)**, once recovery knows unwritten
  extents and a measurement shows `fdatasync` after it publishes no
  metadata.

## Amendment — the published names follow the SDK's roles (2026-10-13, v0 breaking changes)

The domain shipped in `pkg/v1/app` v0.1.1 without the gate of the SDK's
linter, and its first run reported names and signatures every other domain
already follows. They are changed here, while the module is v0, under
[ADR 0040](0040-changing-a-published-shape-while-v0.md)'s licence, said out
loud as it requires: **every rename below breaks a caller**, and the release
that carries them is a minor one. No behaviour, wire byte or on-disk byte
changes with them.

| Before | After | Why |
|---|---|---|
| `Entry`, `Position`, `Token`, `LogIdentity`, `Member`, `Membership`, `Stamp`, `Status`, `Versions`, `StopReason`, `SnapshotMeta`, `InstallOffer`, `InstallAccept`, `AuthenticatedPeer`, `FencedAttestation`, `JoinRequest`, `Message`, `Health`, `PeerHealth`, `Report` | the same name + `Value` | a plain value says so, as in every other domain (KTN-STRUCT-ROLE); `Message` and `JoinRequest` are binary-coded by `wire`, never by reflection, and their old suffixes declared a JSON DTO they are not |
| `TimingContract`, `Precondition` | `TimingContractSpec`, `PreconditionSpec` | what every member must agree on, what an operation expects |
| `InstallRequest` | `InstallParams` | the arguments the transport hands the engine |
| raft `Start`, `Ready`, `Status`, `PeerStat`, `FetchRequest`, `UnsupportedEntry`; wire `Admin` | `StartState`, `ReadyValue`, `StatusValue`, `PeerStatValue`, `FetchSpec`, `UnsupportedEntryValue`; `AdminValue` | same rule, internal packages |
| `NewRaft`, `OpenDataDir`, `OpenDataDirWith`, raft `New` | `NewNode`, `NewDataDir`, `NewDataDirWith`, raft `NewRaft` | a behaviour-bearing type is built by `New<Type>` in its own file (KTN-STRUCT-CTOR) |
| `MemoryNetwork.Transport(id)` | `NewMemoryTransport(net, id)` | same rule; it still returns the one end of that id |
| `LoadPEMIdentity(…) (*PEMIdentity, *x509.CertPool, error)` | `NewPEMIdentity(…) (*PEMIdentity, error)` + `(*PEMIdentity).Roots()` | same rule |
| `FileLog`, `FileSnapshots` | unexported | reachable only through `DataDir.Log()` / `Snapshots()`, as the ports |
| `Bootstrap(ctx, Config, …)`, `Recover(…, RecoverOptions)`, `RestoreSnapshot(…, RestoreOptions)`, `NewTCPTransport(TransportConfig)`, `NewTCPTransportFromPEM(…, TransportConfig)`, raft `New(Config, Start)`, `(Status).Precondition`, `(Config).MemoryBudget` | pointer parameters and receivers | 120–416-byte structs were copied at every call (KTN-VAR-BIGSTRUCT); the callee reads them, and the transport constructors apply their defaults to a copy |
| `Health.Ready`, `HasLeader`, `QuorumReachable`, `Stopped`; `PeerHealth.Voter`, `Unreachable`, `Snapshot`; raft `PeerStat.Voter`, `Heard`, `Probing`, `Snapshot` | `Flags` with `HealthReady`… / `PeerVoter`… / `StatVoter`…, read with `Has` | three or more booleans are one bit-set (KTN-STRUCT-FLAGS) |
| `DefaultSegmentBytes`, `MaxJoinReasonBytes` | typed `int64`, `int` | KTN-CONST-TYPED |
| tlsid `PeerIdentity`, `Files` | `PeerIdentityValue`, `FilesConfig` | KTN-STRUCT-ROLE |
| raft `(*Raft).Applied` | removed | it did nothing and nothing called it |

Added beside them: `ReadyValue.ChainRefusedBy`, which feeds
`consensus_chain_refusals_total` — the counter D13 names and the engine never
emitted. `Bootstrap` honours its context: a caller that gave up before the
first durable write gets its cancellation back and nothing is written.

## Amendment — the survivor's key in `Inspect`, and the snapshot limit announced at JOIN (2026-10-22)

Two gaps between this ADR and the code, closed in `pkg/v1/app` as additions
(a minor release, nothing renamed, nothing removed):

- **`Inspect` reports `LastTerm` ([issue
  #534](https://github.com/kitsunium/sdk/issues/534)).** D17 chooses the
  survivor of a `Recover` by the highest `(lastLogTerm, lastLogIndex)`, but
  `ReportValue` carried only `Last` and the hard state, whose `Term` is the
  vote term, not the term of the last entry. Compared on what it showed,
  an operator picked a longer log whose tail no majority ever held, and the
  committed writes only the right survivor held were lost. `ReportValue.LastTerm`
  is the term of the entry at `Last`, or of the snapshot that ends at `Last`
  when an interrupted install published it past the log or at its last index
  under another term: the start, and `Recover`, move the log onto it. The runbook
  compares `(LastTerm, Last)`, in that order, never `Last` alone.
- **A joiner announces its `MaxSnapshotBytes`, and `Health` warns before the
  limit (R2-30, [issue #556](https://github.com/kitsunium/sdk/issues/556),
  following [#512](https://github.com/kitsunium/sdk/issues/512)).** The
  member's limit is `MemberValue.MaxSnapshotBytes`, already in the
  membership's encoding; the chunk writer is bounded by the smallest a
  member announces (#512). Nothing filled it but an operator's member list.
  - **The JOIN carries it, versioned.** A joiner whose `JoinConfig` names
    a limit sends its JOIN on a second pre-admission lane, `laneJoinV2`
    (4): one version byte (`joinVersion2` = 2), the limit as a little-endian
    `u64`, then the JOIN of lane 3 unchanged. The member records the limit
    in the learner it proposes. An older member serves no unknown lane and
    closes the connection without an answer; the joiner then sends the same
    JOIN on lane 3, which announces nothing: the behaviour before this
    amendment. A joiner whose `JoinConfig` names no limit uses lane 3 only.
    No frame of the log, of a snapshot or of replication changes.
  - **`Health` warns.** `HealthValue.SnapshotBytes` is the newest snapshot's
    size (the larger of its encoded and decoded sizes, the two the writer
    bounds), `SnapshotLimit` the limit a cut is held to now — this node's
    `MaxSnapshotBytes`, or the smallest a member announces — and
    `HealthSnapshotNearLimit` is set from 80 % of it. Readiness does not
    depend on it: the node still serves; it says the next cuts are about to
    be refused `SNAPSHOT_TOO_LARGE`.

## Amendment — what a snapshot's metadata is trusted for, and its checksummed version read before it is written (2026-10-22)

Three defects of the snapshot metadata, closed in batch S1 of the Raft
roadmap ([issue #564](https://github.com/kitsunium/sdk/issues/564)) with no
format written that a node of the previous release cannot read:

- **The codec the metadata names is the codec read ([issue
  #548](https://github.com/kitsunium/sdk/issues/548)).** D18 records the
  codec id "so a snapshot written with a codec a node does not know is
  refused cleanly". The node's restore and install already decode with it
  (#512); the offline readers (`OpenSnapshot`, `RestoreSnapshot`,
  `ImportObserved`) now do too when the caller names no codec, and refuse
  `VERSION_UNSUPPORTED` one this build does not link. A rolling change of
  `SnapshotCodec` keeps a leader throughout. This guard ships before any
  change of the codec a product cuts with.
- **No committed entry has term 0 ([issue
  #544](https://github.com/kitsunium/sdk/issues/544)).** A durable machine
  resumed past its snapshot resumes at the term of its applied position,
  the log's term there (a machine that reports another refuses the start,
  `LOG_CORRUPT`). A cut, an offer or a send at term 0 above index 0 is
  refused, and a snapshot already published so is set aside at the start
  as unreadable, never restored by truncating the log.
- **The metadata is checked before it drives the log ([issue
  #546](https://github.com/kitsunium/sdk/issues/546)).** Version 1 of the
  `SnapshotMeta` encoding has no checksum. Until version 2 is written, its
  id (`%020d-%020d`, index then term) must name its position, or the
  snapshot is not listed, not restored and not installed; and a leader
  ignores an acknowledgement past its own last index, which no honest
  follower sends (Leader Completeness).
  - **Version 2 is fixed, and read one release before it is written.**
    Its leading byte is 2, then the fields of version 1 unchanged, then the
    little-endian CRC-32C (Castagnoli) of every byte before it. A version 2
    whose sum fails is `FRAME_INVALID`: on disk the snapshot is not
    listed, in an offer it is refused. This release reads both versions
    and writes version 1 only; the writer ships in batch S3 behind the one
    raise of the cluster version, so that every member, and every release
    a node may be rolled back to from then on, reads what it writes (rule 5
    of #564). The wire rule "one encoding per value" holds per version:
    a version 2 accepted round-trips as version 2. Once a version 2 is
    written, rolling back below this release is not supported.

## Amendment — protocol version 2, raised once (2026-10-23, lot S3)

The formats lot S3 of the Raft hunt needs (kitsunium/sdk#564) ride on one
protocol version, 2, and one raise of the cluster version. D18 is kept and
completed:

- **Announcement.** A node of protocol 2 sets `FlagProtocol2` on every frame
  it sends. A node of protocol 1 tests only the flag bits it names, so the
  frame stays a version 1 frame it parses and acts on as before: this is an
  announcement, not a field a version 1 node skips. The leader keeps, per
  member, whether its last message of the term carried the flag. The ranges
  are not stored in the replicated membership: only the leader that admits
  the raise reads them, and it hears every member within a heartbeat.
- **The raise.** `RaiseClusterVersion(2)` is refused with
  `VERSION_UNSUPPORTED` while any member of the latest configuration, voter
  or learner, was not heard in the term announcing protocol 2. A member
  rolled back stops announcing at its next message. A node of protocol 1
  that finds the raise committed stops (`VERSION_UNSUPPORTED`): the rollback
  after the raise is refused cleanly, never run on the formats below.
- **What protocol 2 writes**, each chosen by the committed cluster version,
  never before the raise, and each older form still read:
  - the snapshot metadata in version 2, exactly as the amendment of
    2026-10-22 fixed it and the release before reads it: version 1's fields
    followed by a CRC-32C (#546 phase 2). The version follows the
    `ClusterVersion` the meta carries;
  - the admin payload of entry version 2 (`wire.AdminVersion2`), which adds
    `Origin`: the authenticated node the leader received the operation
    from, written by the leader over whatever the proposer wrote, as D8
    requires. The leader logs every engine entry it appends with its origin
    (#561). An admin payload version a node does not know stops it when the
    entry commits;
  - the stamp in version 2 when the log identity records `AdoptedFrom`,
    which Recover and RestoreSnapshot set only in a history at protocol 2:
    the last kept index the predecessor had committed. Only the stamp of
    the node they ran on carries it; the snapshot meta does not, so a node
    that learns the lineage from a snapshot keeps phase 1's answer. A predecessor's token at or below it is
    trusted by its index once its term can no longer be read; one in the
    adopted range above it is `LINEAGE_MISMATCH`, as phase 1 answered for
    the whole kept range (#509).
- `MsgLeaderRecord`, the admission of a stale voter by a leader a member
  vouches for (D4, #542, arbitrated on 2026-10-22): sent only at cluster
  version 2.
- **Not in protocol 2.** The fresh epoch in the stamp and D18's announced `PayloadMax` with
  `PAYLOAD_VERSION_REFUSED` (follow-ups of kodflow/forgejo-raft#65 and #50)
  have no issue yet. Each that changes a format will need protocol 3.

## Revision history

| Version | Round | What changed |
|---|---|---|
| R1 to R3 | review run 1, rounds 1 to 3 | a pure `Apply` over a replicated ref table with Git as an asynchronous projection, the storage actor, narrow tail repair, three connections per peer, operations as Go functions with no `package main`, any-node administration (Q1 to Q9) |
| R4 | review run 2, round 1 | divergence handling fully automatic with guardrails, durable state-machine ports kept (Q10, Q11) |
| R5 | review run 2, round 2 | divergence split into ADR 0173 (Q12), snapshots back to the core model, sync watermark, restart gc barrier, git transaction tracking, symbolic refs in the table, mandatory backup reconciliation |
| R6 | review run 2, round 3 | two retained snapshots, the implicit sync watermark, ADR 0173 activated all or nothing by the cluster version |
| R7 | review run 3, round 1 (overall round 7), 2026-10-12 | **State: rewritten-unreviewed.** Aligned with the SDK after its reorganisation: the `app` family paths, one design file, codes in the core, one module, stdlib bounded codecs, budgets in the design and absolute targets in `BENCH.md` (R7-00a to R7-00f, R7-01 to R7-11). Storage: numbered batches, logical truncation and zeroing, the commit clamped to the synced index, distrust after a failed sync, stop markers and exit codes, `fallocate` off (R7-12 to R7-14, R7-22, R7-44). Safety: `ENTRY_SUPERSEDED` only on a committed conflict, the election restriction, the one-campaign transfer flag, the leader's vote refusal, instants for the lease, submission ids across restarts, the "need snapshot" path (R7-15 to R7-21, R7-23). Recover fenced durably, Vault per slot, the pre-handshake admission sibling, `MinVoters`, listeners on the management address (R7-24 to R7-28). The first consumer: SQL as a log consumer, the backup order, Patroni, a replicator independent of the database, every filesystem watched, the Nomad deployment of Q13 with slots, `JOIN` and fenced replacement, D/F conflicts, two-phase bootstrap, alerts and limits, snapshot pins (R7-29 to R7-43, R7-45, R7-46). Infrastructure prerequisites listed in D20. The companion renumbered 0173, the TLS sibling 0174 reserved, the admission hook folded into it |
| R8 | review run 3, round 2 (overall round 8), 2026-10-13 | **State: rewritten-unreviewed.** Log recovery as one procedure: explicit truncate records, the effective log, a torn tail only in the highest batch and only with a tear pattern, zeroing only after classification, no batch across a rotation, the `STORAGE_FAILED` marker keyed on `boot_id` (R8-01 to R8-06). Identity: the member's key pinned by SPKI, slots pinned on Nomad node ids and bound by `nomad_job_id` and `nomad_task`, a published JOIN surface with the product's `JoinAuthorizer`, `Init` offline with `Redraw`, the replacement marker's owner and exit, an executable key-compromise runbook, the renewer, Consul ACLs (R8-07 to R8-14). Nomad: reschedule and disconnect corrected, the slot on a replacement VPS, `max_kill_timeout`, offline subcommands outside Nomad, the rolling script of Q16 (R8-15 to R8-19). The Recover fence is a witness on the NAS that fails closed (Q17, R8-20). Snapshot ports completed and rotation with pinned anchors (R8-21, R8-22). The SQL event lifecycle, its cursor and its batches (R8-23 to R8-30). Infrastructure rows and stale making.codes facts (R8-31 to R8-34). Exit codes through `errs.WithExitCode`, API and editorial fixes, the dead-voter gate stated (R8-35 to R8-38) |
| R9 | review run 3, round 3 (final, overall round 9), 2026-10-13 | **State: rewritten after the final round, not re-reviewed.** Batch sequences contiguous, stale recovery rules withdrawn, tears judged on the highest batch's end record per 512 B chunk (R9-01 to R9-03). SPKI required at every founding and recovery, break-glass certificates over the pinned key, `STORAGE_FAILED` markers in two places, a `join` callback off the event loop, the lane in the frame type's high bits (R9-04 to R9-07). The witness: signed receipts from both NAS, a check that blocks Forgejo only and fails closed, reached through a dated read-only matrix exception (Q18, Q19, Q20, R9-08 to R9-11). The job kept from generic submitters and rolled with a plan check, Vault roles bound by namespace with a claim per slot (R9-12, R9-13). The SQL boundary: backup cursor, step table, per-database dumps, predecessor events drained (R9-14 to R9-16, R9-19). Submission counter exhaustion, `DenyPin`, the image outside the forge, `require_cn` lab-gated (R9-17, R9-18, R9-20, R9-21) |
| Accepted | the owner's decision, 2026-10-13 | Accepted on R9 as written; the SDK stays public. No text changed but the status |
| S3 | Raft hunt lot S3, 2026-10-23 | protocol version 2 raised once: the announcement, the raise refused until every member speaks it, the checksummed snapshot meta, `Origin`, `AdoptedFrom`, the stale voter's admission of a vouched-for leader in D4 (amendment of 2026-10-23) |

## raft-sql-poc (owner's prior Raft)

The owner's earlier Raft was read in full before this ADR was amended:
the README, `raft.js`, `storage.js`, `shard.js`, `host.js`, `transport.js`,
`wire.js`, `secure.js`, `sqlstore.js`, `network.js`, and the storage,
resilience and TCP cluster suites. What it does well and this ADR takes
is listed apart from what it does badly, riskily or incompletely, which
this ADR must not take. Every item carries its proof: a measurement from
its README or a `file:line`. Each decision above that it changed says
**from raft-sql-poc**.

### What it does well, and this ADR takes

| # | Practice | Proof | Where here |
|---|---|---|---|
| G1 | An acknowledgement waits for the `fsync`. Votes, AppendEntries acknowledgements and the final snapshot reply are held back until the batch holding them is durable, and nothing else waits | `raft.js:50-57`, `raft.js:1549-1561`. With power cut on every machine at once: 300/300 acknowledged writes back in simulation and 622/622 over encrypted TCP. With the negative control `unsafeAckBeforeSync`, 50 to 139 of 300 were lost | D6, D15 |
| G2 | Group commit with no linger timer: what arrived during one turn of the loop becomes one write and one `fsync` | `raft.js:870-874`, `storage.js:447-505`. 101 to 143 writes per `fsync` under load. 7 000 to 9 400 durable writes/s, against 184 to 216/s with one `fsync` per write | D6 |
| G3 | The leader writes in parallel with replication and counts itself only at its persisted index. It never commits an entry of an older term by counting replicas (Figure 8) | `raft.js:1073-1093`, test at `raft.js:1086` | D6 |
| G4 | The hard state is a log record. A checkpoint opens every segment, and only a prefix of segments is ever deleted | `storage.js:24-37`, `storage.js:361-370`, `storage.js:507-591` | D4 |
| G5 | A torn tail is truncated, even under 8 bytes. A fault before the last segment is refused | `storage.js:755-771`, README bug 4, 19 storage tests | D4 |
| G6 | PreVote is always paired with CheckQuorum, and a restarted node behaves as if it had just heard from a leader | `raft.js:502-560`, `raft.js:693-705`, `raft.js:205-213`. Two bugs found and fixed: a minority inflating its term, and `propose` blocked forever without quorum | D5 |
| G7 | Single-server changes take effect on append. They are refused before the leader commits in its term, and only one is in flight. A learner never runs an election timer | `raft.js:403-465`, `raft.js:344-356` | D8 |
| G8 | A transfer refuses new proposals and sends `TimeoutNow` only once everything proposed is committed | `raft.js:789-809`, `raft.js:841-846`. 10 ms instead of an election timeout. README bug 7 was a write applied twice | D8 |
| G9 | `NOT_LEADER` (nothing appended, retryable) is kept apart from `UNKNOWN_OUTCOME` (appended, may commit) | `raft.js:723-737`, `raft.js:835-846` | D6 |
| G10 | Concurrent ReadIndex barriers share one quorum round. A lease is used only once the leader has applied its commit index and that index is past its no-op | `raft.js:1418-1531`. 2 000 concurrent reads cost 3 rounds. Strictly sequential reads cost −13 %, and the README says so | D9 |
| G11 | Snapshots are verified by SHA-256, inflated no further than the announced size, compressed once for every follower, and resumed by offset | `raft.js:1176-1343`. zstd gives ÷10.8. Adding a machine costs 42 KB instead of 204 KB | D7 |
| G12 | The transport sends one batch per peer per turn and keeps a bounded queue that drops when full. It refuses an announced length before allocating, bounds the handshake in time, and cuts a removed machine | `transport.js:1-22`, `transport.js:110-120`, `transport.js:294-296`, `shard.js:796-803`. 20 attacks, all failing | D4 |
| G13 | An empty AppendEntries is sent only as a heartbeat or when it carries a new commit index | `raft.js:918-924`. RPCs per entry went from 198 to 8.1 | D6 |
| G14 | A test that cannot fail proves nothing. Each defence is removed in turn, and the suite must turn red | `tools/security-mutations.js` (10/10 detected), `tools/balance-mutation.js`, `raft.js:77-80` | D15 |
| G15 | A fixed-layout frame with a closed schema and a version byte. The control path reads its fields without allocating | `wire.js:1-40`. Header reads at 0 B/op, measured under `--expose-gc`. ×18 to ×1 486 against JSON | D4 |
| G16 | Multi-Raft at the machine level: one heartbeat frame per pair of machines, delta-encoded, and one WAL per machine. Shards are placed by rendezvous hashing, and leadership is balanced to within one | `host.js:1-27`, `raft.js:1351-1368`. Idle messages ÷32 to ÷34 at 256 shards. Placement study on 100 000 keys | D11 (v2) |
| G17 | Replicate the effect, not the statement | `sqlstore.js:65-70`, and the measurements below | Context, D3 |

### What it does badly, riskily or incompletely, and this ADR must not take

| # | Defect | Proof | Risk | Answer here |
|---|---|---|---|---|
| B1 | Every exception from `apply` counts as deterministic output, including a divergence the replica detected itself | `raft.js:1109-1117`, `sqlstore.js:83` ("changeset conflict on apply: replica state diverged") | a fork reported to the client as an SQL error while the replica keeps applying | D3: a failure that is not a refusal stops the node |
| B2 | A failed `fsync` records the error and never syncs again. The node stays up | `storage.js:489-495` | safe but silent: every acknowledgement waits forever and nothing says why | D4: `STORAGE_FAILED`, the node stops |
| B3 | The lease runs on `Date.now()` | `raft.js:1495-1506`, among 28 `Date.now()` calls in `raft.js` | a step of the wall clock lengthens a lease that the rate argument does not cover | D9: monotonic `Since` of the injected clock |
| B4 | The lease survives the start of a transfer, and the transfer's `RequestVote` skips the live-leader check, which lives in PreVote only | `raft.js:756-787` does not clear `leaseUntil`. `raft.js:524-531` against `raft.js:588-620`, with `raft.js:563` and `raft.js:827` | a stale lease read if the vote request to the old leader is delayed or lost | D5 and D9(c) |
| B5 | Bootstrap is implicit: an empty log given `peerIds` writes a founding configuration | `raft.js:168-179` | a wiped node restarted with its peers founds a second cluster under the same name | D8: an explicit, one-time `Bootstrap`, and an ID is never reused |
| B6 | The pipeline is optimistic with no bound on what is in flight | `raft.js:937-939` | on a 27 ms link, one slow follower means megabytes resent, capped only by the transport's 64 MiB | D6: `MaxInflight` |
| B7 | `COMMIT_TIMEOUT` names what is really an unknown outcome | `raft.js:850-858` | a caller reads "failed" and retries an entry that may commit | D6 and D12: `OUTCOME_UNKNOWN`, with the context as its cause |
| B8 | A snapshot is cut synchronously on the apply path, held whole in memory, and written with blocking `fsyncSync` | `raft.js:1151-1159`, `storage.js:397-431` | apply, and so every write, pauses for as long as the state takes to serialise | D3 and D7: copy-on-write cut, streamed write |
| B9 | A snapshot that fails its hash is ignored without being reported | `storage.js:823-837`, storage test 11 | a corrupt disk goes unnoticed until "log base lost" | D7: `SNAPSHOT_CORRUPT` |
| B10 | Catch-up before promotion is checked only by the orchestrator. The engine promotes a lagging learner | `shard.js:662-676` against `raft.js:422-426` | a caller that skips the wait makes the group unavailable | D8: `Promote` refuses it |
| B11 | Its own handshake and record protection. Two cryptographic bugs found so far. No certificate expiry or rotation, a revocation list kept in memory and not replicated, nothing encrypted at rest, no rate limit on handshakes | `secure.js`, README bugs 5 and 6, README §"Ce que la sécurité ne couvre pas encore" | the next bug in hand-written crypto is found in production | D4: TLS 1.3 through `tlsid`, admission by membership |
| B12 | Tests are chosen scenarios on real timers, with unseeded randomness and sleeps. There is no deterministic simulation and no linearizability checker | `network.js:94`, `network.js:112`, the `sleep` calls in `resilience-tests.js`, README §"Limites assumées" | an interleaving nobody chose is never run, and a failing run cannot be replayed | D15 |
| B13 | One process and one event loop for every "machine". The figures come from macOS with `F_FULLFSYNC` | README §"Limites assumées" and §"Ce que mesure le banc" ("the throughput column is noise here") | CPU scaling and Linux `fdatasync` costs are not shown | D16: a floor, not a target, measured again on the VPS |
| B14 | Choices made for JavaScript: `f64` indexes because `u64` would mean `BigInt`, `u32` terms, an HMAC assembled by hand to dodge `createHmac`'s ~10 µs setup, and allocation claims about JavaScript strings | `wire.js:14-19`, README §"Deux découvertes de mesure" | none in Go, where `uint64` is native and the stdlib has no such setup cost | not carried over |
| B15 | A `leader` read level that reads locally on the leader with no confirmation | README §"Les trois niveaux de cohérence" | it is exactly the read a deposed leader serves stale | D9: not offered |
| B16 | Retries are not idempotent: `UNKNOWN_OUTCOME` goes back to the client, with no request identifiers | README §"Limites assumées" | a blind retry applies twice | D6: never retry blindly after `OUTCOME_UNKNOWN`; D19: per-ref revisions against ABA (R1-04); Deferred: client sessions |

### G17 in detail: replicate effects, not statements

The POC replicated SQL statements first, and its replicas diverged within
milliseconds. `CURRENT_TIMESTAMP` is evaluated by each replica as it
applies. After that was frozen on the leader, `uuid7()` gave three values on
three nodes, measured, and `LIMIT` without a total `ORDER BY` lets SQLite
pick different rows. Freezing known functions, in its README's words, is "a
whitelist applied to a blacklist problem". The POC fixed it in two steps.
First it executed on the leader inside a transaction that is always rolled
back, captured the row changeset and replicated that (`sqlstore.js:65-70`).
Then it built a storage engine whose mutation IS `{key, value, version}`,
where "the problem is not solved, it ceases to exist". It measured the
costs. On an UPDATE of 5 000 rows, row-based replication was 7.5 times
slower and 4 222 times larger in the log (144 KB against about 35 bytes).
And only one capture can be in flight against a given state, so the
non-deterministic writes serialise.

The first consumer's design ends where that path ends, and goes one step
further (R3-24). It replicates a ref transaction —
`{repository, [(ref, expected SHA, expected revision, new SHA)]}` — never the
HTTP request or the git command that produced it. `Apply` checks it against a
ref TABLE that the log alone orders (D19). It never checks it against each
node's Git repository, which the log does not order. Every replica evaluates
the same predicate on the same table, just as the POC's engine detects a
conflict at apply time with a predicate every replica computes identically.
Git is a projection of the table, written after the fact, from state.
The POC's size problem does not arise, because the bulk travels outside the
log: Git objects are written to a majority before the entry is proposed, and
the log carries only the ref transaction. The serialisation lesson remains,
moved: the expected revision does for a ref what the POC's single capture in
flight did for a database.

## IWFS (owner's Go Raft at Halys)

IWFS is the owner's SMS routing platform at Halys, written in Go. It was read
at `origin/master` `aec79ab` (2026-09-29): the consensus core in
`src/internal/cluster` (139 files, about 41 300 lines with tests), the
messages in `src/internal/domain/raft`, the gRPC mesh in
`src/internal/adapters/grpc/mesh`, the acceptance point in
`src/internal/adapters/grpc/peer`, and its documentation of design,
validation and defects. It is Halys code. This ADR takes ideas from it and
copies none of it. Paths below are relative to its root, and line numbers
are at that commit.

**What IWFS is.** It is not a durable Raft. In its own words it is "Shared Log
on Shared Store": a Raft-like election with PreVote, terms, a single vote
per term and a log-completeness check, all **in memory**, with no per-node
WAL. The live log is an in-memory ring buffer replicated over the mesh.
Valkey is an optional durability layer and the external arbiter of write
authority, through a lease with an epoch fence
(`docs/src/cii-src/07-innovations/01-shared-log-single-writer.md` §4–§5,
`docs/src/implementation/architecture/cluster.md`). The local-WAL Raft of
its Phase 2 was dropped at "~200 ops/s"
(`01-shared-log-single-writer.md` §3.1, §6.1). Phase 7 then ran
7 980 SMS/s for 8 h with 0 failures on 229 833 637 SMS and term 1 throughout
(§7). That run acknowledged a write from the leader's memory, before
replication (§5.4, §8 point 5), with Valkey's AOF and RDB both disabled
(§5.5). Since 2026-09-10, majority-commit acknowledgement exists behind a
policy switch (`docs/src/design/2026-09-10-point-acceptation-commit.md`,
`src/internal/adapters/grpc/peer/acceptance.go:70-83`).

The two prior arts contradict each other on the central question, and the
measurements settle it. IWFS gave up local durability because a local-WAL
Raft reached about 200 ops/s. raft-sql-poc measured exactly that ceiling,
184 to 216 durable writes/s, when every write paid its own `fsync`. With
group commit it measured 7 000 to 9 400 durable writes/s on a laptop SSD
with a 5 ms `F_FULLFSYNC` (G2 above). The ceiling that pushed IWFS off disk
was the missing group commit, not the disk. **raft-sql-poc is right, and
this ADR keeps a majority on disk.**

### What IWFS does well, and this ADR takes

| # | Practice | Proof | Where here |
|---|---|---|---|
| I1 | A vote is refused while a leader heard within the lease window is alive, in RequestVote as well as in PreVote. This is the check raft-sql-poc puts in PreVote only (its B4) | `src/internal/cluster/election.go:552-560`, `election.go:603-609`, `election.go:650-660` | D5 — confirmed. Here IWFS is right and the POC is not |
| I2 | The proposer waits on the INDEX and the TERM of its entry. If the position was truncated and reused, the wait ends with "entry replaced", and the request is never reported as accepted | `2026-09-10-point-acceptation-commit.md` §2. Tests `TestWaitForCommit_ReplacedEntry` and `TestAcceptance_ReplacedEntryIsNotAccepted` (`docs/src/testing/validation-2026-09-10/README.md:79`) | **D6, D12 amended**: `ENTRY_SUPERSEDED` |
| I3 | A wait that runs out is "unknown outcome", never "cancelled", and a retry joins the pending submission | `validation-2026-09-10/README.md:73`, `TestWaitForCommit_TimeoutIsUnknown` | D6 — confirms the amendment taken from raft-sql-poc's B7 |
| I4 | Client idempotence: the same identifier with the same content (FNV-64a) gets the same answer, and the same identifier with different content is refused | `2026-09-10-point-acceptation-commit.md` §2 (codes `0x0031`, `0x0020`) | **Deferred amended**: the client-session design, with a content digest |
| I5 | Refusal BEFORE acceptance when a queue is full, so nothing is half-accepted (`0x0032`). The `noeviction` and ring-full backpressure reach the admission gate, not a drop | `2026-09-10-point-acceptation-commit.md` §2, `01-shared-log-single-writer.md` §5.5 | **D6, D12 amended**: `OVERLOADED` |
| I6 | A commit advance is pushed to the followers at once (coalesced, at most every 20 ms, `election_commit_wait.go:15`, `:55-63`), and a follower's apply callbacks fire on a commit advance that brings no new entry. When commit waited for the 1 s heartbeat ack, 0 of 3 integration tests passed | `election.go:2396-2400`, `election.go:964-975`, `2026-09-10-point-acceptation-commit.md` §1 and §3 | D6 — confirms the POC's rule that an empty AppendEntries carries a new commit |
| I7 | A follower dials the leader as soon as it learns of it, and opens its forwarding stream before the first entry. Without that, the first forwarded write after an election exceeded its wait | `election.go:805-807`, `2026-09-10-point-acceptation-commit.md` §2–§3, `TestLeaderForwarder_WarmsUpStreamBeforeAnyForward` | **D6 amended** |
| I8 | Commit counts only an entry of the current term (Figure 8) and aborts if the term changed during the computation | `election.go:2426-2446`, `election.go:2455-2461` | D6 — confirmed |
| I9 | A fence advances only on a SUCCESSFUL acquisition. An epoch bumped by a losing contender fenced the legitimate leader | `01-shared-log-single-writer.md` §5.2 and §7.1, Valkey Lua script cited there | D10 — confirmed: the fence is the committed entry's index, which only a success produces |
| I10 | Defects found under adversity, each named with the regime that exposes it: a split vote replayed in the same term forever on simultaneous start, a follower never re-requesting committed entries it missed during a partition, a PreVote fan-out saturating the shared mesh client under CPU starvation, and the fence above | `01-shared-log-single-writer.md` §7.1 | **D15 amended**: each becomes a simulator scenario. **D4 amended**: control frames never wait behind entries |
| I11 | A chaos E2E on kind with Calico network policies for real partitions, polling invariants from fresh `/debug/raft` snapshots throughout | `tests/e2e/chaos_invariants_test.go:395-401`, `:456-461`, `tests/e2e/scenarios_consensus_inv_test.go` | D15 — the shape of the first consumer's `tc netem` lane, with the oracles of D15 rather than IWFS's (J20) |
| I12 | Documentation that dates its claims, states its evidence level and revises itself when the code disagrees ("Révision assumée"), with a validation record pinned to a SHA and to binary digests | `01-shared-log-single-writer.md` §9, `validation-2026-09-10/README.md` | the practice `BENCH.md` and this ADR follow |

### What IWFS does badly, riskily or incompletely, and this ADR must not take

| # | Defect | Proof | Risk | Answer here |
|---|---|---|---|---|
| J1 | **The quorum is a majority of the members the health check currently sees as ACTIVE**, not of a configured voter set | `election.go:1305`, `election.go:1538-1539`, `src/internal/cluster/election_coverage.go:113-129`, `src/internal/cluster/membership.go:567-585`. Members turn Suspect after 5 s and Dead after 10 s (`cluster.md`, Membership) | in a partition {A} / {B, C}, A sees B and C dead, its majority of one elects itself, and two leaders write. Only the Valkey lease, when it is enabled, stands in between | D8: the membership is log entries; a majority is computed over the configured voters and never over who answers |
| J1b | The vote quorum and the commit quorum are not the same set. Votes count active members. Commit counts `len(progress)`, every member the leader ever heartbeated. And any `VoteRequest` sender is added as an active member | `election.go:2354-2357` against `election.go:1305`, and `election.go:532` | two quorums that need not intersect break Leader Completeness, and an unauthenticated peer can change the size of a majority | D8 |
| J2 | **Term and vote are not persisted** ("StableStore non câblé") | `2026-09-10-point-acceptation-commit.md` §1 and §4 | a node that restarts within a term can vote twice in it, which allows two leaders in one term | D4: the hard state is durable before any reply |
| J3 | **No local durability at all.** The log is in memory, and Valkey's AOF and RDB are off in the labs. A power loss on every node loses everything | `01-shared-log-single-writer.md` §5.1, §5.5 | "zero data loss" holds only while at least one replica's memory survives | the title of this ADR |
| J4 | The legacy acknowledgement point is still available: the client is answered from the leader's memory before replication | `acceptance.go:70-83` (`ackAfterCommit=false` keeps it), `01-shared-log-single-writer.md` §8 point 5 | an acknowledged write is lost with the leader | Consequences: no asynchronous mode |
| J5 | **Degraded leadership**: on loss of the majority, a survivor takes write authority through the external lease, without a quorum and without advancing the term | `01-shared-log-single-writer.md` §5.2.1, `election.go:1167-1190` | at most one writer, but its writes exist on one node: availability bought with durability | not taken — a minority is read-only (Consequences) |
| J6 | **There is no log matching in production, and an acknowledged write can be lost.** No production code calls `SetLogStore`, so followers take the path with no `prevLogIndex` check (`election.go:728-731`, `election.go:826-839`). They acknowledge their journal's highest sequence and commit `min(leaderCommit, that)`. Entries travel through a broadcaster that retries per node and accepts gaps and disorder, setting the sequence to the highest one seen. Even the unused Raft path acknowledges `GetLatestIndex()` rather than the verified prefix | `election.go:826-839`, `election.go:2028-2030`, `cluster/journal/journal.go:276-335`, `cluster/journal/broadcaster.go:599-641`, and the dead path at `election.go:845-867` | a follower that missed B1, queued for retry, but received B2 reports B2. The leader commits and acknowledges B1, then crashes. That follower wins the election, because its last index is high enough, and B1 is gone. This is raft-sql-poc's bugs 2 and 3 in their widest form | D6: Log Matching with a check on the verified prefix. The POC is right and IWFS is not |
| J7 | The mesh is plaintext gRPC (`insecure.NewCredentials()`), with no peer identity | `src/internal/adapters/grpc/mesh/client.go:74` | anyone who reaches `:8081` can forge a heartbeat with a higher term and take the cluster, as the POC's README warns | D4: mandatory mTLS, admission by membership |
| J8 | Members are identified by name or address strings. In static mode the same node appears twice (`iwfs1` and `iwfs1:8081`) | `2026-09-10-point-acceptation-commit.md` §1 and §4 | "the majority stays correct (each follower counts twice, symmetrically)" is an accident of symmetry, not a property | D8: a numeric `NodeID` bound to its certificate and never reused |
| J9 | Discovery drives membership (Kubernetes DNS or a static list), and the Raft settings, election timeouts included, are hot-reloaded through the journal | `src/internal/cluster/discovery.go`, `cluster.md` (Hot Reload) | a timing change while a lease is held breaks the lease's arithmetic, and the operator's DNS becomes part of the quorum | not taken: membership by explicit change, timings at construction |
| J10 | Elections time out at 3 to 5 s with a 1 s heartbeat, and the anti-spin backoff reaches 30 s (`2^min(n,3)`) | `cluster.md` (Election), `election.go:90-98` | failover slower than Gitaly's 4 s, and up to 30 s under a storm | D5 keeps 1 to 2 s. D4 removes the storm's cause, election traffic queued behind data, instead of slowing elections down |
| J11 | Every timer is `time.Now()` / `time.Since`. They are monotonic in Go, but they cannot be injected | `election.go:267-268`, `election.go:606-609`, `election.go:809` | no reproducible schedule: the defects of I10 were found by E2E, not by a test that can replay them | D15: injected clock, deterministic simulator |
| J12 | Tests: 5 191 unit functions and 6 integration tests that assert delivery, "not the order between majority commit and acknowledgement". The chaos E2E is skipped in CI without `IWFS_E2E_FLOOR_ENABLED`. There is no deterministic simulation and no linearizability checker | `validation-2026-09-10/README.md` (results and limits), `01-shared-log-single-writer.md` §5.2.1, `tests/` tree | the safety properties rest on unit tests of components, not on histories | D15 |
| J13 | `Sharded.LoadFromStore`, the recovery from total memory loss, has no caller in production. The Valkey epoch restarts at 1 after a reset | `01-shared-log-single-writer.md` §5.3, `2026-09-10-point-acceptation-commit.md` §1 | the last-resort recovery has never run, and a fence that restarts reissues numbers a resource has already accepted | D7: recovery from snapshot plus log is the normal path. D10: the fence lives in the log |
| J14 | CPU and memory thresholds put followers in a "security mode" that refuses writes | `src/internal/cluster/security_mode.go:233-244`, `src/internal/cluster/election_config.go:8-54` | a heuristic on host load decides admission, across nodes that do not share the load | D6 takes the admission refusal (I5) only from the leader's own pending bytes |
| J15 | **No CheckQuorum.** Quorum contact feeds only the security mode, and a partitioned leader stays leader | `election.go:2506-2532`, `security_mode.go:41`, `security_mode.go:242` | a leader cut off with its clients keeps their writes waiting until the timeout | D5 |
| J16 | **The term can go backwards.** A refusal that names a leader makes the candidate a follower at the response's term, even a lower one, and that clears its vote | `election.go:1500-1506`, `election.go:1716-1718` | a candidate at T+1 refused by the leader of T drops back to T and can vote a second time in T | D5: a term only ever grows, and only a higher term clears a vote, as raft-sql-poc fixed (its bug 1) |
| J17 | **No no-op entry at election, no leadership transfer, no read path, no learners.** `LogEntryNoop` and `LogEntryConfiguration` are defined and unused. Reads are local with no linearizable option | `election.go:1588-1673`, `src/internal/domain/raft/leader_heartbeat.go:51`, `election_coverage.go:22-48`, `election_coverage.go:70-78` | entries of an earlier term commit only when a new client write arrives, and a read after a failover may be stale | D6, D8, D9 |
| J18 | **Snapshots are stubs.** Create and load are not implemented. Chunks of a chunked `InstallSnapshot` other than the last are acknowledged and discarded, and the path in use returns a whole snapshot in one unary reply under a 16 MiB cap | `cluster/journal/journal_adapter.go:220-245`, `election.go:2230-2246`, `src/internal/adapters/grpc/mesh/server.go:688-733` | a follower behind by more than 16 MiB of state can never catch up. A follower's safety valve also compacts uncommitted entries (`broadcaster.go:947-978`) | D7 |
| J19 | **The transport loses errors.** `SyncJournal` logs a handler's error and still ends the stream in EOF, which the client takes as confirmation. A request missing its body panics the node, with no recovery interceptor. `Stop` holds a lock across `GracefulStop` that handlers wait on | `mesh/server.go:518-520`, `mesh/client.go:931-934`, `mesh/server.go:370`, `mesh/server.go:197-225` | silent entry loss under backpressure, a remote crash, and a shutdown that can hang | D4: framing errors close the connection (`FRAME_INVALID`), and Raft retransmits from `next` rather than trusting a stream's end |
| J20 | **The chaos oracles are weak.** Two leaders in one term are tolerated for one 2 s sample, Log Matching compares only `(lastLogIndex, lastLogTerm)`, Leader Completeness compares lengths, and the performance tests never fail | `tests/e2e/chaos_invariants_test.go:296-318`, `:387-393`, `:481`, `tests/e2e/scenarios_performance_test.go:125-129` | an invariant checked on a sample of summaries passes histories that violate it | D15: invariants on every event and on content, and a linearizability checker with a negative control |

### Three columns: ADR 0152, raft-sql-poc, IWFS

`>` marks where the two prior arts disagree and names the one that is right.

| Decision | ADR 0152 | raft-sql-poc | IWFS |
|---|---|---|---|
| D1 name | `consensus`, `NewRaft` | `RaftNode` | `Election`, "Shared Log" |
| D2 placement | stdlib-only, three layers | no dependency | gRPC, Valkey, Kubernetes |
| D3 state machine | deterministic `Apply`; a non-refusal failure stops the node | every throw is deterministic (B1) | apply on commit through `commitApplyQueue`, in order |
| D4 log | segmented WAL, CRC-32C, the hard state as a record, `fdatasync` per batch | the same, JavaScript | **none: in memory**, Valkey optional (J3). `>` POC right |
| D4 hard state | durable before reply | durable before reply | **not persisted** (J2). `>` POC right |
| D4 transport | mTLS checked both ways, admission by membership, batches, a separate control connection (R1-12) | its own SIGMA handshake, revocation | **plaintext gRPC** (J7). `>` POC right on the need, TLS on the means |
| D5 elections | PreVote, CheckQuorum, refusal in both votes, 1–2 s | PreVote and CheckQuorum, refusal in PreVote only (B4) | PreVote, refusal in both votes (I1), **no CheckQuorum** (J15), a term that can go back (J16), 3–5 s, 30 s backoff. `>` IWFS right on the refusal, POC right on CheckQuorum and on the term |
| D6 replication | majority on disk, verified prefix, bounded pipeline, no-op per term | verified prefix, unbounded pipeline, no-op per term | **no log matching in production**, gaps accepted, an acknowledged write losable (J6), no no-op (J17). `>` POC right |
| D6 outcome | three answers plus `ENTRY_SUPERSEDED` and `OVERLOADED` | `NOT_LEADER` / `UNKNOWN_OUTCOME` / `COMMIT_TIMEOUT` | accepted / unknown / refused / replaced / full (I2–I5). `>` IWFS more complete |
| D7 snapshots | copy-on-write cut, streamed, compressed, verified | synchronous, zstd, SHA-256, chunked | **stubs**, one unary reply under 16 MiB, chunks discarded (J18). `>` POC right |
| D8 membership | log entries, one at a time, learners, IDs never reused, transfer | the same, with its catch-up in the orchestrator (B10) | **a majority of the members the health check sees as active**, a different set for commit, any voter auto-added (J1, J1b), no transfer, no learners. `>` POC right |
| D9 reads | ReadIndex, lease opt-in on a monotonic clock | strong / leader / stale | local reads only, no linearizable read (J17). `>` POC right |
| D10 fencing | the committed index | `{index, term}`, unused | a Valkey epoch advanced only on success (I9), reset with Valkey (J13) |
| D11 groups | one group, `GroupID` on the wire | one group per shard, heartbeats coalesced | one leader, a journal sharded for CPU parallelism only. `>` both are right for what they do: the POC's shards are independent groups, IWFS's are lanes of one log |
| D12 errors | dotted-quad codes | string codes | SMPP statuses `0x0020`–`0x0032` |
| D13 observability | `metrics`, `trace`, `logger` | events on demand | Prometheus, a `/debug/raft` view with `write_authority` and `write_refusal_reason` |
| D14 API | `Propose`, `Barrier`, `WaitApplied` | `propose`, `readIndex` | `WaitForCommit(index, term)` |
| D15 tests | deterministic simulator, linearizability, mutations | scenarios, mutations | unit tests and Kubernetes chaos E2E, outside CI (J12) |
| D16 performance | ≥ 10 000 entries/s on disk | 7 000–9 400 durable/s | 7 980 SMS/s acknowledged from memory, not comparable (J4) |

## References

- Diego Ongaro, *Consensus: Bridging Theory and Practice*, Stanford PhD
  thesis, 2014 — §3.10 leadership transfer, §4.1 single-server membership
  changes, §4.2.3 disruptive servers, §4.3 joint consensus, §6.2 CheckQuorum,
  §6.3 client sessions, §6.4 ReadIndex and leases, §9.6 PreVote, §10.2.1
  parallel leader write.
- Diego Ongaro, "bug in single-server membership changes", raft-dev mailing
  list, 2015.
- Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm",
  USENIX ATC 2014.
- Wing & Gong, "Testing and Verifying Concurrent Objects", 1993; Gavin Lowe,
  "Testing for linearizability", 2017 — the checker of D15.
- PostgreSQL "fsyncgate", pgsql-hackers, 2018 — why a failed `fsync` is not
  retried.
- raft-sql-poc, the owner's prior Raft (Node.js, no dependency):
  `src/raft.js`, `src/storage.js`, `src/host.js`, `src/shard.js`,
  `src/transport.js`, `src/secure.js`, `src/wire.js`, `src/sqlstore.js`,
  `src/network.js`, `src/resilience-tests.js`, `src/storage-tests.js`,
  `tools/security-mutations.js` and its README. It is not published, and the
  paths are relative to its root.
- IWFS (Halys), `origin/master` `aec79ab`, read only:
  `src/internal/cluster/election.go`, `election_coverage.go`,
  `membership.go`, `security_mode.go`, `discovery.go`,
  `src/internal/adapters/grpc/mesh/client.go`,
  `src/internal/adapters/grpc/peer/acceptance.go`,
  `docs/src/cii-src/07-innovations/01-shared-log-single-writer.md`,
  `docs/src/design/2026-09-10-point-acceptation-commit.md`,
  `docs/src/testing/validation-2026-09-10/README.md`,
  `docs/src/implementation/architecture/cluster.md`, `tests/e2e/`. Not
  published.
- GitLab Gitaly, `internal/gitaly/storage/raftmgr` and
  `internal/gitaly/config` before their deletion in 2026 — the gaps listed in
  Context.
- ADR 0052 (fences and D11), ADR 0056 (publication and the deferred streaming
  writer), ADR 0029 (stream connections and `tlsid`), ADR 0090 (`clock`),
  ADR 0011 (withdrawn as the snapshot tool), ADR 0039, ADR 0031, ADR 0074, ADR 0068.
