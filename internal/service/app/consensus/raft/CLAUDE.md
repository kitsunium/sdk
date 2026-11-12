<!-- updated: 2026-11-12T02:00:00Z -->
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
control (`lincheck_internal_test.go`). `simOptions.speaks` runs a node of a
previous release in a mixed cluster, read at each start so a restart
upgrades it (`TestAMixedClusterRaisesTo3OnceEveryMemberSpeaksIt`).
`simOptions.applyLag` bounds apply by `MaxApplyLagBytes` and makes it
asynchronous, the entries handed by index read back from the disk model
and a restart loading the committed commands past `recent` without their
payloads, as the driver does (`TestSimBoundedApply*`, #663, #664).

`walkthrough_internal_test.go` is D15's datadriven reader (#1112),
stdlib only: `TestDataDriven` runs every `testdata/*.txt`, each a list of
commands to the step function — `add-nodes`, `campaign`, `tick`,
`propose`, `step` (a message written in the script), `read-index`,
`transfer`, `install-snapshot`, `process-ready`, `storage-done`,
`deliver-msgs`, `stabilize`, `raft-state`, `raft-log` — each followed by
`----` and what it printed: the Ready's fields, the messages delivered or
dropped, the writes made durable. A difference fails naming the file, the
command's line and the first line that differs, with a line diff, and
`go test -run TestDataDriven ./raft/ -rewrite` writes the expectations back
(the flag after the package: before it, `go test` reads it as its own). A
change of the step function that moves a script is read in its diff before
it is rewritten. Each script is named after its rule and sits beside the
Go check that pins it, not instead of it: D15's five schedules
(`figure-8-commits-no-older-term-by-count`,
`same-term-vote-kept-on-step-down`, `heartbeat-commit-clamped-to-match`,
`learner-answers-a-vote`, `transfer-flag-at-another-term-is-refused`) and
eleven more (the ack after its sync, the election restriction, the live
leader's refusal, TimeoutNow's term, the verified prefix, an install's
acknowledgement, an older term's append, a forward delivered twice, a
silent node, a read index, an election and its first commit).

The simulator's faults are values a scenario composes (#924,
`nemesis_internal_test.go`): a `nemesis` is a kind on nodes from an
offset for a duration with a parameter — `nemPartition`, `nemSkew` (a
clock rate), `nemStallDisk` (no sync completes and no install is served
until it ends, every write issued meanwhile queued behind it),
`nemCrash`, `nemDrop`, `nemDup`, `nemCrashes` (random crashes per
second), `nemQuiet` (no load) — and `sim.play(schedule, span, load)` runs
a window under a `schedule`, cut at each instant one starts or ends, ends
before starts, every one still holding ended at the span, so a window
leaves the cluster healed with every node up. A schedule draws nothing
from the generator: every scenario of `sim_scenarios_internal_test.go`
is one, and each seed's run is the one the ad-hoc calls it replaced made,
compared bit for bit on all 213 runs when they were rewritten.
`simOptions.watch` runs the durable half of the driver's progress watch
(consensus's `watchProgress`) at each tick, and
`TestSimStalledDiskLeaderHandsOver` (`yieldonstall_internal_test.go`)
stalls the leader's disk for three `ElectionTimeoutMax` under load: it
hands over within one, a follower commits without it, and the same seed
without the watch keeps the stalled leader (#554);
`TestSimComposedScheduleKeepsSafetyAndProgress` composes a partition, a
stall and a skew in one schedule. `FuzzStep`
(`zfuzzstep_internal_test.go`) warms a 3-node cluster up, records every
message, then steps ops that replay a recorded message or forge one
(kind, term, index, log term, commit, entries, reject, receiver):
replays keep every check and end on convergence, a forgery switches off
the checks that compare nodes (`sim.forged`) and keeps each node's own;
`sim.budget` and `maxReadyTurns` fail a run that never settles. Its seed
corpus runs with the suite; `scripts/ci/consensus-fuzz.sh` fuzzes it 10
minutes a night. Both files are named to sort after every seed suite.

Bazel deals top-level tests to shards by their index, so a new test file
whose name sorts before a seed suite's (`checks_*`, `lincheck_*`, `sim_*`,
`unapplied_*`, every file calling `seedPart` or `eachSeed`) moves its seed
parts to other shards: name it after them, or measure the shards again.
`strictsilent_*` sorted before `unapplied_*` and put
`TestSimBoundedApplyP2` beside `TestChecksPassUnmutated` and two more seed
parts, a shard past 60 s under coverage (#790's PR). The datadriven reader
was measured so (#1112): the sums per shard of a `-race -covermode=atomic
-json` run, its subtests counted, on a machine shared with seven other
suites, put the slowest of 27 at 95.7 s, `TestChecksPassUnmutated` beside
`TestSimSafetyUnderFaultsP05`. Named `datadriven_*`, the issue's name, it
sorted after `checks_*` and before the seed parts and made that shard
136.1 s, `TestSimSafetyUnderFaultsP04` and `TestSimBoundedApplyP3` moved
beside it; named `walkthrough_*`, after every seed suite, it leaves each
shard as it was (95.7 s, the reader itself 0.14 s).

The suites cost ~300 CPU-seconds under race and coverage against a 60 s cap
per target, so `raft_test` has 27 shards and Bazel deals them TOP-LEVEL tests:
a seed suite is split into top-level parts (`seedPart`) and a loop of seeds
runs them as parallel subtests (`eachSeed`) — never fewer seeds (#126, #304).
A new heavy suite follows the same shape; measure every shard side by side
under coverage before touching `shard_count`.

## Do NOT

- Copy an appended entry's `Data` out of the message `Step` was handed: the
  entries' Data are the caller's gift (`Step`'s doc), and `appendNew` keeps
  them aliasing the frame the driver copied once out of the transport's
  buffer; an empty one is kept nil. The copy made a follower's append of 64
  entries cost 65 allocations (#1110). A caller that keeps the bytes it
  hands copies them first, as the simulator does at each delivery
  (`ownEntries`).
- Iterate a map where the order reaches a message: a run must stay a pure
  function of its seed.
- Hand apply more than `MaxApplyLagBytes` past what the driver reports it
  holds (`SetApplyLag`), but one entry when it holds nothing, or anything
  in memory while it reads back entries handed by index: a follower slower
  to apply than its leader held everything it was behind by (#664,
  `mutApplyUnbounded`, `checkApplyBounded`; `mutApplyBehindRead`,
  `checkNothingHandedBehindARead`). Nor keep the payloads of the committed
  entries held back past `RecentEntriesBytes`: they are durable, and the
  driver reads them back (`ReadyValue.ApplyRead`, `mutEvictAppliedOnly`,
  `checkHeldPayloadsEvicted`) — but on a node with no table, whose install
  replays the engine effects of what it holds committed from their
  payloads (#416, `mutNoTableEvictsCommitted`,
  `checkNoTableKeepsCommittedPayloads`). Such a node evicts nothing past
  its base, whatever its applied index: a `DurableStateMachine`'s, which
  #707 takes as committed, let the first Ready drop the payloads an
  install below it replays, and the engine state of those entries was
  forgotten for good (#720, `evictable`). A start's `Evicted` loads
  commands only without their payloads; every other entry keeps its own,
  whose effects `NewRaft` applies (#663,
  `TestAStartWithEvictedPayloadsHandsThemByIndex`).
- Send a fetched batch as the store returned it: a store bounds its read
  by the payloads alone, and one append carries no more than
  `MaxBatchBytes` of its entries' encoding (`batchLen`, `wire.EntryLen`),
  the bound Config holds `MaxFrameBytes` above by 1 KiB. Sent whole, a
  batch of 500-byte entries outgrew the follower's frame bound, was refused
  at every probe, and the follower never caught up (#683). Nor ask a fetch
  for more entries than one append carries (`fetchSpan`): a run of small
  entries was read whole for its head alone
  (`TestAFetchedBatchIsCutByItsEncoding`).
- Measure a peer's round trip on `rounds`: a round a majority confirmed
  leaves it at once, and the slower peer of three never measured one.
  `measureRTT` reads the send time from `sentRounds`, the last
  `rttRounds` broadcasts, and measures a round newer than the peer's last,
  so a duplicate or an overtaken answer measures nothing
  (`PeerStatValue.RTT`, #914, `TestAHeartbeatAnswerMeasuresItsRoundTrip`,
  `TestTheSimulatorMeasuresEachPeersRoundTrip`). `Status.PreVotesLost`
  counts a refusal once per voter and campaign, a later term's refusal
  included (`TestAPreVoteRefusalCountsOncePerVoterAndCampaign`).
- Read a heartbeat's `RejectHint` as the leader's last index without
  `FlagProtocol4` beside it, or send one below protocol 4: a leader of
  protocol 3 leaves it 0, and a follower of protocol 3 ignores it on a
  heartbeat, so the field needs no raise (#919, `heartbeatLast`,
  `hearLeader`). `Status.LeaderLast` and `Status.Appended` are read only
  while the leader heard leads the node's term, and a raise to 4 waits for
  every member to announce `FlagProtocol4` (`announcedBy`, a table so that
  `checkAnnounced` stays inlined)
  (`TestAFollowerHearsTheLeadersLastIndexOnlyBesideFlagProtocol4`,
  `TestAMixedClusterOfProtocol3And4KeepsReplicatingAndRefusesTheRaise`).
- Acknowledge a snapshot install past its index (review H1,
  `mutSnapshotAckLastIndex`).
- Append, acknowledge or skip an entry kind `Known` refuses: report it in
  `Ready.Unsupported` (review M3).
- Adopt a snapshot's cluster version above what the node speaks, at a
  start or an install: report it in `Ready.Unsupported` and leave the
  version as it was (`adoptClusterVersion`, `refusesClusterVersion`), and
  refuse such an offer VERSION_UNSUPPORTED in `CheckOffer`. The raise
  replayed from the log was the only one that stopped a node (#640,
  `TestASnapshotAboveTheProtocolStopsTheNode`).
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
- Campaign on a TimeoutNow while the driver holds its apply stalled
  (`SetApplyStalled`): a leader whose apply is stuck hands over (#554) and
  still acknowledges, and the transfer back to it made it lead almost all
  the time. An election is untouched (#674, `mutStalledTakesOver`,
  `checkStalledApplyDeclinesTransfer`).
- Clear a campaign's transfer issuer when it wins: `Status.HandedBy` names it
  while the term lasts, and the driver's watch of `PreferredLeaders` pauses
  after a transfer on it (#676,
  `TestTheStatusNamesTheLeaderThatHandedTheTermOver`).
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
- Re-base an append on the commit without comparing the committed entries
  it carries, or count at once the refusals of a leader whose entry 1
  differs from this founder's committed one: founders given different
  timings each commit their own entry 1, and the odd one refused every
  leader's chain, three refusals before the majority's leader first
  committed, cut off for the term with nothing said (#698).
  `committedDivergence` compares the entries at or below the commit an
  append carries, `foundingDivergence` the chain of the entry after index
  1 (entry 1 alone, the one entry a data directory founded before #698's
  fix holds committed by fiat), each only on a body
  and a chain the leader's own sums verify; `heldOnDivergence` names it in
  `Ready.Diverged` once the leader's commit passed it, and otherwise drops
  the append unanswered and uncounted for `ElectionTimeoutMax`, then lets
  the chain check refuse it (`TestAnAppendOfALeaderThatCommittedPastAnotherEntry1IsADivergence`,
  `TestADivergenceNoCommitProvesIsHeldBackThenRefused`; no mutation, the
  type is full).
- Match a held entry 1 of term 1 on its term alone, or take the chain
  past a held entry from the append rather than from this log: entry 1 is
  the one position no leader created, every founder writing its own from
  its InitialTiming, and a founder that kept its own refused every
  append, or, from index 0, appended the leader's entry 2 on top of it,
  its chain broken (#698). Bootstrap leaves entry 1 uncommitted, and
  `replacesFounding` lets a leader's founding entry over the same members,
  verified by its own sums, replace this node's while it is uncommitted;
  `firstNewEntry` chains past a held entry on `log.chain`, and refuses as a
  broken chain an append carrying another entry at a position held
  uncommitted under the same term. Nor acknowledge an append whose prefix
  is the uncommitted founding entry matched by its term alone: the
  leader's heartbeat, clamped to that match, committed the founder's own
  entry 1 while a majority committed the leader's. `foundingUnproven`
  refuses it uncounted, hint 0, unless its first entry verifies chained on
  this node's entry 1
  (`TestAnUncommittedFoundingEntryGivesWayToTheLeaders`,
  `TestAFoundingEntryIsReplacedOnlyByAVerifiedOneOfTheSameMembers`,
  `TestTheSameFoundingEntryIsKept`,
  `TestAnEntry1MatchedByItsTermAloneIsNeverCommitted`,
  `TestAnotherEntry1ThisNodeKeepsIsNeverAcknowledged`; no mutation,
  the type is full).
- Judge entry 1 on its successor's chain alone (`foundingDivergence`) in
  any log but one that started holding entry 1 alone, committed
  (`fiatFounding`, what a data directory founded before Bootstrap left
  entry 1 uncommitted holds): a node whose entry 1 a leader committed
  stopped `BOOTSTRAP_REFUSED` on an entry 2 whose chain field rotted in
  the leader's memory (#698's review,
  `TestARottedChainPastALeaderCommittedEntry1IsNoDivergence`, its control
  in the same test).
- Refuse in silence a leader's verified founding entry 1 over another
  member list while this node's own is uncommitted: the divergence stop
  asked a committed entry, and such a founder refused every leader for
  good. `foundingContradicted` hands it to `heldOnDivergence`, which names
  it in `Ready.Diverged` once the leader committed past 1 and holds it
  back for `ElectionTimeoutMax` otherwise (#698's review,
  `TestAFounderOfAnotherMemberListStopsOnceTheLeaderCommitsPastEntry1`).
- Report to a heartbeat any generation but 0 while the node holds nothing
  durably committed and no snapshot: its engine base is then its own
  InitialTiming, which no entry confirmed. A founder before its entry 1
  commits reported generation 1 on that guess, the leader sized it with
  its own generation 1, and a founder given a shorter timing granted a
  vote inside the leader's lease (#698's review, `reportedContract`,
  `TestAFounderBeforeItsEntry1CommitsSizesNoLease`, its control in the
  same test).
- Admit a `ChangeSnapshotLimit` from any origin but the member it names
  (the authenticated origin `validateAdmin` holds, a forwarder or this
  leader), of a limit of 0 or below, or of the limit already recorded: only
  a member knows the limit it runs with, a 0 lifts every bound on what it
  is offered, and a repeat appends an entry for nothing. It needs an
  answering majority, as an address change does (`updateSnapshotLimit`,
  #677, `mutLimitAnyOrigin`, `mutLimitZero`, `mutLimitUnchanged`,
  `checkLimitFromItsMemberOnly`, `checkLimitAboveZero`,
  `checkLimitThatChanges`). The kind keeps every change's layout: a leader
  of an earlier release refuses it in `nextConf`'s default branch, and a
  follower of one adopts the membership the payload carries
  (`TestAnOlderReleaseRefusesTheLimitAsLeaderAndAdoptsItAsFollower`).
- Admit a DenyPin of this leader, or one that leaves no answering majority
  without its target, or withdraw the pin from the admission set before the
  change commits (#530, `mutDenyPinUnchecked`, `mutDenyPinAtAppend`). Nor
  refuse one for the stopped voter it leaves with no voucher: it is
  admitted, and `Isolated` names that voter (#773, the owner's option (c)).
- Judge a voter's isolation, for `Isolated` or `GuardRefusal`, other than
  by `reaches`, #603's own count: its zero `reach` is the guard's, and a
  bit set counts one more voucher (`reachUndenied`, a withdrawn pin
  counted back) or takes one bet on the leader less
  (`reachLeaderWhereKnown`, `reachNoBaseBet`) to tell the cause, never to
  admit or refuse a change (#773, #765,
  `TestAStoppedVoterADenyPinLeftWithNoVoucherIsNamedWithItsCause`,
  `TestAVoterReachedOnlyThroughAMovedLeaderIsNamed`,
  `TestAVoterReachedOnlyOnTheCompactedLogsBetIsNamed`). Nor hand `vouches`
  the whole `reach`: the leader is counted as any other voucher by passing
  no leader (`vouchingLeader`), the withdrawn pin as one bool, and
  `vouches` stays under the inline budget kit holds it to (cost 78 of
  80).
- Count a silent voter, one whose last answer carried `FlagTruncated`
  (`peerSilent`), in the strict reconfiguration check (`activeMajority`):
  it answers heartbeats but acknowledges no append, and a removal of a
  live voter admitted over voters {leader, silent} needed an
  acknowledgement that waited for a commit the removal blocked; nothing
  committed again, every node up (#790,
  `TestARemovalThatLeavesASilentVoterInTheMajorityIsRefused`,
  `TestAChangeIsJudgedWithoutTheSilentVoters`; no mutation, the type is
  full). `answering` still counts it, for #603's guard and `checkHeard`: it
  answers a PreVote with the leader's record.
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
  to 3 while one was not heard announcing FlagProtocol3, or apply a raise
  above the version this node speaks: D18 refuses the raise while any
  member does not speak it (lot S3, #648, `mutRaiseUnannounced`,
  `checkRaise3Announced`). A node of protocol 3 announces both flags. Nor
  raise while a member was last heard a vote window ago or more, whatever
  it announced: a voter that announced 3 once and went down was rolled
  back meanwhile, and met the raise only as a snapshot it could not read
  (#665, `checkHeard`, `mutRaiseOverSilence`, `checkRaiseHeardRecently`).
  `PeerStats` says per peer whether its last message announced protocol 3
  (`StatProtocol3`), for the driver that withholds a snapshot of cluster
  version 3 from a node of protocol 2.
- Add a learner over a key the configuration retired, or remove a member,
  or deny its pin, without retiring its key, at protocol 3: one key, one id
  for the cluster's whole life, or a node whose configuration still listed
  the removed member under the key refused the new one as leader (#648,
  `mutRetiredKeyAdmitted`, `mutKeyNotRetired`, `checkRetiredKeyRefused`).
  Below protocol 3, retire no key and write no entry of payload version 3
  (`mutProtocol3BelowRaise`, `checkNothingNewBelowProtocol3`); and read the
  retired keys in `addLearner` alone, never in the admission set or a
  leader record, so a retired key never cuts a live member
  (`TestARetiredKeyNeverCutsALiveMember`).
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
  `mutSilenceEndsBeforeCommit`, `mutSilenceEndsOnARefusal`,
  `mutSilenceIndexBelowSnapshot`,
  `TestTheSilenceHoldsUntilTheSameTermLeadersCommit`,
  `TestTheSilenceNeverEndsOnARefusedAppendOverASnapshot`). Nor ignore
  the FlagTruncated heartbeat answer of a silent node: it acknowledges no
  append, the probe never moved past its first batch, and the node stayed
  silent for as long as its leader led (`mutSilentReportIgnored`,
  `TestTheSilenceEndsOnceTheSameTermLeadersCommitIsHeld`). Nor move that
  commit once the rest of the condition holds (`silenceTarget`), nor fix
  it before, from a refused append or below the highest an accepted one
  carried: the commit each append carries counts the writes made since,
  and a node fed one batch per heartbeat answer stayed silent for as long
  as writes outran it (#766,
  `TestTheSilenceEndsUnderTheSameTermLeadersSustainedWrites`,
  `TestTheSilenceTargetIsFixedOnceTheRestOfTheConditionHolds`).
- End a silence in the Ready whose entries meet its condition, or before
  the log is durable through the commit that met it: the driver removes
  SILENT as soon as a Ready says `SilenceEnded`, before the storage actor
  syncs that Ready's entries, and a crash during their sync restarted a
  voter without them. `maybeEndSilence` records the commit in
  `silenceEndAt`, and `endSilenceOnceDurable` ends the silence from
  `StorageDone` or `InstallSnapshot` once `stable` reaches it (#684,
  `mutSilenceEndsBeforeDurable`, `checkSilenceEndsOnceDurable`). A test
  that judges a silence reports its writes durable first (`stillSilent`).
- Answer with a leader record a PreVote that does not carry FlagProtocol2,
  or wait for the cluster version 2 to send one: a node of protocol 1 does
  not know the kind, and the raise is refused while the stale voter the
  record is for stays unheard (#638, `mutRecordAtClusterVersion`,
  `mutRecordUnannounced`, `checkLeaderRecordFollowsTheAnnouncement`).
- Judge a PreVote behind the commit by its last index alone
  (`behindCommit`): a voter back with an uncommitted tail of an older term,
  longer than the commit, or a learner's question over one, lacks the
  committed entry as surely as a shorter log, and was never sent the record
  on a quiet cluster (#675, `mutRecordByIndexOnly`,
  `checkLeaderRecordReachesAnOldTail`,
  `TestAStaleVoterWithAnOldTailPastTheCommitIsSentTheRecord`,
  `TestALearnerWithAnOldTailPastTheCommitIsSentTheRecord`). Nor send it to
  a log whose last term is the committed entry's at or past its index, or
  a later one at or past it: such a log holds, or may hold, the commit.
- Leave a node that may not campaign and hears no leader waiting for
  nothing, or let its question be a campaign: a learner, a silent voter or
  one with no table never sends the PreVote #542's record answers. A
  learner down while a member was made leader was never fed by it (#639);
  a silent voter whose truncated log does not list that leader was never
  rewritten and never voted again while it led, and a voter with no table
  was never installed (#718). Every follower or learner `promotable`
  excludes asks (`askForLeader`, `mutLearnerNeverAsks`,
  `checkStrandedLearnerAsks`, `checkStrandedVoterAsks`,
  `TestASilentVoterRejoinsALeaderItsLogDoesNotList`). The question is a
  PreVote it never counts, keeping its role and its term, and a campaign
  never asks (`mutAskCampaigns`, `checkLearnerAskBindsNothing`,
  `checkSilentNodeNeverCampaigns`).
- Leave a follower or a learner that hears its leader but cannot reach it
  with no way to learn where that leader listens: the TCP transport answers
  on the node's own outbound link, dialled to the address its
  configuration names, and a member down while `UpdateAddress` moved the
  leader answered the old address for as long as it led (#724). A report
  of that leader unreachable (`ReportUnreachable`, read at the next tick by
  `noteUnreached`) makes the node ask its members but the leader
  (`askForLeaderAddress`), and a record of the leader it follows, under the
  key its configuration pins, from a member, read at a commit past its own
  commit and latest configuration, gives the address its admission set
  names (`takeLeaderAddress`, `dialLeaderAddress`) until its configuration
  catches up (`endLeaderAddress`). Never an admission, another key, or a
  member that does not lead
  (`TestTheLeaderAddressIsTakenOnlyForTheUnreachedLeaderUnderItsKey`,
  `TestTheMemberBehindAMovedLeaderDialsItAtTheCommittedAddress`; no
  mutation, the type is full). Nor test the role again in
  `ReportUnreachable`: only a leader tracks progress, and the test took the
  function past its inlining budget.
- Rank a record of the leader's address below a latest configuration that
  is uncommitted and of a term older than the node's, or end the record on
  one: such a configuration ends a divergent tail the leader of the node's
  term never wrote and never commits (#675), and a node back with one past
  the cluster's commit refused every record and dialled its moved leader's
  old address for as long as the cluster stayed quiet (#755).
  `heldConfIndex` is the latest configuration's index when it is committed
  or of the node's term, the committed one's otherwise
  (`TestTheMemberBehindAMovedLeaderTakesItsAddressOverAnOlderTermsTail`,
  `TestTheTailOfTheLeadersTermStillOutranksTheRecord`; no mutation).
- Answer a compaction at or below the base with nothing: the store may
  hold the log below it, back to the older retained snapshot less the
  trailing window (R6-02), once a restart started the step function at the
  newest one. `Compact` forgets nothing there and still names the point in
  `Ready.Compact`: a node restarted between every two of its cuts never
  compacted its log, and the alarm refused every write (#741,
  `TestACompactionAtOrBelowTheBaseStillReachesTheStore`; no mutation, the
  type is full).
- Hand the admission set without the index it is as of: a joining node's
  transport ranks it against its JOIN answer, as of the `AddLearner` it
  answers, and keeps the newer record of a member both list.
  `Ready.MembershipAt` ranks each record: the latest configuration's
  index, or, for the leader alone, the commit a record of its address was
  read at; raising every record by it let stale ones of other members win
  over a newer answer (`admissionAt`, #742, #724,
  `TestTheAdmissionSetSaysWhatItIsAsOf`; no mutation).
- Start the commit at the persisted one alone under a state machine
  applied past it: a commit-only Ready is written lazily, a
  `DurableStateMachine` crashed past the persisted commit, and `EngineAt`
  at its applied index answered the engine state of that commit, which a
  cut recorded and the node restarted from it ran for good (#707).
  `NewRaft` takes `StartState.Applied` as committed while the log holds it,
  never past the log's end (`startCommit`), and the driver passes such an
  index only when the log holds there the entry the machine applied
  (`TestAStartPastThePersistedCommitTakesTheAppliedIndexAsCommitted`).
- Promote a learner whose acknowledged match is below the configuration
  that admitted it (`admittedAt`: the first appended one that lists it, or
  the base when the base does): `PromoteLag` alone let a learner heard at
  match 0 pass while the commit was young, its joining stamp refused every
  vote, and once the leader failed the voters left elected no one (#722,
  `TestPromoteWaitsForTheLearnerToHoldItsAdmission`,
  `TestPromoteWaitsForTheLearnerToHoldTheBase`; no mutation, the type is
  full).
- Resend a probing peer's probe only with entries: the probe a learner is
  sent as its admission commits reaches a joiner whose transport does not
  admit the leader yet, and is dropped. With nothing written after it, the
  learner was never fed, and its promotion, which waits for it to hold its
  admission, was refused for as long as the cluster stayed quiet. A
  heartbeat answer from a probing peer behind the log probes it again,
  empty if need be (#722, `handleHeartbeatResp`,
  `TestALostProbeOfALearnerIsSentAgainInAQuietLog`).
- Send a probe again, or move a replicating peer to probe, on a heartbeat
  answer that reports less than the oldest append in flight: on a link
  narrower than one batch per heartbeat interval the answer overtakes an
  append still crossing the data lane, and the copies, from match+1 at
  every answer, took some 95 % of the link and the follower never caught
  up (#767). `probeBehind` sends an empty append after next-1 instead,
  behind what is in flight: its refusal finds a lost append, its
  acknowledgement at the match moves a probing peer to replicate. A silent
  peer, which answers no append, is still sent the batch at its next
  (`TestAnAppendStillCrossingANarrowLinkIsNotSentAgain`,
  `TestAProbeStillCrossingANarrowLinkIsNotSentAgain`,
  `TestAFollowerCatchesUpOverALinkNarrowerThanABatchPerHeartbeat`,
  `TestALostAppendIsStillProbedOnHeartbeats`; no mutation, the type is
  full).
- Leave a silent peer in replicate on its FlagTruncated heartbeat answer:
  it acknowledges nothing, so its window never moves again, and with that
  window full it was sent only `probeBehind`'s empty append, never an
  entry of the leader's term, and stayed silent for as long as that leader
  led. Only an unreachable report, which nothing ensures reaches the
  leader, moved it to probe. `followSilentReport` probes it, its match
  kept, past the commit it reports (#774,
  `TestASilentPeerLeftInReplicateWithAFullWindowEndsItsSilence`; no
  mutation, the type is full).
- Leave a silent peer whose heartbeat answer reports this leader's whole
  log committed waiting for a write: truncated past its last entry, its
  truncation point lies past that log, `refeed` has nothing to send, and
  in a cluster that wrote nothing it stayed silent for as long as nobody
  wrote, one failure from no leader (#787). `feedSilence` appends one
  no-op of the term for it, once per silence (`progress.silentFed`, its
  index, cleared with `peerSilent`) and never during a transfer
  (`TestASilentNodeHoldingTheWholeLogEndsItsSilenceInAQuietCluster`,
  `TestTheLeaderAppendsOneNoOpPerSilence`). Nor keep that no-op spent
  once a snapshot transfer to the peer covered it: a node restarted
  between that install and the leader's next append came back with its
  point raised past the leader's whole log (`raiseSilenceOverSnapshot`),
  and stayed silent while nobody wrote (#795). `SnapshotDone` clears
  `silentFed` when the snapshot installed is at or past it, so the next
  silent answer that reports the whole log is fed one more no-op. Each
  further no-op needs a whole state's transfer, and an unreadable
  condition is still fed once a term in a quiet cluster. A snapshot the
  node cut itself over the no-op is not seen: restarted on it before its
  end is durable, it reports what an unreadable condition reports, and
  only its point on the wire would tell them apart (ADR 0152's #795
  amendment)
  (`TestASilentNodeRestartedOnAnInstalledSnapshotEndsItsSilenceInAQuietCluster`,
  `TestATransferFeedsASilenceNoTermEndsOnceMore`,
  `TestOnlyATransferOverTheNoOpFeedsTheSilentPeerAgain`; no mutation, the
  type is full). Nor keep a silent node's
  truncation point above a conflict an append of a leader of a term at
  least `SilentTerm` finds below it: no entry the node held from there on
  ever counted towards a commit (Leader Completeness, Log Matching), and
  past a tail of an older term the leader's log may never reach the old
  point (`lowerSilence`,
  `TestASilentNodeOverAnOlderTermsTailEndsItsSilenceInAQuietCluster`,
  `TestAConflictLowersTheTruncationPointOnlyUnderALeaderOfItsTerm`; no
  mutation, the type is full).
- Lower a silence's truncation point in memory alone: the truncation that
  justifies it is durable, and a node restarted before the commit reached
  it came back on the old point with a log that no longer reached it, and
  stayed silent in a cluster that wrote nothing (#793). `lowerSilence`
  names the point in `ReadyValue.SilenceLowered`, in the Ready that
  carries the truncation, for the driver to persist first
  (`TestALoweredSilencePointSurvivesARestartBeforeTheCommit`,
  `TestOnlyALoweringLeavesInTheReady`; no mutation, the type is full).
- Keep a silent node's truncation point above a snapshot it installs over
  a log that held the snapshot's index under another term: the snapshot is
  committed, so no entry of that log from there on ever counted towards a
  commit (Log Matching), and a node brought onto the leader's snapshot over
  a tail of an older term kept a point past the leader's whole log, silent
  in a cluster that wrote nothing (#792). `restartLogAt` lowers it to the
  index after the snapshot through `lowerSilence`, asked at `SilentTerm`
  itself: the proof is the snapshot's commit, whatever its sender's term,
  and a node whose truncation dropped a later term's record (#418) may be
  installed by a leader below it. A boundary held at its term keeps the
  suffix for the next append, and a log that ends before the index proves
  nothing
  (`TestASilentNodeInstalledOverAnOlderTermsTailEndsItsSilenceInAQuietCluster`,
  `TestAnInstalledSilencePointSurvivesARestart`,
  `TestAnInstallLowersTheSilencePointOnlyOverAnotherTerm`; no mutation, the
  type is full).
