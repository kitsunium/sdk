package raft

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// The client sessions of #918 under the simulator. Each client opens a
// session, then writes one key at a time under increasing sequences and
// retries every write whose outcome it does not learn — a crash, a lost
// answer, a leader change, a position another entry took — blindly, with
// the same session, sequence and payload, through whichever node it picks
// next. The nodes apply what their own session table, the production fold,
// says to apply, from the snapshot they continue: the history must be
// linearizable and no command applied twice. The file sorts after the seed
// suites (raft's CLAUDE.md, Tests).

// sessKey names one command of one session.
type sessKey struct {
	s   core.SessionID
	seq uint64
}

// sessAttempt is where a client waits for its command: the node and life it
// submitted through, and the position it was answered, or the submission id
// of a forward not answered yet. committed is set for a position the
// leader answered at or below its commit.
type sessAttempt struct {
	node      core.NodeID
	life      int
	pos       core.PositionValue
	fwd       uint64
	committed bool
	deadline  time.Time
}

// sessClient is one client: its session, its sequence, the command under
// way and its kvOp, and where it waits.
type sessClient struct {
	id     core.SessionID
	opened bool
	seq    uint64
	key    string
	val    string
	op     int
	busy   bool
	wait   *sessAttempt
	tries  int
}

// sessClients are the clients, each node's session table, and the counts a
// run is judged on.
type sessClients struct {
	clients []*sessClient
	tables  map[core.NodeID]*SessionTable
	// fresh is the index each command was applied at, the first node to
	// apply it deciding; another index is a second application.
	fresh map[sessKey]uint64
	// noDedupe applies every session command, as a node without the
	// table would: the negative control.
	noDedupe bool
	err      error
	fctx     uint64
	retries  int
	skipped  int
	known    int
}

func newSessClients(k int) *sessClients {
	c := &sessClients{tables: map[core.NodeID]*SessionTable{}, fresh: map[sessKey]uint64{}}
	for i := range k {
		var id core.SessionID
		id[0], id[15] = 0x5e, byte(i+1)
		c.clients = append(c.clients, &sessClient{id: id})
	}
	return c
}

// reload starts n's table from the snapshot it continues.
func (c *sessClients) reload(n *simNode) {
	c.tables[n.id] = NewSessionTable(n.base.Sessions)
}

// applied folds a session command n applies, applies the command when the
// fold says so, and resolves the client waiting at that position.
func (c *sessClients) applied(w *kvWorld, s *sim, n *simNode, e *core.EntryValue, st map[string]string) {
	t := c.tables[n.id]
	if t == nil {
		t = NewSessionTable(nil)
		c.tables[n.id] = t
	}
	answer, code := t.Apply(e)
	sid, seq, data, err := wire.ParseSession(e.Data)
	if err != nil {
		c.fail("node %d: session entry %d does not parse", n.id, e.Position.Index)
		return
	}
	apply := code == 0 && answer == e.Position && seq != sessionOpen
	if c.noDedupe {
		apply = seq != sessionOpen
	}
	if apply {
		k, v, _ := strings.Cut(string(data), "=")
		st[k] = v
		key := sessKey{s: sid, seq: seq}
		if at, ok := c.fresh[key]; ok && at != e.Position.Index {
			c.fail("session %x command %d applied at %d and at %d", sid[15], seq, at, e.Position.Index)
		}
		c.fresh[key] = e.Position.Index
		if _, ok := w.firstApplied[string(data)]; !ok {
			w.firstApplied[string(data)] = s.now.UnixNano()
		}
	} else if seq != sessionOpen && code == 0 {
		c.skipped++
	}
	if code != 0 {
		c.fail("node %d: session entry %d refused %v by a client that never reuses a sequence", n.id, e.Position.Index, code)
	}
	for _, cl := range c.clients {
		a := cl.wait
		if a == nil || a.fwd != 0 || a.node != n.id || a.life != n.life || a.pos.Index != e.Position.Index {
			continue
		}
		if a.committed || a.pos.Term == e.Position.Term {
			c.done(w, s, cl)
			continue
		}
		cl.wait = nil // another entry took the position: try again
	}
}

