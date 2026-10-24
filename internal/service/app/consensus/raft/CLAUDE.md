<!-- updated: 2026-10-24T04:18:33Z -->
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
- Answer, or take as contact, the appends and heartbeats of a leader this
  node refused for the term after `MaxChainRefusals` chain refusals: every
  rejection was contact for CheckQuorum and the trigger of an immediate
  resend, and the refusing majority never campaigned (#508,
  `mutRefusedLeadHeard`, `checkRefusedLeaderIsNotHeard`).
- Admit a DenyPin of this leader, or one that leaves no answering majority
  without its target, or withdraw the pin from the admission set before the
  change commits (#530, `mutDenyPinUnchecked`, `mutDenyPinAtAppend`).
- Report to a heartbeat a contract generation a restart would not rebuild,
  or campaign inside the node's own vote-refusal window: a lease granted on
  that report outlived the window after a crash (#521,
  `mutReportUnsyncedContract`, `mutCampaignInVoteWindow`).
- Size an acknowledgement of a generation the leader does not know as any
  window but none, or keep a node's own InitialTiming as a known contract
  once a snapshot named the cluster's (#522, `mutUnknownGenShortestKnown`,
  `mutOwnTimingKept`).
- Hold a lease under a contract with no drift budget in (0, 1), or run a
  founding contract without its maximum (#523, `mutLeaseWithoutDrift`,
  `mutFoundingAsWritten`).
- Take a read's answer from any peer but the leader this node asked (#510,
  `mutReadRespAnyPeer`).
- Hand the driver the admission set at a commit that did not change it:
  every node paid a SetMembership, an address resolution and an Info line
  per committed entry (#531, `mutMembershipEveryCommit`,
  `checkMembershipPublishedOnChange`).
- Raise the cluster version to 2 while a member of the latest
  configuration was not heard in the term announcing it (FlagProtocol2), or
  apply a raise above the version this node speaks: D18 refuses the raise
  while any member does not speak it (lot S3, `mutRaiseUnannounced`).
- Keep the origin a proposer wrote in an engine entry: at protocol 2 the
  leader writes the authenticated sender, a forwarder or itself (#561,
  `mutOriginAsWritten`). Skip an engine entry whose payload version this
  node does not know: it stops the node (`TestAnUnknownAdminVersionStopsTheNode`).
- Accept from a leader admitted by a `MsgLeaderRecord` alone anything but
  its appends and heartbeats, or take a record from a non-member, at an
  older term, for a known or retired ID, or while a leader is live (#542,
  `mutHintAnyKind`, `TestALeaderRecordIsBoundedAndEnds`).
- End a leader record while its leader is heard, or extend it on anything
  but an append or a heartbeat this node took as that leader's contact at
  its own term: the delay counted from the record cut a leader still
  heard, its snapshot stream included, and a node that hears its leader
  sends no PreVote that could bring a new record (#618,
  `mutHintEndsWhileHeard`, `checkVouchedLeaderAdmittedWhileHeard`,
  `TestAVouchedLeaderEndsItsDelayAfterTheLastContact`).
- End a silence under the leader of the node's own SilentTerm before the
  node's commit reaches the one the append carries, on an append the node
  refused, or on the commit of the snapshot it started from: that leader
  committed the entries the truncation dropped with the node's own
  acknowledgements, and the node voted again without them (#636,
  `mutSilenceEndsBeforeTheLeadersCommit`, `mutSilenceEndsOnARefusal`,
  `mutSilenceIndexUnderTheSnapshot`,
  `TestTheSilenceHoldsUntilTheSameTermLeadersCommit`,
  `TestTheSilenceNeverEndsOnARefusedAppendOverASnapshot`). Nor ignore
  the FlagTruncated heartbeat answer of a silent node: it acknowledges no
  append, the probe never moved past its first batch, and the node stayed
  silent for as long as its leader led (`mutSilentReportIgnored`,
  `TestTheSilenceEndsOnceTheSameTermLeadersCommitIsHeld`).
