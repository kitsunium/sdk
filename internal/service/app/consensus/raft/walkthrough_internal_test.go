package raft

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The datadriven scripts of ADR 0152 D15: each file of testdata/ is a list
// of commands to the step function, each followed by "----" and the text it
// prints, a blank line ending the block. A regression reads as a diff of
// that text. The reader is stdlib only and lives here (R7-11); -rewrite
// writes what the step function prints back as the expectation.

var ddRewrite = flag.Bool("rewrite", false, "write the datadriven scripts' expectations from the step function's output")

func TestDataDriven(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.txt"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no datadriven script under testdata/ (%v)", err)
	}
	for _, path := range files {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".txt"), func(t *testing.T) { runScript(t, path) })
	}
}

// ddBlock is one directive of a script: the lines before it (comments and
// blanks), its command, the expected output and where that output starts.
type ddBlock struct {
	before  []string
	cmd     string
	line    int
	want    []string
	ended   bool // a blank line closed the block
	wantPos int
}

func parseScript(t *testing.T, path string) (blocks []ddBlock, tail []string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines []string
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "#") {
			tail = append(tail, l)
			continue
		}
		b := ddBlock{before: tail, cmd: l, line: i + 1}
		tail = nil
		if i+1 >= len(lines) || lines[i+1] != "----" {
			t.Fatalf("%s:%d: %q is not followed by ----", path, i+1, l)
		}
		i += 2
		b.wantPos = i + 1
		for ; i < len(lines) && lines[i] != ""; i++ {
			b.want = append(b.want, lines[i])
		}
		b.ended = i < len(lines)
		blocks = append(blocks, b)
	}
	return blocks, tail
}

func runScript(t *testing.T, path string) {
	blocks, tail := parseScript(t, path)
	env := &ddEnv{t: t, nodes: map[core.NodeID]*ddNode{}, now: t0.Add(5 * time.Second)}
	var out strings.Builder
	for i := range blocks {
		b := &blocks[i]
		env.out.Reset()
		env.run(path, b.line, b.cmd)
		got := strings.Split(strings.TrimRight(env.out.String(), "\n"), "\n")
		if len(got) == 1 && got[0] == "" {
			got = []string{"ok"}
		}
		if !*ddRewrite && !slices.Equal(got, b.want) {
			at := b.wantPos
			for k := 0; k < len(b.want) && k < len(got) && got[k] == b.want[k]; k++ {
				at++
			}
			t.Errorf("%s:%d: %s: output differs from line %d on\n%s", path, b.line, b.cmd, at, lineDiff(b.want, got))
		}
		for _, l := range b.before {
			out.WriteString(l + "\n")
		}
		out.WriteString(b.cmd + "\n----\n" + strings.Join(got, "\n") + "\n")
		if b.ended {
			out.WriteString("\n")
		}
	}
	for _, l := range tail {
		out.WriteString(l + "\n")
	}
	if *ddRewrite {
		if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// lineDiff is a line diff of want and got, by their longest common
// subsequence: "-" for a line expected and not printed, "+" for one printed
// and not expected.
func lineDiff(want, got []string) string {
	n, m := len(want), len(got)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if want[i] == got[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var b strings.Builder
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && want[i] == got[j]:
			b.WriteString("  " + want[i] + "\n")
			i, j = i+1, j+1
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			b.WriteString("+ " + got[j] + "\n")
			j++
		default:
			b.WriteString("- " + want[i] + "\n")
			i++
		}
	}
	return b.String()
}

// ddNode is one step function and what its driver holds for it: the writes
// not yet durable and the durable log, read back for a Fetch.
type ddNode struct {
	r      *Raft
	writes []ReadyValue
	disk   map[uint64]core.EntryValue
}

type ddEnv struct {
	t     *testing.T
	nodes map[core.NodeID]*ddNode
	ids   []core.NodeID
	now   time.Time
	net   []core.MessageValue
	out   strings.Builder
}

func (e *ddEnv) printf(format string, args ...any) { fmt.Fprintf(&e.out, format+"\n", args...) }

// ddArgs are a command's positional values and key=value pairs.
type ddArgs struct {
	pos []string
	kv  map[string]string
}

func (a ddArgs) has(k string) bool { _, ok := a.kv[k]; return ok }

func (a ddArgs) num(k string, def uint64) uint64 {
	v, ok := a.kv[k]
	if !ok {
		return def
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		panic(fmt.Sprintf("%s=%q is not a number", k, v))
	}
	return n
}

func (a ddArgs) list(k string) []uint64 {
	var out []uint64
	for f := range strings.SplitSeq(a.kv[k], ",") {
		if f != "" {
			n, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				panic(fmt.Sprintf("%s=%q is not a list of numbers", k, a.kv[k]))
			}
			out = append(out, n)
		}
	}
	return out
}

