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
exhaustion (`transport.js:294-296`, 64 MiB in the POC). **From IWFS:**
control frames (votes, PreVotes, heartbeats and their replies,
`TimeoutNow`) are never queued behind entries. Each peer queue has a small
lane that is drained first. Under CPU starvation, IWFS's PreVote fan-out
saturated the shared mesh client, and elections never settled (its
"death-spiral leaderless",
`docs/src/cii-src/07-innovations/01-shared-log-single-writer.md` §7.1). IWFS
answered by backing elections off up to 30 s. This ADR removes the cause
instead and keeps elections fast.

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
  the commit. **From IWFS:** a follower dials the leader the moment it
  learns of it and opens the forwarding stream before the first proposal
  (`src/internal/cluster/election.go:805-807`). Without that, IWFS's first
  forwarded write after an election exceeded its wait
  (`docs/src/design/2026-09-10-point-acceptation-commit.md` §3).
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

**A replaced entry is a definite failure, from IWFS.** `Propose` waits on
its entry's index AND term. If a new leader truncated that position and
reused it, the wait ends with `ENTRY_SUPERSEDED`: the entry certainly did
not commit, and a retry is safe. That is better than `OUTCOME_UNKNOWN`, and
it must not be read as success because something committed at that index.
IWFS waits this way (`WaitForCommit` on index and term, "entry replaced")
and tests both halves (`TestWaitForCommit_ReplacedEntry`,
`TestAcceptance_ReplacedEntryIsNotAccepted`).

**Refused before anything is appended, from IWFS.** When the leader's
pending proposals exceed `MaxPendingBytes`, `Propose` answers `OVERLOADED`
and appends nothing, so a retry is safe. IWFS refuses a full queue before
acceptance (`0x0032`), never after, so a write is never half-accepted. The
SDK does not take IWFS's admission on host CPU and memory thresholds (its
J14): the leader's own pending bytes are the only signal.

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
| `0.2.57.12` | `ENTRY_SUPERSEDED` | the proposal's position was truncated and reused: certainly not committed (D6, from IWFS) |
| `0.2.57.13` | `OVERLOADED` | refused before append, the leader's pending bytes over `MaxPendingBytes` (D6, from IWFS) |

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

**Scenarios from IWFS's defects.** IWFS documents four defects that its
8-hour nominal run could not expose. They were found later, under injected
partitions, CPU starvation and simultaneous starts
(`01-shared-log-single-writer.md` §7.1). Each one is a named simulator
scenario here:
- every node starts at the same instant, and a split vote must not replay
  in the same term;
- a partition heals, and a follower that missed committed entries must
  converge through the leader's log repair alone, with no separate
  catch-up path;
- control traffic is starved, and elections must still settle;
- concurrent acquisitions, and a fence must not advance on a lost one.
IWFS's Kubernetes chaos suite with real network-policy partitions, which
asserts "at most one writer" throughout (`tests/e2e/chaos_invariants_test.go`),
is the model for the first consumer's `tc netem` lane.

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
  `OUTCOME_UNKNOWN` applies once. **Specified from IWFS:** the session also
  keeps a digest of the command. The same identifier with the same digest
  joins the pending proposal or returns the known answer. The same
  identifier with a different digest is refused, never applied (IWFS: same
  identifier and FNV-64a content give the same answer, different content
  gives `0x0031`). The first consumer's compare-and-swap makes a
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
| B16 | Retries are not idempotent: `UNKNOWN_OUTCOME` goes back to the client, with no request identifiers | README §"Limites assumées" | a blind retry applies twice | Deferred, client sessions. The first consumer's compare-and-swap makes a duplicate fail identically everywhere |

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
| I6 | A commit advance is pushed to the followers at once (coalesced), and a follower's apply callbacks fire on a commit advance that brings no new entry. When commit waited for the 1 s heartbeat ack, 0 of 3 integration tests passed | `election.go:2396-2400`, `election.go:964-975`, `2026-09-10-point-acceptation-commit.md` §1 and §3 | D6 — confirms the POC's rule that an empty AppendEntries carries a new commit |
| I7 | A follower dials the leader as soon as it learns of it, and opens its forwarding stream before the first entry. Without that, the first forwarded write after an election exceeded its wait | `election.go:805-807`, `2026-09-10-point-acceptation-commit.md` §2–§3, `TestLeaderForwarder_WarmsUpStreamBeforeAnyForward` | **D6 amended** |
| I8 | Commit counts only an entry of the current term (Figure 8) and aborts if the term changed during the computation | `election.go:2426-2446`, `election.go:2455-2461` | D6 — confirmed |
| I9 | A fence advances only on a SUCCESSFUL acquisition. An epoch bumped by a losing contender fenced the legitimate leader | `01-shared-log-single-writer.md` §5.2 and §7.1, Valkey Lua script cited there | D10 — confirmed: the fence is the committed entry's index, which only a success produces |
| I10 | Defects found under adversity, each named with the regime that exposes it: a split vote replayed in the same term forever on simultaneous start, a follower never re-requesting committed entries it missed during a partition, a PreVote fan-out saturating the shared mesh client under CPU starvation, and the fence above | `01-shared-log-single-writer.md` §7.1 | **D15 amended**: each becomes a simulator scenario. **D4 amended**: control frames never wait behind entries |
| I11 | A chaos E2E on kind with Calico network policies for real partitions, with invariants asserted throughout ("at most one writer") | `tests/e2e/chaos_invariants_test.go`, `tests/e2e/scenarios_degraded_lease_test.go`, `tests/e2e/scenarios_consensus_inv_test.go` | D15 — the model for the first consumer's `tc netem` lane, not for the SDK suite |
| I12 | Documentation that dates its claims, states its evidence level and revises itself when the code disagrees ("Révision assumée"), with a validation record pinned to a SHA and to binary digests | `01-shared-log-single-writer.md` §9, `validation-2026-09-10/README.md` | the practice `BENCH.md` and this ADR follow |

