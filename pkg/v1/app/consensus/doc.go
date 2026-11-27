// Package consensus is the public facade of the SDK's consensus domain
// (ADR 0152): agreement of several machines on the order of their writes.
// A write is committed when a majority holds it on disk.
//
//	id, inc, _ := consensus.Init(dir, cluster, "fra")      // offline, every founder
//	_ = consensus.Bootstrap(ctx, &cfg, members)             // offline, identical list
//	node, _ := consensus.NewNode(cfg)                       // never bootstraps
//	tok, result, err := node.Propose(ctx, command)
//
// # After OUTCOME_UNKNOWN, never retry blindly
//
// NOT_LEADER means the proposal was certainly NOT appended and is safe to
// retry. OUTCOME_UNKNOWN means it was appended, or possibly delivered, and the
// answer was lost: it may still commit. Read the state through Barrier and
// decide, or carry a per-key revision in the command, because a replayed
// compare-and-swap is not harmless under ABA (D6, R1-04).
//
// A proposal made on a follower travels to the leader as a forward, and
// once every member runs this version no forward is applied twice,
// whatever the delay of a copy of it, even across a restart (#1482). To
// keep that, the node a forward went to declines a frame it may have
// handled already, and the proposal meets OUTCOME_UNKNOWN in three cases:
// a copy that arrives after the forward window, when the first answer
// was lost; a forward its connection delivered behind a later one, when
// that connection was replaced; and any forward still sent to a leader
// that restarted, until the next election. Two bounds add their own,
// and neither drops a new forward: the leader keeps at most 2^20
// answers, and past them forgets the oldest, whatever its age, so a copy
// of a forward whose answer went before the window ended, when the first
// answer was lost, meets OUTCOME_UNKNOWN too, which only a load above
// 47 662 forwards a second under the defaults reaches; and, past 65 536
// marks of the current term (runs per lane: about 32 768 restarts when
// each run forwards on both lanes), the least recent mark of the node
// holding the most goes, and that node's new runs wait for the next
// term, which the leader opens by handing its leadership over to a
// voter that holds the commit and was heard, once a term at most
// (#1540); a leader alone, with no such voter, or whose transfer
// failed, leaves them fenced until the next election, restarts spread
// over several nodes fencing each of them in turn, and its last
// resorts are counted in consensus_forward_last_resorts_total by
// action. The bound of 2^20 answers, about 152 MiB and 280 MiB at most
// under churn, holds per Raft group: a multi node with several groups
// adds them. None of them is a refusal, true
// or false: the entry may or may not have been appended. Handle it as
// any OUTCOME_UNKNOWN: read the state through Barrier before a Propose
// is made again, and retry a ProposeSession with the same arguments.
// The forwards declined are
// counted by reason in consensus_forwards_dropped_total, and the answers
// forgotten at the bound in consensus_forward_answers_forgotten_total.
//
// # Exactly once, through a client session
//
// A command without a natural key — an event appended, a counter moved —
// is retried through a client session instead (ADR 0241). Once the
// cluster version is 4, a client opens a session it names with 16 random
// bytes, Node.OpenSession, then submits each command under the next
// sequence, Node.ProposeSession, and retries it after OUTCOME_UNKNOWN with
// the same session, sequence and command, through any node: the state
// machine applies it once, and the retry is answered with the first
// one's token. The same sequence with another command is
// SESSION_DIGEST_MISMATCH. The cluster keeps 1024 sessions, the one used
// least recently evicted first, and a command of a session
// evicted, or of a sequence below its last, is SESSION_EXPIRED: read the
// state through Barrier, then open a new session.
//
// # Reads
//
// Barrier returns once the local state machine reflects every write
// committed before the call: ReadIndex by default, confirmed by a heartbeat
// round started after the read arrived. ReadLease is opt-in and sound only
// under the four conditions of D9; a leader paused after its lease check
// serves a stale read, which ReadIndex never does.
//
// A node that knows no leader of a stable term, or whose leader changes
// while the read waits, refuses the read with NOT_LEADER: nothing was read,
// and Barrier may be retried, for instance after the next leader change.
//
// # Backup
//
// A backup is three calls. Node.ExportBackup writes a full file: the
// snapshot SaveSnapshot cuts, then the committed entries after it up to
// the commit at the call, then a trailer naming the point it ends at.
// Node.AppendBackup writes, from that point, only the entries committed
// since, and returns the next point; keep the last point beside the
// files. VerifyBackup checks the files offline (every record's sum, the
// snapshot's SHA-256, the chain from its ChainTip), and RestoreBackup
// restores the export and its tails, in order, through ImportObserved on
// every node of the new cluster, refusing a tail that does not continue
// the chain before anything is written.
//
// The recovery point is the last committed entry the backup node had
// applied when the last append ran: a restore loses only what committed
// after it. Store the tail files in the repository beside the export
// (restic keeps them as it keeps the snapshot), export again when an
// append is refused COMPACTED or names an entry that changes the
// engine's state, and restore from the newest export and every tail
// written after it.
//
// # Apply is a pure transition
//
// StateMachine.Apply is called for every committed command, in log order, on
// one goroutine, and must be deterministic. An Apply that cannot complete
// must panic: the node stops with STATE_MACHINE_FAILED, and the entry IS
// committed.
package consensus
