package consensus

import (
	"fmt"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// A follower sent every byte of a snapshot whose install never ends — a
// stuck disk under its spool's sync, a stuck Restore, an apply goroutine
// held — kept the leader's stream waiting for its verdict, and the stream's
// pin, which had no end while it streamed, held the leader's compaction at
// the snapshot for good: the log grew to the NO_SPACE alarm (#602). The pin
// now ends pinAfterStream maximum election timeouts after the last byte
// was read, and the stream is cancelled with it.
func TestAFollowerStuckInItsInstallDoesNotPinCompaction(t *testing.T) {
	c := newCluster(t, 3, 20)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	c.net.Isolate(f.id, true)
	for i := range 100 {
		propose(t, l.node, fmt.Sprintf("a%03d=v", i))
	}
	waitFor(t, "the leader compacted past the isolated follower", func() bool {
		first, _ := l.log.Bounds()
		_, fl := f.log.Bounds()
		return first > fl+1
	})
	// The follower's machine hangs: its apply goroutine stops at the
	// restore of the snapshot it is sent.
	f.sm.mu.Lock()
	released := false
	release := func() {
		if !released {
			released = true
			f.sm.mu.Unlock()
		}
	}
	defer release()
	c.net.Isolate(f.id, false)
	waitFor(t, "a snapshot streamed to the follower", func() bool {
		streaming := false
		l.node.exec(func() {
			pin, ok := l.node.sending[f.id]
			streaming = ok && pin.until.IsZero() && pin.sent != nil && pin.sent.Load() != 0
		})
		return streaming
	})
	firstBefore, _ := l.log.Bounds()
	for i := range 400 {
		propose(t, l.node, fmt.Sprintf("b%03d=v", i))
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		first, last := l.log.Bounds()
		if last-first <= 200 {
			break
		}
		if time.Now().After(deadline) {
			l.node.exec(func() { t.Logf("leader pins: %+v", l.node.sending) })
			t.Fatalf("one follower stuck in its install's restore pins the leader's compaction with no bound: log [%d,%d], first was %d before 400 more writes", first, last, firstBefore)
		}
		time.Sleep(10 * time.Millisecond)
	}
	release()
	c.waitApplied(l.node.Status().Applied, 10*time.Second)
}

// A receiver offered again the snapshot it is restoring — the leader's pin
// on it fell due and the leader probed again — opened a spool over the
// published id, was streamed the whole snapshot a second time, committed it
// over the file its restore reads, and restored it once more (#610). It
// refuses that offer OVERLOADED now.
func TestAnOfferOfTheSnapshotBeingRestoredIsRefused(t *testing.T) {
	c := newCluster(t, 3, 20)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	c.net.Isolate(f.id, true)
	for i := range 100 {
		propose(t, l.node, fmt.Sprintf("a%03d=v", i))
	}
	waitFor(t, "the leader compacted past the isolated follower", func() bool {
		first, _ := l.log.Bounds()
		_, fl := f.log.Bounds()
		return first > fl+1
	})
	f.sm.mu.Lock()
	released := false
	release := func() {
		if !released {
			released = true
			f.sm.mu.Unlock()
		}
	}
	defer release()
	c.net.Isolate(f.id, false)
	var id string
	waitFor(t, "the follower restoring a snapshot", func() bool {
		f.node.snapState.mu.Lock()
		defer f.node.snapState.mu.Unlock()
		for k := range f.node.snapState.restoring {
			id = k
		}
		return id != ""
	})
	h, err := f.snaps.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	meta := h.Meta()
	_ = h.Close()
	acc := f.node.offer(coreconsensus.AuthenticatedPeerValue{ID: l.id},
		coreconsensus.InstallOfferValue{Term: l.node.Status().Term, SnapshotID: id, Meta: meta})
	if acc.Refused != coreconsensus.CodeOverloaded {
		t.Errorf("an offer of the snapshot the follower is restoring was answered %+v, not refused OVERLOADED", acc)
	}
	release()
	c.waitApplied(l.node.Status().Applied, 10*time.Second)
}
