<!-- updated: 2026-10-22T18:00:00Z -->
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

Under Bazel `consensus_test` has 6 shards: under race and coverage the suite
passed the 60 s cap per target once the disk scenarios of #507, #516 and
#524 joined it (4 shards), and the snapshot and disk scenarios of #506,
#512, #513, #515, #520, #525, #526 and #533 brought the slowest of 4 to
28 s on an 8-core machine, too close to the cap on a CI runner.

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
  back and carried by the next cut — a node with a table evicted the
  payloads it applied, so they cannot be replayed (#416,
  `TestAnInstallBelowTheCommitLeavesTheEngineStateAlone`).
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
  `TestReadChunkRefusesALengthAboveTheLimit`, run by `test-386`).
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
  (`TestAFailedCutClearsTheCoalescedTrigger`).
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
- Stream again a snapshot a peer refused SNAPSHOT_CORRUPT without reading
  it back: `sentCorrupt` verifies the sender's copy and replaces one that
  fails (#525, `TestALeaderReplacesASnapshotItsPeerFindsCorrupt`).
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
  `guardSpace` proposes it, raft refuses every command NO_SPACE while it
  stands, and `SnapshotEveryBytes` cuts. A full disk used to stop every node
  on STORAGE_FAILED at the same rotation (#515,
  `TestALogPastItsQuotaRaisesNoSpaceAndRefusesWrites`).
- Bound an election in a test by a fixed number of split votes: a split
  is a draw, about one round in a hundred on the memory network, and a
  third round fails a two-round total every ten to twenty runs. Derive
  the bound from the rounds the winner's term counts, each one election
  timeout and a tick, and cap the rounds by the odds of that many splits
  (`electionBound`, `maxRounds`).
