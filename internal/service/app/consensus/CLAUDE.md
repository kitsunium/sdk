<!-- updated: 2026-10-27T03:00:00Z -->
# internal/service/app/consensus/

## Purpose

The Raft engine of ADR 0152 and everything it ships: the event loop over the
step function (`raft/`), the storage actor, the apply goroutine, snapshots
in checksummed chunks, the segmented file log and its recovery procedure,
the no-space guard over the log quota and the free space (`space.go`),
the file snapshot store, the data directory with its lock, stamp and stop
markers, the offline operations (`Init`, `Recover`, `RestoreSnapshot`,
`Inspect`, `ClearQuarantine`), what a non-member observer reads and
rebuilds from (`Node.ReadCommitted`, COMPACTED below the log's first
index; `OpenSnapshot`, SNAPSHOT_CORRUPT at the end of a stream that
differs from its metadata; `ImportObserved`, a snapshot and the tail read
after it, its chain checked from the snapshot's `ChainTip` before anything
is written, grouped as an `ImportParams`), the mTLS TCP transport over `net/tlsid` and
`net/server.Admission` (ADR 0174), and the in-memory doubles. It declares no
code: it raises the core's.

Platforms. The data directory's lock is `fs/flock`: where it has none
(`flock.Native` false — illumos and Solaris, ADR 0144) `NewDataDir` refuses
with `proc.UnsupportedPlatform` before writing anything, and the suite asserts
that refusal and skips what needs a directory. The free-space gauge
(`consensus_fs_free_bytes`, `fsfree_*.go`) is read with statfs(2) on Linux,
macOS, FreeBSD, DragonFly and OpenBSD and with GetDiskFreeSpaceEx on Windows;
on NetBSD, illumos and Solaris, whose statvfs(2) the standard library does not
wrap, it is not published (`TestResourceMetrics` asserts both).

## Lanes

| Lane | How |
|---|---|
| unit, cluster, disk, TCP | `go test -race ./...` |
| simulator soak | `CONSENSUS_SIM_SOAK=1 go test -run TestSimSoak ./raft/`; a seed replays with `CONSENSUS_SIM_SEED` |
| fuzz | `go test -fuzz` on the targets of `wire/` and this package; nightly in CI |
| dm-crypt | `testdata/dmcrypt/run.sh` (Docker, privileged): fsync latency and power cuts through LUKS; results in `testdata/dmcrypt/RESULTS.md` |

