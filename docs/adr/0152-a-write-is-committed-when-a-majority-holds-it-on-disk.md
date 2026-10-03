# ADR 0152 — consensus domain (`consensus`): a write is committed when a majority holds it on disk, and the log is compacted from the first version

- **Status**: Proposed — review run 2 complete (3 rounds); **R6 version not yet reviewed**
- **Date**: 2026-10-02
- **Deciders**: SDK maintainers
- **Related**: [ADR 0052](0052-sdk-lock-domain.md) (fencing, and the guarantee a lock does NOT make — this domain is the first in the SDK that can make it across machines), [ADR 0056](0056-sdk-vfs-domain.md) (publication by rename, and the streaming writer it deferred), [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md) (the clock port every timer here runs on), [ADR 0029](0029-sdk-net-domain.md) (the stream connection port and the TLS identity the transport rides), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (frozen ports, siblings), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (clamp or refuse), [ADR 0011](0011-kernel-snapshot-primitive.md) (withdrawn as the snapshot tool, D3, R1-14), [ADR 0068](0068-layer-firewall-is-a-checked-graph.md) (the layers), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (which layer owns a type), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) / [ADR 0035](0035-pp-range-ownership-enforcement.md) (the code ranges), [ADR 0120](0120-a-state-machine-keeps-an-agenda-not-a-sweep.md) (a different "state machine" — see D3), [ADR 0148](0148-a-private-socket-is-gated-by-its-directory-and-the-kernel-names-the-peer.md) (the first consumer's proposal socket, D19); **Split into** [ADR 0160](0160-divergence-is-prevented-by-construction-and-repaired-automatically.md) (divergence prevention and automatic repair, Q12); **Depends on** the TLS sibling ADR of D4, which must be Accepted before this one (R3-17)

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

Add **`consensus`** as a new domain in the three-layer shape of ADR 0001:
`internal/core/consensus` (`0.2.57.*`), `internal/service/consensus`
(`0.3.92.*`), `pkg/v1/consensus`. One algorithm ships — Raft — and **no
registry**.

**The SDK is a library and nothing else (Q4, R2-36).** It ships no
`package main`, no daemon, no CLI, no admin HTTP route and no admin handler.
Every operation, online or offline, is a Go function. A product that embeds
the domain owns its process, its command line, its administration interface,
its authorization and its audit. For the first consumer, that product is
Forgejo itself (Q5, Q6, Q7, D17, D19). No `framework/consensus` module ships
in v1, because Forgejo is not a kit application.

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

### D2 — Placement and allowed dependencies

| Layer | Package | Holds | May import |
|---|---|---|---|
| core | `internal/core/consensus` | every type a port speaks or that implementing a published port requires (ADR 0074, R2-32): `NodeID`, `Incarnation`, `ClusterID`, `LogIdentity`, `GroupID`, `Position`, `Entry`, `EntryKind`, `HardState`, `Stamp`, `Member`, `Membership`, `Message`, `SnapshotMeta`, `SnapshotSink`, `InstallRequest`, `Versions`, `TimingContract`, `Status`; the ports `Replicated`, `StateMachine`, `Snapshot`, `LogStore`, `LogReader`, `SnapshotStore`, `Transport`; the frozen siblings `DurableStateMachine`, `BatchingStateMachine`, `UnreachableReporter`, `LeadershipWatcher`; the `0.2.57.*` sentinels | kernel only (`errs`, `clock`) |
| service | `internal/service/consensus` | the Raft engine (a pure step function, its event loop and its storage actor), the segmented file log with its `_linux.go` I/O, the snapshot store, the mTLS transport, the offline functions over the shipped layout, the in-memory log and transport doubles, the simulator's fault models (test-only) | kernel; core `consensus`, `net`, `lock`, `metrics`, `trace`, `logger`, `transform`; service `lock` (the data-directory lock, D11), `net/server` (the inbound stream groups), `transform` (flate) |
| pkg | `pkg/v1/consensus` | type aliases for every type needed to implement a published port (R2-32), `NewRaft`, `Bootstrap`, `OpenDataDir`, the offline functions `Recover`, `RestoreSnapshot` and `Inspect`, the adapters' constructors, the sentinels | its own lower layers, sibling `pkg/v1/*` |

There is no `tools/` row and no `cmd/` row: the SDK has no `package main`
(Q4, R2-36). An external-module test implements every published port from
`pkg/v1/consensus` alone, so a missing alias fails to compile in CI rather
than in a consumer's build (R2-32).

The service-to-service edges are lateral and permitted (`internal/CLAUDE.md`).
Nothing is added to the kernel: the step function is generic only to Raft, so
it fails rule 1's second half. Stdlib only — no `go.etcd.io`, no
`hashicorp/raft`, no `golang.org/x/*` — so `pkg/v1/consensus` stays as
dep-light as `lock`. The one new SDK surface outside this domain is ONE
declared TLS sibling in `net`/`tlsid`, with the three parts listed in D4
(R1-23, R2-20). It gets its own ADR in the implementing change.

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

**Divergence prevention: ADR 0160 (Q12).** Detecting that replicas which
applied the same log reached different states, and repairing that
automatically, moved to ADR 0160 by the owner's decision Q12, with every
related decision of rounds 3 to 5. ADR 0152 keeps only the contract it
offers:
- **A deterministic `Apply`**, as this section states: a pure transition of
  state the state machine owns, no node-local precondition, log order, one
  goroutine.
- **ADR 0160 is a cluster capability, activated all or nothing (R6-04).**
  - A range of entry kinds is reserved for it. Until `RaiseClusterVersion`
    raises the cluster to the version that enables ADR 0160, an entry of
    that range is `VERSION_UNSUPPORTED`.
  - After the raise, ADR 0160 applies those entries only beyond the gate
    index of the raise. A node running ADR 0152 alone therefore never meets
    one before the gate.
  - `Message` carries no field of ADR 0160. `CheckIndex` and `RootHash` are
    removed from it; ADR 0160 introduces them in `Message` version 2.
  - `SnapshotMeta` has a reserved alarm field that ADR 0152 carries without
    interpreting it.
- **The internal hook points ADR 0160 gets (R6-04).** They are internal to
  the engine, not published API:
  - a per-node gate on three permissions: may campaign, may serve reads, may
    acknowledge;
  - batch boundaries imposed at given indexes;
  - self-proposed control entries, their origin stamped from TLS;
  - a floor on log retention, which compaction respects;
  - state replacement through `Restore`;
  - a pre-vote gate slot at step 4 of the start sequence (D14).
- **What may delay what at start (R6-04).** Nothing beyond the checksums
  blocks LOADING or REPLAY. Only ADR 0160, through its gate slot, may delay
  the VOTE.
- `Entry.Version`, the entry checksum chain of D4, checksummed snapshots and
  the frozen siblings of ADR 0039 are the other extension points.
- The error codes and metrics of ADR 0160 keep their serials in this ADR's
  range, which reserves them (D12).
Nothing in ADR 0152 waits for ADR 0160, and a node can run without it.

The name collides on purpose with the textbook and must not be confused with
ADR 0120's `statemachine`, which moves entities between declared states on a
timer. That domain decides WHAT state an entity is in; this port is HOW a
deterministic program is replicated. Both package comments say so.

### D4 — The other ports: log, snapshots, transport, all frozen (ADR 0039)

```go
type LogReader interface {
	Entries(lo, hi uint64, maxBytes int, dst []Entry) ([]Entry, error)
	Term(index uint64) (uint64, error)
	Bounds() (first, last uint64)
}
type LogStore interface {
	LogReader
	// Append buffers entries and, when hs is not nil, a hard state, in ONE
	// record batch. Sync makes everything appended so far durable.
	Append(entries []Entry, hs *HardState) error
	Sync() error
	TruncateSuffix(from uint64) error      // drop a conflicting tail
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
type SnapshotStore interface {
	Create(id string, meta SnapshotMeta) (SnapshotSink, error) // a spool is bound to its snapshot id
	Latest() (SnapshotHandle, error)
}
type Transport interface {
	Send(to NodeID, m *Message) // never blocks; may drop
	// Stream sends a snapshot; the RECEIVER chooses the offset to resume from (R2-10).
	// open returns an io.ReadCloser at an ENCODED offset on a chunk boundary; Stream closes it (R3-16).
	Stream(ctx context.Context, to NodeID, meta SnapshotMeta, open func(offset int64) (io.ReadCloser, error)) error
	Bind(group GroupID, deliver func(*Message), install func(InstallRequest) error) error
	SetMembership(group GroupID, admitted []Member) // per group; the transport computes the union (R2-31)
}
type UnreachableReporter interface { Unreachable(func(NodeID)) } // sibling (R1-11)
```

**The LogStore contract (R1-08, R2-04).**
- Term, vote and entries travel in one `Append` and become durable under one
  `Sync`. A commit-only change of the hard state is written lazily and never
  makes a reply wait for a sync.
- **Readers see appended entries before they are synced.** The leader sends
  entries to its followers before its own `Sync` returns (D6), so `LogReader`
  must return them as soon as `Append` returns.
- The store allows concurrent readers and exactly one writer, the storage
  actor of D5b.
- **The step function never does I/O.** It reads an in-memory view of the
  unsynced and recent entries (D6, recent-entries window). A read below that
  window is an asynchronous fetch through the storage actor, whose result
  comes back as an event.
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
`length | CRC-32C | version | term | index | kind | body checksum | previous
header checksum | payload`, little-endian (the entry checksum chain below),
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
- Both are verified when the entry is appended and again before `Apply`. A
  mismatch at append refuses the acknowledgement, and the leader resends. A
  mismatch found before `Apply` re-fetches the entry from a peer, or stops
  the node. It never skips the entry.
- **At a snapshot boundary the chain is carried, never reseeded (R5-05).**
  `SnapshotMeta` holds the authenticated chain tip at the snapshot's index,
  and the bytes of the retained suffix are preserved as written. A fixed
  seed is used only at genesis, or after a replicated chain reset.
- **No silent loop (R6-05).** After N identical chain refusals at the same
  index, a follower stops answering that leader's heartbeats for the rest of
  the term, so CheckQuorum deposes it. N defaults to 3, provisional, and zero
  is refused. Each refusal is counted, and the follower reports itself
  unready in `Health` while it lasts.

**Durability, as the kernel gives it (R1-09).** On Linux, `segment_linux.go`
calls `fdatasync(2)` and `fallocate(2)`. Elsewhere, `os.File.Sync` is the
fallback, and it is stated as the weaker or slower path it is on each
platform. A segment has a fixed size, preallocated at creation, so a
`fdatasync` never has a file size to publish. The lifecycle is crash-consistent
and spelled out:

1. create `seg-N.tmp` and give it its full size. Either zero-fill it at
   creation, or use `fallocate` once a measurement has shown that
   `fdatasync` after `fallocate` publishes no metadata on the target
   filesystems (R2-05). Until that is measured, zero-fill;
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
- **A zero record header ends the data of a segment only if everything after
  it in that segment is zeros AND no later segment exists.** Otherwise a
  zeroed sector in the middle of synced data would be read as the end of the
  log. That is `LOG_CORRUPT` and quarantine.
- Indexes are continuous across segments. A gap or an overlap between one
  segment's last index and the next one's first is `LOG_CORRUPT`.
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
- a torn record whose damaged bytes are zeroed 4 KiB sectors, the signature
  of an interrupted write on the devices this targets.

**The tear model is stated (R4-02).** The supported model is crash-only: power
loss or a process crash, not a device that rewrites old sectors. A zeroed
sector proves that a record was never acknowledged ONLY when it lies inside
the last unsynced write window, which the writer records as its sync
watermark. A zeroed sector before the watermark is damage to synced data,
and is quarantined.

**The sync watermark is implicit in the write order (R5-02, R6-03).** Round 5
wrote an explicit watermark record. It is replaced by the ordering the
storage actor already keeps: it writes batch k, syncs it, and only then
writes batch k+1.
- **Proof:** any valid record of batch k+1 proves that batch k was synced.
- **Only the last batch is ambiguous.** Under the declared crash-only model,
  a tear in the last batch was never acknowledged, because acknowledgement
  follows that batch's sync (D5b). It is truncated. Damage in any earlier
  batch is corruption, and is quarantined.
- **Nothing else changes.** D5b's sync-done still means "data durable", and
  D16 keeps one `fsync` per side.
- *Tests:* crashes between a batch's write, its sync, the next batch's write
  and the acknowledgement, in every order the kernel allows.
- **Fallback, stated in advance:** if review shows the implicit form unsafe,
  the explicit watermark record of round 5 comes back, and D16 is rewritten
  with two `fsync` per batch.

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
   third returns.
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
`pkg/v1/server` with `server.TLS(identity)` and a `ConnHandler`. Outbound
dials with the same `tlsid.Identity`'s `ClientConfig()` (the HTTP client of
`pkg/v1/client` is not a stream dialer).

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
- A node's certificate carries the URI SAN
  `kitsunium-consensus://<cluster>/<node>`. The dialer checks the listener's
  SAN, and the listener checks the dialer's. A wrong cluster or node is
  `PEER_REJECTED`. The transport takes its own cluster and node from its own
  certificate's SAN, never from a separate setting (R2-40). `NewRaft` refuses
  a `Config.Cluster` or `Config.ID` that differs from that SAN (R3-24).
