# ADR 0241 — a client session applies a command once, whatever its retries

- **Status**: Accepted
- **Date**: 2026-11-12
- **Deciders**: SDK maintainers (kitsunium/sdk#918, lot L1)
- **Related**: [ADR 0152](0152-a-write-is-committed-when-a-majority-holds-it-on-disk.md) (D6 the outcome of a proposal, D7 the snapshot metadata, D18 the protocol versions; its Deferred "Client sessions"), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a sibling port, never a wider one), [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md) (the design first)

## Context

A proposal answered `OUTCOME_UNKNOWN` may still commit (D6). Until now a
product had to read its state through `Barrier` and decide, carrying a
per-key revision against ABA (R1-04): every consumer rebuilt the same retry
protocol, and a command with no natural key — an event appended, a counter
moved — had no safe retry at all. ADR 0152 deferred the remedy, client
sessions (Raft thesis §6.3), and specified it from IWFS: the same identifier
with the same digest joins or returns the known answer, another digest is
refused. The forward-answer ring (#401) deduplicates a frame delivered
twice, but only by `(From, Context)` and only on the leader that kept it: it
is not replicated state and does not survive a leader change.

## Decision

1. **A sibling port.** `core/app/consensus.SessionProposer` — `OpenSession(ctx,
   s)` and `ProposeSession(ctx, s, seq, data)` — beside the frozen
   `Replicated` (ADR 0039); `Node` implements it. A `SessionID` is 16 bytes
   the client draws. A session is opened once (sequence 0, idempotent), then
   each command takes the next sequence and is retried after
   `OUTCOME_UNKNOWN` with the same session, sequence and data, through any
   node.

2. **A new entry kind in the log.** `EntrySessionCommand` (9) carries
   `session (16) | sequence (u64) | command`; its `BodySum`, the CRC-32C of
   that payload, is the digest. The kind is protocol version 4: a node of
   protocol 4 announces `FlagProtocol4`, the raise to 4 is refused while a
   member has not announced it (D18), and below cluster version 4 a leader
   refuses every session command `PAYLOAD_VERSION_REFUSED` before it is
   appended, so a node of the previous release is never sent one. Protocol
   version 4 is the one #919 opened in the same lot (a heartbeat carrying
   the leader's last index) and #1115 builds on (a forward's trace context,
   `FlagTrace`): none of them was released apart, so a node of protocol 4
   speaks all three, under the one announcement, and no fifth version is
   opened — `MessageFlags` has no bit left for one.

3. **The session table is a pure fold of the committed log.**
   `raft.SessionTable` keeps per session the last sequence applied, its
   digest, its position and the index of the session's last entry. A command
   of a higher sequence is applied and recorded; the same sequence with the
   same digest is a copy, answered with the recorded position; another digest
   is `SESSION_DIGEST_MISMATCH` (0.2.57.26); a lower sequence, or a session
   not open, `SESSION_EXPIRED` (0.2.57.27). A refused command changes nothing.
   The fold runs twice over the same entries: in the step function at commit,
   so a leader answers a copy it knows — committed, or still in its own tail —
   without appending and refuses a mismatch before append; and in the driver
   at apply, which decides what the state machine is handed. Only the second
   is a safety: a copy the leader missed is appended and skipped at apply.
   The state machine receives an applied session command as an `EntryCommand`
   carrying the command alone.

4. **The table rides the snapshot.** `SnapshotMetaValue.Sessions`, encoded
   from metadata version 4, written at cluster version 4 (D7's versioning).
   A cut records the driver's table at the applied index, on the apply
   goroutine; a restore takes it with the machine; a start folds again the
   session commands the tail holds past the snapshot through the index a
   durable machine resumes from (the start never drops a session command's
   payload); an offline import folds the tail it replays.

5. **Expiry is eviction by count, not by time.** The log carries no committed
   clock, so a time-to-live would need one, and the issue's proposal of a TTL
   admin entry would only bound idle sessions on the leader's word. Instead
   the cluster keeps `MaxSessions` (1024) sessions; opening one more evicts
   the one whose last entry is the oldest — a choice every node makes alike,
   each index being the last of one session at most. The bound is part of
   protocol 4, never a node's setting: two nodes evicting differently would
   apply differently. It bounds each node's two tables and the metadata an
   offer carries (about 52 KiB). A command of an evicted session is
   `SESSION_EXPIRED`, never applied: the client reads its state through
   `Barrier` and opens a new session — and never reopens the old one, whose
   retries would then apply again.

6. **The answer.** A copy is answered with the first command's token, and its
   value while the node still holds it (the result kept for forwards), nil
   otherwise: the value is not replicated state. A forwarded command's answer
   carries the leader's commit, so the follower tells a copy already
   committed, which it answers once it applied that index, from one to wait
   for.

## Consequences

- A blind retry is safe for session commands. The simulator's
  `TestBlindSessionRetriesAreAppliedOnce` retries every unknown outcome
  through drops, duplicated frames, partitions, crashes and snapshot
  installs; the linearizability checker accepts the history with each command
  applied once, with the leader's check on and off, and the negative control
  — check off, every command applied — applies one twice.
- A session command is appended alone, never batched with others: each is
  judged against what the leader holds of its session. The leader's check
  scans its uncommitted tail, bounded by the window of entries in flight.
- One outstanding command per session: a client that wants concurrency opens
  several sessions.
- `Recover` starts a new lineage with no session: a client of the old one
  gets `SESSION_EXPIRED`.

## Alternatives rejected

- **A TTL in an admin entry** (the issue's proposal): it needs a clock the log
  does not carry, and still lets a client's retry apply twice after its
  session expired unless commands of an unknown session are refused — which
  the count bound does with no clock at all.
- **Sessions opened implicitly by their first command**: after an eviction a
  retry of that first command would open the session again and apply twice.
- **The session table in `EngineAt`'s records**: a copy of the table per
  committed command; the driver's table at the applied index is the one a cut
  needs, on the goroutine that cuts.