// done resolves a client's command: it took effect by now.
func (c *sessClients) done(w *kvWorld, s *sim, cl *sessClient) {
	cl.wait = nil
	if !cl.opened {
		cl.opened = true
		return
	}
	w.ops[cl.op].ret = s.now.UnixNano()
	cl.busy = false
}

// forwardAnswered takes the leader's answer to a client's forward, and
// reports whether the answer was one.
func (c *sessClients) forwardAnswered(n *simNode, m core.MessageValue) bool {
	for _, cl := range c.clients {
		a := cl.wait
		if a == nil || a.fwd != m.Context || a.node != n.id {
			continue
		}
		if a.life != n.life || m.Reject {
			cl.wait = nil
			return true
		}
		a.fwd, a.pos = 0, core.PositionValue{Term: m.LogTerm, Index: m.Index}
		a.committed = m.Index <= m.Commit
		return true
	}
	return false
}

func (c *sessClients) fail(format string, args ...any) {
	if c.err == nil {
		c.err = fmt.Errorf(format, args...)
	}
}

// step moves every client: a command resolved gives way to the next one,
// a wait past its deadline or on a crashed life is given up, and a client
// with nothing under way submits, again if need be, through node n.
func (c *sessClients) step(w *kvWorld, s *sim, n *simNode, keys []string) {
	for i, cl := range c.clients {
		if a := cl.wait; a != nil {
			nd := s.nodes[a.node]
			if nd.life == a.life && nd.alive && s.now.Before(a.deadline) {
				if a.fwd == 0 && a.committed && appliedThrough(nd) >= a.pos.Index {
					c.done(w, s, cl)
				}
				continue
			}
			cl.wait = nil
		}
		if cl.opened && !cl.busy {
			cl.seq++
			cl.key = keys[(int(cl.seq)+i)%len(keys)]
			cl.val = fmt.Sprintf("c%d-%d", i, cl.seq)
			w.ops = append(w.ops, kvOp{key: cl.key, write: true, val: cl.val, call: s.now.UnixNano(), ret: math.MaxInt64})
			cl.op, cl.busy, cl.tries = len(w.ops)-1, true, 0
		}
		c.submit(s, n, cl)
	}
}

// appliedThrough is the last index n applied in its life.
func appliedThrough(n *simNode) uint64 {
	if k := len(n.applied); k > 0 {
		return n.applied[k-1].Position.Index
	}
	return n.appliedFrom
}

// submit sends a client's command, or its session's opening, through n:
// proposed when n leads, forwarded otherwise, as the driver does.
func (c *sessClients) submit(s *sim, n *simNode, cl *sessClient) {
	var data []byte
	seq := sessionOpen
	if cl.opened {
		seq, data = cl.seq, []byte(cl.key+"="+cl.val)
	}
	payload := wire.AppendSession(nil, cl.id, seq, data)
	e := core.EntryValue{Kind: core.EntrySessionCommand, BodySum: wire.BodySum(payload), Data: payload}
	a := &sessAttempt{node: n.id, life: n.life, deadline: s.now.Add(time.Second)}
	st := n.r.Status()
	switch {
	case st.Role == core.RoleLeader:
		pos, code := n.r.ProposeSession(s.clock(n), e)
		if code != 0 {
			s.process(n)
			return // refused before append: submitted again at the next step
		}
		c.tried(cl)
		a.pos, a.committed = pos, pos.Index <= n.r.Status().Commit
		if a.committed {
			c.known++
		}
		cl.wait = a
		s.process(n)
	case st.Lead != 0 && st.Lead != n.id:
		c.tried(cl)
		c.fctx++
		a.fwd = 1<<48 | c.fctx
		cl.wait = a
		s.deliver(core.MessageValue{
			Version: 1, Group: 1, Kind: core.MsgForward, From: n.id, To: st.Lead, Term: st.Term,
			Context: a.fwd, Entries: []core.EntryValue{e},
		})
	}
}