Under Bazel `consensus_test` has 24 shards: under race and coverage the suite
passed the 60 s cap per target once the disk scenarios of #507, #516 and
#524 joined it (4 shards), and the snapshot and disk scenarios of #506,
#512, #513, #515, #520, #525, #526 and #533 brought the slowest of 4 to
28 s on an 8-core machine, too close to the cap on a CI runner (6
shards). The slowest of 6 reached 52 s under coverage on CI, and #648's
scenarios took it past 60 s: Bazel deals top-level tests by index, and
the sums per shard of a `-race -json` run, 43 s for the slowest of 6 on
one machine, are 31 s at 8 (8 shards). Shard 5 of 6 had timed out under
`bazel coverage` on main as well (60.1 s, after its PASS). The suite
waits rather than computes: that shard spent 6.7 s of CPU in 63 s, and
coverage adds 7 % to the whole suite, so no grid shrinks under
`testing.CoverMode()`. Dealt by index (`i % shard_count`), the 10 to 14 s
scenarios (`TestDiskClusterSurvivesFullRestart`,
`TestTheEntriesRecoverKeepsHaveOneTokenOnEveryNode`, the ten election
timeouts of `TestAPinThatEndsCompactsTheLogWithoutAWrite`'s pin) still
paired at 8 and 10: on a loaded machine the slowest of 8 summed 49 to
53 s, of 12 37 to 39 s. #663's tests shifted every index past them, and
shard 3 of 12 timed out under coverage after its PASS: it now dealt
`TestAMetaOfAVersionThisBuildCannotReadRefusesTheOpen` beside
`TestHealthWarnsBeforeTheSnapshotLimit`, and the former's data directory,
on 64 MiB segments, spent 25 of its 26 s scanning the unused tail of the
active segment at reopen, a loop coverage slows six times where it adds
7 % to the suite as a whole. It opens on 64 KiB segments now (0.05 s).
The two heaviest left, `TestReopeningAFollowersLogDoesNotHoldItsHardStates`
(23 s) and `TestTheStartHoldsNoMoreOfTheTailThanItsBudget` (22 s, the
collector at GOGC=10), fill most of a shard wherever they are dealt, so the
rest is spread thinner (24 shards): the sums per shard of a `-race
-covermode=atomic -json` run, 43.6 s for the slowest of 12 on one
machine, are 32.9 s at 24, and 34 s for the slowest on average over
4,000 draws of up to 40 tests inserted anywhere (36 to 37 s at 19 or
20, 31 s at 28).

The BSD, illumos and Solaris guests of `e2e-cross.yml` run one test binary
per package under a 5-minute `-test.timeout`, the alarm a hang panics on
(`scripts/ci/run-test-binaries.sh`). Whole, this suite took 272 to 301 s
on NetBSD 10.1 and 268 s on OpenBSD 7.9, and timed out on NetBSD in 19
runs of 55 (#701): `scripts/ci/test-binaries.sh` cuts it in two runs,
`-test.run=^TestA` and `-test.skip=^TestA`, each under that cap, which
stays 5 minutes. Measured with `-test.v` on NetBSD, `^TestA` held 163 s
of the 298 and the rest 135; `TestTheStartHoldsNoMoreOfTheTailThanItsBudget`
took 18 s there (2 s on a Linux workstation) and
`TestHealthWarnsBeforeTheSnapshotLimit` 13 s, settling its cuts after each
of some 150 writes of 40 bytes: it writes 200 bytes now, some 30 writes.

## Do NOT

- Let the event loop wait on storage or a socket (D5b).
- Retry a failed sync: the node stops (D4).
- Return a snapshot's STORAGE_FAILED without stopping the node: every cut
  ends in `cutFailed` (on its trigger, coalesced, requested by a peer, saved,
  aside or on the apply goroutine), and an install whose `Commit` fails it
  stops the node before it answers the leader. A requested cut written on the apply goroutine and an
  install used to drop it, and the node kept voting, acknowledging and
  appending on a disk that had failed an fsync (#404,
  `TestARequestedCutWhoseSyncFailsStopsTheNode`,
  `TestAnInstallWhoseSyncFailsStopsTheNode`).
- Truncate a hole before every segment is classified (R8-02, R9-03).
- Serve a log recovery read without syncing it first: a process killed
  between a batch's write and its fdatasync leaves the batch in the page
  cache, and a same-boot restart would acknowledge it from there.
  `syncAtOpen` fdatasyncs every segment and syncs the log, data and
  snapshot directories before anything is trusted (#414,
  `TestRecoverySyncsWhatItTrustsBeforeServing`).
- Return a sync that failed at open without the STORAGE_FAILED markers. The
  kernel reports a failed write-back once, to the first descriptor that
  syncs (errseq): the open that saw it failed with no marker, and the next
  one in the same boot synced clean and trusted pages the disk had refused.
  `syncFailedAtOpen` writes them first, boot-scoped one first, for a
  segment's, the log, data or snapshot directory's, or a zeroed torn tail's
  sync, and `noteUnmarked` covers a device that refuses both (#637,
  `TestASyncThatFailsAtOpenRefusesTheSameBootRestart`,
  `TestAnUnmarkedSyncFailureAtOpenRefusesAnyLaterOpenInTheProcess`). Nor
  write them for a failure that is no sync's: a read at open, or a
  directory that cannot be opened to be synced (a descriptor table full,
  `syncRan`), consumes no write-back error, and a marker would refuse every
  start until the reboot (`TestAReadThatFailsAtOpenWritesNoMarker`,
  `TestADirectoryThatCannotBeOpenedToSyncWritesNoMarker`). A recovery that
  fails closes the segments it opened
  (`TestAFailedRecoveryClosesTheSegmentsItOpened`).
- Hold the log in memory to recover it: every segment read whole and kept,
  with an 88-byte record per record pointing into them, took one to three
  times the log in heap until `recover` returned, and a node whose log grew
  while its compaction stalled was OOM-killed at every restart, `Inspect`
  and WipeAndRejoin with it. `scanSegments` reads one segment at a time
  into the same buffer (`scanBufs`), a sealed one up to its seal and the
  rest of a file only for a hole (`segRead`); only the last segment keeps
  its bytes and a 40-byte `scanRec` per record, and replay sizes the index
  once (#630, `TestReopeningTheLogHoldsOneSegmentAtATime`,
  `TestASealedSegmentIsReadUpToItsSeal`, `FuzzScanSegment`). Nor parse
  bytes past `segRead.n`: they are the previous segment's, and
  `segRead.record` reads what `parseRecord` judges first.
- Keep a segment's hard states past its scan when it is not the last: a
  follower writes one at nearly every Ready, and a `scanRec` and a copied
  payload for each kept 0.5 to 0.9 times the log in heap until `recover`
  returned. The next segment's checkpoint sets the hard state again
  (`applyCheckpoint`), so an earlier segment keeps its checkpoint, decoded
  (`scanned.cp`), and every record but its hard states and commits
  (`replayed`); the last one's hard states are read back from its bytes
  (`payloadAt`). Nor judge the batch sequence or the layouts on the records
  kept: `batchSeq` and `scanned.note` judge every record read, and the
  first break, then the first unknown layout, are refused in log order, as
  before (#660, `TestReopeningAFollowersLogDoesNotHoldItsHardStates`,
  `TestAnEarlierSegmentIsJudgedOnEveryRecordItHeld`).
- Read the tail past the snapshot whole, or hand apply every committed
  entry: `readTail` read every entry with its payload, raft's first Ready
  handed them all to apply, and so did every Ready of a follower slower to
  apply than its leader. A node needed several times its log in heap to
  start, and a slow follower held everything it was behind by until it was
  OOM-killed (#663, #664). `readTail` reads `MaxBatchBytes` at a time into
  one slice raft keeps as its log, a quarter of room past it, and past
  `RecentEntriesBytes` drops the payloads of the oldest committed commands
  (`StartState.Evicted`; every other kind keeps its own, which `NewRaft`
  applies). Raft hands apply `MaxApplyLagBytes` at most past what the
  apply goroutine holds (`SetApplyLag`, from `handleReady`), drops the
  payloads of committed entries past `RecentEntriesBytes` whether handed or
  not, and hands those by index (`ReadyValue.ApplyRead`): `applyRead`
  reads them back on the apply goroutine, a batch at a time within both
  bounds, in order, past what a restore covers, and stops the node when
  the log cannot give back what it holds committed. Nothing is handed in
  memory behind a read, and a batch leaves the apply lag once the machine
  applied it, by the bytes it was handed. Nor admit a command while raft
  holds committed entries back or a read is pending (`applyOverloaded`):
  the bytes apply holds no longer measure the gap between commit and
  applied, and a follower's forwards grew it with no bound. The apply
  goroutine wakes the loop once it has room (`applyMoved`, `awaitApply`)
  (`TestTheStartHoldsNoMoreOfTheTailThanItsBudget`,
  `TestAFollowerSlowToApplyHoldsNoMoreThanMaxApplyLagBytes`,
  `TestALeaderSlowToApplyStillRefusesForwardsPastMaxApplyLagBytes`,
  `TestAMachineThatDropsItsPayloadsLeavesNoApplyLag`,
  `TestADurableMachineResumesInsideTheTailReadBack`).
- Sample a heap peak in the package's own process: the tests before it
  leave hundreds of MiB live, and the collector lets a share of that grow
  in garbage on top of what is measured.
  `TestTheStartHoldsNoMoreOfTheTailThanItsBudget` measures in a child
  process of the test binary (`startInAChild`); in the package's process it
  sampled 60 to 87 MiB over a 600 MiB baseline on every CI runner, whatever
  the start held.
- Compact the store past its end without the term: it holds no entry to read
  it from, and a node restarted with no table votes on that term. The engine
  passes it (`storeReq.compactTerm`, `compactLog`), the file log refuses a
  compaction past its end without one, and `noTableBase` never takes a term
  of 0 above index 0 (#415,
  `TestACompactionPastTheEndRecordsTheTermItIsGiven`,
  `TestANoTableBaseNeverTakesATermOfZero`, and through the driver
  `TestClusterSnapshotInstallsOnALaggingNode`).
- Seal a segment again in recovery: a durable seal only needs the next
  segment published (`publishNext`, review H2).
- Restore a snapshot the machine has applied past, or install one whose
  freshness was not checked again after the transfer (review H3).
- Move raft onto an install it committed past while the machine restored
  it, or onto any install on a leader: `InstallSnapshot` ignores both, or
  the engine state of the entries committed past the snapshot is rolled
  back and carried by the next cut — a node with a table evicts the
  payloads it committed, so they cannot be replayed (#416,
  `TestAnInstallBelowTheCommitLeavesTheEngineStateAlone`).
- Sync once per queued Ready: every request the storage actor takes is
  staged, then synced ONCE, then completed in order, so no `AfterSync`
  leaves before the sync that covers it; a group of commit-only Readys is
  not synced, its record rides the next sync (#558,
  `TestABacklogOfWritesIsSyncedAsOneGroup`). Nor sync a group that writes
  nothing once every earlier write is synced: its acknowledgements vouch
  for durable writes, and a follower draining a backlog paid one Sync per
  heartbeat or resent append it answered (#654,
  `TestAGroupOfAcknowledgementsAloneIsNotSynced`).
- Probe a replicating peer whose heartbeat answer reports holding the
  oldest append in flight: its acknowledgement is only late, and the probe
  resent a window it held (#560, `TestASlowAcknowledgementIsNotALostAppend`).
- Hold `mu` across a batch's write and fdatasync: writers serialise on
  `wmu`, readers must not wait for the disk (review L6).
- Write a batch's end record before the rest of the batch is durable: a
  power cut during one fdatasync may persist the end record's page without
  an earlier one's, and recovery quarantines a valid end after a hole.
  `writeBatch` syncs the body, then writes and syncs the end record (#417,
  `TestTheEndRecordIsWrittenOnceTheRestOfItsBatchIsDurable`).
- Bound a read of the store by the event loop's commit: the loop commits a
  suffix it truncated and appended in memory before the storage actor
  rewrote it, and an install moves the commit before the store. A reader
  stops at what the store holds as committed — its own `HardState().Commit`,
  or `logApplied`, the last entry applied from the log — never at the loop's
  commit alone (#400, `TestReadCommittedNeverReadsASuffixTheStoreHasNotRewritten`),
  and never at the store's commit alone, which trails an entry Propose
  already answered (`TestReadCommittedReturnsAnAppliedEntryAtOnce`).
- Truncate for `TruncateAndStaySilent` before the silence condition is
  durable, or persist a term below one a crashed run already wrote: a rerun
  reads a log whose dropped records no longer name their term (#418,
  `TestARerunOfTruncateAndStaySilentKeepsTheDroppedTerm`).
- Restore a snapshot without answering the proposals it covers: applied1
  never runs at their indexes and `sweepWaiters` answers only above the
  commit. `answerCovered` answers them OUTCOME_UNKNOWN and releases their
  pending bytes (#419, `TestARestoredSnapshotAnswersTheProposalsItCovers`).
- Wait for a proposal's answer on `p.done` and the context alone: a submit
  racing a stop can land in `propc` after the loop drained it for the last
  time. `awaitProposal` also waits for `loopDone` (#420,
  `TestAProposeThatRacesAStopIsAnswered`).
- Run generation 1 on a node's own `InitialTiming`: the founding entry
  carries the contract `Bootstrap` proposed, and it rules once committed
  (`commitFounding`); a snapshot's contract is adopted at an equal generation
  (#421, `TestTheFoundingContractRules`).
- Answer a pinned snapshot's `Remove` with STORAGE_FAILED, or a failed
  unlink or directory sync with anything else: `retain` stops the node on
  the second (D4) and only logs the first, a transfer in flight (#422,
  `TestRemovingAPinnedSnapshotIsNotAStorageFailure`,
  `TestARetentionWhoseRemoveFailsStopsTheNode`).
- Convert a length read off the wire or the disk to `int` before bounding
  it: on 32 bits it turns negative and passes the bound (#422,
  `TestReadChunkRefusesALengthAboveTheLimit`, run by `test-386`). Nor add
  to it as an int, nor make a buffer of an int64 size an int may not hold:
  near 2^31 the sum wraps negative, and the bound passes or make panics.
  A torn log tail stopped every open, a member's frame the node, and an
  offer's chunk size the node verifying the snapshot (#630,
  `TestARecordOfLength2To31IsAHole`,
  `TestReadFrameRefusesALengthThatWrapsOn32Bits`,
  `TestASegmentAnIntDoesNotHoldIsRefusedOn32Bits`,
  `TestASnapshotChunkAnIntDoesNotHoldIsRefused`).
- Keep `Recover`'s index at a persisted commit below the log's base: the
  forced entry cannot be appended there, and every rerun failed the same
  way. `adoptedPastTheBase` adopts from the base, and refuses with a reason
  when nothing records the chain there (#422,
  `TestRecoverAdoptsPastABaseAboveThePersistedCommit`).
- Resume a spool across an empty chunk: the writer never emits one, and a
  region of zeros reads as a run of them (#422,
  `TestASpoolResumesBeforeARegionOfZeros`).
- Create a directory of the data directory without `mkdirDurable`, or put
  the data directory on NFS: Linux emulates its `flock` with POSIX locks,
  which one process does not exclude from itself (#422, ADR 0152).
- Take a JOIN answer from an unpinned peer (`JoinConfig.Pins`, review M4),
  or admit a key two members share (review M5).
- Admit at the JOIN, once the cluster is at protocol 3, the key of a member
  removed or whose pin was denied, under any new id: one key, one id for
  the cluster's whole life. A host back over a removed member's key file
  led under a new id, and every node whose configuration still listed the
  removed member under that key refused it as leader, a stale voter as a
  joiner (#648, `TestAJoinOnARemovedMembersKeyIsRefusedOnceTheProtocolIsRaised`;
  raft's `checkRetiredKeyRefused`). Nor keep a retired key below the raise
  (`TestBelowProtocol3ARemovedMembersKeyIsAdmittedAsBefore`), drop the
  retired keys at a `Recover` or a `RestoreSnapshot`
  (`TestRecoverKeepsTheRetiredKeysOfAHistoryAtProtocol3`,
  `TestARestoredSnapshotKeepsItsRetiredKeys`) or at an install
  (`TestANodeThatInstallsASnapshotRefusesItsRetiredKeys`), or read them
  anywhere but the JOIN and raft's `addLearner`: a retired key never cuts a
  live member, and the transport's admission stays M5's.
- Extend the JOIN of `laneJoin` in place: a member that predates a field
  reads it as part of the address. A joiner announcing its
  `MaxSnapshotBytes` uses `laneJoinV2` (`joinVersion2`, the limit, then the
  old JOIN) and, when the member ends that connection unanswered — closed
  or reset, never a deadline — sends the same JOIN on `laneJoin`; one of
  another version is refused, never read as one this build knows. The
  learner records the limit, and `Health` reports `SnapshotBytes`,
  `SnapshotLimit` and `HealthSnapshotNearLimit` from four fifths of it
  (R2-30, #556, `TestTCPJoinAnnouncesItsSnapshotLimit`,
  `TestTCPJoinFallsBackOnAMemberThatPredatesTheLane`,
  `TestHealthWarnsBeforeTheSnapshotLimit`).
- Leave the term of the last entry out of `Inspect`: the survivor of a
  `Recover` is the highest `(LastTerm, Last)`, and a longer log or a higher
  vote term chose one whose tail no majority held (#534,
  `TestInspectReportsTheTermOfTheLastEntry`).
- Cut `Recover`'s log back to the commit the survivor persisted: that commit
  lags the cluster's, and the entries past it may be writes the old cluster
  acknowledged. `adoptedEnd` keeps every entry whose `BodySum` and
  `ChainSum` verify from the chain at that commit and cuts at the first
  broken link; the audit line reports how many were adopted. The price,
  stated in ADR 0152 D17: an entry refused `ENTRY_SUPERSEDED` may be applied
  in the new history, a duplicate and never a loss (#399,
  `TestRecoverAdoptsTheVerifiedSuffixPastThePersistedCommit`,
  `TestRecoverCutsTheSuffixAtTheFirstBrokenLink`).
- Write `Recover`'s forced entry before `STOP-RECOVER` is durable, or remove
  the marker before the new stamp is: between the two writes the directory
  holds the new members' configuration under the old cluster's stamp, and
  it started in the old cluster (#405). A rerun to the same cluster resumes
  from the index the marker kept, never from `adoptedEnd` over a log ending
  in the interrupted forced entry, which would read the new members as the
  old voters and ask for no fence (`TestAnInterruptedRecoverRefusesAStartAndResumes`,
  `TestARecoverInterruptedAfterItsStampIsCompleted`).
- Report a refused transfer as done on the node's own view that the target
  leads: a deposed leader that has not heard its successor still names
  itself. `confirmLeader` makes the view a fact with a barrier first.
- Fill a cut's metadata from the engine state standing now: `metaAt`
  reads `raft.EngineAt(idx)`, the timing, cluster version and alarm the
  committed entries through the cut left. An engine entry commits ahead of
  the applied index, and a snapshot carrying it named a contract its
  installer never applied (#402, `TestACutCarriesTheEngineStateAtItsIndex`).
- Publish a snapshot without `retain` after it: a cut and an install both
  keep the two newest (D7, R6-02). An install used to skip it and left three
  until the node's next cut (#304, `TestAnInstallKeepsTheTwoNewestSnapshots`).
- Dial a sender link (control or data lane) without `boundUnacknowledged`,
  or drop the batch a reused connection failed: a link quiet since a
  partition keeps taking small writes into a stream the kernel retransmits
  under exponential backoff, and an election waited 15 s for it
  (`TestTCPFrameOnALinkThatDiedQuietlyIsDelivered`). The snapshot and JOIN
  lanes stay unbounded: a receiver writing a snapshot may stop reading.
- Drop a snapshot trigger that falls while a cut is in flight: D7 coalesces
  it (`snapState.pending`, `cutPending`), and the cut is made once the one in
  flight is written. Dropped, a node that applied nothing more kept its tail
  uncut and its log uncompacted (#375, `TestACoalescedCutIsMade`). A write
  that fails clears the coalesced trigger with the in-flight flag
  (`TestAFailedCutClearsTheCoalescedTrigger`), and a `SaveSnapshot` asks
  for the one coalesced while it waited for the apply goroutine (#604,
  `TestATriggerCoalescedDuringASaveIsAsked`).
- Let a stopped node of a memory-network test hear what the cluster sent
  while it was down: the network keeps those frames and delivers them at the
  restart, which catches the node up by appends where the test meant an
  install. Isolate it before it stops (#374,
  `TestInstallResumesAfterACrashAtPublication`).
- Note a compact record in replay and apply it only once the replay is
  done: a compaction past the log's end moves its start past it, and the
  entry appended next — the state of every node that caught up by an
  install, a `RestoreSnapshot` that took a write — read as out of sequence
  and quarantined a sound disk at the next restart. `applyCompactRecord`
  applies it where it stands, as `compactPrefixAt` did (#516,
  `TestAnAppendAfterACompactionPastTheEndSurvivesARestart`,
  `TestAnInstalledNodeRestartsAfterAppending`).
- Release from the apply lag only the head of a batch a
  `BatchingStateMachine` applies: `queueApply` counts every entry, and the
  bytes of entries 2..n stayed counted until every proposal was refused
  OVERLOADED (#514, `TestBatchingMachineReleasesApplyLag`).
- End the TCP accept loop on anything but its listener's close: one EMFILE
  left the node sending and never receiving again. A failed Accept waits a
  backoff from 5 ms to 1 s (#518, `TestTCPAcceptSurvivesATransientFailure`).
- Ship a snapshot of another lineage, or below a Recover's kept index, or
  install one that does not continue the node's own: the entries Recover
  kept are applied under the predecessor's token on every node, and a node
  admitted afterwards learns the kept index from the snapshot it installs
  (`adoptLineage`, durable before the restore), never from an append —
  raft sends a snapshot to a peer below the kept index. It applied them
  under the new identity, and the state built on the token diverged (#507,
  `TestTheEntriesRecoverKeepsHaveOneTokenOnEveryNode`,
  `TestAnOfferOfAnotherLineageIsRefused`).
- Keep the silence condition across `Recover`: a silent node never
  campaigns (raft's `promotable`), and a survivor left silent would be the
  one voter of its new history and never be elected (#524,
  `TestRecoverEndsTheSilence`; the election side,
  `TestASilentNodeIsNeverElected`).
- End a silence under the leader of the node's own SilentTerm before the
  node holds that leader's commit, or on an append it refused: that leader
  committed the entries the truncation dropped with the node's own
  acknowledgements, one batch past the truncation point committed an entry
  of its term, and the node voted again without them (#636,
  `TestASilentNodeVotesAgainOnlyWithTheWritesItAcknowledged`; the
  liveness half, the silent node caught up across batches it never
  acknowledges, `TestASilentNodeRejoinsUnderItsLeaderWithTheWritesItAcknowledged`).
- End a silence on disk before the entries that meet its condition are: raft
  judged it on the append's entries, in memory, and said so in the same
  Ready, and `endSilence` removed SILENT on the loop before the storage
  actor synced them. A crash during that fdatasync restarted a voter whose
  log stopped at the truncation point, elected at the leader's next failure
  over the writes it had acknowledged. Raft now ends the silence once
  `StorageDone` reports the log durable through the commit that met the
  condition, and the driver removes SILENT in the Ready after (#684,
  `TestTheEndOfASilenceIsDurableOnlyAfterTheEntriesThatEndIt`).
- Turn a joining stamp into a member's on raft's commit alone: it moves
  when an append is accepted, in memory, and `checkAdmission` wrote the
  stamp on the loop in the turn that handed the storage actor the entries
  that admit the node, before they were written. A crash during their sync
  restarted a member with an empty configuration, which admitted no peer
  and never sent a JOIN again, and every raise was refused while it stayed
  listed. The admission is judged at the commit bounded by `Stable`, at
  each Ready and each storage completion (#695,
  `TestAJoinerBecomesAMemberOnDiskOnlyOnceTheEntriesThatAdmitItAreDurable`).
- Refuse a JOIN on a held slot when the member holding it is the requester
  itself — id, incarnation, slot and key all equal: a joiner restarted
  before anything reached its log admits the members only from a JOIN
  answer, and was locked out for good. `alreadyJoined` answers it and
  proposes nothing (#533, `TestAJoinerRestartedBeforeItsFirstInstallRejoins`).
- Remove the snapshot a node with no table holds before its replacement
  is published, or an installed copy that did not restore on such a node:
  its members are read from that metadata, and a node left none admitted
  nobody at its next start. A start with nothing naming the members is
  refused SNAPSHOT_CORRUPT (#526,
  `TestANodeWithNoTableKeepsItsMembersThroughAFailedReplacement`,
  `TestANodeWithNothingNamingItsMembersIsRefused`).
- Skip a snapshot of a version or codec this build does not know: a
  Restore refused VERSION_UNSUPPORTED, an offer of an unknown metadata
  version or codec, stop the node (D18), and a start over one is refused.
  A snapshot is decoded with the codec its metadata names (#513, #512,
  `TestASnapshotOfAnUnknownFormatStopsTheNode`,
  `TestASnapshotIsReadWithTheCodecItWasWrittenWith`).
- Refuse at the transport, unjudged, an offer whose metadata does not
  decode for its version, or skip such a `.meta` as one that rotted: the
  stop of #513 and #640 runs only on a metadata that decodes, and the
  release of protocol 2, offered #659's version 3, left replication
  without a word while its leader offered it again at every probe (#665).
  `refuseOffer` hands such an offer to the node with its term
  (`undecodableBinder`), which stops VERSION_UNSUPPORTED when it comes from
  its leader in its term and refuses NOT_LEADER otherwise
  (`TestAnOfferOfAMetadataVersionThisBuildCannotReadStopsTheNode`). A
  `.meta` of a version this build does not know whose trailing CRC-32C
  holds, what a newer build wrote, refuses the store's open, and so the
  data directory's, before anything is removed, and a listing reports it;
  one whose sum fails rotted and is skipped (`newerMetas`, `newerMeta`,
  `TestAMetaOfAVersionThisBuildCannotReadRefusesTheOpen`).
- Offer a snapshot at cluster version 3 to a peer heard in the term whose
  last message announced no protocol 3: a release of protocol 2 cannot
  decode its version 3 metadata and does not stop on it (#665). The
  leader withholds it, says so once per snapshot, and offers it once the
  peer announces 3 (`unannounced`, raft's `StatProtocol3`,
  `TestALeaderWithholdsAVersion3SnapshotFromAPeerOfProtocol2`).
- Take a snapshot's `ClusterVersion` without comparing it with
  `ProtocolMax`. Once the log is compacted past a raise, the snapshot is
  all that carries it: a node rolled back started or installed it and ran
  at a version whose formats it cannot read back. `knownClusterVersion`
  refuses it VERSION_UNSUPPORTED in `snapshotCodec` (a start, an offer) and
  `restoreCodec` (`RestoreSnapshot`, `OpenSnapshot`, `ImportObserved`), and
  raft flags it at `NewRaft`, `CheckOffer` and `InstallSnapshot` (#640,
  `TestANodeRefusesToStartOnASnapshotAboveItsProtocol`,
  `TestAnOfferOfASnapshotAboveTheProtocolStopsTheNode`,
  `TestTheOfflineReadersRefuseASnapshotAboveTheProtocol`). Nor skip a
  snapshot `verifySnapshotFile` refuses VERSION_UNSUPPORTED as if it failed
  its checksums: `interruptedInstall` did, and Recover planned from the
  older log below an install above the protocol and wrote a new history
  from it (#640, `TestRecoverRefusesAnInterruptedInstallAboveTheProtocol`).
- Stream again a snapshot a peer refused SNAPSHOT_CORRUPT without reading
  it back: `sentCorrupt` verifies the sender's copy and replaces one that
  fails (#525, `TestALeaderReplacesASnapshotItsPeerFindsCorrupt`).
- Let `Recover` name a voter other than the survivor: a member `Init` only
  prepared cannot vote before a leader admits it, so the forced
  configuration that needed its vote never elected a leader, and two
  survivors naming each other recovered two histories into one identity.
  `checkRecoverMembers` refuses it RECOVER_REFUSED; the others enter as
  learners (R4-04, #642, `TestRecoverRefusesAMemberInitPreparedAsAVoter`,
  `TestTwoSurvivorsNamingEachOtherAVoterAreRefused`). `RestoreSnapshot`
  keeps every voter it is given: it runs on every node it names
  (`TestARestoreOnEveryNodeWithTheSameMembersElects`).
- Excuse an old voter from its fence because the new members list it, or
  accept beside the survivor a member under an old member's or a retired
  ID: two survivors that each named the other a LEARNER were both accepted,
  neither fenced, and ran two histories under one identity. `checkFenced`
  asks a receipt for every old voter but the survivor, and
  `checkOldMembers` refuses any old or retired ID beside it (R1-17, #691,
  `TestRecoverRefusesAnOldMemberBesideTheSurvivor`,
  `TestCheckOldMembersRefusesAnyOldOrRetiredID`).
- Judge the members of a `Recover` or a `RestoreSnapshot` one line at a
  time: the survivor's ID passes every per-line check, and listed twice
  under two keys it was a voter counted twice toward a quorum and two keys
  admitted as one node, or, its learner line first, a survivor that never
  campaigned. `sharedID` refuses a repeated ID before anything is written,
  as `checkFounders` does at `Bootstrap` (review of #693,
  `TestRecoverAndRestoreRefuseTwoMembersUnderOneID`).
- Remove anything in `WipeAndRejoin` before `STOP-WIPE` is durable, the
  stamp before the stores' removal is synced, or `STOP-WIPE` before the
  stamp and the other markers are durably gone: cut between the log and
  the stamp, the wipe left a member stamp over an empty log, and the node
  restarted as the same voter with no term, no vote and no entry, then
  lost acknowledged writes; `Init` handed back the old pair. While the
  marker is there the open skips recovery, the start and every offline
  write but the wipe are refused, and the wipe run again completes (#690,
  `TestAnInterruptedWipeRefusesTheStartUntilItIsRunAgain`,
  `TestAWipeCutAtEveryStepIsSafe`).
- Plan `Recover` from the log's bounds alone: an install a crash
  interrupted after its publication left the snapshot past the log, and
  the forced entry written after the short log was erased at the next
  start. `planKept` keeps the snapshot's index and `writeRecover` finishes
  the install before STOP-RECOVER (#506,
  `TestRecoverFinishesAnInterruptedInstall`).
- Publish a cut over `MaxSnapshotBytes` (or the smallest a member
  announces): no node could restore it, and a rolling restart left the
  cluster with no table and no leader. The chunk writer refuses it
  SNAPSHOT_TOO_LARGE, and a log still whole from index 1 is replayed when
  no snapshot restores (#512, `TestNoSnapshotPastMaxSnapshotBytesIsPublished`,
  `TestALogFromIndexOneIsReplayedWhenNoSnapshotRestores`).
- Leave a `.tmp` a failed zero-fill wrote, or start a cut again right after
  one that failed without a storage failure: on a full disk the first held
  every free byte, the second filled the disk again at every applied batch.
  `abandonTmp` removes it; `cutFailed` waits one second, doubling to a
  minute (#520, `TestAFailedZeroFillLeavesNoPartialSegment`,
  `TestAFailedCutWaitsBeforeTheNext`).
- Let the log grow past `MaxLogBytes` less `LogReserveBytes`, or the
  filesystem under `LogReserveBytes`, without the no-space alarm:
  `guardSpace` proposes it at each tick and each completed write, the node
  refuses its own commands NO_SPACE from the moment it finds its disk short
  (`refuseShort`: on a slow runner the commands queued behind the alarm
  took the log past `MaxLogBytes`), raft refuses every command NO_SPACE
  while the alarm stands, and `SnapshotEveryBytes` cuts. A full disk used to stop every node
  on STORAGE_FAILED at the same rotation (#515,
  `TestALogPastItsQuotaRaisesNoSpaceAndRefusesWrites`).
- Keep `SnapshotTrailing` entries behind the compaction point once the
  log has no room for them, or accept a quota whose headroom over
  `LogReserveBytes` is two segments or less. Counted whatever their size,
  4 096 entries of 256 KiB outweighed the 768 MiB the recommended quota
  leaves: nothing was compacted before the no-space alarm, and no
  `SaveSnapshot` compacted anything under it, for good. A headroom of one
  segment was filled at the open, of two at every rotation (#696).
  `trailingKept` keeps none once the disk is short or the log half way to
  the alarm, the log still back to the older snapshot (R6-02);
  `refuseCeilings` judges the headroom by the shipped log's `segmentBytes`
  (`TestAHeadroomOfTwoSegmentsOrLessIsRefused`,
  `TestTheTrailingWindowYieldsToTheLogQuota`,
  `TestTheRepairOfAShortLogCompactsPastTheTrailingWindow`). A headroom
  under twice `SnapshotEveryBytes` and two segments may still reach the
  alarm under steady large entries; it is repaired online.
- Hand raft a caller's buffer: `Propose` copies its payload. The leader
  keeps the entry and resends it while a peer lags, and a caller that
  reused its buffer changed it under its body sum: every follower refused
  it (#508, `TestAMajorityRefusingTheChainDeposesTheLeader` in `raft/`).
- Number a read with a counter that restarts at 1: a leader may still hold
  a read of the previous incarnation, unconfirmed, and answer it later, at
  the commit index from before a write acknowledged since. `nextID` gives
  reads the forwards' random epoch (#510,
  `TestARestartedFollowersReadIsNeverAnsweredForItsPredecessor`).
- Overwrite a waiter: a second position learned at an index already waited
  on — the new leader hands out first the indexes the old one's tail held —
  keeps the later term's waiter and answers the other OUTCOME_UNKNOWN, its
  pending bytes released (`displace`, #517,
  `TestAppendedNeverOrphansAWaiterAtTheSameIndex`).
- Read a proposal's stage to answer a cancelled caller: the hand-off is a
  compare-and-swap. The caller abandons an unsent proposal
  (`stageAbandoned`) or the loop claims it (`claimed`) before raft or the
  leader sees it; read without the swap, a proposal answered ctx.Err() was
  appended from propc afterwards (#519,
  `TestAProposeCancelledWhileQueuedIsNeverAppended`).
- Report a token applied on its index alone: `WaitApplied` compares the
  term of the entry applied there (`appliedAs`). Recover adopts a suffix
  past the survivor's persisted commit that may hold another term than the
  write the old cluster acknowledged, and a token of the predecessor whose
  term cannot be read any more is LINEAGE_MISMATCH (#509,
  `TestWaitAppliedComparesTheTermRecoverKept`).
- Write the founding contract raw: `foundingTiming` completes and refuses
  `InitialTiming` as NewNode does, so the contract that rules every node is
  the one NewNode validated (#523,
  `TestTheFoundingContractIsTheOneNewNodeValidates`,
  `TestAFoundingContractWithoutItsMinimumRulesEveryNode`).
- Cut a denied member before its DenyPin commits, or deny the pin of the
  last voter a quorum needs: over TCP the entry then waited forever for the
  acknowledgement the transport refused (#530,
  `TestTCPDenyPinNeverCutsTheLastQuorum`; raft's `denyPin` and `admitted`).
- Take a log record whose crc holds but whose version this build does not
  write for a hole, or skip a record kind it does not know or a hard state
  of another length: a newer build wrote them, synced, and a rollback
  zeroed them as a torn tail — acknowledged entries and a cast vote lost.
  `unknownVersionAt` and `checkLayouts` refuse the open VERSION_UNSUPPORTED
  before any tail is classified or trimmed — a record of an unknown kind in
  an unfinished last batch was trimmed with the tear — and change nothing
  (D18, #511, `TestALogANewerBuildWroteStopsTheOpen`).
- Keep a receive spool once an offer of another id is accepted, or past a
  reopen when it is not the newest, or read a spool whole to resume it: an
  abandoned transfer left up to a whole snapshot on the data disk for good,
  and a resume held the spool in memory on the event loop. `Create` sweeps
  every spool no sink writes to and reads one chunk at a time
  (`alignedOffsetAt`), the chunk size an offer names clamped to what a
  chunk header can name (`maxEncodedChunk`); a short stream releases its
  sink (#527, `TestAnOrphanSpoolIsRemoved`,
  `TestAResumeReadsItsSpoolInBoundedMemory`,
  `TestAResumeBoundsTheChunkSizeAnOfferNames`).
- Leave published a snapshot that failed to restore for a reason of its own
  once an older one restored and continues the log, or answer held for a
  snapshot past the applied index: the leader probed past it, the append
  was refused and the offer came again for as long as it stayed the
  leader's newest (#528, `TestASnapshotThatDidNotRestoreIsUnpublished`,
  `TestAHeldSnapshotPastTheAppliedIndexIsReplaced`). Nor unpublish one that
  failed on this node's disk or a limit lowered since, or before
  `finishInstall` accepted the fallback: it may restore at the next start,
  and a log compacted to it starts from nothing else
  (`TestASnapshotOverALoweredLimitStaysPublished`,
  `TestNoSnapshotIsUnpublishedBeforeItsFallbackContinuesTheLog`).
- Refuse with LOG_CORRUPT a start whose fallback base does not continue the
  log: the gap is the newest snapshot's failure to restore, and a log
  compacted to an install has no older snapshot to fall back on. The start
  refuses with the newest's reason — SNAPSHOT_TOO_LARGE under a limit
  lowered since, STORAGE_FAILED, SNAPSHOT_CORRUPT — and LOG_CORRUPT sent an
  operator to rebuild a sound log (#574, `fallbackGap`,
  `TestAnInstallOverALoweredLimitIsTheRefusal`).
- End a streamed snapshot's pin on its own index: caught up is the peer's
  match at the last index. A snapshot cut at the last index — an idle
  leader's, one cut on request — ended its pin at the first tick after its
  stream, before the peer answered, and the cuts of the writes that
  followed compacted past the entry it needed next (#588, `watchPins`,
  `TestAStreamedSnapshotPinsCompactionUntilThePeerCatchesUp`).
- Write a test whose setup needs a node to be offered a snapshot as a
  wait for the leader's log to pass a fixed index: a cut in flight when the
  next fell due coalesces them, the two snapshots kept can lie far apart and
  the log, compacted to the older, stops short of it with the writes done.
  Wait for the log to start past the stopped node's end
  (`compactedPastItsEnd`, #588). A test whose leader must keep its
  leadership cuts the restarted node off from the other followers, which it
  would grant a PreVote, and runs on `steadyTiming`: testTimingFast's
  100 ms CheckQuorum window stepped a leader down on a loaded run
  (`hearOnlyTheLeader`, #588). A setup on disk proposes to whichever node
  leads (`proposeOnDisk`): a leader whose own fdatasync stalls past the
  maximum election timeout hands its leadership over (#554, #587).
- Hand a held snapshot stream its release with a bare channel send: the
  stream waits for it only while its context lives, and a leader that lost
  its leadership stops its streams (#547), so the send had no taker and the
  test blocked until the binary's timeout —
  `TestATransferInFlightPinsCompaction`, on `testTimingFast` and with the
  restarted node granting any PreVote, once in a few hundred `-race` runs
  (#598). `releaseHeld` bounds the send and says why nobody took it; that
  test runs on `steadyTiming` behind `hearOnlyTheLeader`, as its siblings.
- Run a timing contract whose `ElectionTimeoutMin` is not well above the
  disk's fdatasync latency: a campaign is a durable term and vote on the
  candidate and on each voter, a voter's queued behind the sync its storage
  actor is in, so a round costs up to two syncs, and a candidate whose
  timer fires first starts another term. Measured on three nodes over the
  shipped data directory, each sync 200–700 ms, a leader crashed eight
  times: 500 ms–1 s elections stayed leaderless up to 31 s, through 41
  terms; the default 1–2 s at most 4.8 s, and 11.4 s with syncs of
  500 ms–1 s (#598). Keep `ElectionTimeoutMin` above three times the p99
  of `consensus_fsync_latency_seconds`.
- Forward a removal of the leader without moving leadership first: the
  leader refuses to remove itself, and the call failed MEMBERSHIP_INVALID
  on a follower only. `Remove` transfers to this node (or another voter),
  then submits the removal again while the new leader's no-op is pending
  (#529, `TestRemovingTheLeaderFromAFollowerTransfersFirst`).
- Start a transfer to a preferred node at the tick the previous one
  expired, to one not heard within `ElectionTimeoutMin`, or always to the
  first of `PreferredLeaders`: a transfer refuses every proposal, admin
  included, so a preferred node that died before taking over kept every
  write and its own `Remove` refused for as long as the leader led.
  `watchPreferred` waits a pause after any transfer ended, doubled by each
  earlier one it started in the leadership up to 16 × `ElectionTimeoutMax`,
  and `preferLeader` starts after the last node tried (#662,
  `TestAPreferredNodeLostBeforeTakingOverLeavesTheMajorityWriting`,
  `TestAPreferredNodeThatCannotTakeOverIsTriedAgainOnlyAfterAPause`,
  `TestAPreferredNodeThatCannotTakeOverYieldsToTheNextOne`).
- Campaign on a `TimeoutNow` after handing the leadership over for a stuck
  apply, before the apply moves: the node still acknowledges, and its
  successor's watch of `PreferredLeaders` transferred straight back to it,
  which led about 99 % of the time with every write refused `OVERLOADED`.
  `watchProgress` remembers the hand-over across the change of role
  (`ceded`), raft declines the `TimeoutNow` (`SetApplyStalled`) until
  `watchApplied` sees the applied index move or reach the commit, and
  `declineNoted` logs it. Nor decline on a follower's own stalled apply
  alone: a stuck leader then ran expiring transfers, refusing proposals
  `NOT_LEADER`, against every other stuck voter (`TestApplyLagBackpressure`).
  Nor hand over again to the voter whose hand-off expired when another can
  take it: the node that handed over first kept a second stuck node leading
  (#674, `mostCaughtUpVoter`,
  `TestAStuckPreferredLeaderIsNotHandedLeadershipBack`,
  `TestANodeThatHandedOverForAStuckApplyDeclinesATransferBack`,
  `TestALeaderWithAStuckApplyHandsOverPastAVoterAlsoStuck`).
- Start the watch of `PreferredLeaders` at the first tick of a leadership a
  transfer handed over: a `Remove` of the preferred leader, submitted on it,
  moves leadership away and submits the removal again while the new no-op
  is pending (#529), and the watch handed leadership back before that, so
  the removal failed `MEMBERSHIP_INVALID` at every attempt.
  `leadershipChanged` pauses the watch for `handOffWindow` maximum election
  timeouts, the window `removeAfterTransfer` submits for, when raft's
  `HandedBy` names the leader whose `TimeoutNow` won the term; an election
  starts with no pause (#676,
  `TestRemovingAPreferredLeaderOnItselfIsNotUndoneByTheWatch`,
  `TestRemovingAPreferredLeaderFromAFollowerIsNotUndoneByTheWatch`,
  `TestAPreferredNodeHandedOverByATransferLeadsAgainAfterTheWindow`).
- Hand the leadership off to one voter only, or start a local transfer
  while raft still runs another: a voter that handed over for a stuck apply
  declines every transfer (#674), and only it knows, so a `Remove` of the
  leader submitted on it, whose hand-off went to that voter, the first of
  the configuration on an equal match, failed `MEMBERSHIP_INVALID` at every
  attempt. `handOff` tries the next most caught-up voter, each once while
  the node leads (`handOffTarget`), and `transferLocal` waits out a
  transfer to another voter still under way: raft starts none while one
  runs, and the wait for the voter that declined ends a tick before raft's
  deadline (#681,
  `TestRemovingTheLeaderOnItselfHandsOverPastAVoterThatDeclines`,
  `TestALocalTransferWaitsOutTheOneUnderWay`).
- Bound a hand-off try by `InitialTiming`, or by anything but the maximum
  election timeout of the contract raft runs on: `InitialTiming` is
  proposed at Bootstrap only, raft's transfer lasts the committed
  contract's `ElectionTimeoutMax` (`startTransfer`), and under a longer
  contract every try past the first expired while it waited out the
  transfer to the voter before, so the `Remove` of the leader submitted on
  it failed `NOT_LEADER` at every attempt again. `handOff` reads the bound
  from raft's status with the target, so a try's wait in `transferLocal`
  for the previous try's transfer is at most a tick, the gap between the
  two deadlines. A transfer started elsewhere, an operator's or the
  preferred-leader watch's, can still hold the first try for up to the
  contract's `ElectionTimeoutMax` (#694,
  `TestRemovingTheLeaderOnItselfHandsOverPastAVoterThatDeclinesUnderALongerContract`,
  `TestRemovingTheLeaderOnItselfHandsOverPastADeadVoterUnderALongerContract`).
- Answer a `Remove` of the leader submitted on it with the `NOT_LEADER` of
  its own hand-off: the `TimeoutNow` makes the target campaign at once, its
  vote request deposes the node, which knows no leader until the winner's
  first append reaches it, and a try that expired in that gap ended the
  hand-off and submitted the removal with no leader to forward it to; a
  try whose transfer raft still ran was refused the same way, raft taking
  no proposal while one runs. Both say nothing was appended (D6), and both
  are the transfer the call made: `removeAfterTransfer` submits again on
  `NOT_LEADER` as on `MEMBERSHIP_CHANGE_PENDING`, each heartbeat within
  `handOffWindow` maximum election timeouts, and a node no voter took over
  from ends on `MEMBERSHIP_INVALID`, a leader refusing its own removal
  (#701, `TestRemovingTheLeaderOnItselfWaitsForTheLeaderItsHandOffElected`).
- Leave a peer's series at their last value once it is no longer published,
  or keep a departed member's certificate in `peerExpiry`: a series is never
  deleted, and "peer unreachable" and "certificate expiring" alerted for a
  member that no longer existed. The gauges read as a quiet peer, a
  departed certificate as never expiring (`departedCertExpiry`) (#532,
  `TestADepartedPeersSeriesAreReset`, `TestAnExLeaderResetsItsPeersSeries`,
  `TestTCPForgetsADepartedPeersCertificate`). The admission set is handed
  to the transport only when it changes (#531,
  `TestAnUnchangedMembershipIsNotRepublished`).
- Bound an election in a test by a fixed number of split votes: a split
  is a draw, about one round in a hundred on the memory network, and a
  third round fails a two-round total every ten to twenty runs. Derive
  the bound from the rounds the winner's term counts, each one election
  timeout and a tick, and cap the rounds by the odds of that many splits
  (`electionBound`, `maxRounds`).
- Decode an offline restore with flate when the expected metadata names
  another codec: `OpenSnapshot`, `RestoreSnapshot` and `ImportObserved`
  read the codec `RestoreOptions.Expected` names when the caller gives
  none, and refuse VERSION_UNSUPPORTED one this build does not link (#548,
  `TestTheOfflineReadersUseTheCodecTheMetadataNames`; the whole rolling
  codec change of the issue is `TestARollingSnapshotCodecChangeKeepsALeader`).
- Resume a durable machine past its snapshot without the term of its
  position, or cut, offer, send or restore a snapshot at term 0 above
  index 0: the next start truncated the log, then refused to compact it,
  STORAGE_FAILED at every start of the cutter and of every peer that
  installed it (#544, `TestADurableRestartResumesWithTheAppliedTerm`,
  `TestASnapshotPublishedAtTermZeroIsSetAside`,
  `TestAPeerInstallsTheSnapshotOfADurableLeaderRestarted`).
- Trust a version 1 `.meta` whose id does not name its position, or take
  an acknowledgement past the leader's last index as match: version 1
  carries no checksum, and one bit of `Position.Index` erased the log,
  served unapplied reads and committed writes on the leader's disk alone
  (#546, `TestAMetadataWhosePositionRottedIsNotRestored`, raft's
  `checkMatchBoundedByLastIndex` and its mutation).
- Refuse, skip or write a version 2 `SnapshotMeta`: this release reads it
  (the fields of version 1, then their CRC-32C, `wire`) so that it runs
  beside, or is rolled back under, the release that writes it; the writer
  ships with the cluster version that admits it (#546 phase 2, batch S3 of
  #564; `TestASnapshotMetaOfVersion2IsRestored`).
- Write a format of protocol version 2 before the committed cluster
  version is 2: the version 2 snapshot meta (CRC-32C, #546), the admin
  payload with Origin (#561) and the stamp with AdoptedFrom (#509, the
  meta never carries it) are chosen by the cluster version, and a node of protocol 1 stops on the
  raise instead of meeting them (`TestAVersion2MetaThatRottedIsNotRestored`,
  `TestRecoverBelowProtocol2RecordsNoAdoptedRange`, raft's
  `checkRaiseAnnounced`). Nor one of protocol 3 before it is 3: the admin
  payload and the snapshot meta of version 3, which carry the retired keys,
  and `Recover`'s forced entry of version 3 (#648, raft's
  `checkNothingNewBelowProtocol3` and `TestAMixedClusterRaisesTo3OnceEveryMemberSpeaksIt`).
- Commit an administrative entry with an origin the forwarder wrote, or
  append one as leader without a Warn naming its origin (#561,
  `TestAForwardedRemoveRecordsTheFollowerAsItsOrigin`, raft's
  `checkOriginAuthenticated`).
- Admit a sender at the memory double that its receiver's configuration
  does not list when the test turns the admission model on
  (`MemoryNetwork.admission`): the suites never saw #542 because the double
  admitted everyone (`TestTheMemoryAdmissionModelRefusesAnUnknownSender`).
- Leave a voter that came back behind a leader its configuration does not
  list with no way to hear it: a follower of a live leader answers its
  PreVote with that leader's committed record, which it admits for
  replication only until its configuration lists or retires it, or four
  maximum election timeouts pass without that leader's contact (#542,
  `TestHuntR4ProveStaleVoterNewLeader`,
  `TestHuntR4ProveStaleVoterReplacementThenFailure`, raft's
  `checkLeaderRecordReplicationOnly`). Counted from the record alone, the
  delay cut a leader still heard, its snapshot stream included, and a
  voter whose install outlasted it never caught up under writes (#618,
  `TestAStaleVoterWhoseInstallOutlastsTheRecordCatchesUp`, raft's
  `checkVouchedLeaderAdmittedWhileHeard`). Behind the commit is judged by
  the PreVote's last term and last index, never its index alone: a voter
  back with an uncommitted tail of an older term, longer than the commit,
  was never sent the record on a quiet cluster (#675, raft's
  `checkLeaderRecordReachesAnOldTail`).
- Gate the leader record on the cluster version: the raise to 2 is refused
  while a member is unheard, and the stale voter the record is for is one
  the leader cannot hear, so a cluster that had not raised never repaired
  (#638, `TestAStaleVoterHearsTheNewLeaderBeforeTheRaise`, raft's
  `checkLeaderRecordFollowsTheAnnouncement`). The record follows the
  PreVote's own `FlagProtocol2`, never an older state of its sender.
- Leave a learner whose configuration does not list the leader with no
  way to hear it: it never campaigns, so it asks the members of its
  configuration for the leader's record with a PreVote it never counts
  (#639, `TestALearnerDownWhileANewLeaderWasAdmittedIsFed`, raft's
  `checkStrandedLearnerAsks`).
- Trust a predecessor's token by its index above the identity's
  AdoptedFrom once its term can no longer be read: that part of the kept
  range is the suffix Recover adopted past the persisted commit (#509,
  `TestRecoverRecordsWhereTheAdoptedRangeStarts`).
- Write anything, open included, before the STORAGE_FAILED markers of the
  current boot are read: recovery, Recover, Init and
  TruncateAndStaySilent wrote past pages the disk did not hold (#563,
  `TestAnOpenInTheBootOfAStorageFailureWritesNothing`,
  `TestRecoverInTheBootOfAStorageFailureIsRefused`).
- Open a data directory whose boot-scoped marker directory the process
  cannot write: the open refuses CONSENSUS_MISCONFIGURED, and a stop that
  wrote no marker refuses any later open in the process (#562,
  `TestAnUnwritableRuntimeDirIsRefusedAtOpen`,
  `TestAStorageStopRefusesTheSameBootRestart`).
- Write on an outbound lane without a reader on it, or after its key left
  the admission: a write into a link the peer closed cleanly succeeds and
  is lost to the peer's reset (#553, `watchLink`), and a fenced key kept
  receiving entries on lanes dialled before `DenyPin` (#551,
  `stillAdmitted`; `TestTCPFrameAfterACleanCloseByTheReceiverIsDelivered`,
  `TestTCPNothingIsSentOnAnOldLaneToAKeyNoLongerAdmitted`).
- Install a JOIN answer in the engine's admission group, or send a snapshot
  whose configuration does not list the leader: the joiner's first older
  configuration cut its leader (#552, `joinGroup`,
  `TestAJoinerKeepsALeaderNewerThanTheConfigurationItReplays`).
- Keep a joiner on the JOIN answer it got first while it hears no leader:
  a leader admitted after that answer was refused at every connection, and
  the joiner was never fed while it led. Raft's `LeaderUnheard` sends the
  JOIN again with the kept configuration (#632, `refreshJoin`,
  `TestAJoinerHearsALeaderAdmittedAfterItsJoinAnswer`); never once the
  admission released the answer (`joinHold`,
  `TestAJoinSentAgainReplacesOnlyAHeldAnswer`), nor over a later explicit
  Join: an answer installs only in the hold's generation its JOIN began in
  (`joinHold.gen`, `TestAJoinSentAgainNeverInstallsOverALaterJoin`).
- Accept a `PeerQueueBytes` below `MaxFrameBytes`, or drop without a word
  a frame larger than the whole queue: `Send` drops it even into an empty
  queue, and an entry under `MaxEntryBytes`, or a lagging follower's
  catch-up batch, never reached a follower while the commit stopped for
  good under the same leader (#669). `validate` refuses the queue below
  the frame bound, an unset one defaults to the larger of 64 MiB and
  `MaxFrameBytes`, and such a frame is counted and logged at the first
  drop and each power of two (`droppedOversized`,
  `TestAPeerQueueBelowTheFrameBoundIsRefused`,
  `TestAFrameAboveThePeerQueueIsCountedAndLogged`).
- Refuse without a word a frame a member announces above the bound its
  lane is read under: a leader whose fetched batch outgrew the follower's
  `MaxFrameBytes` sent it again at every probe, the follower ended each
  connection in silence, and never caught up (#683). The leader now cuts a
  fetched batch by its encoding (raft's `batchLen`); the reader still ends
  the connection, and counts and logs the refusal at the first and each
  power of two (`refusedOversized`,
  `TestAFollowerBehindTheEvictedPayloadsCatchesUpUnderTheTightestFrame`,
  `TestAFrameAboveTheReadBoundIsCountedAndLogged`).
- Read a snapshot offer under `MaxControlFrameBytes`, or end a refused one
  without a line: its metadata carries the configuration, `Retired`
  included, and from protocol 3 `RetiredKeys` (#648), which every Remove
  grows for good, and past about 8 100
  removals no snapshot was ever installed over TCP again (#634,
  `serveSnapshot`, `offerRefused`,
  `TestASnapshotOfferCarryingManyRetiredIDsIsInstalledOverTCP`). The memory
  double holds the same bound (`maxOffer`,
  `TestASnapshotOfferAboveTheFrameBoundIsRefusedAndLogged`).
- Report a leader known from heartbeats alone: a leader answered one of the
  node's reads within `ElectionTimeoutMax`, or the node leads (#541, #540,
  `probeLeader`). Keep a leader whose sync or apply has not moved for
  `ElectionTimeoutMax` (#554, `watchProgress`), or compact past a snapshot
  in flight (#550, `n.sending`).
- Let a learner raise the cluster-wide no-space alarm: its own cuts are held
  to the limit it announced, and once the state outgrew it its log reached
  its quota and the alarm refused every write of the cluster (#600,
  `guardSpace`, `TestALearnerTheStateOutgrewDoesNotRaiseTheClusterNoSpace`).
  Nor a node the configuration it holds does not name: a joiner offered no
  snapshot hears its leader and holds none, and its alarm refused every
  write of the cluster (#619,
  `TestAJoiningNodeOnAShortDiskDoesNotRaiseTheClusterNoSpace`).
- Write a caller's writer on the apply goroutine: an abandoned
  `SaveSnapshot` into a writer that stopped reading froze the node (#601,
  `copySaved`, `TestASaveSnapshotWriterThatStopsReadingDoesNotFreezeApply`).
- Hold a stream's pin past its peer's `pinBound` after its last byte, or
  once the log is tight: a receiver that never answers its verdict pinned
  the leader's compaction for good (#602, `pinAt`, `dropPin`,
  `TestAFollowerStuckInItsInstallDoesNotPinCompaction`,
  `TestTCPStreamWhoseReceiverNeverAnswersEndsWithItsContext`). Nor fix the
  bound for good: a receiver only slow to restore never caught up, and the
  bound doubles for a peer that still answered when its pin fell due (#610,
  `growPin`, `TestAFollowerSlowerToRestoreThanThePinBoundCatchesUp`).
- Accept an offer of the snapshot this node is restoring: it was streamed
  again and committed over the file the restore reads (#610,
  `answerRestoring`, `TestAnOfferOfTheSnapshotBeingRestoredIsRefused`).
- Feed the log to a learner whose announced limit the newest snapshot is
  over: it compacts nothing and its log filled its disk (#613,
  `watchUnfed`, `TestALearnerTheStateOutgrewIsNotFedTheLogPastItsQuota`).
- Leave a member's recorded `MaxSnapshotBytes` as its JOIN wrote it: a
  voter raised and restarted, #512's remedy, was held to the old one by
  every cut of every node, nothing was compacted and NO_SPACE refused every
  write (#677). `watchSnapshotLimit` (`node_snaplimit.go`) proposes, at each
  tick, the node's own limit in a `ChangeSnapshotLimit` when its record
  holds another above 0, off the loop, one at a time, one second after a
  refusal doubling to a minute — a leader of an earlier release refuses the
  kind (`TestARaisedSnapshotLimitReachesTheMembership`,
  `TestALearnerWhoseLimitIsRaisedIsFedInstalledAndPromoted`,
  `TestAnOlderLeaderRefusesTheLimitAndANewerOneRecordsIt`). Nor replace a
  recorded 0, which announces no limit (#591's trap), nor propose a lowering
  below the newest snapshot, the store's newest included, which a start
  under that limit may not have restored: it is said once and the record
  kept, or every cut would freeze
  (`TestALowLimitIsNeverOfferedMoreAndItsLoweringBelowTheStateIsNotProposed`).
  Only the member states its limit: raft refuses any other origin
  (`TestASnapshotLimitChangeFromAnotherNodeIsRefused`). A test's joiner
  that announces a limit runs with it, or it states its own once admitted
  (`joinWithLimit`).
- Cut again at an index whose snapshot is published: the commit renamed
  over a file a copy may hold open, which Windows refuses (#611,
  `publishedAt`, `TestASecondSaveOfAnIdleNodeDoesNotRecommitThePublishedSnapshot`).
- Let `SaveSnapshot`'s copy write to `w` once the caller left (#612,
  `ctxReader`, `TestSaveSnapshotStopsWritingOnceTheCallerLeft`).
- Remove an answering member that may be the last voter a silent voter
  knows: that voter comes back hearing no one and the cluster loses its vote
  for good (#603, `strands`, `TestAStaleVoterIsNotLeftWithNoKnownPeer`,
  `TestARemovalThatMayStrandAStaleVoterIsRefused`). Never widen admission
  for it: a key is admitted only on a member's word or a configuration.
  Nor refuse the removal of a learner that never voted: it vouches for no
  one (#614, `neverVoter`,
  `TestAnAnsweringLearnerIsRemovedWhileAVoterIsAlreadyStranded`).
- Cancel a snapshot stream because the log is tight: its pin's hold on
  compaction is lifted, for good, and the stream goes on; only a stream
  that is due is cancelled (`streamDue`). Cancelled with it, no stream
  outlived the band's first tick (#622, `releasePin`,
  `TestAStreamSurvivesATightLogOverTCP`,
  `TestTheVerdictBoundStillEndsAStreamOnATightLog`). Nor start the band
  before half way to the no-space alarm: within twice `LogReserveBytes` of
  `MaxLogBytes` alone, it covered the whole of a quota of at most twice the
  reserve, which the configuration accepts, and no pin ever held (#622,
  `logTight`, `TestAPinHoldsCompactionUnderAQuotaOfTwoReservesOrLess`).
- Bound a stream from its last byte only: a stream whose offer was never
  answered, the receiver's loop standing still in its judgment, and one to
  a peer removed meanwhile held the leader's compaction for as long as it
  led. A stream is due once it has not moved — offer, accept, a read of the
  snapshot — for its peer's `pinBound`, and once its peer left (#623,
  `streamDue`, `TestAnOfferNeverAcceptedDoesNotPinCompaction`,
  `TestRemovingAPeerCancelsItsSnapshotStream`); `Stream` reads the accept
  under `JoinTimeout` (`TestTCPStreamWhoseOfferIsNeverAnsweredEndsAtItsBound`).
  Nor let an offer wait in `Create` for the `Commit` of the same id: the
  loop stood still behind the spool's fsync. The install counts as
  restoring from before its freshness check, and the offer is refused
  `OVERLOADED` (#623, `markRestoring`,
  `TestAnOfferOfTheSnapshotBeingCommittedIsRefused`,
  `TestAReofferDuringTheCommitIsRefusedWithoutHoldingTheLoop`,
  `TestAReofferToAFollowerStuckInItsCommitDoesNotPinCompaction`).
- Position an offer's spool on the event loop: the store's `Create` sweeps,
  opens and, for a resume, reads the spool back whole, and a cold 1 GiB one
  held the loop past the sender's `JoinTimeout` (D5b), the judgment was cut,
  and every later offer read the spool back again. The loop screens and
  reserves (`judgeOffer`, `reservePosition`: one positioning at a time,
  `OVERLOADED` meanwhile), `positionOffer` positions off it, and a spool
  released with nothing written resumes from the offset kept in memory
  (#643, `fileSnapshots.resume`,
  `TestAResumeWhoseJudgmentOutlastsTheAcceptBoundCompletes`,
  `TestTheLoopRunsWhileAnOfferIsPositioned`). Nor accept a resume past the
  snapshot's encoded size: the sender refuses to open there, and every
  later offer resumed at the same offset (#643, `openSpool`,
  `TestAResumeIsNeverAcceptedPastTheSnapshotsEnd`).