- The sender of every frame is stamped from the connection's TLS identity, not
  read from the frame. A frame that claims another sender is dropped and
  counted.
- `TimeoutNow` carries the sender's term. It is accepted only from the leader
  of the receiver's current term, and only when that term equals the
  receiver's own (R2-13). A mutation that drops the term check must fail.
- The cluster and group in the first frame are a guard against
  misconfiguration, not an authentication: the certificate already decided.
- An identity with nil `RootCAs`, or a listener without
  `RequireAndVerifyClientCert`, is refused at construction. TLS session
  tickets are disabled, so a resumed session never skips the certificate
  check.

**Admission follows the membership, from raft-sql-poc, fed by the engine
(R1-16, R2-31).** The transport admits only the `NodeID`s and incarnations of
the admission set: the union of the latest appended membership and the latest
committed one, rebuilt when a truncation removes a membership entry. The
engine pushes each group's admitted members and their addresses through
`SetMembership(group, admitted)`, and the transport computes the union over
its groups.
The static map given at construction is only a seed for the first contact.
A `Remove` that commits cuts that node's connections and refuses its
handshakes from then on. The POC learned that draining a machine is not
removing it: with a credential every machine shared, a removed machine could
come back (README bug 8). Its fix revokes the removed machine's key on every
peer (`shard.js:796-803`, `transport.js:110-120`). Here a `NodeID` is never
reused (D8), and a retired ID is refused even with a valid certificate.

