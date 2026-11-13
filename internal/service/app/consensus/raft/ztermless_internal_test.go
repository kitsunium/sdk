package raft

import (
	"testing"
	"time"

	core "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// A message that carries no term is handled at this node's own term, and
// never lowers it: an append or a heartbeat of term 0 reaching a candidate
// made it follow the sender at term 0, and the next Ready wrote a HardState
// of term 0 over a durable term of 3 (#1362, FuzzStep's crasher
// d606a50547634a83: "term went back on disk on node 2: 0 < 44"). No correct
// peer sends one, every send stamping its term, but the term on disk must
// not depend on that.
func TestATermlessMessageNeverLowersTheTerm(t *testing.T) {
	for _, kind := range []core.MessageKind{core.MsgApp, core.MsgHeartbeat} {
		for _, pre := range []bool{false, true} {
			f, now := followerOfOne(0, nil)
			if pre {
				for f.role != core.RolePreCandidate {
					now = now.Add(100 * time.Millisecond)
					f.Tick(now)
				}
			} else {
				f.Step(now, msg(core.MsgTimeoutNow, 1, 2, 2))
				if f.role != core.RoleCandidate {
					t.Fatalf("setup: role %v after TimeoutNow, want a candidate", f.role)
				}
			}
			term := f.term
			rd := f.Ready()
			f.StorageDone(rd.Seq, f.log.lastIndex(), f.log.lastTerm())
			f.Step(now, msg(kind, 3, 2, 0))
			if f.term != term {
				t.Errorf("kind %#x of term 0 at a %v of term %d: the term is now %d", kind, map[bool]string{false: "candidate", true: "pre-candidate"}[pre], term, f.term)
			}
			if hs := f.Ready().HardState; hs != nil && hs.Term < term {
				t.Errorf("kind %#x of term 0 at a %v of term %d: a HardState of term %d is written", kind, map[bool]string{false: "candidate", true: "pre-candidate"}[pre], term, hs.Term)
			}
		}
	}
}
