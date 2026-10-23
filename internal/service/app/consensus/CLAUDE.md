<!-- updated: 2026-10-23T20:00:00Z -->
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
- Sync once per queued Ready: every request the storage actor takes is
  staged, then synced ONCE, then completed in order, so no `AfterSync`
  leaves before the sync that covers it; a group of commit-only Readys is
  not synced, its record rides the next sync (#558,
  `TestABacklogOfWritesIsSyncedAsOneGroup`).
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
  `guardSpace` proposes it at each tick and each completed write, the node
  refuses its own commands NO_SPACE from the moment it finds its disk short
  (`refuseShort`: on a slow runner the commands queued behind the alarm
  took the log past `MaxLogBytes`), raft refuses every command NO_SPACE
  while the alarm stands, and `SnapshotEveryBytes` cuts. A full disk used to stop every node
  on STORAGE_FAILED at the same rotation (#515,
  `TestALogPastItsQuotaRaisesNoSpaceAndRefusesWrites`).
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
  `checkRaiseAnnounced`).
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
  election timeouts pass (#542, `TestHuntR4ProveStaleVoterNewLeader`,
  `TestHuntR4ProveStaleVoterReplacementThenFailure`, raft's
  `checkLeaderRecordReplicationOnly`).
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
- Report a leader known from heartbeats alone: a leader answered one of the
  node's reads within `ElectionTimeoutMax`, or the node leads (#541, #540,
  `probeLeader`). Keep a leader whose sync or apply has not moved for
  `ElectionTimeoutMax` (#554, `watchProgress`), or compact past a snapshot
  in flight (#550, `n.sending`).
- Let a learner raise the cluster-wide no-space alarm: its own cuts are held
  to the limit it announced, and once the state outgrew it its log reached
  its quota and the alarm refused every write of the cluster (#600,
  `guardSpace`, `TestALearnerTheStateOutgrewDoesNotRaiseTheClusterNoSpace`).
- Write a caller's writer on the apply goroutine: an abandoned
  `SaveSnapshot` into a writer that stopped reading froze the node (#601,
  `copySaved`, `TestASaveSnapshotWriterThatStopsReadingDoesNotFreezeApply`).
- Hold a stream's pin past `pinAfterStream` maximum election timeouts after
  its last byte: a receiver that never answers its verdict pinned the
  leader's compaction for good (#602, `pinAt`, `dropPin`,
  `TestAFollowerStuckInItsInstallDoesNotPinCompaction`,
  `TestTCPStreamWhoseReceiverNeverAnswersEndsWithItsContext`).