**Limits TLS does not set (R1-24, from raft-sql-poc).**
- A frame's announced length is checked against `MaxFrameBytes` before
  anything is allocated.
- A snapshot is inflated no further than `MaxSnapshotBytes` and the size its
  metadata announces, whichever is smaller (R1-15), before its hash is checked
  (`raft.js:1272-1289`).
- A connection that has not finished its handshake within `HandshakeTimeout`
  is closed. The default is 1 s, and a larger value is refused.
- Unauthenticated connections are capped per IPv4 address and per IPv6 /64
  (R2-22).
- Slots are reserved for members, keyed on the IP addresses that the
  committed members' addresses resolve to. A member address that does not
  resolve is refused, never given an unkeyed slot (R2-22).
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

**Shutdown (R2-38).** `Close` and `HardClose` are idempotent, and both return
only after every goroutine above has exited. The storage actor owns the final
`fdatasync`: on `Close` it syncs what it holds before it exits. On
`HardClose` it stops without syncing, which is what a crash test needs.

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
  inflated by a linger delay — the reason `internal/kernel/batcher`'s
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
sequence number. The follower tracks each submission through four states:

1. **unsent** — not written to the connection;
2. **possibly accepted** — written, no answer yet;
3. **appended** — the leader answered with the `Position` it appended at;
4. **committed** — that position committed with that term.

A submission that fails while unsent is `NOT_LEADER` and is safe to retry.
Once it is possibly accepted, any failure is `OUTCOME_UNKNOWN`. The only
exception is a rejection from the leader that correlates with the submission
id and says nothing was appended. The forward acknowledgement carries the
`Position`. `ENTRY_SUPERSEDED` is answered only when the term at that index is
OBSERVABLE and differs: a committed or retained entry at that position with
another term. When the position is compacted or unknown, the answer is
`OUTCOME_UNKNOWN`. **From IWFS:** a follower dials the leader the moment it
learns of it and opens the forwarding connection before the first proposal
(`src/internal/cluster/election.go:805-807`). Without that, IWFS's first
forwarded write after an election exceeded its wait
(`docs/src/design/2026-09-10-point-acceptation-commit.md` §3).

**Outcomes.** `Propose` answers in one of these ways:
- success: a lineage-scoped `Token` (its `Position` and `LogIdentity`, R4-27) and the `Apply` result, once applied locally;
- `NOT_LEADER`: certainly not appended, with the known leader readable through
  `consensus.LeaderHint`;
