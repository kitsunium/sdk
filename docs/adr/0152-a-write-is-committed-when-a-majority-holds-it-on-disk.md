# ADR 0152 — consensus domain (`consensus`): a write is committed when a majority holds it on disk, and the log is compacted from the first version

- **Status**: Proposed
- **Date**: 2026-10-02
- **Deciders**: SDK maintainers
- **Related**: [ADR 0052](0052-sdk-lock-domain.md) (fencing, and the guarantee a lock does NOT make — this domain is the first in the SDK that can make it across machines), [ADR 0056](0056-sdk-vfs-domain.md) (publication by rename, and the streaming writer it deferred), [ADR 0090](0090-a-port-named-in-public-must-be-implementable-in-public.md) (the clock port every timer here runs on), [ADR 0029](0029-sdk-net-domain.md) (the stream connection port and the TLS identity the transport rides), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (frozen ports, siblings), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (clamp or refuse), [ADR 0011](0011-kernel-snapshot-primitive.md) (the copy-on-write view a snapshot can be cut from), [ADR 0068](0068-layer-firewall-is-a-checked-graph.md) (the layers), [ADR 0074](0074-what-a-public-alias-may-point-at.md) (which layer owns a type), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) / [ADR 0035](0035-pp-range-ownership-enforcement.md) (the code ranges), [ADR 0120](0120-a-state-machine-keeps-an-agenda-not-a-sweep.md) (a different "state machine" — see D3)

## Context

The SDK has no consensus. `agree` is key agreement, `lock` excludes goroutines
of one process or processes of one machine (ADR 0052 D6), and ADR 0052 D11
sends any lock that spans machines to `third-party/`, where nothing has been
written. A program that must agree with two other machines on the order of its
writes has nothing to import.

The first consumer has that problem exactly. A replicator makes a Forgejo
deployment active on three nodes — Warsaw (WAW), Frankfurt (FRA), Roubaix
(RBX), measured round trips WAW–FRA 22.8 ms, WAW–RBX 27.3 ms, FRA–RBX 9.6 ms.
It replicates the EFFECTS of Git writes rather than the requests: the objects
are pushed to the peers from the `pre-receive` hook, and the reference update
`{repository, ref, old SHA, new SHA}` is proposed to a replicated log from the
`reference-transaction` hook (`preparing` to order it, `prepared` to have it
committed). Every node applies the log in order, with a compare-and-swap on
the old SHA and a per-repository XOR checksum of `(ref, SHA)` pairs to detect
divergence. A request that follows a write carries the log position the write
committed at, and the node serving it waits until it has applied that far.
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

### D1 — The name says the capability, the constructor says the algorithm

The domain is named for what a caller gets — agreement on an order — not for
the algorithm that produces it, the way `lock` is not called `flock` and
`token` is not called `jwt`. The algorithm is named once, where it is chosen:
`consensus.NewRaft`. Rejected names:

- **`raft`** names an implementation. The ports (D3, D4) speak terms, indexes,
  entries and memberships, which Multi-Paxos or Viewstamped Replication would
  speak as well; a second engine would otherwise live in a package named after
  the first.
- **`replica`** names a participant, not the capability, and reads as a
  database read replica — the asynchronous copy this domain exists NOT to be.
- **`agree`** is taken by key agreement (ADR 0014), and a reader meeting
  `agree.Propose` beside `agree.SharedKey` would be right to be confused.

No registry, for ADR 0052 D10's reason sharpened: an engine resolved from a
configuration string is a place where a typo selects different safety
properties with no failure at the moment of the swap.

### D2 — Placement and allowed dependencies

| Layer | Package | Holds | May import |
|---|---|---|---|
| core | `internal/core/consensus` | every type a port speaks (ADR 0074): `NodeID`, `GroupID`, `Position`, `Entry`, `EntryKind`, `HardState`, `Membership`, `Message`, `SnapshotMeta`; the ports `StateMachine`, `Snapshot`, `LogStore`, `SnapshotStore`, `Transport`; the `0.2.57.*` sentinels | kernel only (`errs`, `clock`) |
| service | `internal/service/consensus` | the Raft engine (a pure step function and its driver), the segmented file log, the snapshot store, the mTLS stream transport, the in-memory log/transport doubles, the deterministic simulator's fault models (test-only) | kernel; core `consensus`, `net`, `lock`, `metrics`, `trace`, `logger`; service `lock` (the data-directory lock, D11), `net/server` (the inbound stream group) |
| pkg | `pkg/v1/consensus` | type aliases, `NewRaft`, `Bootstrap`, the adapters' constructors, the sentinels | its own lower layers, sibling `pkg/v1/*` |

The service-to-service edges are lateral and permitted (`internal/CLAUDE.md`).
Nothing is added to the kernel: the step function is generic only to Raft, so
it fails rule 1's second half. Stdlib only — no `go.etcd.io`, no
`hashicorp/raft`, no `golang.org/x/*` — so `pkg/v1/consensus` stays as
dep-light as `lock`.