// tried counts a submission that may reach the log: past the first, of the
// same command, a retry.
func (c *sessClients) tried(cl *sessClient) {
	if cl.tries > 0 {
		c.retries++
	}
	cl.tries++
}

// sessionRun runs one seed of the session workload over drops,
// duplication, partitions, crashes and snapshots, then checks that every
// command was applied once and that the history is linearizable.
func sessionRun(seed uint64, unchecked, noDedupe bool) (*sessClients, error) {
	s := newSim(simOptions{seed: seed, snapEvery: 24, sessionUnchecked: unchecked})
	if code := s.raiseOnceHeard(Protocol4); code != 0 {
		return nil, fmt.Errorf("seed %d: the raise to 4 was refused: %v", seed, code)
	}
	s.kv = newKVWorld()
	s.kv.noLeas = true
	s.kv.sess = newSessClients(3)
	s.kv.sess.noDedupe = noDedupe
	for _, id := range s.ids {
		s.kv.restart(s.nodes[id])
	}
	s.opt.drop, s.opt.dup, s.opt.crashRate = 0.03, 0.25, 0.3
	keys := []string{"a", "b", "c"}
	step := 0
	for k := 0; k < 6 && s.err == nil && s.kv.sess.err == nil; k++ {
		switch k % 3 {
		case 0:
			s.partition([]core.NodeID{core.NodeID(k%3 + 1)}, nil)
		case 1:
			s.heal()
		}
		end := s.after(3 * time.Second)
		for s.now.Before(end) && s.err == nil && s.kv.sess.err == nil {
			s.run(s.after(15*time.Millisecond), false)
			step++
			n := s.nodes[s.ids[step%len(s.ids)]]
			if !n.alive {
				continue
			}
			if step%2 == 1 {
				s.kv.read(s, n, keys[step%len(keys)])
				continue
			}
			s.kv.sess.step(s.kv, s, n, keys)
		}
	}
	s.heal()
	s.opt.crashRate, s.opt.drop, s.opt.dup = 0, 0, 0
	for _, id := range s.ids {
		if !s.nodes[id].alive {
			s.start(s.nodes[id])
		}
	}
	s.run(s.after(5*time.Second), false)
	c := s.kv.sess
	switch {
	case s.err != nil:
		return c, fmt.Errorf("seed %d: %w", seed, s.err)
	case c.err != nil:
		return c, fmt.Errorf("seed %d: %w", seed, c.err)
	}
	s.kv.settleUnknownWrites()
	done := 0
	for _, o := range s.kv.ops {
		if o.write && o.ret > 0 && o.ret != math.MaxInt64 {
			done++
		}
	}
	if done < 30 {
		return c, fmt.Errorf("seed %d: only %d session commands took effect", seed, done)
	}
	if ok, key := linearizable(s.kv.history()); !ok {
		return c, fmt.Errorf("seed %d: history of key %q is not linearizable", seed, key)
	}
	return c, nil
}

// TestBlindSessionRetriesAreAppliedOnce is #918's acceptance: clients that
// retry every unknown outcome blindly through ProposeSession, across leader
// changes, crashes, drops and duplicated frames, leave a linearizable
// history with each command applied once. The leader answers the copies it
// knows before any append; with that check off, every copy is appended and
// the apply's fold alone keeps each command applied once.
func TestBlindSessionRetriesAreAppliedOnce(t *testing.T) {
	for _, unchecked := range []bool{false, true} {
		var retries, skipped, known int
		for _, seed := range seeds(t, 6) {
			c, err := sessionRun(seed, unchecked, false)
			if err != nil {
				t.Fatalf("leader's check off %v: %v", unchecked, err)
			}
			retries, skipped, known = retries+c.retries, skipped+c.skipped, known+c.known
		}
		t.Logf("leader's check off %v: retries %d, copies skipped at apply %d, answered from the table %d", unchecked, retries, skipped, known)
		switch {
		case !unchecked && known == 0:
			t.Fatal("no retry was answered from the leader's table")
		case unchecked && skipped == 0:
			t.Fatal("with the leader's check off no copy was appended: the apply's fold was never put to the test")
		}
	}
}

