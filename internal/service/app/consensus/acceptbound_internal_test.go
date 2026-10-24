package consensus

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
)

// stuckSyncSnapshots is a follower's snapshot store over a disk whose fsync
// hangs once armed. It keeps the two orderings of the shipped store that
// matter here: fileSink.Commit holds the sink's mu across f.Sync
// (filesnap.go Commit), and a Create of an id whose Commit is in progress
// waits for that Commit, through claim then supersede, which takes the old
// sink's mu (filesnap.go Create, supersede).
type stuckSyncSnapshots struct {
	*MemorySnapshots
	armed   atomic.Bool
	release chan struct{}
	mu      sync.Mutex
	syncing map[string]chan struct{}
	commits atomic.Int32
	waited  atomic.Int32
}

func newStuckSyncSnapshots(s *MemorySnapshots) *stuckSyncSnapshots {
	return &stuckSyncSnapshots{
		MemorySnapshots: s, release: make(chan struct{}),
		syncing: map[string]chan struct{}{},
	}
}

func (s *stuckSyncSnapshots) Create(id string, meta coreconsensus.SnapshotMetaValue) (coreconsensus.SnapshotSink, int64, error) {
	s.mu.Lock()
	ch := s.syncing[id]
	s.mu.Unlock()
	if ch != nil {
		s.waited.Add(1)
		<-ch
	}
	sink, off, err := s.MemorySnapshots.Create(id, meta)
	if err != nil {
		return nil, 0, err
	}
	return &stuckSyncSink{SnapshotSink: sink, s: s, id: id}, off, nil
}

type stuckSyncSink struct {
	coreconsensus.SnapshotSink
	s  *stuckSyncSnapshots
	id string
}

func (k *stuckSyncSink) Commit(final coreconsensus.SnapshotMetaValue) error {
	if !k.s.armed.Load() {
		return k.SnapshotSink.Commit(final)
	}
	ch := make(chan struct{})
	k.s.mu.Lock()
	k.s.syncing[k.id] = ch
	k.s.mu.Unlock()
	k.s.commits.Add(1)
	<-k.s.release // the spool's fsync does not return
	err := k.SnapshotSink.Commit(final)
	k.s.mu.Lock()
	delete(k.s.syncing, k.id)
	k.s.mu.Unlock()
	close(ch)
	return err
}

// hungCreate is a snapshot store whose Create waits for release once
// armed: the receiver's loop stands still in its judgment of an offer,
// whatever holds it there, and the offer is never answered.
type hungCreate struct {
	*MemorySnapshots
	armed   atomic.Bool
	release chan struct{}
	waiting atomic.Int32
}

func (s *hungCreate) Create(id string, meta coreconsensus.SnapshotMetaValue) (coreconsensus.SnapshotSink, int64, error) {
	if s.armed.Load() {
		s.waiting.Add(1)
		<-s.release
	}
	return s.MemorySnapshots.Create(id, meta)
}