- `OVERLOADED`: refused before append;
- `ENTRY_SUPERSEDED`: certainly not committed, under the conditions above;
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
`ctx.Err()`, as everywhere in the SDK.

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
    applies nothing until an install at or below the commit index it knows.
    A test corrupts every snapshot and cold-restarts (R6-02);
  - **integrity checks beyond the checksum never gate start-up.** ADR 0160
    may add anchors on top of these snapshots without changing that rule.
- **Cut:** `StateMachine.Snapshot` on the apply goroutine; `WriteTo` streams
  into `SnapshotStore.Create` off it, with a SHA-256 of the stream recorded in
  `SnapshotMeta` beside `Position`, `Membership`, the snapshot format version
  and the compression id (R1-22). Only one snapshot is in flight; a trigger
  during one is coalesced.
- **The engine's own replicated state rides in the snapshot (R2-12).**
  `SnapshotMeta` is versioned and carries the committed cluster version, the
  committed timing contract, the no-space alarm, a reserved alarm field
  carried uninterpreted for ADR 0160 (R6-04), and the set of retired IDs. A node that installs a snapshot restores them before it
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
    sender's term, the SHA-256 and the size are checked;
  - `Restore` is serialised with `Apply`;
  - **the install is recorded durably, step by step.** An installation record
    names the snapshot and the step reached: spooled and synced, published,
    stamp and hard state written, restored. Recovery reads it and finishes or
    abandons the install. A crash at any step leaves either the old base or
    the new one, never a mix;
  - the log suffix after the snapshot's index is kept only if the entry at
    the boundary has the snapshot's term and index. Otherwise the whole log is
    replaced.
  - **Resumption, exactly (R3-16).**
    - The leader streams from a `SnapshotHandle` that `Latest` pins, with
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
  so a crash during publication leaves one valid snapshot.
- **Compression, from raft-sql-poc.** The snapshot stream passes through a
  `transform` codec chosen at construction: flate from
  `internal/service/transform` by default, or zstd from
  `third-party/transform` when the caller supplies it, since `pkg` may not
  import `third-party` (ADR 0068). The compression id is recorded in
  `SnapshotMeta`. **The compressed format is chunked (R2-11).** The stream is a
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
  an `AddLearner` naming its `(NodeID, Incarnation)` is appended (R2-23).** The
  operator reads the incarnation on the new machine with the product's
  offline `Inspect` (for Forgejo, `forgejo cluster inspect`) and gives it to
  `AddLearner`. The incarnation is an identifier, not a secret (R2-36).
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
- **Bootstrap (R2-24).** It is an explicit call, run on every founding node
  with the IDENTICAL member list.
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
- **`Init` prepares a joining node offline (R3-02).** `Init(dataDir, cluster,
  nodeID)` writes a joining stamp and returns the incarnation, which the
  operator gives to `AddLearner` (D17).

**Leadership transfer.** `TransferLeadership(ctx, to)` stops accepting
proposals, brings `to` up to date, and sends it `TimeoutNow`; `to` campaigns
at once, its vote requests flagged so D5's vote refusal does not apply. It
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
      fail.
    - Validation: `0 < MaxClockDrift < 1` and finite, and the cross-field rules
      of D14 are checked after defaults are applied.

  The lease is used only when the leader has applied its commit index and
  that index is past the no-op of its term (`raft.js:1427-1432`).
  `MaxClockDrift` has no default: `ReadLease` with a zero drift is refused at
  construction (ADR 0031, refuse half). Each condition has its mutation test
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
  retained entry keeps its identity across the recovery.
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
  or truncates anything, and `DataDir.Close` releases it. A second process
  opening the same directory fails at once with `DATA_DIR_LOCKED`, before
  either can touch the tail.
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

Core `0.2.57.*` (`0x00_02_39_*`), allocated in `codeRangeOwners` in the
change that introduces them (ADR 0035):

| Code | Reason | Meaning |
|---|---|---|
| `0.2.57.1` | `CONSENSUS_MISCONFIGURED` | refused at construction: a bound missing or out of range (D14), a timing rule (D5), a lease without drift (D9) |
| `0.2.57.2` | `NOT_LEADER` | certainly not appended; the known leader in `Fields`, read with `LeaderHint` |
| `0.2.57.3` | `OUTCOME_UNKNOWN` | appended or possibly delivered, then the answer was lost — may still commit; a context's end is its cause (D6) |
| `0.2.57.4` | `LEADERSHIP_UNCONFIRMED` | a barrier's round did not reach a majority in time |
| `0.2.57.5` | `MEMBERSHIP_CHANGE_PENDING` | one change at a time |
| `0.2.57.6` | `MEMBERSHIP_INVALID` | unknown node, last voter, promotion without durable progress or a prospective majority |
| `0.2.57.7` | `TRANSFER_FAILED` | the target did not win within one election timeout |
| `0.2.57.8` | `ENTRY_TOO_LARGE` | a command above `MaxEntryBytes` |
| `0.2.57.9` | `COMPACTED` | an index below the first retained one; internally, the switch to the snapshot path |
| `0.2.57.10` | `STATE_MACHINE_FAILED` | `Apply` panicked; the entry IS committed, and the node stopped applying |
| `0.2.57.11` | `NODE_STOPPED` | the node was closed or stopped itself |
| `0.2.57.12` | `ENTRY_SUPERSEDED` | the term at the proposal's index is observable and differs: certainly not committed (D6, from IWFS) |
| `0.2.57.13` | `OVERLOADED` | refused before append: pending bytes or apply lag over their bound (D6) |
| `0.2.57.14` | reserved | `STATE_DIVERGED`, moved to ADR 0160 with its meaning (Q12) |
| `0.2.57.15` | `VERSION_UNSUPPORTED` | an entry kind, record, frame or snapshot version this node does not know; the node stops (D18, R1-22) |
| `0.2.57.16` | `NODE_ID_RETIRED` | a removed `NodeID` offered again (D8, R1-17) |
| `0.2.57.17` | `NO_SPACE` | the replicated no-space alarm is raised: writes are refused until it is cleared; the alarm, its clearing, membership changes, `TransferLeadership` and the control entries of D3 are still admitted (D17, R2-29, R6-13) |
| `0.2.57.18` | `RESULT_TYPE_MISMATCH` | `ProposeAs` got an `Apply` result of another type; the entry IS committed, and the error carries its `Token` (D14, R2-19, R5-14, R6-13) |
| `0.2.57.19` | `PAYLOAD_VERSION_REFUSED` | a proposal's payload version is above what some member announced; refused before append, retryable after the upgrade (D18, R2-16) |
| `0.2.57.20` | `LINEAGE_MISMATCH` | a position, outbox identity or fence from another `LogIdentity` lineage (D10, R3-22) |
| `0.2.57.21` | reserved | `HASHER_FAULT`, moved to ADR 0160 with its meaning (Q12) |
| `0.2.57.22` | reserved | `BINARY_REPLAY_MISMATCH`, moved to ADR 0160 with its meaning (Q12) |
| `0.2.57.23` | reserved | `APPLY_FINGERPRINT_REFUSED`, moved to ADR 0160 with its meaning (Q12) |
| `0.2.57.24` | reserved | `SELF_TEST_FAILED`, moved to ADR 0160 with its meaning (Q12) |
| `0.2.57.25` | `ADMIN_PRECONDITION_FAILED` | a clearing or raising operation named an alarm, version or contract that is not the current one (D17, R4-06) |

