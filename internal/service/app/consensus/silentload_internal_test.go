package consensus

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// #766, on disk: the victim restarts under the leader of its SilentTerm,
// which commits a steady stream of writes with the mended lagger. The
// victim acknowledges no append, so it gains one batch per heartbeat
// answer (#636): 4 KiB every 50 ms here, two of these 1.5 KiB writes. A
// silence that ended only once one append carried the victim to the commit
// that same append carried chased the leader's moving commit, and under
// writes faster than one batch per heartbeat it never ended while that
// leader led: the cluster ran one voter short, one failure from having no
// leader. Four writers, each at most one write every 10 ms, outrun it by
// far. The victim must end its silence while they write, under the same
// leader, holding every write it acknowledged before the damage.
func TestASilentNodeRejoinsUnderItsLeaderDuringSustainedWrites(t *testing.T) {
	s := silenceUnderItsLeaderOnDisk(t)
	s.trs[s.lagger.id].setDrop(nil)
	term := s.lead.node.Status().Term
	waitAppliedOnly(t, s.lagger, propose(t, s.lead.node, "mend=1").Index)
	s.start(t, s.victim)
	s.c.net.Isolate(s.victim.id, false)
	// The writes start once the first batch past the truncation point
	// reached the victim: the leader's probe follows its reports.
	for deadline := time.Now().Add(10 * time.Second); ; {
		if _, last := s.victim.dd.Log().Bounds(); last > s.hint {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no batch reached the victim (status %+v)", s.victim.node.Status())
		}
		time.Sleep(2 * time.Millisecond)
	}
	silent := func() bool {
		_, err := os.Stat(filepath.Join(s.victim.dir, fileSilent))
		return err == nil
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var written atomic.Int64
	for w := range 4 {
		wg.Go(func() {
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				case <-tick.C:
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, _, err := s.lead.node.Propose(ctx, fmt.Appendf(nil, "w%d-%05d=%s", w, i, strings.Repeat("x", 1500)))
				cancel()
				if err == nil {
					written.Add(1)
				}
			}
		})
	}
	const window = 5 * time.Second
	start := time.Now()
	for silent() && time.Since(start) < window {
		time.Sleep(10 * time.Millisecond)
	}
	ended, took := !silent(), time.Since(start)
	st, vc := s.lead.node.Status(), s.victim.node.Status().Commit
	close(stop)
	wg.Wait()
	if st.Role != coreconsensus.RoleLeader || st.Term != term {
		t.Skipf("the leadership moved during the writes (%+v)", st)
	}
	if !ended {
		t.Fatalf("the victim stayed silent through %v of writes under the leader of its SilentTerm %d: %d writes committed, leader commit %d, victim commit %d",
			window, term, written.Load(), st.Commit, vc)
	}
	t.Logf("the victim ended its silence %v into the writes (%d committed by then), at commit %d, the leader at %d",
		took.Round(time.Millisecond), written.Load(), vc, st.Commit)
	waitAppliedOnly(t, s.victim, s.acked["ack29"])
	if lost := s.lost(s.victim); len(lost) > 0 {
		t.Fatalf("the victim ended its silence without %d acknowledged writes, first %s", len(lost), lost[0])
	}
}