// A receiver offered a snapshot whose install of the same id is in its
// Commit — the leader's stream fell due while the spool's fsync hung, and
// the leader offered it again — judged the offer on its loop, where Create
// waited for that Commit: the loop stood still, the offer was never
// answered, and the leader's pin on it held its compaction for as long as
// it led (#623). The install counts as restoring from before its Commit,
// and the offer is refused OVERLOADED at once. The offers and the install
// are the transport's calls, made by the test, so the follower is offered
// the snapshot whatever its log: restarted behind a compacted leader, it
// was caught up by appends from the leader's recent entries on a loaded
// runner, and the Commit that hung was its own cut's.
func TestAnOfferOfTheSnapshotBeingCommittedIsRefused(t *testing.T) {
	stores := map[coreconsensus.NodeID]*stuckSyncSnapshots{}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 8}, every: 1 << 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		if stores[tn.id] == nil {
			stores[tn.id] = newStuckSyncSnapshots(tn.snaps)
		}
		cfg.Snapshots = stores[tn.id]
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	stuck := stores[f.id]
	var once sync.Once
	release := func() { once.Do(func() { close(stuck.release) }) }
	t.Cleanup(release)
	waitFor(t, "the follower following the leader", func() bool {
		st, ls := f.node.Status(), l.node.Status()
		return st.Leader == l.id && st.Term == ls.Term && ls.Role == coreconsensus.RoleLeader
	})
	st := f.node.Status()
	// An offer past the follower's commit, as the leader sends a lagging
	// follower its newest snapshot.
	idx, term := st.Commit+1000, st.Term
	const size = 4096
	meta := coreconsensus.SnapshotMetaValue{
		ID: snapshotID(idx, term), Version: 1, Position: coreconsensus.PositionValue{Term: term, Index: idx},
		Log: f.node.lineage(), ChunkSize: defaultChunkSize, EncodedSize: size, DecodedSize: size,
	}
	offer := coreconsensus.InstallOfferValue{Term: term, SnapshotID: meta.ID, Meta: meta}
	from := coreconsensus.AuthenticatedPeerValue{ID: l.id}
	if acc := f.node.offer(from, offer); acc.Refused != 0 || acc.Held || acc.Offset != 0 {
		t.Fatalf("setup: the first offer was answered %+v", acc)
	}
	stuck.armed.Store(true)
	installed := make(chan error, 1)
	go func() {
		installed <- f.node.install(coreconsensus.InstallParams{Ctx: context.Background(), From: from, Offer: offer, Body: bytes.NewReader(make([]byte, size))})
	}()
	waitFor(t, "the follower's install stuck in its spool's fsync", func() bool { return stuck.commits.Load() > 0 })
	answered := make(chan coreconsensus.InstallAcceptValue, 1)
	go func() { answered <- f.node.offer(from, offer) }()
	select {
	case acc := <-answered:
		if acc.Refused != coreconsensus.CodeOverloaded {
			t.Errorf("an offer of the snapshot the follower is committing was answered %+v, not refused OVERLOADED", acc)
		}
	case <-time.After(2 * time.Second):
		t.Errorf("an offer of the snapshot the follower is committing is still unanswered 2 s later: its loop waits in Create for that Commit (Create waits %d)", stuck.waited.Load())
		release()
		<-answered
	}
	release()
	t.Logf("the first install returned %v (its body is no snapshot)", <-installed)
}