**Reserved serials (R6-13).** The five serials reserved for ADR 0160 are
declared by ADR 0160's package under this range's owner,
`internal/core/consensus`. That keeps the errs registry's two rules: one
owner per `MM.LL.PP` range (ADR 0035), and one `Define` per value. No
`Define` in ADR 0152's code uses them.

Service `0.3.92.*` (`0x00_03_5C_*`):

| Code | Reason | Meaning |
|---|---|---|
| `0.3.92.1` | `LOG_CORRUPT` | a CRC failure that is not a short read or a zeroed-sector tear, or a gap in the segments; never repaired (D4) |
| `0.3.92.2` | `HARD_STATE_CORRUPT` | the hard-state record or the checkpoint holding it is unreadable; never defaulted (R1-30) |
| `0.3.92.3` | `SNAPSHOT_CORRUPT` | a snapshot's SHA-256 or size does not match its metadata |
| `0.3.92.4` | `STORAGE_FAILED` | a write, `fsync` or read failed; the node stops, no retry |
| `0.3.92.5` | `DATA_DIR_LOCKED` | another process holds the data directory |
| `0.3.92.6` | `DATA_DIR_MISMATCH` | the stamp names another cluster, node or incarnation |
| `0.3.92.7` | `PEER_REJECTED` | the certificate's URI SAN does not name the expected cluster and node, or the incarnation is not admitted |
| `0.3.92.8` | `CLUSTER_MISMATCH` | a peer's first frame names another cluster or group (a misconfiguration guard) |
| `0.3.92.9` | `FRAME_INVALID` | a frame that does not parse or exceeds `MaxFrameBytes`; the connection closes |
| `0.3.92.10` | `SNAPSHOT_TOO_LARGE` | an announced snapshot above the local `MaxSnapshotBytes`; refused before spooling (D7) |
| `0.3.92.11` | `BOOTSTRAP_REFUSED` | a listed peer is already stamped, or the member lists differ (D8) |
| `0.3.92.12` | `TRANSPORT_MISCONFIGURED` | an identity with nil `RootCAs`, a listener without client-certificate verification, or a limit out of range (D4) |
| `0.3.92.13` | `RECOVER_REFUSED` | `Recover` on a directory that is not stopped and locked, without a new `ClusterID`, without the typed confirmation, or while an old seed answers and no force is given (D17, R2-25) |
| `0.3.92.14` | `LOG_QUARANTINED` | the data directory carries a quarantine marker from a `LOG_CORRUPT`; the node refuses to start until it is cleared (D4, R2-01) |
| `0.3.92.15` | `RESTORE_REFUSED` | `RestoreSnapshot` into a non-empty directory, with a zero expected digest, without the typed confirmation, or with a digest or bound that does not match (D17, R2-26, R3-15) |
| `0.3.92.16` | `DATA_DIR_IN_USE` | `DataDir.Close`, `Recover` or `Inspect` while a live `Node` holds the directory (D11, R3-18) |

### D13 — Observability (R1-25)

Through `metrics` (ADR 0044), attributed by `group`. The names are part of
the contract and are listed here so that dashboards and alerts can be
written before the code:

| Metric | Kind |
|---|---|
| `consensus.role`, `consensus.term`, `consensus.leader_changes` | gauge, gauge, counter |
| `consensus.commit.latency`, `consensus.apply.latency` | histograms |
| `consensus.fsync.latency`, `consensus.batch.entries`, `consensus.batch.bytes` | histograms |
| `consensus.apply.lag_bytes`, `consensus.apply.lag_entries` | gauges |
| `consensus.peer.match_lag`, `consensus.peer.inflight`, `consensus.peer.unreachable` | gauges by peer |
| `consensus.elections.started`, `consensus.prevotes.lost` | counters |
| `consensus.snapshot.duration`, `consensus.snapshot.bytes`, `consensus.snapshot.installs` | histogram, histogram, counter |
| `consensus.log.bytes`, `consensus.log.quota_bytes`, `consensus.nospace` | gauges |
| `consensus.tail.truncated`, `consensus.frames.dropped` | counters by reason |
| `consensus.barrier.latency` | histogram by mode |
| `consensus.failure_tolerance` | gauge: how many voters can fail now with a majority left |
| `consensus.tls.cert_expiry_seconds` | gauge per identity |
| `consensus.has_leader`, `consensus.peer.last_contact`, `consensus.peer.rtt` | gauge, gauge by peer, histogram by peer (R2-28) |
| `consensus.proposals.failed`, `consensus.proposals.pending` | counter by code, gauge (R2-28) |
| `consensus.fs.free_bytes`, `consensus.cluster_version`, `consensus.protocol_version` | gauges (R2-28) |
| `consensus.tls.reload_failures`, `consensus.tls.peer_cert_expiry_seconds` | counter, gauge by peer (R2-28) |