func (a ddArgs) id() core.NodeID { return a.idsOf(a.pos[:1])[0] }

func (a ddArgs) ids() []core.NodeID { return a.idsOf(a.pos) }

func (a ddArgs) idsOf(pos []string) []core.NodeID {
	var out []core.NodeID
	for _, p := range pos {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			panic(fmt.Sprintf("%q is not a node id", p))
		}
		out = append(out, core.NodeID(n))
	}
	return out
}

func (e *ddEnv) run(path string, line int, cmdLine string) {
	defer func() {
		if p := recover(); p != nil {
			e.t.Fatalf("%s:%d: %s: %v", path, line, cmdLine, p)
		}
	}()
	fields := strings.Fields(cmdLine)
	a := ddArgs{kv: map[string]string{}}
	for _, f := range fields[1:] {
		if k, v, ok := strings.Cut(f, "="); ok {
			a.kv[k] = v
		} else if f == "elect" || f == "nolease" || f == "drop" || f == "lease" || f == "reject" {
			a.kv[f] = ""
		} else {
			a.pos = append(a.pos, f)
		}
	}
	switch fields[0] {
	case "add-nodes":
		e.addNodes(a)
	case "campaign":
		n := e.node(a.id())
		n.r.now = e.now
		n.r.campaign(!a.has("elect"), 0, 0)
	case "tick":
		e.now = e.now.Add(time.Duration(a.num("ms", 100)) * time.Millisecond)
		for _, id := range a.ids() {
			e.node(id).r.Tick(e.now)
		}
	case "propose":
		data := []byte(a.pos[1])
		pos, code := e.node(a.id()).r.Propose(e.now, []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(data), Data: data}})
		e.printf("positions=%v%s", pos, fmtCode(code))
	case "step":
		e.step(a)
	case "read-index":
		e.node(a.id()).r.ReadIndex(e.now, a.num("ctx", 1), a.has("nolease"))
	case "transfer":
		ids := a.ids()
		e.printf("transfer%s", fmtCode(e.node(ids[0]).r.TransferLeadership(e.now, ids[1])))
	case "install-snapshot":
		e.installSnapshot(a)
	case "process-ready":
		e.processReady(e.pick(a))
	case "storage-done":
		e.storageDone(e.pick(a))
	case "deliver-msgs":
		e.deliver(e.pick(a), a.has("drop"))
	case "stabilize":
		e.stabilize()
	case "raft-state":
		e.state()
	case "raft-log":
		e.raftLog(e.node(a.id()))
	default:
		panic("unknown command " + fields[0])
	}
}

func (e *ddEnv) node(id core.NodeID) *ddNode {
	n := e.nodes[id]
	if n == nil {
		panic(fmt.Sprintf("no node %d", id))
	}
	return n
}

func (e *ddEnv) pick(a ddArgs) []core.NodeID {
	if ids := a.ids(); len(ids) > 0 {
		return ids
	}
	return e.ids
}