// #602's scenario over TCP, the issue's proof of #623: a follower F is sent
// every byte of a snapshot and its install hangs in the spool's fsync. The
// #602 bound cancels the stream; raft probes F again and the same snapshot
// is offered again. F's loop judged that offer, its Create of the same id
// waited for the Commit still in its fsync, the offer was never answered,
// and the new stream's pin, no byte read, held the leader's compaction for
// as long as it led: its log grew to the NO_SPACE alarm. F refuses that
// offer OVERLOADED now, and the leader compacts again.
func TestAReofferToAFollowerStuckInItsCommitDoesNotPinCompaction(t *testing.T) {
	tweak := func(c *Config) {
		c.SnapshotEvery, c.SnapshotTrailing = 20, 4
		c.SnapshotEveryBytes, c.LogReserveBytes = 1<<20, 1<<20
	}
	ca, cid, ns := newTCPClusterWith(t, tweak)
	proposeOnTCPLeader(t, ns, "settle=1")
	l := tcpLeader(t, ns)
	var f *tcpNode
	for _, tn := range ns {
		if tn != l {
			f = tn
			break
		}
	}
	stuck := newStuckSyncSnapshots(f.snaps)
	var once sync.Once
	release := func() { once.Do(func() { close(stuck.release) }) }
	// Registered after the cluster's cleanup, so it runs first.
	t.Cleanup(release)

	f.stop(t)
	for i := range 100 {
		proposeOnTCPLeader(t, ns, fmt.Sprintf("a%03d=v", i))
	}
	l = tcpLeader(t, ns)
	waitFor(t, "the leader compacted past the stopped follower", func() bool {
		first, _ := l.log.Bounds()
		_, fl := f.log.Bounds()
		return first > fl+1
	})
	stuck.armed.Store(true)
	f.tweak = func(c *Config) { tweak(c); c.Snapshots = stuck }
	f.listen(t, ca)
	f.start(t, cid)

	waitFor(t, "the follower's install stuck in its spool's fsync", func() bool { return stuck.commits.Load() > 0 })
	var first *atomic.Int64
	waitFor(t, "the leader's stream read the last byte", func() bool {
		l.node.exec(func() {
			if pin, ok := l.node.sending[f.id]; ok && pin.until.IsZero() && pin.sent != nil && pin.sent.Load() != 0 {
				first = pin.sent
			}
		})
		return first != nil
	})
	waitFor(t, "the first stream's pin ended at its verdict bound", func() bool {
		ended := false
		l.node.exec(func() {
			pin, ok := l.node.sending[f.id]
			ended = !ok || pin.sent != first
		})
		return ended
	})
	st := l.node.Status()
	for i := range 300 {
		propose(t, l.node, fmt.Sprintf("b%03d=v", i))
	}
	bound := time.Duration(pinAfterStream) * testTimingFast().ElectionTimeoutMax
	wait := 3 * max(bound, 2*time.Second)
	for deadline := time.Now().Add(wait); ; time.Sleep(10 * time.Millisecond) {
		first, last := l.log.Bounds()
		if last-first <= 100 {
			break
		}
		if time.Now().After(deadline) {
			st2 := l.node.Status()
			if st2.Role != coreconsensus.RoleLeader || st2.Term != st.Term {
				t.Fatalf("setup: leadership moved during the measure (term %d -> %d, role %v)", st.Term, st2.Term, st2.Role)
			}
			var pin sendPin
			l.node.exec(func() { pin = l.node.sending[f.id] })
			t.Fatalf("%v after 300 more writes, the leader's log is [%d,%d]: its pin for the follower stuck in its Commit holds index %d, its offer unanswered; Create waited %d times",
				wait, first, last, pin.index, stuck.waited.Load())
		}
	}
	if n := stuck.waited.Load(); n != 0 {
		t.Errorf("the follower's loop waited %d times in Create for the Commit of the same id", n)
	}
}

// The pin of a stream whose offer is never answered — the receiver's loop
// stands still in its judgment, whatever holds it — held the leader's
// compaction with no bound: the #602 bound ran from the last byte only, and
// a stream that had read none had none (#623). The stream is cancelled once
// it has not moved for its peer's pinBound, from its offer on, and the
// leader compacts again. Over TCP, the transport's own deadline on the
// accept (JoinTimeout, 10 s here) is not what ends it.
func TestAnOfferNeverAcceptedDoesNotPinCompaction(t *testing.T) {
	tweak := func(c *Config) {
		c.SnapshotEvery, c.SnapshotTrailing = 20, 4
		c.SnapshotEveryBytes, c.LogReserveBytes = 1<<20, 1<<20
	}
	ca, cid, ns := newTCPClusterWith(t, tweak)
	proposeOnTCPLeader(t, ns, "settle=1")
	l := tcpLeader(t, ns)
	var f *tcpNode
	for _, tn := range ns {
		if tn != l {
			f = tn
			break
		}
	}
	hung := &hungCreate{MemorySnapshots: f.snaps, release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(hung.release) }) }
	t.Cleanup(release)

	f.stop(t)
	for i := range 100 {
		proposeOnTCPLeader(t, ns, fmt.Sprintf("a%03d=v", i))
	}
	l = tcpLeader(t, ns)
	waitFor(t, "the leader compacted past the stopped follower", func() bool {
		first, _ := l.log.Bounds()
		_, fl := f.log.Bounds()
		return first > fl+1
	})
	hung.armed.Store(true)
	f.tweak = func(c *Config) { tweak(c); c.Snapshots = hung }
	f.listen(t, ca)
	f.start(t, cid)
	waitFor(t, "the follower's loop held in its judgment of the offer", func() bool { return hung.waiting.Load() > 0 })
	st := l.node.Status()
	for i := range 300 {
		propose(t, l.node, fmt.Sprintf("b%03d=v", i))
	}
	bound := time.Duration(pinAfterStream) * testTimingFast().ElectionTimeoutMax
	wait := 3 * max(bound, 2*time.Second)
	for deadline := time.Now().Add(wait); ; time.Sleep(10 * time.Millisecond) {
		first, last := l.log.Bounds()
		if last-first <= 100 {
			return
		}
		if time.Now().After(deadline) {
			st2 := l.node.Status()
			if st2.Role != coreconsensus.RoleLeader || st2.Term != st.Term {
				t.Fatalf("setup: leadership moved during the measure (term %d -> %d, role %v)", st.Term, st2.Term, st2.Role)
			}
			var pin sendPin
			sent := int64(-1)
			l.node.exec(func() { pin = l.node.sending[f.id] })
			if pin.sent != nil {
				sent = pin.sent.Load()
			}
			t.Fatalf("%v after 300 more writes (3x the %v bound), the leader's log is [%d,%d]: its pin for the follower holds index %d, no byte read (sent %d), the offer unanswered",
				wait, bound, first, last, pin.index, sent)
		}
	}
}