### What IWFS does badly, riskily or incompletely, and this ADR must not take

| # | Defect | Proof | Risk | Answer here |
|---|---|---|---|---|
| J1 | **The quorum is a majority of the members the health check currently sees as ACTIVE**, not of a configured voter set | `election.go:1305`, `election.go:1538-1539`, `src/internal/cluster/election_coverage.go:113-129`, `src/internal/cluster/membership.go:567-585`. Members turn Suspect after 5 s and Dead after 10 s (`cluster.md`, Membership) | in a partition {A} / {B, C}, A sees B and C dead, its majority of one elects itself, and two leaders write. Only the Valkey lease, when it is enabled, stands in between | D8: the membership is log entries; a majority is computed over the configured voters and never over who answers |
| J2 | **Term and vote are not persisted** ("StableStore non câblé") | `2026-09-10-point-acceptation-commit.md` §1 and §4 | a node that restarts within a term can vote twice in it, which allows two leaders in one term | D4: the hard state is durable before any reply |
| J3 | **No local durability at all.** The log is in memory, and Valkey's AOF and RDB are off in the labs. A power loss on every node loses everything | `01-shared-log-single-writer.md` §5.1, §5.5 | "zero data loss" holds only while at least one replica's memory survives | the title of this ADR |
| J4 | The legacy acknowledgement point is still available: the client is answered from the leader's memory before replication | `acceptance.go:70-83` (`ackAfterCommit=false` keeps it), `01-shared-log-single-writer.md` §8 point 5 | an acknowledged write is lost with the leader | Consequences: no asynchronous mode |
| J5 | **Degraded leadership**: on loss of the majority, a survivor takes write authority through the external lease, without a quorum and without advancing the term | `01-shared-log-single-writer.md` §5.2.1, `election.go:1167-1190` | at most one writer, but its writes exist on one node: availability bought with durability | not taken — a minority is read-only (Consequences) |
| J6 | **The follower acknowledges its whole log, and commits up to it.** An empty heartbeat reports `GetLatestIndex()`, and so does an append. The follower commits `min(leaderCommit, lastLogIndex)` | `election.go:845-857`, `election.go:867`, `election.go:743-745`, `election.go:836-837`, then `election.go:2028-2030` on the leader | raft-sql-poc's bugs 2 and 3, still present: a stale suffix from an old term can be counted in a majority, or applied | D6: the verified prefix, never `lastIndex`. The POC is right and IWFS is not |
| J7 | The mesh is plaintext gRPC (`insecure.NewCredentials()`), with no peer identity | `src/internal/adapters/grpc/mesh/client.go:74` | anyone who reaches `:8081` can forge a heartbeat with a higher term and take the cluster, as the POC's README warns | D4: mandatory mTLS, admission by membership |
| J8 | Members are identified by name or address strings. In static mode the same node appears twice (`iwfs1` and `iwfs1:8081`) | `2026-09-10-point-acceptation-commit.md` §1 and §4 | "the majority stays correct (each follower counts twice, symmetrically)" is an accident of symmetry, not a property | D8: a numeric `NodeID` bound to its certificate and never reused |
| J9 | Discovery drives membership (Kubernetes DNS or a static list), and the Raft settings, election timeouts included, are hot-reloaded through the journal | `src/internal/cluster/discovery.go`, `cluster.md` (Hot Reload) | a timing change while a lease is held breaks the lease's arithmetic, and the operator's DNS becomes part of the quorum | not taken: membership by explicit change, timings at construction |
| J10 | Elections time out at 3 to 5 s with a 1 s heartbeat, and the anti-spin backoff reaches 30 s (`2^min(n,3)`) | `cluster.md` (Election), `election.go:90-98` | failover slower than Gitaly's 4 s, and up to 30 s under a storm | D5 keeps 1 to 2 s. D4 removes the storm's cause, election traffic queued behind data, instead of slowing elections down |
| J11 | Every timer is `time.Now()` / `time.Since`. They are monotonic in Go, but they cannot be injected | `election.go:267-268`, `election.go:606-609`, `election.go:809` | no reproducible schedule: the defects of I10 were found by E2E, not by a test that can replay them | D15: injected clock, deterministic simulator |
| J12 | Tests: 5 191 unit functions and 6 integration tests that assert delivery, "not the order between majority commit and acknowledgement". The chaos E2E is skipped in CI without `IWFS_E2E_FLOOR_ENABLED`. There is no deterministic simulation and no linearizability checker | `validation-2026-09-10/README.md` (results and limits), `01-shared-log-single-writer.md` §5.2.1, `tests/` tree | the safety properties rest on unit tests of components, not on histories | D15 |
| J13 | `Sharded.LoadFromStore`, the recovery from total memory loss, has no caller in production. The Valkey epoch restarts at 1 after a reset | `01-shared-log-single-writer.md` §5.3, `2026-09-10-point-acceptation-commit.md` §1 | the last-resort recovery has never run, and a fence that restarts reissues numbers a resource has already accepted | D7: recovery from snapshot plus log is the normal path. D10: the fence lives in the log |
| J14 | CPU and memory thresholds put followers in a "security mode" that refuses writes | `src/internal/cluster/security_mode.go:233-244`, `src/internal/cluster/election_config.go:8-54` | a heuristic on host load decides admission, across nodes that do not share the load | D6 takes the admission refusal (I5) only from the leader's own pending bytes |