// addNodes starts n nodes, numbered after the existing ones, over the
// configuration voters= and learners= name; term=, vote=, commit= and
// log= (the terms of entries 1..) are their durable state, silent=I,T their
// silence condition.
func (e *ddEnv) addNodes(a ddArgs) {
	n, _ := strconv.Atoi(a.pos[0])
	var conf core.MembershipValue
	for _, v := range a.list("voters") {
		conf.Members = append(conf.Members, core.MemberValue{ID: core.NodeID(v), Voter: true, SPKI: [32]byte{byte(v)}})
	}
	for _, v := range a.list("learners") {
		conf.Members = append(conf.Members, core.MemberValue{ID: core.NodeID(v), SPKI: [32]byte{byte(v)}})
	}
	ents := chainEntries(0, 1, a.list("log")...)
	for range n {
		id := core.NodeID(len(e.ids) + 1)
		cfg := &Config{
			ID: id, Group: 1, Seed: uint64(id), Timing: testTiming(), MaxInflight: 8, MaxInflightBytes: 1 << 20,
			MaxBatchBytes: 1 << 16, MinVoters: 1, PromoteLag: 16, Lease: a.has("lease"), CanVote: true, MaxChainRefusals: 3,
		}
		if s := a.list("silent"); len(s) == 2 {
			cfg.SilentIndex, cfg.SilentTerm = s[0], s[1]
		}
		hs := core.HardState{Term: a.num("term", 0), Vote: core.NodeID(a.num("vote", 0)), Commit: a.num("commit", 0)}
		dn := &ddNode{disk: map[uint64]core.EntryValue{}}
		for _, x := range ents {
			dn.disk[x.Position.Index] = x
		}
		dn.r = NewRaft(cfg, &StartState{HardState: hs, Snapshot: core.SnapshotMetaValue{Membership: conf}, Entries: slices.Clone(ents), Now: t0})
		e.nodes[id] = dn
		e.ids = append(e.ids, id)
	}
}

var ddKinds = map[string]core.MessageKind{
	"MsgPreVote": core.MsgPreVote, "MsgPreVoteResp": core.MsgPreVoteResp, "MsgVote": core.MsgVote,
	"MsgVoteResp": core.MsgVoteResp, "MsgHeartbeat": core.MsgHeartbeat, "MsgHeartbeatResp": core.MsgHeartbeatResp,
	"MsgTimeoutNow": core.MsgTimeoutNow, "MsgReadIndex": core.MsgReadIndex, "MsgReadIndexResp": core.MsgReadIndexResp,
	"MsgForwardAdmin": core.MsgForwardAdmin, "MsgForwardAdminResp": core.MsgForwardAdminResp,
	"MsgLeaderRecord": core.MsgLeaderRecord, "MsgApp": core.MsgApp, "MsgAppResp": core.MsgAppResp,
	"MsgForward": core.MsgForward, "MsgForwardResp": core.MsgForwardResp,
}

func kindName(k core.MessageKind) string {
	for name, v := range ddKinds {
		if v == k {
			return name
		}
	}
	return fmt.Sprintf("Msg(%#x)", uint8(k))
}

var ddFlags = []struct {
	name string
	f    core.MessageFlags
}{
	{"transfer", core.FlagTransfer},
	{"need-snapshot", core.FlagNeedSnapshot},
	{"no-lease", core.FlagNoLease},
	{"truncated", core.FlagTruncated},
	{"p2", core.FlagProtocol2},
	{"p3", core.FlagProtocol3},
}