// A peer removed while a snapshot streamed to it kept the stream and its
// pin: pinAt read no Progress for a stream's pin, so only the stream's end
// released the leader's compaction (#623). The removal cancels the stream
// at the next tick, long before its bound.
func TestRemovingAPeerCancelsItsSnapshotStream(t *testing.T) {
	trs := map[coreconsensus.NodeID]*holdTransport{}
	var mu sync.Mutex
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{1, 2, 9}, every: 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		ht := &holdTransport{
			MemoryTransport: cfg.Transport.(*MemoryTransport),
			started:         make(chan coreconsensus.InstallOfferValue), release: make(chan struct{}),
		}
		if old := trs[tn.id]; old != nil {
			ht.started, ht.release = old.started, old.release
			ht.holding.Store(old.holding.Load())
		}
		trs[tn.id] = ht
		cfg.Transport = ht
		// The bound runs ten seconds from the offer.
		cfg.InitialTiming = steadyTiming()
	}
	newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	lag := c.otherThan(l)
	c.kill(lag)
	for i := range 200 {
		propose(t, l.node, fmt.Sprintf("a%d=v", i))
	}
	compactedPastItsEnd(t, l, lag)
	mu.Lock()
	lt := trs[l.id]
	mu.Unlock()
	lt.holding.Store(true)
	defer lt.holding.Store(false)
	hearOnlyTheLeader(c, l, lag)
	c.start(lag)
	select {
	case <-lt.started:
	case <-time.After(10 * time.Second):
		t.Fatal("setup: no snapshot offered to the lagging node")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.node.Remove(ctx, "op:test", lag.id); err != nil {
		t.Fatalf("remove the lagging node: %v", err)
	}
	for deadline := time.Now().Add(2 * time.Second); lt.cancelled.Load() == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the snapshot stream to a removed peer still runs 2 s after its removal")
		}
	}
	pinned := true
	waitFor(t, "the removed peer's pin dropped", func() bool {
		l.node.exec(func() { _, pinned = l.node.sending[lag.id] })
		return !pinned
	})
}

// eofHookReader reads r, and runs atEOF once, before it answers io.EOF: by
// then io.Copy wrote every byte r held, and nothing holds the sink's lock.
type eofHookReader struct {
	r     io.Reader
	once  sync.Once
	atEOF func()
}

func (b *eofHookReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		b.once.Do(b.atEOF)
	}
	return n, err
}

