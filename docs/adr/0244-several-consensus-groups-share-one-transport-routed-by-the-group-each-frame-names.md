# ADR 0244 — several consensus groups share one transport, routed by the group each frame names, and coalescing and quiescing are specified before they are built

- **Status**: D1 to D4 Accepted on 2026-11-11 for [kitsunium/sdk#925](https://github.com/kitsunium/sdk/issues/925), decided autonomously and revisable while v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md)), and built. D5 to D7 Proposed: no coalescing, quiescing or shared-store code lands until the owner accepts them, and each lands behind the protocol version D5 names.
- **Date**: 2026-11-11
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0152](0152-a-write-is-committed-when-a-majority-holds-it-on-disk.md) D11 ("a v2 that multiplexes groups on one `Transport` changes no wire format": true of every frame, not of the snapshot offer, see D2) and its *Deferred* item "Multi-group (v2)", which this record opens
- **Related**: [ADR 0220](0220-a-consensus-frame-of-version-2-is-routed-by-a-compact-header-chosen-per-connection.md) (proposed: the compact v2 header; its gate D7.3 needs this routing, and D5's coalesced heartbeat is a kind on that header), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (the frozen `Transport` grows by siblings), [ADR 0174](0174-a-tls-identity-is-checked-on-every-handshake-and-reloaded-in-place.md) (the admission a key is held to), kitsunium/sdk#1334 (a guard on `Bind`, kept out), #812 (the extracted log primitive D7 hosts), #820, #552

## Context

ADR 0152 D11 left four pieces to a v2: groups multiplexed on one transport,
heartbeat coalescing between node pairs, quiescing idle groups, and a
routing table. Two and a half existed on main `b4c299a46`:

- every frame carries its group in the frozen header (`length u32 | version
  u8 | type u8 | group u64`, D18), and `raft/step.go:15` drops a frame of
  another group;
- `TCPTransport.SetMembership` already unions every group's admitted members
  (`admittedUnion`), the JOIN answer's group 0 among them (#552, #742).

What did not exist was any routing. `TCPTransport.Bind` ignored its
`GroupID` and kept one `deliver`/`offer`/`install`/`join` set, and
`Unreachable`, `bindUndecodable` and `BindJoinReason` kept one callback each:
a second `Node` on the same transport silently replaced the first's
callbacks, the first heard nothing more, and `readFrames` handed every frame
of both groups to the second, whose step function dropped the other group's.
A product that wanted two independent logs, two unrelated domains on one
machine, had to run two transports on two ports.

Two facts the issue assumed did not hold, and shape D2 and D3:

- **A snapshot offer names no group.** `InstallOfferValue` is a term and a
  `SnapshotMetaValue`, which carries the log's identity (cluster, epoch) but
  no `GroupID`; two groups of one cluster share the cluster id. The receiver
  cannot tell which group an offer is for from its bytes.
- **Neither does a JOIN.** `JoinRequestValue` names the cluster, the id, the
  incarnation, the slot and the address.

## Decision

### D1 — The transport routes by the group the frame names

`TCPTransport` keeps one binding per group (`groups map[GroupID]*binding`):
`Bind(group, …)` and `BindJoinReason(group, …)` fill that group's, and a node
binds its unreachable report and its judgment of an undecodable offer under
its group through an unexported sibling, `groupBinder` (ADR 0039: the
exported `Unreachable` stays, and is reported beside every group's). Its
reader takes the group from bytes 6 to 13 of each frame, its length prefix
included — the frozen header, read and never changed — and hands the frame
to that group's `deliver`. A frame of a group nothing bound is **dropped and
counted** (`unrouted`, logged at the first and at each power of two, as
`refusedOversized` is), exactly as `Step` drops a frame of another group. A
second `Bind` of the same group replaces its callbacks, which a restarted
`Node` on a kept transport needs; refusing it is kitsunium/sdk#1334's.

The outbound side needs nothing: frames are multiplexed on the lanes a peer
already has, one queue per peer and lane whatever the group, `PeerQueueBytes`
shared.

### D2 — A snapshot offer of a group other than 1 names it on its own lane

`laneSnapshotGroup` (lane byte 7; 5 and 6 are left to ADR 0220's
`laneControlV2` and `laneDataV2`) carries one offer and its stream for the
group it names: the group as a little-endian u64, then `laneSnapshot`'s
offer, in one chunk read under `MaxFrameBytes` plus 8. A node streams its
own group's snapshot through an unexported sibling, `groupStreamer`; the
exported `Stream` names none and keeps `laneSnapshot`.

**Group 1 keeps `laneSnapshot`, byte for byte.** Every v1 cluster runs group
1 (`Config` clamps 0 to it), so no deployment sends anything new. A cluster
whose one group is numbered otherwise sends its offers on the new lane: a
member of an earlier release ends it unanswered and the install is retried,
so such a cluster upgrades its receivers before an install is needed. There
is no fallback to `laneSnapshot` on an unanswered end: a multi-group
receiver would serve that offer to group 1, another group's node.

An offer on `laneSnapshotGroup` for a group nothing bound is answered with
an accept refused `PEER_REJECTED` and counted, never handed to another
group's node.

### D3 — A lane that names no group is served to the legacy group

`laneSnapshot`, `laneJoin` and `laneJoinV2` are served to the **only group
bound**, whatever its number — a node of one group is served as it always
was —, and with several bound to **group 1**, or refused unjudged when group
1 is not among them. A JOIN therefore admits into group 1 on a multi-group
machine; a JOIN into another group, which needs a group in the request and
in the joiner's hold (`joinGroup`), is deferred.

### D4 — One machine is one member of every group it runs

The admission is a union keyed by id and by SPKI, and a key two members
share is refused (review M5). So a machine runs every group under **one id,
one incarnation, one slot and one key**: its second group's log carries the
first's stamp. The cluster suite stamps it so
(`TestTwoGroupsOnOneTransportElectCommitSnapshotAndInstallIndependently`); an
offline `InitLog` for a second group under an existing identity is deferred
with D3's JOIN.

### D5 — Coalesced heartbeats (Proposed; targets before code)

The specification is D11's measured POC (raft-sql-poc, `host.js:1-27`,
`raft.js:1351-1368`; ADR 0152 G16), and its numbers are this record's
**targets**, which the change that builds it must measure and meet:

- **One heartbeat frame per pair of machines** carries every quiescent group
  the two share, in place of one per group.
- **Delta-encoded**: a group whose term and commit the receiver already
  confirmed costs **4 bytes** in it; the receiver's answer is one **52-byte
  "all clear"** acknowledgement for every group it confirms.
- **Idle traffic divided by 32 to 34 at 256 groups** on 6 machines: from
  10 240 messages/s to 300 to 320 (D11).

It is a new `MessageKind` behind a new protocol version, raised as protocol
2 and 3 were (`RaiseClusterVersion` refused while a member is unheard
announcing it, #665), never a group value (ADR 0220 D1), on ADR 0220's
header when that record is accepted. A transport without D1's routing never
announces it.

### D6 — Quiescing (Proposed)

A group with no write, no membership change and no lagging peer for a
quiescence window stops its own heartbeats and is carried by D5's frame
alone: a **per-group flag** on the leader, cleared by any proposal, any
unreachable report or any peer behind the commit. A follower of a quiescent
group keeps its election timer only while D5's frame for the pair keeps
arriving; one machine missing for an election timeout wakes every group it
led. Its gate is D11's own cost measurement: with 8 groups at replication 3
on 6 machines, losing 2 machines left 4 of 8 unavailable, and that number
must not worsen.

### D7 — A shared log store (Proposed)

D11's "a shared store hands out per-group views whose `Sync` is the same
call": one WAL per machine, one fdatasync for every group's Ready, hosted by
#812's extracted primitive. Each view keeps its own compaction and its own
`Stamp`; a group's corruption quarantines that group alone.

## Consequences / Semantics

- Two `Node`s of groups 1 and 2 over one `TCPTransport` and one listener per
  machine elect, commit, snapshot and install independently, and a frame of
  group 3 is dropped and counted (the cluster suite above,
  `TestAnOfferForAnUnboundGroupIsRefusedAndCounted`,
  `TestALaneThatNamesNoGroupIsServedToTheLegacyGroup`).
- The frame header, the admission union and the JOIN path are unchanged; a
  single-group node of any number is served as before.
- `TCPTransport.Bind`, `BindJoinReason` and `Stream` keep their signatures;
  their docs say what they route.

## Breaking changes

None for a caller. On the wire, the one addition is `laneSnapshotGroup`,
used only for a group other than 1 (D2).

## Alternatives considered

### Why not recognise the offer's group from its metadata

The metadata names the cluster and the epoch, which two groups of one
cluster share, and its membership, which two groups of the same machines
also share. Any guess hands one group's snapshot to another's node.

### Why not a group inside `laneSnapshot`'s offer

The offer's metadata starts with its version byte, and a member that does
not know a version stops `VERSION_UNSUPPORTED` (#513, #665): a prefix there
would stop every member of an earlier release.

### Why not route an unbound group's frame to the only node bound

That is the defect: the step function drops it, but the transport cannot
count it, and a misconfigured group is invisible.

## Deferred

- A guard on `Bind` (kitsunium/sdk#1334): a second `Bind` of one group still
  replaces the first.
- A JOIN into a group other than 1 on a multi-group machine, and an offline
  initialisation of a second group's log under the machine's identity (D3,
  D4).
- D5 to D7, each in its own change once accepted; the memory transport's
  routing beside them.

## References

- `internal/service/app/consensus/tcp_groups.go` (the binding, the legacy
  binding, `laneSnapshotGroup`, the count), `tcp.go` (`readFrames`,
  `serveSnapshot`, `streamGroup`, `serveJoin`), `node.go` (`bindTransport`),
  `snapshot.go` (`streamOffer`)
- [ADR 0152](0152-a-write-is-committed-when-a-majority-holds-it-on-disk.md) D11, D18, G16 and *Deferred*