// step hands a node a message written in the script: type= and from= name
// it, and the rest of its fields are named as fmtMsg prints them. ents=
// lists the terms of the entries an append carries after index=, chained
// from the receiver's own chain there; cmd= is a forward's command.
func (e *ddEnv) step(a ddArgs) {
	to := a.id()
	n := e.node(to)
	kind, ok := ddKinds[a.kv["type"]]
	if !ok {
		panic(fmt.Sprintf("unknown message type %q", a.kv["type"]))
	}
	m := core.MessageValue{
		Version: 1, Group: 1, Kind: kind, From: core.NodeID(a.num("from", 0)), To: to,
		Term: a.num("term", 0), LogTerm: a.num("logterm", 0), Index: a.num("index", 0), Commit: a.num("commit", 0),
		Reject: a.has("reject"), RejectHint: a.num("hint", 0), Context: a.num("ctx", 0), Contract: a.num("contract", 0),
		TransferIssuer: core.NodeID(a.num("issuer", 0)), TransferTerm: a.num("transfer-term", 0),
	}
	for f := range strings.SplitSeq(a.kv["flags"], ",") {
		for _, x := range ddFlags {
			if x.name == f {
				m.Flags |= x.f
			}
		}
	}
	if ts := a.list("ents"); len(ts) > 0 {
		m.Entries = chainEntries(n.r.ChainAt(m.Index), m.Index+1, ts...)
	}
	if c, ok := a.kv["cmd"]; ok {
		d := []byte(c)
		m.Entries = []core.EntryValue{{Kind: core.EntryCommand, BodySum: wire.BodySum(d), Data: d}}
	}
	n.r.Step(e.now, m)
}

// installSnapshot installs on a node the snapshot of node from= at its
// commit, as a driver does once the transfer is restored.
func (e *ddEnv) installSnapshot(a ddArgs) {
	n, src := e.node(a.id()), e.node(core.NodeID(a.num("from", 0))).r
	idx := src.Status().Commit
	term, _ := src.TermAt(idx)
	n.r.InstallSnapshot(core.SnapshotMetaValue{
		Position: core.PositionValue{Term: term, Index: idx}, ChainTip: src.ChainAt(idx),
		Membership: src.ConfAt(idx), Timing: testTiming(),
	})
	e.printf("installed %d/%d from %d", idx, term, a.num("from", 0))
}

func (e *ddEnv) processReady(ids []core.NodeID) bool {
	moved := false
	for _, id := range ids {
		n := e.node(id)
		for n.r.HasReady() {
			moved = true
			rd := n.r.Ready()
			e.printf("> %d handling Ready", id)
			e.printReady(&rd)
			e.net = append(e.net, rd.Messages...)
			if rd.NeedsWrite() {
				n.writes = append(n.writes, rd)
			}
			for _, f := range rd.Fetch {
				var got []core.EntryValue
				for i := f.Lo; i < f.Hi; i++ {
					x, ok := n.disk[i]
					if !ok {
						break
					}
					got = append(got, x)
				}
				n.r.Fetched(f.To, f.Lo, got, false)
			}
		}
	}
	return moved
}

func (e *ddEnv) storageDone(ids []core.NodeID) bool {
	moved := false
	for _, id := range ids {
		n := e.node(id)
		for len(n.writes) > 0 {
			moved = true
			rd := n.writes[0]
			n.writes = n.writes[1:]
			if tf := rd.TruncateFrom; tf != 0 {
				for i := tf; ; i++ {
					if _, ok := n.disk[i]; !ok {
						break
					}
					delete(n.disk, i)
				}
			}
			var idx, term uint64
			for _, x := range rd.Entries {
				n.disk[x.Position.Index] = x
				idx, term = x.Position.Index, x.Position.Term
			}
			e.printf("> %d storage done seq=%d through %d/%d", id, rd.Seq, idx, term)
			n.r.StorageDone(rd.Seq, idx, term)
			e.net = append(e.net, rd.AfterSync...)
		}
	}
	return moved
}

// deliver hands each message in flight to one of ids to its receiver, in
// the order they left, or drops them.
func (e *ddEnv) deliver(ids []core.NodeID, drop bool) bool {
	var keep, take []core.MessageValue
	for _, m := range e.net {
		if slices.Contains(ids, m.To) {
			take = append(take, m)
		} else {
			keep = append(keep, m)
		}
	}
	e.net = keep
	for _, id := range ids {
		var head bool
		for _, m := range take {
			if m.To != id {
				continue
			}
			n := e.nodes[id]
			switch {
			case drop || n == nil:
				e.printf("  dropped %s", fmtMsg(m))
				continue
			case !head:
				e.printf("> %d receiving messages", id)
				head = true
			}
			e.printf("  %s", fmtMsg(m))
			n.r.Step(e.now, m)
		}
	}
	if drop {
		return false
	}
	for _, m := range take {
		if e.nodes[m.To] == nil && !slices.Contains(ids, m.To) {
			e.printf("  dropped %s", fmtMsg(m))
		}
	}
	return len(take) > 0
}