### Three columns: ADR 0152, raft-sql-poc, IWFS

`>` marks where the two prior arts disagree and names the one that is right.

| Decision | ADR 0152 | raft-sql-poc | IWFS |
|---|---|---|---|
| D1 name | `consensus`, `NewRaft` | `RaftNode` | `Election`, "Shared Log" |
| D2 placement | stdlib-only, three layers | no dependency | gRPC, Valkey, Kubernetes |
| D3 state machine | deterministic `Apply`; a non-refusal failure stops the node | every throw is deterministic (B1) | apply on commit through `commitApplyQueue`, in order |
| D4 log | segmented WAL, CRC-32C, the hard state as a record, `fdatasync` per batch | the same, JavaScript | **none: in memory**, Valkey optional (J3). `>` POC right |
| D4 hard state | durable before reply | durable before reply | **not persisted** (J2). `>` POC right |
| D4 transport | mTLS, admission by membership, batches, a priority lane for control frames | its own SIGMA handshake, revocation | **plaintext gRPC** (J7). `>` POC right on the need, TLS on the means |
| D5 elections | PreVote, CheckQuorum, refusal in both votes, 1–2 s | refusal in PreVote only (B4) | refusal in both votes (I1), 3–5 s, 30 s backoff. `>` IWFS right on the refusal |
| D6 replication | majority on disk, verified prefix, bounded pipeline | verified prefix, unbounded pipeline | **acknowledges `lastIndex`, commits `min(leaderCommit, lastIndex)`** (J6). `>` POC right |
| D6 outcome | three answers plus `ENTRY_SUPERSEDED` and `OVERLOADED` | `NOT_LEADER` / `UNKNOWN_OUTCOME` / `COMMIT_TIMEOUT` | accepted / unknown / refused / replaced / full (I2–I5). `>` IWFS more complete |
| D7 snapshots | copy-on-write cut, streamed, compressed, verified | synchronous, zstd, SHA-256 | none: catch-up over the mesh, gzip snapshots in Valkey |
| D8 membership | log entries, one at a time, learners, IDs never reused | the same, with its catch-up in the orchestrator (B10) | **a majority of the members the health check sees as active** (J1). `>` POC right |
| D9 reads | ReadIndex, lease opt-in on a monotonic clock | strong / leader / stale | local reads from the in-memory store (RouteStore) |
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
  ADR 0011 (copy-on-write snapshot), ADR 0039, ADR 0031, ADR 0074, ADR 0068.