### D3 — The caller's state machine is a frozen three-method port

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
```

`Apply` is called for every committed `EntryKind` `Command`, in log order, on
ONE goroutine, exactly once per entry per incarnation of the state, and must
be **deterministic**: the same entries in the same order produce the same
state on every node. It returns a value, delivered only to the `Propose` that
created the entry on the node that created it. It returns no error, and that
is the design: a command a state machine refuses (the old SHA did not match)
is an ANSWER, carried in the result, and it must be the same answer on every
node. An apply that cannot complete — a disk failure under `git update-ref` —
is not a refusal; the state machine must stop the process, because a node that
skips an entry has silently forked. `StateMachine` panicking is treated the
same: the node stops with `STATE_MACHINE_FAILED` and does not apply further.

raft-sql-poc is the counter-example this rule exists for. Its core treats
every exception `apply` throws as deterministic and part of the output
(`raft.js:1109-1117`), and its SQLite state machine throws
"changeset conflict on apply: replica state diverged" (`sqlstore.js:83`).
A divergence the replica has DETECTED therefore reaches the proposer as an
ordinary `SQL_ERROR` while the replica keeps applying on a forked state.

`Snapshot` is called on the apply goroutine and must be CHEAP: it captures a
view at the current applied position (the kernel's copy-on-write snapshot,
ADR 0011, is the intended tool) and returns. `WriteTo` then runs on another
goroutine while `Apply` continues, and `Release` frees the view. `Restore`
replaces the whole state.

The name collides on purpose with the textbook and must not be confused with
ADR 0120's `statemachine`, which moves entities between declared states on a
timer. That domain decides WHAT state an entity is in; this port is HOW a
deterministic program is replicated. Both package comments say so.

### D4 — The other ports: log, snapshots, transport, all frozen (ADR 0039)

```go
type LogStore interface {
	Append(entries []Entry) error          // buffered, not durable
	Sync() error                           // durable up to the last Append
	Entries(lo, hi uint64, maxBytes int, dst []Entry) ([]Entry, error)
	Term(index uint64) (uint64, error)
	Bounds() (first, last uint64)
	TruncateSuffix(from uint64) error      // drop a conflicting tail
	CompactPrefix(through uint64) error    // drop what a snapshot covers
	HardState() (HardState, error)         // term, vote, commit
	SetHardState(HardState) error          // durable on return
}
type SnapshotStore interface {
	Create(meta SnapshotMeta) (SnapshotSink, error) // Write, Commit, Abort
	Latest() (SnapshotMeta, io.ReadCloser, error)
}
type Transport interface {
	Send(to NodeID, m *Message)                       // never blocks; may drop
	Stream(ctx context.Context, to NodeID, meta SnapshotMeta, r io.Reader) error
	Bind(group GroupID, deliver func(*Message), install func(SnapshotMeta, io.Reader) error) error
}
```

`Append` and `Sync` are separate so that one `Sync` covers a batch (D6). The
contract that carries the safety of the whole domain is `Sync` and
`SetHardState`: when they return nil, what they cover survives a power loss.

**The shipped log** is a directory of preallocated segment files, each record
`length | CRC-32C | term | index | kind | payload`, little-endian, written with
`write(2)` into the current segment and made durable with `fdatasync(2)` once
per batch. On open the tail is scanned: a record whose CRC fails AT THE TAIL
is a write the process died inside, never acknowledged because acknowledgement
follows `Sync`, and it is truncated and counted. A CRC failure BEFORE the tail
is corruption and is refused, never repaired (`LOG_CORRUPT`) — the same choice
as ADR 0052's `LOCK_FENCE_CORRUPT`: a log repaired by guessing has lost the
property that made it a log. **The hard state is a record of the log, from raft-sql-poc.** The first
draft kept term and vote in a separate file published by rename, which costs
two device round trips per vote (ADR 0056's measurement). The POC writes a
hard-state record into the current batch instead, made durable by the same
`fdatasync` as the entries, and writes the commit index lazily so that a
heartbeat which only advances it never makes an acknowledgement wait for a
sync (`storage.js:361-370`). Every segment opens with a checkpoint record
holding the hard state, and segments are deleted only as a prefix: together
the two rules let compaction delete old segments without losing a vote or
resurrecting an entry a later record truncated (`storage.js:35-37`,
`storage.js:507-591`). Adopted. It was not measured head to head, and the
reason is structural: a vote then costs nothing beyond the sync the batch
pays anyway.

A failing `fsync` stops the node (`STORAGE_FAILED`) and is never retried. The
PostgreSQL "fsyncgate" of 2018 established that a retried `fsync` can return
success after the kernel has dropped the dirty pages the first one failed to
write; a node that continues after one is a node whose acknowledged entries
may not exist. raft-sql-poc does not retry either, but it does not stop: after a failed
`fsync` it records the error and never syncs again (`storage.js:489-495`),
so every later acknowledgement waits forever. That is safe and silent. Here
the node stops and says so.

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

**The shipped transport** rides ADR 0029. Inbound is a stream group on
`pkg/v1/server` with `server.TLS(identity)` and a `ConnHandler`; outbound
dials with the same `tlsid.Identity`'s `ClientConfig()` (the HTTP client of
`pkg/v1/client` is not a stream dialer). mTLS is mandatory and the peer's
certificate must name the `NodeID` the configuration gives for that address
(`PEER_REJECTED`); the first frame on a connection names the cluster and
group, and a mismatch closes it (`CLUSTER_MISMATCH`).

**Admission follows the membership, from raft-sql-poc.** The transport
admits only the `NodeID`s of the current membership, learners included. A
`Remove` that commits cuts that node's connections and refuses its
handshakes from then on. The POC learned that draining a machine is not
removing it: with a credential every machine shared, a removed machine
could come back (README bug 8). Its fix revokes the removed machine's key
on every peer (`shard.js:796-803`, `transport.js:110-120`). Here a `NodeID`
is never reused (D8), so a certificate whose node is no longer a member is
refused without a revocation list.

**Three limits TLS does not set, from raft-sql-poc.** A frame's announced
length is checked against `MaxFrameBytes` before anything is allocated. A
snapshot is inflated no further than the size its metadata announces,
before its hash is checked (`raft.js:1272-1289`). A connection that has not
finished its handshake within `HandshakeTimeout` is closed. These are three
of the twenty attacks the POC's transport suite runs. TLS 1.3 already stops
the others: a forged identity, a downgrade, a replay, reordering, a
substituted ephemeral key.

**Batching on the wire, from raft-sql-poc.** Every frame queued for a peer
during one turn of the loop leaves in one write (`transport.js:1-8`). The
per-peer queue is bounded and drops when full, because Raft retransmits
what matters and an unbounded queue turns an outage into memory
exhaustion (`transport.js:294-296`, 64 MiB in the POC).

**Why TLS and not the POC's handshake.** The POC wrote a SIGMA handshake of
its own over Ed25519, X25519 and AES-256-GCM (`secure.js:1-60`). Its README
lists two cryptographic bugs it found in its own earlier versions: a
`SHA256(key ‖ msg)` MAC open to length extension, and a static key whose
nonce counter restarted at zero (bugs 5 and 6). TLS 1.3 gives the same
properties from the standard library: a signed transcript, ephemeral keys,
record sequence numbers. The SDK does not write a handshake. Two connections per
peer and direction: one for messages, one for snapshot streams, so a
multi-megabyte snapshot never sits in front of a heartbeat. Frames are
`length | type | group | body`, hand-encoded; TLS gives the integrity, so the
wire carries no checksum of its own.

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
  timeout refuses votes** (thesis §4.2.3), unless the request carries the
  leadership-transfer flag (D8). This is the vote-side half of CheckQuorum and
  what makes leases safe.

Neither is a configuration option. Both exist to prevent failures that only
appear under partition, which is where nobody tests a disabled flag.

**A node that starts refuses votes for one election timeout, from
raft-sql-poc.** It behaves as if it had just heard from a leader
(`raft.js:205-213`). Otherwise a node restarted inside a lease window votes
at once, and a second leader can be elected while the first still serves
lease reads. The vote refusal also applies to `RequestVote`, not only to
`PreVote`. The POC checks for a live leader in PreVote only
(`raft.js:524-531` against `raft.js:588-620`). That holds until an election
skips PreVote, and a leadership transfer does (`raft.js:563`,
`raft.js:827`).

**Timings, defaults for this RTT matrix:** heartbeat every 100 ms; election
timeout randomized in [1 s, 2 s). A zero is clamped to the default (ADR 0031,
clamp half: there is one sensible order of magnitude); a heartbeat not below
a fifth of the minimum election timeout is refused (`CONSENSUS_MISCONFIGURED`),
because that is the configuration in which a single delayed heartbeat starts
an election. Failover is therefore about 1–2 s, against Gitaly's 4 s, and the
PreVote round adds one RTT.

### D6 — Replication: pipelined, batched with no linger timer, leader writes in parallel

- **Pipeline.** Per follower, the leader tracks a `Progress` (match, next,
  state `probe` / `replicate` / `snapshot`). In `replicate` it sends the next
  `AppendEntries` without waiting for the previous answer, up to
  `MaxInflight` messages (default 64) or `MaxInflightBytes`; a rejection drops
  the follower back to `probe`.
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
  counted only once its own `Sync` returns. With three nodes the commit is the
  later of the leader's `fsync` and the nearest follower's round trip plus its
  `fsync`.
- **Followers acknowledge after `Sync`**, never before. A follower that
  answered first would be counted in a majority it may not be part of after a
  crash.
- **A follower forwards proposals to the leader** over the same connection.
  This is safe here where it was not for Gitaly, because the domain validates
  NOTHING before ordering: the compare-and-swap is part of `Apply`, evaluated
  against the state at the entry's position on every node alike. The cost of
  a write proposed on WAW is therefore one round trip to the leader on top of
  the commit.
- **A new leader commits a no-op entry of its term** before it serves a read
  or accepts a membership change (thesis §6.4, §4.1).
- **Three rules the POC learned as safety bugs** are stated here rather than
  left to the implementation. All three are silent on the happy path and end
  in divergence (README bugs 1 to 3). A follower advances its commit index to
  at most `min(leaderCommit, prevLogIndex + len(entries))`, the prefix this
  message verified, never the end of its log. Its acknowledgement reports
  that verified prefix, never `lastIndex` (`raft.js:1020-1032`). A node that
  steps down within the same term keeps its vote (`raft.js:711-718`).
- **The pipeline stays bounded.** The POC pipelines optimistically with no
  bound on what is in flight (`raft.js:937-939`), relying on the transport
  queue's cap. This ADR keeps `MaxInflight`: on a 27 ms link an unbounded
  window turns one slow follower into megabytes of retransmission.

**Outcome reporting is three-valued.** `Propose` returns the `Position` and
the `Apply` result once applied locally; `NOT_LEADER` when the entry was
certainly not appended (the error names the known leader, readable with
`consensus.LeaderHint`); and `OUTCOME_UNKNOWN` when the entry was appended but
leadership was lost before it committed — it may still commit under the next
leader. A caller that maps `OUTCOME_UNKNOWN` to "failed" is wrong in a way
that loses no data only if its state machine converges anyway; the first
consumer's does, because every node applies the log and the
compare-and-swap of a duplicate fails identically everywhere. The
`pkg/v1` doc comment says this in so many words.

**A context that ends after the append is `OUTCOME_UNKNOWN` too, from
raft-sql-poc.** Its cause is `ctx.Err()`, but the code is not
`ctx.Err()` alone. The POC's `COMMIT_TIMEOUT` (`raft.js:850-858`) is this
case under another name, and a caller that reads it as "failed" retries an
entry that may still commit. A context that ends before the append returns
`ctx.Err()`, as everywhere in the SDK.

### D7 — Snapshots and compaction ship in v1

- **Trigger:** after `SnapshotEvery` applied entries (default 16 384) or
  `SnapshotEveryBytes` of log (default 64 MiB), whichever comes first.
- **Cut:** `StateMachine.Snapshot` on the apply goroutine; `WriteTo` streams
  into `SnapshotStore.Create` off it, with a SHA-256 of the stream recorded in
  `SnapshotMeta` beside `Position` and `Membership`. Only one snapshot is in
  flight; a trigger during one is coalesced.
- **Compaction:** after the snapshot is committed to disk, `CompactPrefix`
  drops the log through `snapshot.Index − SnapshotTrailing` (default 4 096):
  a follower slightly behind catches up from the log rather than receiving a
  whole snapshot.
- **Install:** a follower whose `next` is below the leader's first index
  receives the latest snapshot on the snapshot connection, in chunks, verified
  against its SHA-256 before `Restore` (`SNAPSHOT_CORRUPT` otherwise), then
  the log from there.
- **Retention:** the previous snapshot is kept until the new one is durable,
  so a crash during publication leaves one valid snapshot.
- **Compression, from raft-sql-poc.** The snapshot stream passes through a
  `transform` codec chosen at construction: flate from
  `internal/service/transform` by default, or zstd from
  `third-party/transform` when the caller supplies it, since `pkg` may not
  import `third-party` (ADR 0068). The POC measured ÷10.8 with zstd level 3
  on a realistic state. Adding a machine cost 41 to 43 KB of snapshot
  instead of 204 to 205 KB of replayed log. The POC also compresses once per
  snapshot and sends every lagging follower the same bytes
  (`raft.js:1176-1194`). Messages are not compressed: under encryption,
  compressing attacker-influenced data with other data leaks through the
  ciphertext's length (CRIME/BREACH), which is why the POC turns compression
  off in its `aead` mode.

The POC cuts a snapshot synchronously on the apply path and holds it whole
in memory (`raft.js:1151-1159`, `storage.js:397-431`). This ADR keeps the
copy-on-write cut and the streamed write: the first consumer's state grows
with `refs/pull/*`, and a pause in apply is a pause in every write.

For the first consumer the snapshot is the ref table and the per-repository
checksums, not Git objects — those travel outside the log. A node restoring a
snapshot must still fetch the objects its refs name; that is the replicator's
duty and the reason `Restore` receives the `Position` it restores to.

### D8 — Membership: one server at a time, through learners

Single-server changes (thesis §4.1), each a log entry that takes effect on a
node when APPENDED, not when committed. Joint consensus (§4.3) is not
implemented in v1:

- With three voters, every change a deployment needs — add, remove, replace —
  decomposes into one-at-a-time steps. Joint consensus buys arbitrary changes
  in one step; nothing here asks for that, and it is the larger and less
  tested path in every implementation that has both.
- The known defect of single-server changes (Ongaro, raft-dev, 2015: a
  change proposed by a leader that has not yet committed an entry of its own
  term can produce two majorities) is closed by the rule of D6's last bullet:
  no membership entry before the no-op of the term is committed.
- One change at a time: a second proposal while one is uncommitted is
  `MEMBERSHIP_CHANGE_PENDING`. Removing the last voter, or a node not in the
  membership, is `MEMBERSHIP_INVALID`.

**Learners.** `AddLearner` adds a non-voting member that receives the log and
snapshots but counts in no majority. `Promote` makes it a voter and is refused
until the learner is within `PromoteLag` entries (default 1 024) of the
leader's commit index — a voter added while it is behind can make the cluster
unavailable, because the majority now needs a node that cannot yet
acknowledge. Replacing a node is therefore: learner, catch up, promote (four
voters, majority three), remove the old node. The POC checks
catch-up in its orchestrator, outside the engine (`shard.js:662-676`). Here
`Promote` refuses a lagging learner itself, so no caller can forget. As in
the POC (`shard.js:707-716`), removing the current leader first transfers
leadership, and a learner never runs an election timer
(`raft.js:344-356`). During that window a majority of
four spans at least two sites; the operator guide says so.

**A node ID is never reused.** A node whose disk was lost and that rejoined
under its old ID could vote twice in one term — once before the loss, once
after — and elect two leaders. The data directory is stamped with
`(ClusterID, NodeID)` at bootstrap; a rejoining machine with an empty disk
needs a new `NodeID` and joins as a learner. Bootstrap is an explicit, one-time
`Bootstrap` call: a node opening an empty directory does NOT form a cluster
on its own, because "a wiped node bootstraps itself" is how a second cluster
with the same name is born.

**Leadership transfer.** `TransferLeadership(ctx, to)` stops accepting
proposals, brings `to` up to date, and sends it `TimeoutNow`; `to` campaigns
at once, its vote requests flagged so D5's vote refusal does not apply. It
fails with `TRANSFER_FAILED` after one election timeout and the old leader
resumes.

**A transfer loses nothing in flight, from raft-sql-poc.** From the moment
it starts, `TransferLeadership` refuses new proposals with `NOT_LEADER`,
which is retryable since nothing was appended. It sends `TimeoutNow` only
once every entry already proposed is COMMITTED (`raft.js:789-809`,
`raft.js:841-846`). The POC's first version stepped down with entries in
flight and answered them "outcome unknown", and a retrying client applied
them twice (README bug 7). The POC measured 10 ms for a transfer, against at
least one election timeout for an election. `PreferredLeaders` in the configuration names the nodes a leader
transfers to after an election when it is not one of them — here FRA and RBX,
9.6 ms apart, so a commit costs ~10 ms of network instead of ~23.

### D9 — Reads: ReadIndex by default, a lease only on a stated clock bound

`Barrier(ctx)` returns once the local state machine reflects every write
committed before the call, and returns the applied index:

- **ReadIndex** (default; thesis §6.4). The leader records its commit index,
  confirms it is still leader with one heartbeat round acknowledged by a
  majority (concurrent barriers share the round), which the POC
  measured at 3 quorum rounds for 2 000 concurrent linearizable reads,
  at the price of −13 % for strictly sequential ones, each of which then
  pays its own round, waits until applied reaches
  the recorded index, and answers; a follower asks the leader for the index
  and waits locally. Cost: one round trip to the nearest peer from the leader.
- **Lease** (`Reads: ReadLease`, opt-in). The leader serves without the
  round while its lease holds: the lease starts at the SEND instant of a
  heartbeat round a majority acknowledged and lasts
  `ElectionTimeoutMin × (1 − MaxClockDrift)`, measured on the injected
  `clock.Clock`'s monotonic `Since`. It is sound only if (a) no node's clock
  RATE differs from another's by more than `MaxClockDrift` — absolute
  agreement is not needed; (b) the vote refusal of D5 holds, which it always
  does here; (c) no transfer is in progress — the lease is dropped the moment
  `TransferLeadership` starts, because a transfer elects a new leader without
  waiting for the timeout the lease relies on. The lease is also used
  only when the leader has applied its commit index and that index is past
  the no-op of its term, as in the POC (`raft.js:1427-1432`). `MaxClockDrift` has no default:
  `ReadLease` with a zero drift is refused at construction (ADR 0031, refuse
  half — zero reads as "clocks are perfect", which no clock is).

  What a lease does NOT guarantee is stated as loudly as ADR 0052 D5 states
  it for fences: a leader process paused AFTER its lease check and BEFORE its
  answer (a GC pause, a VM steal, `SIGSTOP`) serves a read that may be stale.
  ReadIndex has no such window, which is why it is the default.

  **Two gaps in the POC's lease, which this ADR closes.** It measures the
  lease on `Date.now()` (`raft.js:1495-1506`), a wall clock that NTP can
  step, where this ADR uses the monotonic `Since` of the injected clock. It
  also keeps the lease while a transfer runs (`raft.js:756-787` does not
  clear it), and the transfer's `RequestVote` skips the live-leader check.
  So if the vote request to the old leader is delayed or lost, the old
  leader can serve a lease read after the new leader has committed a write.
  That is condition (c) above, and it is a case the simulator's
  linearizability checker must find.

  The POC also offers a `leader` read: a local read on the leader, with no
  confirmation. This ADR does not: it is exactly the read a deposed leader
  serves stale. Its `stale` read is any local read, which needs no API.

- **`WaitApplied(ctx, index)`** is the read-after-write cookie of the first
  consumer: wait until the local node has applied `index`. It gives
  read-your-writes and monotonic reads to a client that carries its index; it
  is NOT a linearizable read for a client that does not, and the doc comment
  says so.

### D10 — Fencing: every applied entry carries `(term, index)`

`Entry.Position{Term, Index}` is handed to `Apply`. `Index` alone is a fencing
token in ADR 0052 D5's sense: unique, strictly increasing over the whole
group's life, across terms, and identical on every node. `Term` is the
leadership epoch: a side effect performed by a leader (a webhook, a CI
trigger) and tagged with its term can be refused by a receiver that has seen
a higher one.

This is the answer to ADR 0052 D11. A lock over this domain is a state
machine whose acquire entry's `Index` is the lease's `Fence()`; it needs no
third-party system, so it belongs in the SDK, and it inherits D11's sentence
unweakened: without a resource that compares the fence, mutual exclusion
against a stalled holder is not guaranteed. The `lock.Locker` adapter itself
is deferred (see Deferred); `Position` is shaped so it fits `Fence() uint64`
without conversion.

### D11 — One group in v1, an API that does not forbid more

- Every `Message` and frame carries a `GroupID`; v1 runs exactly one group,
  `1`. A v2 that multiplexes groups on one `Transport` changes no wire format.
- `Transport.Bind` is keyed by group, and one transport serves a process.
- `Node` is per group; nothing is process-global except the transport.
- The data directory is held for the node's life through `lock.NewFileLocker`
  (ADR 0052), so a second process opening the same log fails at once
  (`DATA_DIR_LOCKED`) instead of interleaving appends.

Heartbeat coalescing and quiescing idle groups — the two things a
group-per-repository design needs and Gitaly never built — are NOT in v1,
because one group has nothing to coalesce. raft-sql-poc built both and
measured them, and they are the v2 specification. ONE heartbeat frame per
pair of machines carries every quiescent group, delta-encoded: 4 bytes for a
group whose term and commit the receiver already confirmed, and a 52-byte
"all clear" acknowledgement (`host.js:1-27`, `raft.js:1351-1368`). At 256
shards on 6 machines that took idle traffic from 10 240 to 300 to 320
messages/s (÷32 to ÷34). ONE WAL per machine is shared by all its groups,
so a tick costs one `fsync` however many groups wrote: in the POC, 1 000
entries over 4 shards made 1 fsync. The `LogStore` port does not forbid it:
a shared store hands out per-group views whose `Sync` is the same call. The
POC also measured rendezvous hashing for placement and balanced leadership
to within one. And it measured the cost: with 8 shards at replication 3 on
6 machines, losing 2 machines left 4 of 8 shards unavailable. A sharded
cluster's availability is no longer a boolean, which is one more reason v1
has one group.

### D12 — Errors

Core `0.2.57.*` (`0x00_02_39_*`), allocated in `codeRangeOwners` in the
change that introduces them (ADR 0035):

| Code | Reason | Meaning |
|---|---|---|
| `0.2.57.1` | `CONSENSUS_MISCONFIGURED` | refused at construction (D5, D9, missing ID, empty membership) |
| `0.2.57.2` | `NOT_LEADER` | certainly not appended; the known leader in `Fields`, read with `LeaderHint` |
| `0.2.57.3` | `OUTCOME_UNKNOWN` | appended, then leadership lost or the context ended before commit — may still commit; a context's end is its cause |
| `0.2.57.4` | `LEADERSHIP_UNCONFIRMED` | a barrier's round did not reach a majority in time |
| `0.2.57.5` | `MEMBERSHIP_CHANGE_PENDING` | one change at a time |
| `0.2.57.6` | `MEMBERSHIP_INVALID` | unknown node, last voter, promotion of a lagging learner |
| `0.2.57.7` | `TRANSFER_FAILED` | the target did not win within one election timeout |
| `0.2.57.8` | `ENTRY_TOO_LARGE` | a command above `MaxEntryBytes` |
| `0.2.57.9` | `COMPACTED` | an index below the first retained one |
| `0.2.57.10` | `STATE_MACHINE_FAILED` | `Apply` panicked; the node stopped applying |
| `0.2.57.11` | `NODE_STOPPED` | the node was closed or stopped itself |

Service `0.3.92.*` (`0x00_03_5C_*`):

| Code | Reason | Meaning |
|---|---|---|
| `0.3.92.1` | `LOG_CORRUPT` | a CRC failure before the tail; never repaired |
| `0.3.92.2` | `HARD_STATE_CORRUPT` | the term/vote file is unreadable; never defaulted |
| `0.3.92.3` | `SNAPSHOT_CORRUPT` | a snapshot's SHA-256 does not match its metadata |
| `0.3.92.4` | `STORAGE_FAILED` | a write or `fsync` failed; the node stops, no retry |
| `0.3.92.5` | `DATA_DIR_LOCKED` | another process holds the data directory |
| `0.3.92.6` | `DATA_DIR_MISMATCH` | the directory is stamped with another cluster or node |
| `0.3.92.7` | `PEER_REJECTED` | the certificate does not name the expected node |
| `0.3.92.8` | `CLUSTER_MISMATCH` | a peer's handshake names another cluster or group |
| `0.3.92.9` | `FRAME_INVALID` | a frame that does not parse; the connection closes |

A context that ends before anything is appended returns `ctx.Err()`, as
everywhere in the SDK. One that ends after the append is `OUTCOME_UNKNOWN`
with `ctx.Err()` as its cause (D6).

### D13 — Observability

Through `metrics` (ADR 0044), every name prefixed `consensus.` and attributed
by `group`: commit latency and `fsync` latency histograms, batch size in
entries and bytes, apply lag (`commit − applied`), per-peer match lag and
inflight, term and role gauges, leader changes, elections started and
PreVotes lost, snapshot duration and bytes, torn tails truncated, barrier
latency by mode. Through `trace` (ADR 0051): one span per `Propose` from
submission to local apply, with events at append, commit and apply; the trace
context travels in the forwarding frame only — never in the log, which must
not grow with telemetry. Through `logger`: role and term changes, membership
changes, snapshots and truncations at `Info`; nothing per entry.

### D14 — Public surface (sketch, signatures only)

```go
package consensus // pkg/v1/consensus

type (
	NodeID   = coreconsensus.NodeID   // uint64, never 0, never reused
	GroupID  = coreconsensus.GroupID
	Position = coreconsensus.Position // struct{ Term, Index uint64 }
	Entry    = coreconsensus.Entry    // Position, Kind, Data []byte

	StateMachine  = coreconsensus.StateMachine
	Snapshot      = coreconsensus.Snapshot
	LogStore      = coreconsensus.LogStore
	SnapshotStore = coreconsensus.SnapshotStore
	Transport     = coreconsensus.Transport

	Member     = coreconsensus.Member     // ID, Address, Voter bool
	Membership = coreconsensus.Membership // the members at a Position
	Status     = svcconsensus.Status      // Role, Term, Leader, Commit, Applied
	ReadMode   = svcconsensus.ReadMode    // ReadIndex (zero value) | ReadLease
	Config     = svcconsensus.Config
	Node       = svcconsensus.Node
)

type Config struct { // svcconsensus.Config, shown for its fields
	Cluster          string
	Group            GroupID
	ID               NodeID
	Machine          StateMachine
	Log              LogStore
	Snapshots        SnapshotStore
	Transport        Transport
	Clock            clock.Timed
	HeartbeatInterval, ElectionTimeoutMin, ElectionTimeoutMax time.Duration
	MaxBatchBytes, MaxInflight, MaxEntryBytes                  int
	SnapshotEvery, SnapshotTrailing, PromoteLag                uint64
	Reads            ReadMode
	MaxClockDrift    float64 // required with ReadLease
	PreferredLeaders []NodeID
	Meter            metrics.Meter
	Tracer           trace.Tracer
	Logger           logger.Logger
}

func Bootstrap(ctx context.Context, cfg Config, members []Member) error
func NewRaft(cfg Config) (*Node, error)

func OpenLog(dir string) (LogStore, error)                 // segmented, fdatasync per batch
func OpenSnapshots(dir string) (SnapshotStore, error)
func NewTCPTransport(id tlsid.Identity, self NodeID, peers map[NodeID]string) (Transport, error)
func NewMemoryLog() LogStore                               // tests
func LeaderHint(err error) (NodeID, bool)

func (n *Node) Propose(ctx context.Context, data []byte) (Position, any, error)
func (n *Node) Barrier(ctx context.Context) (uint64, error)
func (n *Node) WaitApplied(ctx context.Context, index uint64) error
func (n *Node) AddLearner(ctx context.Context, m Member) error
func (n *Node) Promote(ctx context.Context, id NodeID) error
func (n *Node) Remove(ctx context.Context, id NodeID) error
func (n *Node) TransferLeadership(ctx context.Context, to NodeID) error
func (n *Node) Membership() Membership
func (n *Node) Status() Status
func (n *Node) Close(ctx context.Context) error
```

`Config` and `Node` belong to the engine and are aliased from service; every
type a port speaks lives in core (ADR 0074). The frozen ports are the five
interfaces; every later capability is a sibling discovered by assertion
(ADR 0039) — a `LogStore` that can report its on-disk size, for example.

### D15 — Testing: a deterministic simulator first, real clusters second

The engine is split so that it can be tested by simulation: a **step
function** with no goroutine, no I/O and no clock — `Tick()`, `Step(*Message)`,
`Propose(...)`, and a `Ready()` that returns what to persist, what to send and
what to apply — and a **driver** that performs those effects. Everything that
decides safety is in the step function.

**The simulator** (test-only, under the service package) runs N step functions
on ONE goroutine against a virtual clock (`clock.ManualClock`) and an event
queue, so a run is a pure function of its seed:

- a network that delays every message by a draw from the measured RTT matrix
  (9.6 / 22.8 / 27.3 ms, with jitter), drops, duplicates and reorders it with
  seeded probabilities, and applies partition schedules — total, asymmetric
  (A hears B, B does not hear A), and the "bridge" where one node sees both
  halves;
- a disk model that distinguishes WRITTEN bytes from SYNCED bytes: a crash
  drops everything written since the last `Sync`, and optionally tears the
  last unsynced record at a 512-byte boundary or flips a bit in it — the
  "crash in the middle of an fsync" case, and the one that proves the tail
  scan of D4 truncates what it must and refuses what it must;
- crash and restart at any event, including between a follower's `Sync` and
  its acknowledgement, and between a leader's append and its `Sync`;
- clock-rate skew per node, inside and outside `MaxClockDrift`.

**Checked after every event:** at most one leader per term; Log Matching;
Leader Completeness; State Machine Safety (every node's applied sequence is a
prefix of the longest); every `Propose` that returned success is in every
applied sequence from then on; terms and commit indexes never go backwards on
disk across a restart.

**Linearizability.** Clients of a key-value state machine record
`invoke` / `return` pairs with virtual timestamps, through `Propose`,
`Barrier` + local read, and lease reads; the history is checked with the
Wing & Gong search with Lowe's memoization, partitioned per key (the
algorithm Porcupine and Knossos implement). It is written in the test tree —
about three hundred lines, stdlib — rather than imported: the SDK has
implemented a specification before importing a library twice already (the
metrics data model, OTLP/JSON), and a checker the tests depend on is worth
owning. **A negative control is mandatory**: a run with lease reads and clock
skew beyond `MaxClockDrift` must produce a history the checker REJECTS, as
`TestWallClockAuditDetectsAViolation` proves ADR 0052's audit fires. A checker
never seen failing is not a checker.

**Lanes (rule 12).** A short simulation (a few hundred seeds) runs in
`bazel test //...`. A long one (`make test-consensus-soak`, tens of thousands
of seeds) is a named lane, declared in the package `CLAUDE.md` and run on a
schedule in CI; a failure prints the seed, and `CONSENSUS_SIM_SEED=<n>`
replays it bit for bit. A real three-process test over loopback mTLS with
injected latency runs in the normal suite; the `tc netem` reproduction of the
three-site matrix on three containers belongs to the first consumer's
repository, not the SDK's.

**No test sleeps.** Every timer is on the injected clock, with ADR 0052's
wall-clock AST audit copied over and its own detecting negative test.

**Confirmed by raft-sql-poc, in its own words.** Its tests are chosen
scenarios on real timers. It uses `Date.now`, `setTimeout`, and
`Math.random` for loss and jitter (`network.js:94`, `network.js:112`), with
no seed to replay. Its README lists "no deterministic simulation test nor
linearizability checker" among what still separates it from a product.
What it did prove is the shape of the power-loss test: cut power on every
machine at once, right after the last acknowledgement, and count. It got
300 of 300 acknowledged writes back in simulation, and 622 of 622 over
encrypted TCP to disk. Its disk model keeps only the bytes covered by the
last completed `fsync` (`storage.js:601-615`). The simulator here keeps
that, and adds a torn record and a flipped bit.

**Mutation controls, from raft-sql-poc.** The POC removes each of its 10
transport defenses one at a time and requires each removal to be detected
(`tools/security-mutations.js`). It also reinstates its old leadership
balancer to prove the test fails (`tools/balance-mutation.js`). The
simulator gets the same discipline. Behind test-only switches it
reintroduces each of the POC's three core safety bugs (D6) and an
acknowledgement sent before the sync, and the suite must fail on each. In
the POC that last switch, `unsafeAckBeforeSync` (`raft.js:77-80`), lost 50
to 139 of 300 acknowledged writes. The transport's identity, admission and
size checks get the same treatment.

**Benchmarks** ship with `BENCH.md` (rule 9) and allocation gates in the
race-off lane (`tools/alloc-lane-targets.txt`, rule 12):

| Benchmark | Budget |
|---|---|
| step function, leader handling one `AppendEntries` response | 0 allocs/op |
| follower handling `AppendEntries` of 64 entries, memory log | ≤ 1 alloc/op amortized (the entry slice), 0 per entry |
| encode / decode of a log record and a frame | 0 allocs/op |
| `Propose` → applied, 3 in-process nodes, memory log and transport | ≤ 4 allocs/op |
| segmented log, `Append` 64 × 256 B + one `Sync` | reported per device, as ADR 0056's `BENCH.md` reports publication |

### D16 — Performance targets, from the RTT matrix

Commit latency from a leader is `max(fsync_leader, RTT_nearest + fsync_peer)`
plus processing. The VPS's `fdatasync` latency has NOT been measured; it is
the first number the prototype records, and the table assumes 1–3 ms.

| Leader | Proposal from | Expected commit + local apply |
|---|---|---|
| FRA or RBX | the leader's node | ≈ 9.6 ms + fsync ≈ 11–13 ms |
| FRA or RBX | the other of the pair | + 9.6 ms forward ≈ 21–23 ms |
| FRA or RBX | WAW | + ~23–27 ms forward ≈ 33–40 ms |
| WAW (avoided by D8) | anywhere | ≈ 22.8 ms + fsync, + forward |

Targets, to be verified on the three VPS and recorded in `BENCH.md` — missing
one is a finding, not a renegotiation:

- p99 commit latency from an FRA or RBX leader ≤ 2 × the nearest RTT + 2 ×
  the measured fsync p99, at 1 000 proposals/s;
- sustained ≥ 10 000 entries/s of 256 B committed with one `fsync` per batch,
  p99 under 50 ms;
- failover — leader killed to first commit under the new one — p99 ≤ 2.5 s;
- `Barrier` in ReadIndex mode from the leader ≈ one nearest RTT; from WAW ≈
  one RTT to the leader plus that.

The first consumer needs a few hundred writes per second at most; the targets
are set an order of magnitude above it so the SDK does not ship a ceiling.

**A floor, from raft-sql-poc.** The POC ran in one Node.js process on macOS
with a real `F_FULLFSYNC` of 4.6 to 5.4 ms. It measured 7 000 to 9 400
durable writes/s, replicated 3 times on 4 shards. Over TCP with AES-256-GCM
and to disk, on 8 shards across 4 machines, it measured 5 300 to 5 700
durable writes/s. A loopback round trip took 45 µs at p50 under AEAD. A Go
implementation on NVMe with a 1 to 3 ms `fdatasync` should not be below
those numbers. They are evidence that the 10 000 entries/s target is
reachable, not a target themselves.

## Consequences / Semantics

- **The SDK gains a domain that can make ADR 0052's missing guarantee**:
  agreement across machines, with a fence a resource can check.
- **Every commit costs one network round trip and one `fsync`** on two of
  three nodes. There is no asynchronous mode; a caller that wants one does not
  want this domain.
- **A minority is read-only and says so.** A node cut off from the majority
  fails `Propose` with `NOT_LEADER` or `OUTCOME_UNKNOWN`, fails `Barrier` with
  `LEADERSHIP_UNCONFIRMED`, and can still answer `WaitApplied` for what it has.
- **The ports are frozen on first release.** Five interfaces aliased by
  `pkg/v1`; growth is by siblings (ADR 0039), each frozen by a named
  compile-time test as ADR 0052 does.
- **Stopping is the answer to storage failure.** A node does not limp on after
  a failed `fsync` or a corrupt record; operators restore it from a peer.
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
  `OUTCOME_UNKNOWN` applies once. The first consumer's compare-and-swap makes a
  duplicate harmless; a consumer without that property needs this.
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
`wire.js`, `secure.js`, and the storage, resilience and TCP cluster suites.
Each decision above that it changed says **from raft-sql-poc**. The table
covers every decision, including those it confirmed or that this ADR keeps
against it.

| Decision | raft-sql-poc | Verdict here |
|---|---|---|
| D1 name | `RaftNode`, `ShardedCluster`: the algorithm in the type name | unchanged — the SDK names the capability and puts the algorithm in the constructor |
| D2 placement | one dependency-free tree, by design | unchanged — the same stance as stdlib-only |
| D3 state machine | a thrown error is deterministic output (`raft.js:1109-1117`), even "replica state diverged" (`sqlstore.js:83`) | kept stricter: a failure that is not a refusal stops the node |
| D4 log | one WAL per machine, CRC per record, a torn tail cut and a mid-log fault refused (`storage.js:755-771`), the hard state as a record, a checkpoint at the head of each segment, prefix-only GC | **amended**: the hard state goes in the log, with checkpoints and prefix GC |
| D4 fsync failure | no retry, but the node never syncs again and acknowledges nothing, silently (`storage.js:489-495`) | kept: the node stops with `STORAGE_FAILED` |
| D4 transport | its own SIGMA handshake, a per-machine certificate, revocation on removal, one connection per direction, one sealed batch per tick, a bounded queue (`transport.js`, `secure.js`) | **amended**: admission follows the membership, writes are batched, the queue is bounded, three size and time limits. TLS 1.3 is kept over a hand-written handshake, given the POC's bugs 5 and 6 |
| D4 wire | RWP v2: a fixed 48-byte header, a closed schema of eleven types, a version byte, `f64` indexes because JavaScript has no cheap `u64` (`wire.js:1-40`) | unchanged — a fixed layout written by hand. Go has `uint64`, so the `f64` choice does not transfer |
| D5 elections | PreVote paired with CheckQuorum (`raft.js:502-560`, `raft.js:693-705`), a live-leader check in PreVote only, a restarted node silent for one timeout (`raft.js:205-213`) | **amended**: silent on restart, and the check applies to `RequestVote` too |
| D6 replication | batching per tick without a timer, optimistic pipelining with no bound, the leader's write in parallel counted at `persistedIndex` (`raft.js:1073-1093`), replies held back until durable (`raft.js:50-57`, `raft.js:1549-1561`), empty AppendEntries suppressed | **confirmed**, with three safety rules and the context case added; `MaxInflight` kept |
| D7 snapshots | zstd once per snapshot, SHA-256, 256 KiB chunks with one in flight and resume by offset, cut synchronously and held whole in memory | **amended**: snapshot compression. The copy-on-write cut and the stream are kept |
| D8 membership | single-server changes effective on append, refused before the leader commits in its term (`raft.js:403-465`), catch-up checked by the orchestrator, transfer waits for the commit | **amended**: transfer waits for the commit, refuses proposals and removes the leader last. Catch-up is enforced in the engine |
| D9 reads | three levels (`strong` = ReadIndex, `leader`, `stale`), shared read rounds, a lease on `Date.now()` and kept during a transfer | **amended** with the shared-round measurement and the lease's preconditions. Two lease gaps closed. The `leader` level is refused |
| D10 fencing | a write resolves with `{index, term}` (`raft.js:1129-1134`) and nothing builds on it | unchanged |
| D11 groups | multi-Raft per shard, rendezvous placement, coalesced heartbeats ÷32 to ÷34, one WAL per machine, leadership balanced to within one | v1 unchanged. The POC's design and numbers become the v2 specification |
| D12 errors | `NOT_LEADER` (with a leader hint), `UNKNOWN_OUTCOME`, `COMMIT_TIMEOUT`, `TRANSFER_FAILED`, `RETRY` | **amended**: a context that ends after the append is `OUTCOME_UNKNOWN` |
| D13 observability | events built only when someone listens (280 k objects saved) | unchanged — the SDK's metrics already cost nothing when unread |
| D14 API | `propose`, `readIndex`, `transferLeadership`, `proposeConfChange` | unchanged in shape |
| D15 tests | 169 assertions over chosen scenarios on real timers; power loss on every machine; mutation controls | **amended**: mutation controls, and the POC's three safety bugs as regression switches. The deterministic simulator and the linearizability checker are what the POC says it lacks |
| D16 performance | 7 000 to 9 400 durable writes/s, 5 300 to 5 700 over AES-256-GCM to disk, a 45 µs loopback round trip | a floor that confirms the targets, not a target |

### Replicate effects, not statements: the POC's hardest lesson

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

The first consumer's design is already where that path ends. It
replicates `{repository, ref, old SHA, new SHA}`, never the HTTP request or
the git command that produced it. That is a value together with the value
it was read against. Every replica checks it with the same predicate, the
compare-and-swap of `Apply`, just as the POC's engine detects a conflict at
apply time with a predicate every replica computes identically. The POC's
size problem does not arise here, because the bulk travels outside the log:
Git objects are pushed to the peers before the entry is proposed, and the
log carries only the ref update. The serialisation lesson maps onto the
hook too. In `reference-transaction` `prepared`, the replicator reads the
old SHA under git's ref lock: one capture in flight per ref, not per
database.

### Still to confront: IWFS

IWFS, the owner's Raft at Halys, remains to be confronted if access to it
is obtained.

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
- GitLab Gitaly, `internal/gitaly/storage/raftmgr` and
  `internal/gitaly/config` before their deletion in 2026 — the gaps listed in
  Context.
- ADR 0052 (fences and D11), ADR 0056 (publication and the deferred streaming
  writer), ADR 0029 (stream connections and `tlsid`), ADR 0090 (`clock`),
  ADR 0011 (copy-on-write snapshot), ADR 0039, ADR 0031, ADR 0074, ADR 0068.