// stabilize runs every node's Ready, write and delivery until nothing
// moves: a message to a node the script did not add is dropped.
func (e *ddEnv) stabilize() {
	all := slices.Clone(e.ids)
	for _, m := range e.net {
		if !slices.Contains(all, m.To) {
			all = append(all, m.To)
		}
	}
	for {
		a := e.processReady(e.ids)
		b := e.storageDone(e.ids)
		for _, m := range e.net {
			if !slices.Contains(all, m.To) {
				all = append(all, m.To)
			}
		}
		c := e.deliver(all, false)
		if !a && !b && !c {
			return
		}
	}
}

func (e *ddEnv) state() {
	for _, id := range e.ids {
		r := e.nodes[id].r
		st := r.Status()
		e.printf("%d: %v term=%d vote=%d lead=%d commit=%d stable=%d last=%d", id, st.Role, st.Term, r.vote, st.Lead, st.Commit, st.Stable, st.Last)
	}
}

func (e *ddEnv) raftLog(n *ddNode) {
	if s := n.r.SnapshotIndex(); s != 0 {
		t, _ := n.r.TermAt(s)
		e.printf("snapshot %d/%d", s, t)
	}
	for i := n.r.SnapshotIndex() + 1; i <= n.r.Status().Last; i++ {
		x, _ := n.r.Entry(i)
		e.printf("%s", fmtEntry(&x))
	}
}

var ddEntryKinds = map[core.EntryKind]string{
	core.EntryCommand: "command", core.EntryNoOp: "noop", core.EntryMembership: "membership",
	core.EntryTimingContract: "timing", core.EntryClusterVersion: "cluster-version",
	core.EntryNoSpaceRaise: "no-space-raise", core.EntryNoSpaceClear: "no-space-clear", core.EntryTransfer: "transfer",
}

func fmtEntry(x *core.EntryValue) string {
	s := fmt.Sprintf("%d/%d %s", x.Position.Index, x.Position.Term, ddEntryKinds[x.Kind])
	if x.Kind == core.EntryCommand {
		s += fmt.Sprintf(" %q", x.Data)
	}
	return s
}

// fmtCode is " code=<code>" for a refusal, nothing for none.
func fmtCode(c errs.Code) string {
	if c == 0 {
		return ""
	}
	return " code=" + c.String()
}

func fmtMsg(m core.MessageValue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d->%d %s term=%d", m.From, m.To, kindName(m.Kind), m.Term)
	for _, f := range []struct {
		k string
		v uint64
	}{{"logterm", m.LogTerm}, {"index", m.Index}, {"commit", m.Commit}, {"hint", m.RejectHint}, {"ctx", m.Context}, {"contract", m.Contract}} {
		if f.v != 0 {
			fmt.Fprintf(&b, " %s=%d", f.k, f.v)
		}
	}
	if m.Reject {
		b.WriteString(" reject")
	}
	b.WriteString(fmtCode(m.Code))
	if m.TransferIssuer != 0 || m.TransferTerm != 0 {
		fmt.Fprintf(&b, " issuer=%d transfer-term=%d", m.TransferIssuer, m.TransferTerm)
	}
	// Every frame of this build announces protocols 2 and 3: only another
	// announcement is printed.
	if p := m.Flags & (core.FlagProtocol2 | core.FlagProtocol3); p != core.FlagProtocol2|core.FlagProtocol3 {
		b.WriteString(" announces=" + [...]string{"none", "p2", "p3"}[p>>4])
	}
	var flags []string
	for _, x := range ddFlags[:4] {
		if m.Flags&x.f != 0 {
			flags = append(flags, x.name)
		}
	}
	if len(flags) > 0 {
		b.WriteString(" flags=" + strings.Join(flags, ","))
	}
	for i := range m.Entries {
		b.WriteString(" [" + fmtEntry(&m.Entries[i]) + "]")
	}
	return b.String()
}

