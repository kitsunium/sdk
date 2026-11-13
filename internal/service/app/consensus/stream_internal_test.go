package consensus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The stream a node presents (#832), on the memory cluster of three nodes.

// applyHold blocks every Apply while held, so a proposal commits and is
// not applied: its Propose times out after the commit.
type applyHold struct {
	mu   sync.Mutex
	c    *sync.Cond
	held bool
}

func newApplyHold() *applyHold {
	h := &applyHold{}
	h.c = sync.NewCond(&h.mu)
	return h
}

func (h *applyHold) set(held bool) {
	h.mu.Lock()
	h.held = held
	h.mu.Unlock()
	h.c.Broadcast()
}

func (h *applyHold) wait() {
	h.mu.Lock()
	for h.held {
		h.c.Wait()
	}
	h.mu.Unlock()
}

// heldKV is a kv whose Apply waits while its hold is held.
type heldKV struct {
	*kv
	h *applyHold
}

func (k heldKV) Apply(e coreconsensus.EntryValue) any {
	k.h.wait()
	return k.kv.Apply(e)
}

func mustStream(t *testing.T, n *Node, clk clock.Clock) corestream.Stream {
	t.Helper()
	s, err := NewStream(n, StreamConfig{Clock: clk})
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	return s
}

// appendOnLeader appends records through a stream over whichever node leads
// now, again on a new leader when the one asked was certainly not appended to.
func appendOnLeader(t *testing.T, c *cluster, clk clock.Clock, records ...corestream.RecordValue) corestream.AppendValue {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		l := c.leader(5 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		got, err := mustStream(t, l.node, clk).Append(ctx, records)
		cancel()
		if err == nil {
			return got
		}
		if !errors.Is(err, coreconsensus.NotLeader) || time.Now().After(deadline) {
			t.Fatalf("Append on %d: %v", l.id, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAStreamOverANodeIsReadInOrderOnAFollowerAfterApply(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.settledLeader(5 * time.Second)
	clk := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	var want []string
	var last corestream.Offset
	for batch := range 3 {
		var records []corestream.RecordValue
		for i := range 4 {
			p := fmt.Sprintf("b%d-r%d", batch, i)
			want = append(want, p)
			records = append(records, corestream.RecordValue{Key: []byte("k"), Payload: []byte(p)})
		}
		got := appendOnLeader(t, c, clk, records...)
		if got.Partition != 0 || got.Last < got.First+3 || got.First <= last {
			t.Fatalf("batch %d: Append = %+v after %d", batch, got, last)
		}
		last = got.Last
		clk.Advance(time.Second)
	}
	c.waitApplied(uint64(last), 5*time.Second)
	f := c.otherThan(l)
	s := mustStream(t, f.node, nil)
	if s.Partitions() != 1 {
		t.Fatalf("Partitions = %d, want 1", s.Partitions())
	}
	b, err := s.Bounds(t.Context(), 0)
	if err != nil || b.First != 1 || b.Next <= last {
		t.Fatalf("Bounds = %+v, %v; want First 1 and Next past %d", b, err, last)
	}
	// Read three at a time: the follower's log also holds the founding,
	// no-op and settling entries, which are skipped.
	var got []corestream.EntryValue
	for from := b.First; ; {
		page, err := s.Read(t.Context(), 0, from, 3)
		if err != nil {
			t.Fatalf("Read(%d): %v", from, err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, page...)
		from = page[len(page)-1].Offset + 1
	}
	if len(got) != len(want) {
		t.Fatalf("read %d records, want %d", len(got), len(want))
	}
	for i, e := range got {
		if string(e.Record.Payload) != want[i] || string(e.Record.Key) != "k" || e.Partition != 0 {
			t.Fatalf("record %d = %+v, want payload %q", i, e, want[i])
		}
		if i > 0 && e.Offset <= got[i-1].Offset {
			t.Fatalf("record %d offset %d after %d", i, e.Offset, got[i-1].Offset)
		}
		if at := time.Unix(1_700_000_000+int64(i/4), 0); !e.AppendedAt.Equal(at) {
			t.Fatalf("record %d AppendedAt = %v, want %v, the leader's instant carried in the entry", i, e.AppendedAt, at)
		}
	}
	if page, err := s.Read(t.Context(), 0, b.Next, 10); err != nil || len(page) != 0 {
		t.Fatalf("Read(next) = %d entries, %v; want none and nil", len(page), err)
	}
	if _, err := s.Read(t.Context(), 0, b.Next+1, 10); !errs.HasCode(err, corestream.CodeOffsetAhead) {
		t.Fatalf("Read(next+1) = %v, want OFFSET_AHEAD", err)
	}
	if _, err := s.Read(t.Context(), 1, 0, 10); !errs.HasCode(err, corestream.CodeUnknownPartition) {
		t.Fatalf("Read(partition 1) = %v, want UNKNOWN_PARTITION", err)
	}
	if _, err := s.Read(t.Context(), 0, 1, 0); !errs.HasCode(err, corestream.CodeInvalidBatchSize) {
		t.Fatalf("Read(max 0) = %v, want INVALID_BATCH_SIZE", err)
	}
}

func TestAStreamReadBelowTheSnapshotIsOffsetTrimmed(t *testing.T) {
	c := newCluster(t, 3, 10)
	l := c.settledLeader(5 * time.Second)
	var last corestream.Offset
	for i := range 40 {
		last = appendOnLeader(t, c, nil, corestream.RecordValue{Payload: fmt.Appendf(nil, "r%d", i)}).Last
	}
	c.waitApplied(uint64(last), 5*time.Second)
	compactedPast(t, c, 5)
	f := c.otherThan(l)
	s := mustStream(t, f.node, nil)
	b, err := s.Bounds(t.Context(), 0)
	if err != nil || b.First <= 5 {
		t.Fatalf("Bounds = %+v, %v; want First past 5", b, err)
	}
	if _, err := s.Read(t.Context(), 0, 1, 10); !errs.HasCode(err, corestream.CodeOffsetTrimmed) {
		t.Fatalf("Read(1) below the snapshot = %v, want OFFSET_TRIMMED", err)
	}
	// compactedPast returns at the first snapshot past 5, and the follower
	// may still take the later ones: a First read before them is then below
	// the next, and OFFSET_TRIMMED is the answer the contract gives. The read
	// starts again at the First the stream reports then, until it holds.
	deadline := time.Now().Add(5 * time.Second)
	for {
		page, err := s.Read(t.Context(), 0, b.First, 100)
		if errs.HasCode(err, corestream.CodeOffsetTrimmed) && time.Now().Before(deadline) {
			if b, err = s.Bounds(t.Context(), 0); err != nil {
				t.Fatalf("Bounds after a compaction: %v", err)
			}
			continue
		}
		if err != nil || len(page) == 0 || page[len(page)-1].Offset != last {
			t.Fatalf("Read(First) = %d entries, %v; want the retained records up to %d", len(page), err, last)
		}
		return
	}
}

func TestATimedOutAppendCarriesTheTokenWaitAppliedResolves(t *testing.T) {
	t.Run("committed", func(t *testing.T) {
		h := newApplyHold()
		c := newClusterWith(t, &cluster{
			t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 3}, size: 3,
			configure: func(tn *testNode, cfg *Config) { cfg.Machine = heldKV{kv: tn.sm, h: h} },
		})
		t.Cleanup(func() { h.set(false) })
		l := c.settledLeader(5 * time.Second)
		s := mustStream(t, l.node, nil)
		h.set(true)
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		_, err := s.Append(ctx, []corestream.RecordValue{{Payload: []byte("held")}})
		cancel()
		if !errs.HasCode(err, corestream.CodeStreamBackendFailed) {
			t.Fatalf("Append with its apply held = %v, want STREAM_BACKEND_FAILED", err)
		}
		tok, ok := TokenOf(err)
		if !ok || tok.Index == 0 {
			t.Fatalf("TokenOf(%v) = %v, %v; want the appended token", err, tok, ok)
		}
		h.set(false)
		wctx, wcancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer wcancel()
		if err := l.node.WaitApplied(wctx, tok); err != nil {
			t.Fatalf("WaitApplied of a committed record = %v, want nil", err)
		}
		c.waitApplied(tok.Index, 5*time.Second)
		page, err := mustStream(t, c.otherThan(l).node, nil).Read(t.Context(), 0, corestream.Offset(tok.Index), 1)
		if err != nil || len(page) != 1 || string(page[0].Record.Payload) != "held" {
			t.Fatalf("Read(%d) on a follower = %+v, %v; want the record", tok.Index, page, err)
		}
	})
	t.Run("superseded", func(t *testing.T) {
		c := newCluster(t, 3, 0)
		l := c.settledLeader(5 * time.Second)
		s := mustStream(t, l.node, nil)
		c.net.Isolate(l.id, true)
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		_, err := s.Append(ctx, []corestream.RecordValue{{Payload: []byte("lost")}})
		cancel()
		if !errs.HasCode(err, corestream.CodeStreamBackendFailed) {
			t.Fatalf("Append on an isolated leader = %v, want STREAM_BACKEND_FAILED", err)
		}
		tok, ok := TokenOf(err)
		if !ok || tok.Index == 0 {
			t.Fatalf("TokenOf(%v) = %v, %v; want the appended token", err, tok, ok)
		}
		var nl *testNode
		deadline := time.Now().Add(10 * time.Second)
		for nl == nil || nl == l {
			if time.Now().After(deadline) {
				t.Fatal("no new leader beside the isolated one")
			}
			nl = c.leader(5 * time.Second)
			time.Sleep(5 * time.Millisecond)
		}
		propose(t, nl.node, "after=1")
		wctx, wcancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer wcancel()
		if err := nl.node.WaitApplied(wctx, tok); !errs.HasCode(err, coreconsensus.CodeEntrySuperseded) {
			t.Fatalf("WaitApplied of a lost record = %v, want ENTRY_SUPERSEDED", err)
		}
		c.net.Isolate(l.id, false)
	})
}

func TestNewStreamRefusesAMisconfiguration(t *testing.T) {
	c := newCluster(t, 1, 0)
	n := c.nodes[0].node
	for _, cfg := range []StreamConfig{{MaxRecordBytes: -1}, {MaxRecordBytes: n.cfg.MaxEntryBytes}} {
		if _, err := NewStream(n, cfg); !errs.HasCode(err, corestream.CodeStreamMisconfigured) {
			t.Fatalf("NewStream(%+v) = %v, want STREAM_MISCONFIGURED", cfg, err)
		}
	}
	if _, err := NewStream(nil, StreamConfig{}); !errs.HasCode(err, corestream.CodeStreamMisconfigured) {
		t.Fatalf("NewStream(nil) = %v, want STREAM_MISCONFIGURED", err)
	}
	s, err := NewStream(n, StreamConfig{MaxRecordBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), nil); !errs.HasCode(err, corestream.CodeInvalidBatchSize) {
		t.Fatalf("Append(nil) = %v, want INVALID_BATCH_SIZE", err)
	}
	if _, err := s.Append(t.Context(), []corestream.RecordValue{{Key: []byte("ab"), Payload: []byte("cde")}}); !errs.HasCode(err, corestream.CodeRecordTooLarge) {
		t.Fatalf("Append(5 bytes) = %v, want RECORD_TOO_LARGE", err)
	}
}

func TestAStreamWakesOnTheNextApply(t *testing.T) {
	c := newCluster(t, 3, 0)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	w, ok := mustStream(t, f.node, nil).(corestream.Waker)
	if !ok {
		t.Fatal("the stream is not a Waker")
	}
	wake := w.Wake()
	appendOnLeader(t, c, nil, corestream.RecordValue{Payload: []byte("x")})
	select {
	case <-wake:
	case <-time.After(5 * time.Second):
		t.Fatal("no wake after a record was applied on the follower")
	}
}
