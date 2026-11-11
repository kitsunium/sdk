# ADR 0220 — a consensus frame of version 2 is routed by a compact header, and its generation is chosen per connection

- **Status**: Proposed. Not accepted, nothing built: no frame, payload or record changes until the owner accepts it and a protocol version carries it.
- **Date**: 2026-11-11
- **Deciders**: SDK maintainers, owner
- **Amends**: [ADR 0152](0152-a-write-is-committed-when-a-majority-holds-it-on-disk.md) D18, "The frame header is frozen (R8-36, R9-07)", and the frame half of "Unknown means stop"; D14's note "`Lane` and the frozen header (R8-36, R9-07)"; D11's first bullet, the group carried by every frame, kept and re-encoded.
- **Related**: [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a frozen port grows by a sibling interface), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (a published shape changed while v0, said out loud), [ADR 0174](0174-a-tls-identity-is-checked-on-every-handshake-and-reloaded-in-place.md) (the authenticated peer a frame's sender is resolved from), ADR 0152's amendments "protocol version 2, raised once" (2026-10-23) and "protocol version 3" (2026-10-24, #648), [kitsunium/sdk#925](https://github.com/kitsunium/sdk/issues/925) (multi-group routing), [kitsunium/sdk#1333](https://github.com/kitsunium/sdk/issues/1333) and [kitsunium/sdk#1334](https://github.com/kitsunium/sdk/issues/1334) (kept out of this record, see *Deferred*)

## Context

ADR 0152 D18 froze the frame header for every protocol version:
`length (u32) | version (u8) | type (u8) | group (u64)`, little-endian,
14 bytes, the two high bits of `type` the lane (R8-36, R9-07). The body of
every kind then repeats the same fixed layout: flags, `From` and `To` as
u64, and twelve more u64 or u32 fields whether the kind uses them or not
(`wire/codec.go`, `AppendMessage`). A heartbeat is 112 bytes on the wire
before TLS, 14 of header and 98 of body, for a term, a commit and a flag
byte. D11 measured what a multi-group deployment pays for that size: one
heartbeat per group per pair of machines, the traffic a coalesced frame
divides by 32 at 256 groups.

The freeze did its job, a transport routes a frame it cannot parse, and its
cost is now the dominant term for any consumer that runs a quiet cluster
over a metered or slow link, or that will run more than one group. One such
consumer, a message bus built on the domain, measured it as most of its
idle traffic. The decision below is not written for that consumer: every
need it serves is the domain's own, and a consumer that never raises its
cluster past protocol 3 sees no byte change.

### The needs, all of them

1. **Route without parsing the body.** A transport, the shipped one or a
   user's, puts a frame on its lane from its header alone (D14, `Lane`), so
   a kind or a version it does not know still routes.
2. **A v1 reader never receives v2 bytes.** A node of an older release must
   never be handed a frame it would misread. It may refuse a connection; it
   must never desynchronise on one.
3. **Several groups on one connection later** (D11, #925), with no further
   change of the wire.
4. **A per-frame group check.** `raft/step.go:15` drops a frame of another
   group, the only guard a heartbeat has; the group stays on the wire.
5. **The length bounded before the body.** A frame above its lane's bound is
   refused before any allocation (`tcp.go:689`, `readFrame`, and the bound
   per lane, 64 KiB on control by default).
6. **A safe upgrade, downgrade and recovery**: rolling, with a cluster of
   mixed releases until an explicit raise, and a disaster recovery that
   cannot open a directory with a binary that does not speak its formats.
7. **The fewest bytes** that still serve 1 to 6.

### What was measured, and what it rules out

- **A frame's generation cannot be read from its bytes.** A v1 frame starts
  with the low byte of its length. Over a recorded v1 stream, one frame in
  256 starts with `0x02`, as uniformly distributed lengths predict: a
  version byte at offset 0 would be ambiguous with a v1 length. So the
  generation is not self-described; it is fixed for the connection.
- **A v1 reader already ends a lane it does not know.** The dialer's first
  byte after the handshake names the lane (`tcp.go:37-48`), and a member
  serves nothing on an unknown one (`tcp.go:655`). `laneJoinV2` (#556)
  already uses that: a member that predates it ends the connection and the
  joiner falls back to `laneJoin`. The same mechanism carries need 2 with no
  new rule.
- **`From` and `To` are redundant on an authenticated connection.** The
  node drops a frame whose `From` is not the peer TLS authenticated
  (`node.go:1030`, R7-05), so the field can only be wrong, never informative;
  `To` is the receiving node.

## Decision

### D1 — The v2 header, frozen for every frame version from 2

```
version u8 | kind u8 | length uvarint | group uvarint | body
```

- `version` is the frame version, 2 or above, at offset 0.
- `kind` is the message kind; its **two high bits are the lane**, as in v1
  (`00` control, `01` data, `10` snapshot, `11` reserved), at offset 1.
  `LaneOf` reads byte 1 and nothing else.
- `length` is a **minimal** unsigned LEB128 varint of at most 5 bytes (a
  u32). It counts every byte after itself, the group included. A
  non-minimal encoding, a sixth byte, or a value above `MaxUint32` is
  `FRAME_INVALID`. It is read **byte by byte**, and refused as soon as the
  partial value exceeds the lane's bound, before any allocation.
- `group` is the **minimal uvarint of the `GroupID` itself**: no table, no
  alias, no per-connection state. Group 0 is invalid on the wire (`Config`
  clamps a zero group to 1, `config.go:155`, so no node runs it). D11's
  coalesced heartbeat, when it comes, is its own kind, never a group value.
- The header is 4 bytes for group 1 and a frame under 128 bytes, 4 to 7
  bytes at the shipped bounds (a 16 MiB data frame takes a 4-byte length),
  17 bytes at worst (`1 + 1 + 5 + 10`). v1's is 14.

Every frame version from 2 keeps this header exactly; a later version
changes only bodies. `version` selects the body layout; it never moves the
lane or the length.

### D2 — The generation is the connection's, fixed by its lane byte

- New lane bytes, on the `laneJoinV2` model: `laneControlV2` and
  `laneDataV2`, the next free values after `laneJoinV2` (`tcp.go:48`). A
  connection opened on one carries v2-header frames only, in both
  directions; a connection opened on `laneControl` or `laneData` carries v1
  frames only. The snapshot lane is unchanged: its offer and stream are
  their own format.
- After the lane byte, the dialer writes its frame-version range
  (`min u8`, `max u8`); the acceptor answers the highest version in both
  ranges, or ends the connection. The negotiated version holds for the
  connection's life.
- A member that predates the lane ends the connection unanswered
  (`tcp.go:655`), and the dialer dials the v1 lane: **a v1 reader never
  receives a v2 byte.** Need 2 of D18 is rewritten so: an old reader is
  protected by the lane it is offered, not by parsing a header it does not
  know.
- A frame on a negotiated connection whose `version` is outside the
  negotiated range, or whose header does not parse, **closes that
  connection and is counted** (`frames_refused{reason}`). It never stops a
  group: a frame is not persisted, so D18's "unknown means stop" keeps
  applying to what is stored or replicated, never to a frame (this amends
  D18's list, which named the frame version among what stops a node).

### D3 — The frozen port grows by siblings (ADR 0039)

`Transport` (`gen_ports.go:160`) is frozen and stays as it is; `Send(to,
frame)` and `Lane(frame)` stay v1 only.

```go
// FrameVersioner is the sibling of a transport that carries frames of a
// version above 1.
type FrameVersioner interface {
	// FrameVersions returns the frame versions this transport carries.
	FrameVersions() (min, max uint8)
}

// VersionedSender is the send path of a FrameVersioner.
type VersionedSender interface {
	// SendVersion hands over a frame encoded in version; it never blocks
	// and may drop, as Send.
	SendVersion(to NodeID, version uint8, frame []byte)
}

// LaneOf reads the lane of a frame encoded in version from its header alone.
func LaneOf(version uint8, frame []byte) (FrameLane, error)
```

- A transport that implements neither is **v1 only**, and its node never
  announces the protocol version of D4: it **blocks the raise**, as a member
  of an older release does. A user's transport keeps working unchanged and
  never sees a v2 byte.
- A transport that announces `max ≥ 2` without `VersionedSender` is refused
  at `NewNode`, `TRANSPORT_MISCONFIGURED`.
- `LaneOf(1, frame)` is `Lane(frame)`.

### D4 — Activated by a new protocol version, raised explicitly

- The v2 frame rides on **a new protocol version**, the next after
  `ProtocolMax` (3 at the time of writing, `raft/raft.go:45`, #648), never
  on `frameVersion2` alone. A node of that protocol announces it as
  protocol 2 and 3 are, by a flag on every frame it sends (`FlagProtocol2`,
  `FlagProtocol3` model, `message.go:72-83`), and only when its transport is
  a `FrameVersioner` with `max ≥ 2`.
- `RaiseClusterVersion` to it is refused `VERSION_UNSUPPORTED` while any
  member of the latest configuration, voter or learner, was not heard
  announcing it (the #665 rule: every member must answer).
- **Receive what the binary speaks, send what the cluster allows.** A
  receiver accepts any lane its binary speaks, before and after the raise.
  A sender opens v2 connections, and encodes v2, from the moment the
  committed cluster version it knows allows it, and falls back to v1 only
  below. At the raise's apply, the transport closes its v1 links, sends the
  v1 frames still queued on their own lane first, and dials again in v2.
  Raft's retransmission absorbs the gap; no frame is ever re-encoded.
- **The cluster version governs only what is stored or replicated.** The
  frame generation of a connection is negotiated per connection; the
  committed version only decides when a sender starts offering v2.

### D5 — Bodies per kind

- Each kind has its own body: only the fields it uses, in a fixed order.
- **`From` and `To` are removed.** `From` is resolved by the receiver from
  `(group, authenticated SPKI)` against that group's membership; `To` is the
  local node. R7-05's check (`node.go:1030`) becomes true by construction.
- A `NodeID` still carried in a body (a transfer's issuer, a leader record)
  is a **fixed 8 bytes**, never a varint: an identifier is not a magnitude.
- Indexes and terms are uvarints, and may be **deltas against another field
  of the same frame** (`LogTerm` against `Term`, `Commit` against `Index`),
  never against connection state and **only in the frame layout**: the log
  record, the snapshot, the entry header inside an append and every sum are
  unchanged.
- **`BodySum` and `ChainSum` are unchanged**, computed over the same inputs
  as today (`wire/codec.go:67-80`): a v2 frame and a v1 frame carrying the
  same entry carry the same sums, so a follower checks an entry identically
  whatever connection brought it.

### D6 — Sizes, measured and derived

| Frame | v1 | v2 |
|---|---|---|
| header | 14 B | 4 to 7 B at the shipped bounds, 17 B at worst |
| heartbeat (header and body) | 112 B | 8 to 17 B, by the cluster's age (the term and commit varints) |
| heartbeat on the wire, TLS 1.3 and TCP included | 186 B | 87 B |

The first two rows are the layouts' arithmetic. The third was measured on
one machine (arm64) and is indicative; the gate below measures it again on
the deployment's amd64 hosts before acceptance, and this table is corrected
from that run.

### D7 — Gates, before any default changes

1. **A raise under load**: a three-node cluster raised while it commits,
   with zero frames routed to the wrong group or lane and zero
   `FRAME_INVALID` across the raise.
2. **A v1 user transport never sees a v2 byte**: a recording transport
   without `FrameVersioner` in a cluster raised past the version; the raise
   refused while it is a member, and no frame it received starts a v2
   header.
3. **Two groups on one connection at two frame versions**, once #925's
   routing exists: each group's frames parse and route by their own header.
4. **A binary of the previous protocol refused cleanly**: it ends the v2
   lane, falls back to v1 before the raise, and stops `VERSION_UNSUPPORTED`
   on the raise entry or a snapshot above its protocol after it (#640,
   #665), never misreading a frame.
5. **A product payload raised without changing the SDK's version**, with
   #1333 built: this record changes no payload rule and must not need one.
6. Fuzz: every accepted v2 frame round-trips to its own bytes; a
   non-minimal varint, a length past the lane's bound at any partial byte,
   and group 0 are refused before allocation.

### D8 — Recovery (the PRA rule)

The seed or recovery manifest a product keeps for a data directory carries,
per group: the **committed cluster version**, the **payload floor** (#1333),
and the **fingerprint of the product's schema registry**. The recovery
binary checks all three before it opens the directory, and refuses one it
does not speak, rather than relying on `VERSION_UNSUPPORTED` after the
open. The upgrade runbook is part of the product's disaster-recovery
document, and every raise of the cluster version is an operation with its
own approval: it is D18's point of no return.

## Consequences / Semantics

- A cluster below the new protocol version changes no byte: v1 lanes, v1
  frames, `Lane` and `Send` as today.
- After the raise, a heartbeat costs a tenth of its v1 size, and a frame's
  header no longer pays for a 64-bit group a cluster numbers from 1.
- `Lane(frame)` keeps its contract and is now explicitly the v1 reader;
  `LaneOf` is the general one.
- D18's "unknown means stop" no longer names the frame version: an unknown
  frame closes a connection. Entries, records, snapshots and payloads still
  stop the node.
- The downgrade rule is D18's, unchanged: a node may run an older binary
  only while the committed cluster version is within its range; the raise
  is the point of no return.

## Breaking changes

None before the raise and none for a caller: `Transport`, `Send` and `Lane`
are unchanged, the new names are siblings and a function. Once a cluster is
raised, a release of a lower protocol can no longer join it, as with every
raise.

## Alternatives considered

### Why not keep the v1 header and only shrink the bodies

It keeps 14 bytes of header and a 64-bit group on every frame, and still
leaves `From`/`To` to remove: 23 bytes for a heartbeat where 8 to 17 serve
every need.

### Why not drop the group from the wire

12 bytes for a heartbeat, one less than with it, and needs 3 and 4 lost: the
per-frame group check is the only guard a heartbeat has, and a multiplexed
connection could not route.

### Why not a short group alias negotiated at connection open

A table `short ↔ GroupID` checked at the preamble saves at most a byte or
two for groups above 127 and adds connection state every reader must hold
and every reconnect must rebuild; a frame then means nothing outside its
connection.

### Why not a self-describing version byte in the frame

A v1 frame's first byte is its length's low byte; one frame in 256 would
read as version 2. The generation must come from the connection.

### Why not raise on `frameVersion2` alone, without a protocol version

The members must all speak it before any sender uses it, which is exactly
what a protocol version and its announcement already decide; a second
mechanism would be a second, weaker raise.

## Deferred

- **`ProposeVersion`, the per-group payload floor and the
  `PAYLOAD_VERSION_REFUSED` gate**, which D18 states and nothing wires:
  [kitsunium/sdk#1333](https://github.com/kitsunium/sdk/issues/1333). It
  amends R7-36 in its own record.
- **A guard on `Bind`**, which ignores its group on both shipped transports
  so the last bind wins silently:
  [kitsunium/sdk#1334](https://github.com/kitsunium/sdk/issues/1334). It
  precedes #925's routing, which D7's third gate needs.
- D11's coalesced heartbeat and quiescing: their own kind and their own
  record, on this header.

## References

- [ADR 0152](0152-a-write-is-committed-when-a-majority-holds-it-on-disk.md) D4, D11, D14, D18, and its amendments of 2026-10-23, 2026-10-24 (#640, #648) and 2026-10-25 (#665)
- `internal/service/app/consensus/wire/codec.go` (the v1 header and bodies, `Lane`, `BodySum`, `ChainSum`)
- `internal/service/app/consensus/tcp.go` (lane bytes, `readFrame`, the unknown-lane rule, `Bind`)
- `internal/service/app/consensus/raft/step.go` (the per-frame group check)
- `internal/core/app/consensus/gen_ports.go` (`Transport`, frozen under ADR 0039)