// TestBlindSessionRetriesWithoutTheTableAreCaught is the negative control:
// with the leader's check off, a node that applies every session command,
// as one without the table would, applies some retried command twice, and
// the run says so.
func TestBlindSessionRetriesWithoutTheTableAreCaught(t *testing.T) {
	for seed := uint64(1); seed <= 12; seed++ {
		if _, err := sessionRun(seed, true, true); err != nil {
			t.Logf("caught: %v", err)
			return
		}
	}
	t.Fatal("no seed applied a retried session command twice with the table off")
}

// raiseOnceHeard raises the cluster version to v, again while the leader
// has not heard every member announce it in its term yet.
func (s *sim) raiseOnceHeard(v uint32) errs.Code {
	code := s.raiseTo(v)
	for range 10 {
		if code != codeVersion {
			break
		}
		code = s.raiseTo(v)
	}
	return code
}

// sessionEntry is the session command seq of s carrying data.
func sessionEntry(s core.SessionID, seq uint64, data string) core.EntryValue {
	b := wire.AppendSession(nil, s, seq, []byte(data))
	return core.EntryValue{Kind: core.EntrySessionCommand, BodySum: wire.BodySum(b), Data: b}
}

// raisedLeader is a quiet three-node cluster at cluster version 4 and its
// leader.
func raisedLeader(t *testing.T) (*sim, *simNode) {
	t.Helper()
	s := newSim(simOptions{seed: 918})
	if code := s.raiseOnceHeard(Protocol4); code != 0 {
		t.Fatalf("setup: the raise to 4: %v", code)
	}
	l := s.leader()
	if l == nil {
		t.Fatal("setup: no leader")
	}
	return s, l
}