`Node.Health()` returns a structured verdict an orchestrator can poll: role,
leader known, quorum reachable, apply lag over its bound, every current alarm
with its `AlarmID` and the nodes it names (R5-09), the current timing
contract and cluster version, so the product can fill an operation's
preconditions, certificate expiry under its threshold, the smallest peer
snapshot limit approached (R2-30), and **`Stopped` with its `StopReason`**
when the node has stopped itself (R2-28). A `LeadershipWatcher` sibling
notifies leadership changes, so the product does not poll for them. Its
channel has capacity one and holds the latest value: the loop replaces an
unread value without blocking, and the channel is closed on `Close`
(R3-19). Through `trace` (ADR 0051): one span
per `Propose` from submission to local apply, with events at append, commit
and apply; the trace context travels in the forwarding frame only — never in
the log, which must not grow with telemetry. Through `logger`: role and term
changes, membership changes, snapshots and truncations at `Info`; nothing per
entry.

### D14 — Public surface (sketch, signatures only)

```go
package consensus // pkg/v1/consensus

// Every type needed to implement a published port is aliased here (R2-32).
type (
	NodeID         = coreconsensus.NodeID      // uint64, never 0, never reused
	Incarnation    = coreconsensus.Incarnation // [16]byte, drawn at first stamp; not a secret
	ClusterID      = coreconsensus.ClusterID
	LogIdentity    = coreconsensus.LogIdentity // ClusterID + log epoch + lineage (R2-09, R3-01)
	GroupID        = coreconsensus.GroupID
	Position       = coreconsensus.Position    // struct{ Term, Index uint64 }
	Entry          = coreconsensus.Entry       // Position, Kind, Version, BodySum, ChainSum, Data []byte (immutable) (R4-21)
	Token          = coreconsensus.Token       // fixed-size, comparable {Cluster, Epoch, Term, Index}; AppendBinary/UnmarshalBinary (R4-27, R5-14)
	EntryKind      = coreconsensus.EntryKind
	HardState      = coreconsensus.HardState
	Stamp          = coreconsensus.Stamp
	Message        = coreconsensus.Message     // v1: Version, Group, From (stamped from TLS), Term, Kind, Entries — no ADR 0160 field (R6-04); AppendBinary/UnmarshalBinary, versioned, so a custom transport forwards opaque bytes (R4-21)
	SnapshotMeta   = coreconsensus.SnapshotMeta
	SnapshotSink   = coreconsensus.SnapshotSink
	InstallRequest = coreconsensus.InstallRequest
	TimingContract = coreconsensus.TimingContract
	Versions       = coreconsensus.Versions
	Member         = coreconsensus.Member     // ID, Incarnation, Address, Voter, Joining
	Membership     = coreconsensus.Membership // the members at a Position, the retired IDs
	Operator       = coreconsensus.Operator   // opaque id, bounded length, no personal data (R2-36, R3-24)
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
	LeadershipWatcher    = coreconsensus.LeadershipWatcher // Watch() <-chan Status (R2-28)

	Health     = svcconsensus.Health   // includes Stopped, StopReason
	ReadMode   = svcconsensus.ReadMode // ReadIndex (zero value) | ReadLease
	Durability = svcconsensus.Durability
	Config     = svcconsensus.Config
	Node       = svcconsensus.Node
	Report     = svcconsensus.Report
	DataDir    = svcconsensus.DataDir
	QuarantineExit = svcconsensus.QuarantineExit // WipeAndRejoin | TruncateAndStaySilent (R3-09)
	RecoverOptions = svcconsensus.RecoverOptions // Force, Confirm, Operator, Joining, FencedAttestation (R2-25, R3-02, R4-04)
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
	SnapshotCodec       transform.Compressor // core/transform port; flate by default; id recorded
	SnapshotPausesApply bool                 // the state machine cannot cut cheaply (R2-34)
	Reads               ReadMode
	PreferredLeaders    []NodeID
	Meter               metrics.Meter
	Tracer              trace.Tracer
	Logger              logger.Logger
}

type TransportConfig struct {
	Identity                tlsid.ReloadableIdentity // the TLS sibling ADR's source; cluster and node from its URI SAN (R2-40, R3-17)
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
func Init(d DataDir, cluster ClusterID, id NodeID) (Incarnation, error) // joining stamp; idempotent (R3-02, R4-29)
func Recover(d DataDir, newCluster ClusterID, members []Member, opt RecoverOptions) error
func RestoreSnapshot(d DataDir, r io.Reader, newCluster ClusterID, members []Member, opt RestoreOptions) (SnapshotMeta, error) // runs without a Node (R3-15)
func Inspect(d DataDir) (Report, error) // read-only
func ClearQuarantine(d DataDir, exit QuarantineExit, op Operator) error // wipe-and-rejoin, or truncate-and-stay-silent (R3-09)

func NewTCPTransport(cfg TransportConfig) (Transport, error)
func NewMemoryLog() LogStore // tests
func LeaderHint(err error) (NodeID, bool)

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
func (n *Node) HardClose() error                // stops now, no transfer, no final sync
```

