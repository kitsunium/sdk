<!-- updated: 2026-10-22T18:00:00Z -->
# internal/service/app/consensus/raft/

## Purpose

The step function of ADR 0152 (D15): no goroutine, no I/O, no clock. Every
decision of the protocol is here; every effect leaves through `Ready`.
Durability is an input (`StorageDone`), and the replies that depend on a
write leave in `Ready.AfterSync`.

## Tests

The deterministic simulator (`sim_internal_test.go`), the constructed checks
and `TestMutationsAreCaught` (one mutation per cited bug, behind the
unexported `Config.mut`), and the linearizability checker with its negative
control (`lincheck_internal_test.go`).

The suites cost ~300 CPU-seconds under race and coverage against a 60 s cap
per target, so `raft_test` has 26 shards and Bazel deals them TOP-LEVEL tests:
a seed suite is split into top-level parts (`seedPart`) and a loop of seeds
runs them as parallel subtests (`eachSeed`) — never fewer seeds (#126, #304).
A new heavy suite follows the same shape; measure every shard side by side
under coverage before touching `shard_count`.

## Do NOT

- Iterate a map where the order reaches a message: a run must stay a pure
  function of its seed.
- Acknowledge a snapshot install past its index (review H1,
  `mutSnapshotAckLastIndex`).
- Append, acknowledge or skip an entry kind `Known` refuses: report it in
  `Ready.Unsupported` (review M3).
- Admit a forwarded command without the bounds of a local one
  (`admitForward`, review M2).
- Answer a past index from the engine state as it stands: `EngineAt` reads
  the records `noteEngine` keeps per committed engine entry, folded into the
  base by `Compact` and reset by `InstallSnapshot`. A change to the timing,
  the cluster version or the alarm made anywhere but `applyAdmin` bypasses
  them (#402, `TestEngineAtIsTheStateTheCommittedEntriesThroughTheIndexLeft`).
- Propose a forward each time it is delivered: the TCP transport resends a
  batch whose write failed on a reused connection (#377), so a frame can
  arrive twice. `handleForward` keeps its answers by `(From, Context)` —
  the submission id never repeats, R7-23 — in a ring of `maxFwdAnswers`
  that outlives a term, and answers a duplicate with the recorded answer,
  position or refusal, never proposing it again (#401, `mutForwardNoDedupe`,
  `TestForwardedWritesUnderDuplicationAreAppliedOnce`). Every other kind
  is idempotent under duplication, which that scenario exercises.
- Let a silent node campaign, on its election timer or a TimeoutNow: its
  candidacy is a vote for itself, and the silence grants none. Elected with
  one other grant while the leader was down, it overwrote the acknowledged
  entries it had truncated (#524, `mutSilentCampaigns`,
  `checkSilentNodeNeverCampaigns`).
- Admit a command while the no-space alarm is committed, locally or
  forwarded: `checkProposed` refuses it NO_SPACE and still admits the
  engine entries, the alarm's clearing among them (D17, #515,
  `mutNoSpaceAdmits`, `checkNoSpaceRefusesCommands`).
- Append to a peer an entry at or below `Config.Log.KeptIndex`: the entries
  a Recover kept carry the predecessor's identity, which only a snapshot of
  this lineage tells the peer, so `sendAppend` sends one (#507,
  `mutKeptAppended`, `checkKeptEntriesNeverAppended`).