// A retry with the session and sequence of a command and another payload is
// refused SESSION_DIGEST_MISMATCH and appends nothing: against the committed
// command, against one still in the leader's tail, and forwarded.
func TestASessionSequenceWithAnotherPayloadIsRefusedAndAppendsNothing(t *testing.T) {
	s, l := raisedLeader(t)
	sid := core.SessionID{1}
	propose := func(seq uint64, data string) (core.PositionValue, errs.Code) {
		pos, code := l.r.ProposeSession(s.clock(l), sessionEntry(sid, seq, data))
		s.process(l)
		return pos, code
	}
	if _, code := propose(0, ""); code != 0 {
		t.Fatalf("open: %v", code)
	}
	first, code := propose(1, "x=1")
	if code != 0 {
		t.Fatalf("command 1: %v", code)
	}
	s.run(s.after(time.Second), false)
	last := l.r.log.lastIndex()
	if _, code := propose(1, "x=2"); code != core.CodeSessionDigestMismatch {
		t.Fatalf("command 1 again with another payload: %v, want SESSION_DIGEST_MISMATCH", code)
	}
	if pos, code := propose(1, "x=1"); code != 0 || pos != first {
		t.Fatalf("command 1 again: %v at %v, want the committed copy at %v", code, pos, first)
	}
	if _, code := propose(0, ""); code != 0 {
		t.Fatalf("the session opened again: %v", code)
	}
	if got := l.r.log.lastIndex(); got != last {
		t.Fatalf("the copies appended %d entries", got-last)
	}
	tail, code := propose(2, "x=3")
	if code != 0 {
		t.Fatalf("command 2: %v", code)
	}
	if _, code := propose(2, "x=4"); code != core.CodeSessionDigestMismatch {
		t.Fatalf("command 2 again with another payload while it is in the tail: %v", code)
	}
	if pos, code := propose(2, "x=3"); code != 0 || pos != tail {
		t.Fatalf("command 2 again while it is in the tail: %v at %v, want %v", code, pos, tail)
	}
	if _, code := propose(1, "x=1"); code != core.CodeSessionExpired {
		t.Fatalf("command 1 below the tail's 2: %v, want SESSION_EXPIRED", code)
	}
	if got := l.r.log.lastIndex(); got != tail.Index {
		t.Fatalf("the refusals appended %d entries", got-tail.Index)
	}
	if _, code := propose(7, "y=1"); code != 0 {
		t.Fatalf("a later sequence: %v", code)
	}
	if _, code := l.r.ProposeSession(s.clock(l), sessionEntry(core.SessionID{2}, 1, "y=1")); code != core.CodeSessionExpired {
		t.Fatalf("a command of a session never opened: %v, want SESSION_EXPIRED", code)
	}
	s.run(s.after(time.Second), false)

	var f *simNode
	for _, id := range s.ids {
		if id != l.id {
			f = s.nodes[id]
		}
	}
	var got []core.MessageValue
	s.onSend = func(m core.MessageValue) {
		if m.Kind == core.MsgForwardResp && m.To == f.id {
			got = append(got, m)
		}
	}
	last = l.r.log.lastIndex()
	e := sessionEntry(sid, 7, "y=9")
	s.deliver(core.MessageValue{Version: 1, Group: 1, Kind: core.MsgForward, From: f.id, To: l.id, Term: l.r.Status().Term, Context: 77, Entries: []core.EntryValue{e}})
	s.run(s.after(200*time.Millisecond), false)
	if len(got) == 0 || !got[0].Reject || got[0].Code != core.CodeSessionDigestMismatch {
		t.Fatalf("a forwarded copy with another payload was answered %+v", got)
	}
	if l.r.log.lastIndex() != last {
		t.Fatal("the forwarded copy was appended")
	}
}

// The fold applies a sequence once and answers its copies with its
// position, refuses another payload and an older sequence without touching
// the session, survives its snapshot, and evicts the session used least
// recently past MaxSessions, whose commands are then SESSION_EXPIRED.
func TestTheSessionTableAppliesASequenceOnce(t *testing.T) {
	at := func(e core.EntryValue, i uint64) *core.EntryValue {
		e.Position = core.PositionValue{Term: 2, Index: i}
		return &e
	}
	sid := core.SessionID{9}
	tab := NewSessionTable(nil)
	steps := []struct {
		e      *core.EntryValue
		answer uint64
		code   errs.Code
	}{
		{at(sessionEntry(sid, 1, "a"), 1), 0, core.CodeSessionExpired},
		{at(sessionEntry(sid, 0, ""), 2), 2, 0},
		{at(sessionEntry(sid, 1, "a"), 3), 3, 0},
		{at(sessionEntry(sid, 1, "a"), 4), 3, 0},
		{at(sessionEntry(sid, 1, "b"), 5), 0, core.CodeSessionDigestMismatch},
		{at(sessionEntry(sid, 2, "c"), 6), 6, 0},
		{at(sessionEntry(sid, 1, "a"), 7), 0, core.CodeSessionExpired},
		{at(sessionEntry(sid, 0, ""), 8), 8, 0},
		{at(sessionEntry(sid, 2, "c"), 9), 6, 0},
	}
	for i, st := range steps {
		pos, code := tab.Apply(st.e)
		if code != st.code || pos.Index != st.answer {
			t.Fatalf("step %d: answered %v at %d, want %v at %d", i, code, pos.Index, st.code, st.answer)
		}
	}
	restored := NewSessionTable(tab.Values())
	if pos, code := restored.Apply(at(sessionEntry(sid, 2, "c"), 10)); code != 0 || pos.Index != 6 {
		t.Fatalf("after its snapshot the copy was answered %v at %d", code, pos.Index)
	}
	for i := range MaxSessions {
		var id core.SessionID
		id[0], id[1], id[2] = 1, byte(i>>8), byte(i)
		restored.Apply(at(sessionEntry(id, 0, ""), uint64(100+i)))
	}
	if n := len(restored.Values()); n != MaxSessions {
		t.Fatalf("%d sessions kept, want %d", n, MaxSessions)
	}
	if _, code := restored.Apply(at(sessionEntry(sid, 3, "d"), 5000)); code != core.CodeSessionExpired {
		t.Fatalf("the session used least recently was not evicted: %v", code)
	}
	vs := restored.Values()
	if !slices.IsSortedFunc(vs, func(a, b core.SessionValue) int { return strings.Compare(string(a.ID[:]), string(b.ID[:])) }) {
		t.Fatal("the sessions a snapshot records are not ordered by ID")
	}
}

