package raft

import (
	"fmt"
	"slices"
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// answersHolding delivers n heartbeat answers of peer 2, each reporting it
// holds through held, counts the entries the leader sends it, and returns
// the previous index of each empty append it sends it.
func answersHolding(l *Raft, n int, held uint64, sent map[uint64]int) []uint64 {
	var empty []uint64
	for range n {
		l.now = l.now.Add(100 * time.Millisecond)
		hb := msg(core.MsgHeartbeatResp, 2, 1, 2)
		hb.Index = held
		l.Step(l.now, hb)
		rd := l.Ready()
		sentTo(&rd, 2, sent)
		for _, m := range append(rd.Messages, rd.AfterSync...) {
			if m.Kind == core.MsgApp && m.To == 2 && len(m.Entries) == 0 {
				empty = append(empty, m.Index)
			}
		}
	}
	return empty
}

// #767: on a link narrower than one batch per heartbeat interval, the
// follower is still receiving the oldest append in flight when its
// heartbeat answer, on the fast control lane, reports it holds less: no
// append was lost. Probed on that answer (#560 tells only a slow
// fdatasync from a loss), the peer was sent the window again from
// match+1, behind the copies still on the lane, at every heartbeat. The
// leader now sends an empty append after the window's end instead, on the
// data lane behind it: the follower accepts it once the window crossed,
// and its acknowledgement covers the window.
func TestAnAppendStillCrossingANarrowLinkIsNotSentAgain(t *testing.T) {
	l, sent := slowAckLeader(t)
	empty := answersHolding(l, 3, 1, sent)
	for idx := uint64(2); idx <= 5; idx++ {
		if sent[idx] != 1 {
			t.Fatalf("entry %d sent %d times while the window was still crossing the link (sent %v)", idx, sent[idx], sent)
		}
	}
	if pr := l.prs[2]; pr.state != stateReplicate || pr.match != 1 || pr.next != 6 {
		t.Fatalf("peer 2 %+v, want replicate, match 1, next 6: its window is still crossing", *pr)
	}
	if !slices.Contains(empty, 5) {
		t.Fatalf("no empty append after 5, the window's end, behind the window (empty appends after %v)", empty)
	}
	ack := msg(core.MsgAppResp, 2, 1, 2)
	ack.Index = 5
	l.Step(l.now, ack)
	rd := l.Ready()
	sentTo(&rd, 2, sent)
	if pr := l.prs[2]; pr.state != stateReplicate || pr.match != 5 || sent[2] != 1 {
		t.Fatalf("peer 2 %+v after the window crossed, entry 2 sent %d times, want replicate with match 5 and each entry sent once", *pr, sent[2])
	}
}

// #767, in a probe: every heartbeat answer sent the probe of MaxBatchBytes
// again, whatever the lane still carried. One probe is in flight now; a
// heartbeat answer sends an empty append behind it. Accepted at the match,
// that probe was lost: the peer replicates from there, and the entries go
// out once more.
func TestAProbeStillCrossingANarrowLinkIsNotSentAgain(t *testing.T) {
	l, _ := slowAckLeader(t)
	pr := l.prs[2]
	pr.becomeProbe()
	l.sendAppend(2, true)
	l.Ready()
	sent := map[uint64]int{}
	empty := answersHolding(l, 3, 1, sent)
	if len(sent) != 0 {
		t.Fatalf("the probe in flight sent again at the heartbeat answers: %v", sent)
	}
	if len(empty) != 3 || empty[0] != 1 || pr.state != stateProbe {
		t.Fatalf("empty appends after %v, peer 2 %+v: want one after 1 per answer, still probing", empty, *pr)
	}
	// The probe was lost: the follower accepts the empty append at its
	// match, and the peer replicates from there.
	ack := msg(core.MsgAppResp, 2, 1, 2)
	ack.Index = 1
	l.Step(l.now, ack)
	rd := l.Ready()
	sentTo(&rd, 2, sent)
	if pr.state != stateReplicate || sent[2] != 1 || sent[5] != 1 {
		t.Fatalf("peer 2 %+v after accepting the empty append at its match, sent %v: want replicate and the entries sent once", *pr, sent)
	}
}

// narrowLane is a three-node simulation in which the leader's data lane to
// the follower S carries bw bytes a second, FIFO as one TCP connection,
// and drops a frame past a queue of queueCap bytes not yet on the wire, as
// Send does past PeerQueueBytes; the control lane and the other links stay
// fast. S is cut off for cutFor while the leader writes, then healed, and
// the writes go on for runFor at half the size. It returns S's lag behind
// the leader's last index at the end, and the payload bytes the lane
// carried and how many of them it had carried before.
func narrowLane(t *testing.T, batch int, bw float64, queueCap int64, cutFor, runFor time.Duration) (lag uint64, payload, again int64) {
	t.Helper()
	s := newSim(simOptions{seed: 1, nodes: 3})
	s.run(s.after(3*time.Second), false)
	l := s.leader()
	if l == nil {
		t.Fatal("no leader")
	}
	term := l.r.Status().Term
	var sid, fid core.NodeID
	for _, id := range s.ids {
		switch {
		case id == l.id:
		case sid == 0:
			sid = id
		default:
			fid = id
		}
	}
	l.r.cfg.MaxBatchBytes = batch
	l.r.cfg.MaxInflight, l.r.cfg.MaxInflightBytes = 64, 8*batch
	type queued struct {
		at   time.Time
		size int
	}
	var lane []queued
	var free time.Time
	var maxSent uint64
	cut := true
	half := rtt(l.id, sid) / 2
	s.onSend = func(m core.MessageValue) {
		if m.From != l.id || m.To != sid || cut {
			return
		}
		m.Entries = slices.Clone(m.Entries)
		at := s.now.Add(half)
		if m.Kind == core.MsgApp {
			size := 120
			for i := range m.Entries {
				size += len(m.Entries[i].Data) + 40
			}
			for len(lane) > 0 && !lane[0].at.After(s.now) {
				lane = lane[1:]
			}
			var backlog int64
			for _, q := range lane {
				backlog += int64(q.size)
			}
			if backlog+int64(size) > queueCap {
				return
			}
			for i := range m.Entries {
				e := &m.Entries[i]
				payload += int64(len(e.Data))
				if e.Position.Index <= maxSent {
					again += int64(len(e.Data))
				}
				maxSent = max(maxSent, e.Position.Index)
			}
			start := free
			if start.Before(s.now) {
				start = s.now
			}
			lane = append(lane, queued{at: start, size: size})
			free = start.Add(time.Duration(float64(size) / bw * float64(time.Second)))
			at = free.Add(half)
		}
		s.push(&event{at: at, kind: evDeliver, node: sid, msg: m})
	}
	// onSend delivers the leader's frames to S; the sim never does.
	s.cut[[2]core.NodeID{l.id, sid}] = true
	others := [][2]core.NodeID{{sid, l.id}, {fid, sid}, {sid, fid}}
	for _, k := range others {
		s.cut[k] = true
	}
	write := func(d time.Duration, size int) {
		for end := s.after(d); s.now.Before(end) && s.err == nil; {
			s.proposed++
			p := make([]byte, size)
			copy(p, fmt.Sprintf("cmd-%08d-", s.proposed))
			if !s.propose(l, p) {
				t.Fatalf("propose refused at %v", s.now)
			}
			s.run(s.after(10*time.Millisecond), false)
		}
	}
	write(cutFor, 1300)
	cut = false
	for _, k := range others {
		delete(s.cut, k)
	}
	payload, again = 0, 0
	write(runFor, 650)
	if s.err != nil {
		t.Fatalf("sim: %v", s.err)
	}
	if st := l.r.Status(); st.Role != core.RoleLeader || st.Term != term {
		t.Fatalf("leadership moved: %+v", st)
	}
	return l.r.log.lastIndex() - min(l.r.log.lastIndex(), s.nodes[sid].r.log.lastIndex()), payload, again
}

// #767, end to end in the simulator: MaxBatchBytes 64 KiB on a lane of
// 256 KiB/s takes 250 ms, longer than a heartbeat interval; the defaults
// scaled by 1/16 in bytes (a 1 MiB batch on a 4 MiB/s link). S comes back
// some 200 writes behind, and the writes go on at a quarter of the lane:
// it must catch up within 8 s, with the lane carrying little more than
// once what S lacks. Re-probed at every heartbeat answer, it never caught
// up, and nineteen in twenty bytes the lane carried were copies.
func TestAFollowerCatchesUpOverALinkNarrowerThanABatchPerHeartbeat(t *testing.T) {
	lag, payload, again := narrowLane(t, 64<<10, 256<<10, 4<<20, 2*time.Second, 8*time.Second)
	t.Logf("S %d entries behind at the end; the lane carried %d KiB of payload, %d KiB of it again", lag, payload>>10, again>>10)
	if lag > 2 || again*10 > payload {
		t.Fatalf("over a link narrower than a batch per heartbeat, S is still %d entries behind after 8 s, and %d of the %d KiB the lane carried were copies",
			lag, again>>10, payload>>10)
	}
}