func (e *ddEnv) printReady(rd *ReadyValue) {
	if rd.NeedsWrite() {
		e.printf("  seq=%d", rd.Seq)
	}
	if hs := rd.HardState; hs != nil {
		e.printf("  HardState term=%d vote=%d commit=%d", hs.Term, hs.Vote, hs.Commit)
	}
	if rd.Commit != 0 {
		e.printf("  Commit %d", rd.Commit)
	}
	if rd.TruncateFrom != 0 {
		e.printf("  TruncateFrom %d", rd.TruncateFrom)
	}
	e.list("Entries", len(rd.Entries), func(i int) string { return fmtEntry(&rd.Entries[i]) })
	e.list("Messages", len(rd.Messages), func(i int) string { return fmtMsg(rd.Messages[i]) })
	e.list("AfterSync", len(rd.AfterSync), func(i int) string { return fmtMsg(rd.AfterSync[i]) })
	if rd.ApplyRead.Lo != 0 {
		e.printf("  ApplyRead %d..%d", rd.ApplyRead.Lo, rd.ApplyRead.Hi)
	}
	e.list("Apply", len(rd.Apply), func(i int) string { return fmtEntry(&rd.Apply[i]) })
	e.list("ReadStates", len(rd.ReadStates), func(i int) string {
		return fmt.Sprintf("ctx=%d index=%d%s", rd.ReadStates[i].Context, rd.ReadStates[i].Index, fmtCode(rd.ReadStates[i].Code))
	})
	e.list("Forwarded", len(rd.Forwarded), func(i int) string { return fmtMsg(rd.Forwarded[i]) })
	e.list("Transfers", len(rd.Transfers), func(i int) string {
		return fmt.Sprintf("to=%d%s", rd.Transfers[i].To, fmtCode(rd.Transfers[i].Code))
	})
	e.list("Fetch", len(rd.Fetch), func(i int) string {
		return fmt.Sprintf("to=%d %d..%d", rd.Fetch[i].To, rd.Fetch[i].Lo, rd.Fetch[i].Hi)
	})
	if len(rd.SendSnapshot) > 0 {
		e.printf("  SendSnapshot %v", rd.SendSnapshot)
	}
	if rd.Compact != 0 {
		e.printf("  Compact %d", rd.Compact)
	}
	if rd.SilenceEnded {
		e.printf("  SilenceEnded")
	}
	if rd.SilenceLowered != 0 {
		e.printf("  SilenceLowered %d", rd.SilenceLowered)
	}
	if rd.LeaderUnheard {
		e.printf("  LeaderUnheard")
	}
	if len(rd.Membership) > 0 {
		var ms []string
		for i, m := range rd.Membership {
			s := strconv.FormatUint(uint64(m.ID), 10)
			if !m.Voter {
				s += "(learner)"
			}
			if i < len(rd.MembershipAt) {
				s += "@" + strconv.FormatUint(rd.MembershipAt[i], 10)
			}
			ms = append(ms, s)
		}
		e.printf("  Membership %s", strings.Join(ms, " "))
	}
	if rd.LeaderHint.ID != 0 {
		e.printf("  LeaderHint %d", rd.LeaderHint.ID)
	}
	if rd.LeaderAddress.ID != 0 {
		e.printf("  LeaderAddress %d", rd.LeaderAddress.ID)
	}
	if rd.Unsupported.Index != 0 {
		e.printf("  Unsupported %+v", rd.Unsupported)
	}
	if len(rd.ChainRefusedBy) > 0 {
		e.printf("  ChainRefusedBy %v", rd.ChainRefusedBy)
	}
}

func (e *ddEnv) list(name string, n int, line func(int) string) {
	if n == 0 {
		return
	}
	e.printf("  %s:", name)
	for i := range n {
		e.printf("    %s", line(i))
	}
}