`Config`, `Node`, `Health` and `DataDir` belong to the engine and are aliased
from service. Every type a port speaks, `Status` included, lives in core
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
| `MaxBatchBytes` | engine | 1 MiB | clamp; refuse if > `MaxFrameBytes` |
| `MaxInflight`, `MaxInflightBytes` | engine | 64, 8 MiB | clamp; refuse if `MaxInflightBytes` < `MaxBatchBytes` |
| `MaxEntryBytes` | engine | 1 MiB | clamp; refuse if > `MaxBatchBytes` |
| `MaxPendingBytes` | engine | 64 MiB | clamp |
| `MaxApplyLagBytes` | engine | 256 MiB | clamp |
| `RecentEntriesBytes` | engine | 32 MiB | clamp (R2-40) |
| `SnapshotEvery`, `SnapshotEveryBytes` | engine | 16 384, 64 MiB | clamp |
| `SnapshotTrailing` | engine | 4 096 | clamp |
| `MaxSnapshotBytes` | engine, enforced by receiver | none; 1 GiB recommended for the three VPS (R2-29) | refuse zero; refuse if < `SnapshotEveryBytes` |
| `MaxLogBytes` | log | none; 1 GiB recommended for the three VPS (R2-29) | refuse zero |
| `LogReserveBytes` | log | 256 MiB | clamp; refuse if ≥ `MaxLogBytes` |
| `SnapshotCodec` | engine | flate | recorded in `SnapshotMeta` |
| `PromoteLag` | engine | 1 024 | clamp |
| `MaxFrameBytes` | transport | 16 MiB | clamp; refuse if < `MaxBatchBytes` + header |
| `HandshakeTimeout` | transport | 1 s | clamp; refuse > 1 s |
| `MaxUnauthenticatedPerIP`, `ReservedMemberSlots` | transport | 4, 8 | clamp |
| `PeerQueueBytes` | transport | 64 MiB | clamp |
| `InboundDataBytes`, `InboundControlMessages` | transport | 64 MiB, 4 096 | clamp (R2-08) |
| `MaxConnectionAge` | transport | 24 h | clamp (R2-20) |
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
  snapshot versions retained by open views. ADR 0160 adds a second table
  while a repair runs.
- **The start sequence is fixed (R4-18, R5-01):**
  1. open the data directory under its lock;
  2. load the latest snapshot whose checksums pass;
  3. replay the log to the commit boundary;
  4. the pre-vote gate slot, empty unless ADR 0160 is active (R6-04);
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

**The driver is tested too.** The goroutines of D5b run under
`testing/synctest`, so their timers are virtual, and under `-race`.
Step-function behaviour is also pinned by **datadriven scripts**: text files
of inputs and expected outputs, in the style of etcd's raft tests, which make
a regression readable in review.

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

**No test sleeps.** Every timer is on the injected clock, with ADR 0052's
wall-clock AST audit copied over and its own detecting negative test.

**Confirmed by raft-sql-poc, in its own words.** Its tests are chosen
scenarios on real timers, with no seed to replay, and its README lists "no
deterministic simulation test nor linearizability checker" among what still
separates it from a product. What it did prove is the shape of the power-loss
test: cut power on every machine at once, right after the last
acknowledgement, and count. It got 300 of 300 acknowledged writes back in
simulation, and 622 of 622 over encrypted TCP to disk (`storage.js:601-615`).

**Benchmarks** ship with `BENCH.md` (rule 9) and allocation gates in the
race-off lane (`tools/alloc-lane-targets.txt`, rule 12):

| Benchmark | Budget |
|---|---|
| step function, leader handling one `AppendEntries` response | 0 allocs/op |
| follower handling `AppendEntries` of 64 entries, memory log | ≤ 1 alloc/op amortized (the entry slice), 0 per entry |
| encode / decode of a log record and a frame | 0 allocs/op |
| `Propose` → applied, 3 in-process nodes, memory log and transport | ≤ 4 allocs/op |
| segmented log, `Append` 64 × 256 B + one `Sync` | reported per device, as ADR 0056's `BENCH.md` reports publication |
| first consumer's table checksum, one update, at 10³ to 10⁶ repositories | constant time across sizes (R5-07) |

### D16 — Performance targets: provisional, derived, then measured (R1-27)

Every number in this section is **provisional**. None is kept until two
measurements exist:
- the simulator's distributions for failover and commit latency, run with the
  RTT matrix;
- `fdatasync` p50 and p99 on each of the three VPS, measured under their real
  background load: Consul, Nomad and Vault running. An idle VPS measures a
  disk nobody will use.

The first draft assumed 1–3 ms. That assumption is withdrawn until measured.

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
- sustained ≥ 10 000 entries/s of 256 B committed with one `fsync` per batch;
- failover (leader killed, then first commit under the new leader): **p99 ≤
  3.5 s, provisional**. The final number is derived from the simulator's
  distribution with the chosen timeouts, not chosen first. The first draft's
  2.5 s did not account for detection, PreVote, the vote round and the
  no-op commit on a 27 ms link;
- `Barrier` in ReadIndex mode from the leader ≈ one nearest RTT;
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
`init`, `recover`, `restore`, `inspect` (with `--replay`, ADR 0160),
`snapshot save`, `reproject`, `quarantine clear`, `objects
verify|backfill` and `reconcile` (R6-09).

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
  `ClearDivergence` in ADR 0160.
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
  - **The probe cannot detect a partition, and the text says so (R4-04).** A
    seed cut off from this node still runs, and can still serve. So
    `Recover` requires an operator attestation,
    `RecoverOptions.FencedAttestation`, with one entry per old voter: the
    node, and the method that FENCED it. **Only methods that stop the old
    majority from serving count (R5-11):**
    - stopped, or powered off;
    - cut from the edge (Traefik and Cloudflare) AND from each other.
    Certificate revocation is not a method: the TLS sibling ADR does not
    check revocation. It becomes one only if that ADR adds revocation
    checking. The attestation is written into the audit record.
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
  - **Runbook order (R3-04):** Vault, then the certificates, then PostgreSQL
    and Patroni, then LDAP, then `Recover`, then Forgejo, then the site
    administration. Each step needs the ones before it: certificates for new
    node IDs need Vault, and Forgejo's administration needs its database and
    its directory. When LDAP or the database is not back yet, a break-glass
    local administrator (`forgejo admin user create`) gets the operator into
    the site administration.
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
    recorded in the audit record. A test restores three nodes, then
    cold-restarts all three (R4-15). The anchor-validation record that R4-15
    also wrote moves to ADR 0160 (Q12).
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
  - A restore pairs a ref snapshot with a PostgreSQL backup taken AFTER it, so
    the database never names a ref the snapshot lacks (R3-26).
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
  (the check and digest kinds ADR 0160 uses), so the cluster can still be
  repaired (R5-12). Snapshots and
  compaction keep running.