// The same defect over the shipped store and a disk only slow (#623): an
// offer of the same id that arrived while the install was still inside
// fileSink.Commit — which holds the sink's mutex across the spool's fsync,
// its rename and the .meta's write — was not refused, since the install
// counted as restoring only once Commit returned. Its Create superseded the
// committing sink and waited for that mutex on the event loop, then opened
// a fresh spool at offset 0: the whole snapshot was streamed a second time,
// and on Windows its rename over the .snap a restore holds open stopped the
// node STORAGE_FAILED. The test holds the sink's mutex in Commit's place,
// which is what Commit does for its whole length; the offer is refused
// OVERLOADED, and the loop goes on.
func TestAReofferDuringTheCommitIsRefusedWithoutHoldingTheLoop(t *testing.T) {
	stores := map[*testNode]*fileSnapshots{}
	c := &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{9, 9}, every: 1 << 20, size: 3}
	c.configure = func(tn *testNode, cfg *Config) {
		s := stores[tn]
		if s == nil {
			var err error
			if s, err = newFileSnapshots(filepath.Join(t.TempDir(), "snap")); err != nil {
				t.Fatal(err)
			}
			stores[tn] = s
		}
		cfg.Snapshots = s
	}
	c = newClusterWith(t, c)
	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	waitFor(t, "the follower following the leader", func() bool {
		st, ls := f.node.Status(), l.node.Status()
		return st.Leader == l.id && st.Term == ls.Term && ls.Role == coreconsensus.RoleLeader
	})
	st := f.node.Status()
	// An offer past the follower's commit, as the leader sends a lagging
	// follower its newest snapshot.
	idx, term := st.Commit+1000, st.Term
	const size = 4096
	meta := coreconsensus.SnapshotMetaValue{
		ID: snapshotID(idx, term), Version: 1, Position: coreconsensus.PositionValue{Term: term, Index: idx},
		Log: f.node.lineage(), ChunkSize: defaultChunkSize, EncodedSize: size, DecodedSize: size,
	}
	offer := coreconsensus.InstallOfferValue{Term: term, SnapshotID: meta.ID, Meta: meta}
	from := coreconsensus.AuthenticatedPeerValue{ID: l.id}
	if acc := f.node.offer(from, offer); acc.Refused != 0 || acc.Held || acc.Offset != 0 {
		t.Fatalf("first offer answered %+v", acc)
	}
	f.node.snapState.mu.Lock()
	sink1, _ := f.node.snapState.spools[meta.ID].(*fileSink)
	f.node.snapState.mu.Unlock()
	if sink1 == nil {
		t.Fatal("no file sink kept for the accepted offer")
	}
	copied := make(chan struct{})
	var held atomic.Bool
	body := &eofHookReader{r: bytes.NewReader(make([]byte, size)), atEOF: func() {
		// Every byte is in the spool: from here the install runs
		// stillFresh, then Commit, which waits for this mutex as a second
		// Create would wait for a Commit in its fsync.
		sink1.mu.Lock()
		held.Store(true)
		close(copied)
	}}
	installed := make(chan error, 1)
	go func() {
		installed <- f.node.install(coreconsensus.InstallParams{Ctx: context.Background(), From: from, Offer: offer, Body: body})
	}()
	<-copied
	time.Sleep(200 * time.Millisecond) // stillFresh's turn on the loop, then Commit queues on the mutex

	// The leader's pin fell due during that Commit: it offers again.
	reoffer := make(chan coreconsensus.InstallAcceptValue, 1)
	go func() { reoffer <- f.node.offer(from, offer) }()
	time.Sleep(100 * time.Millisecond)
	looped := make(chan struct{})
	go func() {
		f.node.exec(func() {})
		close(looped)
	}()
	loopHeld := false
	select {
	case <-looped:
	case <-time.After(time.Second):
		loopHeld = true
	}
	if held.Load() {
		sink1.mu.Unlock()
	}
	acc2 := <-reoffer
	ierr := <-installed
	<-looped
	t.Logf("second offer of the id being committed answered %+v; the first install returned %v (its body is no snapshot)", acc2, ierr)
	if loopHeld {
		t.Errorf("the follower's event loop waited for another install's Commit (D5b)")
	}
	if acc2.Refused != coreconsensus.CodeOverloaded {
		t.Errorf("an offer of the snapshot being committed was answered %+v: accepted at offset %d, a whole second stream, instead of refused OVERLOADED", acc2, acc2.Offset)
	}
}