// A mixed cluster: node 3 runs the previous release, protocol 3. The raise
// to 4 is refused while it is a member, every session command is refused
// before append, and nothing it is sent carries the new entry kind. Once it
// is upgraded and restarted the raise goes through and sessions replicate
// to it.
func TestANodeOfProtocol3IsNeverSentASessionCommandBeforeTheRaise(t *testing.T) {
	s := newSim(simOptions{seed: 918, speaks: map[core.NodeID]uint32{3: Protocol3}})
	sent := 0
	s.onSend = func(m core.MessageValue) {
		for i := range m.Entries {
			if m.Entries[i].Kind == core.EntrySessionCommand && m.To == 3 {
				sent++
			}
		}
	}
	s.run(s.after(3*time.Second), true)
	if code := s.raiseTo(Protocol3); code != 0 {
		t.Fatalf("setup: the raise to 3: %v", code)
	}
	if code := s.raiseTo(Protocol4); code != codeVersion {
		t.Fatalf("the raise to 4 with node 3 at protocol 3: %v, want VERSION_UNSUPPORTED", code)
	}
	for range 20 {
		if l := s.leader(); l != nil {
			if _, code := l.r.ProposeSession(s.clock(l), sessionEntry(core.SessionID{3}, 0, "")); code != core.CodePayloadVersionRefused {
				t.Fatalf("a session opened below cluster version 4: %v", code)
			}
			e := sessionEntry(core.SessionID{3}, 1, "x=1")
			if _, code := l.r.Propose(s.clock(l), []core.EntryValue{e}); code != core.CodePayloadVersionRefused {
				t.Fatalf("a session command proposed below cluster version 4: %v", code)
			}
			s.process(l)
		}
		s.run(s.after(100*time.Millisecond), true)
	}
	if sent != 0 {
		t.Fatalf("node 3, of protocol 3, was sent %d session commands", sent)
	}
	for _, id := range s.ids {
		if u := s.nodes[id].unsupported; u.Index != 0 {
			t.Fatalf("node %d stopped on entry %d (%v)", id, u.Index, u.Kind)
		}
	}
	s.crash(3)
	delete(s.opt.speaks, 3)
	s.push(&event{at: s.after(100 * time.Millisecond), kind: evRestart, node: 3})
	s.run(s.after(3*time.Second), true)
	if code := s.raiseTo(Protocol4); code != 0 {
		t.Fatalf("the raise to 4 once every member speaks it: %v", code)
	}
	l := s.leader()
	if _, code := l.r.ProposeSession(s.clock(l), sessionEntry(core.SessionID{3}, 0, "")); code != 0 {
		t.Fatalf("a session opened at cluster version 4: %v", code)
	}
	s.process(l)
	s.run(s.after(time.Second), false)
	if sent == 0 {
		t.Fatal("node 3, upgraded, was never sent the session command")
	}
}