- A FOLLOWER that enters its reserve forwards the alarm proposal to the leader,
  like any other proposal (R3-24). The node with the full disk is often not
  the leader.

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
- **Key compromise (R2-36).** `Remove` the compromised node, then add the
  machine back under a NEW `NodeID` with a new certificate. A retired ID is
  refused forever (D8). The incarnation is an identifier, not a secret, and
  rotating it proves nothing.
- **Two replacement runbooks.**
  - A DEAD node: remove it first (four voters never exist), then add a new
    `NodeID` as a learner, catch up, promote.
  - A LIVE node: add the new one as a learner, catch up, promote, transfer
    leadership away from the old one if it leads, remove the old one.
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
  it. The operation is refused while any member does not.
- **Timing contract.** It has its own committed entry and its own version (D9,
  R2-14).
- **Unknown means stop.** An entry kind, record version, frame version or
  snapshot version a node does not know stops that node with
  `VERSION_UNSUPPORTED`. It is never skipped: skipping an entry is a fork.
- **Apply version gate.** An entry whose `Version` is above what the local
  state machine supports stops the node the same way, before `Apply` is
  called. The entry's version SELECTS the frozen code path that applies it.
  The `Apply` fingerprint, the gate entry and the replay gate are in ADR 0160
  (Q12).
- **Downgrade.** A node may run an older binary only while the committed
  cluster version is within its range. Raising the cluster version is the
  point of no return, and the operator guide says so.
- **Compression.** The snapshot codec id is recorded, so a snapshot written
  with zstd is refused cleanly by a node built without it.

### D19 — The first consumer's contract: Forgejo, in process (outside the SDK, recorded here) (Q6, R2-35)

The SDK does not implement the replicator. The replicator — the consensus
engine, the ref table and the Git projector — runs INSIDE the Forgejo
process, embedding `pkg/v1/consensus` (Q6). These rules bind it, because they
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
  built on it is ADR 0160's.
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
    restored node getting ready. It runs on `wg0`, with the same admission,
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
  - The proposal endpoint is in process. The hook reaches it over a `0600`
    Unix socket owned by the Git user (ADR 0148).
- **`DISABLE_GIT_HOOKS = true` is mandatory and checked at start (R2-35).** A
  user-supplied server-side hook would let a repository owner run code inside
  the replication path. The node refuses to start the replicator without it.
- **Administration is Forgejo's (Q5, Q7, Q8).** The site administration calls
  the online functions of D17 with the administrator as `Operator`, on
  whichever node the load balancer chose (R3-03). The subcommands of the
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
- **Exposure (R2-37, R3-24).** The consensus and object listeners bind to
  `wg0`. The making.codes deployment lists both ports in
  `common_container_denied_mesh_ports`. This is the only place the ADR names a
  making.codes setting.
- **Memory (R3-24, R5-15).** Forgejo runs with `GOMEMLIMIT` leaving the
  replicator the headroom D14 computes from the measured table. `GOMEMLIMIT` is soft (R4-18).
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
    durable. The table is rebuilt from the log (R4-29).
  - **On the full-stop path, ADR 0152 relies on the payload-version gate of
    D18 and on gate entries (R4-20, R6-04).** A new release's new semantics
    start only at a gate entry, and a proposal above any member's announced
    payload version is refused before append. `forgejo cluster inspect
    --replay`, a mandatory replay before voting, is an ADDITION of ADR 0160,
    not part of this ADR's runbook. "One voter at a time" belongs to the
    rolling path only.
  - In both cases `RaiseClusterVersion` is called only once all three nodes
    run the new release (D18).
- **Snapshot backup (R3-26, R4-05).** `forgejo cluster snapshot save` writes
  the ref snapshot and its SHA-256 side by side, through the ADR 0148 socket,
  never `/api/internal/manager`. The Ansible backup role runs it before the repository backup, and a
  restore pairs it with a PostgreSQL backup taken after it (D17).

## Consequences / Semantics

- **The SDK gains a domain that can make ADR 0052's missing guarantee**:
  agreement across machines, with a fence a resource can check.
- **Every commit costs one network round trip and one `fsync`** on two of
  three nodes. There is no asynchronous mode; a caller that wants one does not
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
- **One declared sibling outside this domain (R2-20):** the three-part TLS
  sibling of D4, in `net`/`tlsid`, with its own ADR. That ADR must be Accepted
  before this one, because `TransportConfig.Identity` is its type (R3-17).
- **Administration is any-node (Q8, R3-03).** No product has to find the
  leader to administer the cluster.
- **This version is not yet reviewed.** The first review run stopped at its
  cap of three rounds. A second run reviewed the R3 version (round 1, applied
  as R4) and the R4 version (round 2, applied as R5). Round 2 also split the
  divergence work into ADR 0160 (Q12). Round 3, the last of that run,
  reviewed the R5 version alone and found no remaining core safety defect.
  Its objections are applied here as R6. No reviewer has read the R6 version.
- The root `CLAUDE.md` domain list, the error-code mirror and
  `codeRangeOwners` change in the implementing change set, not in this one.

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
