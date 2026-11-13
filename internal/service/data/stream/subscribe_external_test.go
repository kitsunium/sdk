package stream_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	"github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svclock "github.com/kitsunium/sdk/internal/service/app/lock"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
)

// subscribeWait bounds every wait of this file for a subscriber to catch up.
const subscribeWait = 20 * time.Second

// quickRetry retries a refused batch within a millisecond, so a case about
// a failing handler takes no time.
var quickRetry = backoff.Value{BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}

// delivery is one record a handler saw: its partition, offset and payload.
type delivery struct {
	payload   string
	partition int
	offset    corestream.Offset
}

// recorder keeps what a subscriber delivered and wakes a waiter on each batch.
type recorder struct {
	seen    chan struct{}
	refuse  func(entries []corestream.EntryValue) error
	got     []delivery
	mu      sync.Mutex
	batches atomic.Int64
}

// newRecorder returns a recorder whose handler accepts everything unless
// refuse says otherwise.
func newRecorder(refuse func([]corestream.EntryValue) error) *recorder {
	return &recorder{seen: make(chan struct{}, 1), refuse: refuse}
}

// handle is the recorder's Handler.
func (r *recorder) handle(_ context.Context, entries []corestream.EntryValue) error {
	r.batches.Add(1)
	defer r.wake()
	if r.refuse != nil {
		if err := r.refuse(entries); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range entries {
		r.got = append(r.got, delivery{payload: string(e.Record.Payload), partition: e.Partition, offset: e.Offset})
	}
	return nil
}

// wake signals a waiter without blocking.
func (r *recorder) wake() {
	select {
	case r.seen <- struct{}{}:
	default:
	}
}

// deliveries returns a copy of what was accepted so far.
func (r *recorder) deliveries() []delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

// waitFor waits until done holds over the deliveries, failing the test past
// subscribeWait.
func (r *recorder) waitFor(t *testing.T, what string, done func([]delivery) bool) {
	t.Helper()
	deadline := time.After(subscribeWait)
	for !done(r.deliveries()) {
		select {
		case <-r.seen:
		case <-time.After(5 * time.Millisecond):
		case <-deadline:
			t.Fatalf("waiting for %s: got %v", what, r.deliveries())
		}
	}
}

// subscription is a running Subscribe and the way to stop it.
type subscription struct {
	cancel context.CancelFunc
	done   chan error
}

// subscribe starts Subscribe on s with cfg, on the system clock unless cfg
// names one, and stops it at the end of the test.
func subscribe(t *testing.T, s corestream.Stream, cfg svcstream.SubscriberConfig) *subscription {
	t.Helper()
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	sub := &subscription{cancel: cancel, done: make(chan error, 1)}
	cfg.HandlerIsIdempotent = true
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 10 * time.Millisecond
	}
	go func() { sub.done <- svcstream.Subscribe(ctx, s, cfg) }()
	t.Cleanup(func() { sub.stop(t) })
	return sub
}

// stop cancels the subscription and returns what Subscribe returned.
func (s *subscription) stop(t *testing.T) error {
	t.Helper()
	s.cancel()
	select {
	case err, ok := <-s.done:
		if ok {
			close(s.done)
		}
		return err
	case <-time.After(subscribeWait):
		t.Fatal("Subscribe did not return after its context ended")
		return nil
	}
}

// wait returns what Subscribe returned on its own, failing past
// subscribeWait.
func (s *subscription) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-s.done:
		close(s.done)
		return err
	case <-time.After(subscribeWait):
		t.Fatal("Subscribe did not return")
		return nil
	}
}

// payloads returns the payloads of ds in order.
func payloads(ds []delivery) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.payload
	}
	return out
}

// TestSubscribeTwoGroupsEachReceiveEveryRecordOnce: two groups over one
// stream read every record once each, a group's position independent of the
// other's — the second group starts after the first has finished and still
// reads everything, and resumes past what it committed.
func TestSubscribeTwoGroupsEachReceiveEveryRecordOnce(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{Partitions: 2}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		for i := range 6 {
			appendOK(t, s, keyed("k"+strconv.Itoa(i%3), "r"+strconv.Itoa(i)))
		}
		first := newRecorder(nil)
		subscribe(t, s, svcstream.SubscriberConfig{Group: "billing", Handler: first.handle, BatchSize: 4})
		first.waitFor(t, "billing's six records", func(ds []delivery) bool { return len(ds) >= 6 })
		second := newRecorder(nil)
		late := subscribe(t, s, svcstream.SubscriberConfig{Group: "audit", Handler: second.handle})
		second.waitFor(t, "audit's six records", func(ds []delivery) bool { return len(ds) >= 6 })
		if err := late.stop(t); err != nil {
			t.Fatalf("Subscribe(audit) = %v, want nil", err)
		}
		appendOK(t, s, keyed("k0", "r6"))
		first.waitFor(t, "billing's seventh record", func(ds []delivery) bool { return len(ds) >= 7 })
		resumed := newRecorder(nil)
		subscribe(t, s, svcstream.SubscriberConfig{Group: "audit", Handler: resumed.handle})
		resumed.waitFor(t, "audit's resumed record", func(ds []delivery) bool { return len(ds) >= 1 })
		for name, got := range map[string][]string{
			"billing": payloads(first.deliveries()), "audit": append(payloads(second.deliveries()), payloads(resumed.deliveries())...),
		} {
			slices.Sort(got)
			want := []string{"r0", "r1", "r2", "r3", "r4", "r5", "r6"}
			if !slices.Equal(got, want) {
				t.Fatalf("group %s received %v, want each of %v once", name, got, want)
			}
		}
	})
}

// TestSubscribeKeepsEachKeysOrderAndBlocksOnlyTheFailingPartition: four
// partitions, four workers; every key is delivered in Append order, and a
// handler refusing partition 0 holds partition 0 alone until it accepts.
func TestSubscribeKeepsEachKeysOrderAndBlocksOnlyTheFailingPartition(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{Partitions: 4}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		keys := make([]string, 4)
		for p := range keys {
			keys[p] = keyOn(t, p, 4)
		}
		for i := range 5 {
			for p, key := range keys {
				appendOK(t, s, keyed(key, strconv.Itoa(p)+"-"+strconv.Itoa(i)))
			}
		}
		var healed atomic.Bool
		rec := newRecorder(func(entries []corestream.EntryValue) error {
			if entries[0].Partition == 0 && !healed.Load() {
				return errors.New("partition 0 is down")
			}
			return nil
		})
		subscribe(t, s, svcstream.SubscriberConfig{Group: "orders", Handler: rec.handle, BatchSize: 2, Retry: quickRetry})
		rec.waitFor(t, "partitions 1-3", func(ds []delivery) bool { return len(ds) >= 15 })
		for _, d := range rec.deliveries() {
			if d.partition == 0 {
				t.Fatalf("partition 0 delivered %v while its handler refused", d)
			}
		}
		healed.Store(true)
		rec.waitFor(t, "partition 0 once healed", func(ds []delivery) bool { return len(ds) >= 20 })
		perPartition := map[int][]string{}
		for _, d := range rec.deliveries() {
			perPartition[d.partition] = append(perPartition[d.partition], d.payload)
		}
		for p := range 4 {
			want := make([]string, 5)
			for i := range want {
				want[i] = strconv.Itoa(p) + "-" + strconv.Itoa(i)
			}
			if !slices.Equal(perPartition[p], want) {
				t.Fatalf("partition %d delivered %v, want %v in Append order", p, perPartition[p], want)
			}
		}
	})
}

// commitFails is a stream whose next Commit fails: the crash between the
// handler's return and the commit, as the engine sees it.
type commitFails struct {
	corestream.Stream
	committer corestream.Committer
	waker     corestream.Waker
	armed     atomic.Bool
}

// errCrash is the commit the crash cut short.
var errCrash = errors.New("the process died before the commit")

// Commit fails once armed, then forwards.
func (c *commitFails) Commit(ctx context.Context, group string, partition int, next corestream.Offset) error {
	if c.armed.CompareAndSwap(true, false) {
		return errCrash
	}
	return c.committer.Commit(ctx, group, partition, next)
}

// Committed forwards.
func (c *commitFails) Committed(ctx context.Context, group string, partition int) (corestream.Offset, error) {
	return c.committer.Committed(ctx, group, partition)
}

// Wake forwards.
func (c *commitFails) Wake() <-chan struct{} { return c.waker.Wake() }

// TestSubscribeCommitAfterHandlerRedeliversAfterACrashBeforeTheCommit: on
// every engine, a batch accepted but not committed is delivered again to the
// next subscriber of the group — at least once.
func TestSubscribeCommitAfterHandlerRedeliversAfterACrashBeforeTheCommit(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		appendOK(t, s, record("a"), record("b"))
		crashing := &commitFails{
			Stream: s, committer: sibling[corestream.Committer](t, s), waker: sibling[corestream.Waker](t, s),
		}
		crashing.armed.Store(true)
		first := newRecorder(nil)
		err := subscribe(t, crashing, svcstream.SubscriberConfig{Group: "g", Handler: first.handle, BatchSize: 8}).wait(t)
		if !errors.Is(err, errCrash) {
			t.Fatalf("Subscribe() over a failing commit = %v, want the commit's error", err)
		}
		again := newRecorder(nil)
		subscribe(t, s, svcstream.SubscriberConfig{Group: "g", Handler: again.handle, BatchSize: 8})
		again.waitFor(t, "the redelivery", func(ds []delivery) bool { return len(ds) >= 2 })
		if got := payloads(first.deliveries()); !slices.Equal(got, []string{"a", "b"}) {
			t.Fatalf("first delivery = %v, want [a b]", got)
		}
		if got := payloads(again.deliveries()); !slices.Equal(got, []string{"a", "b"}) {
			t.Fatalf("redelivery = %v, want [a b] again", got)
		}
	})
}

// TestSubscribeParksARecordItsHandlerKeepsRefusing: with Park, a batch
// refused After times is narrowed, its failing record published to the queue
// as the JSON of its entry, and the partition moves on past it.
func TestSubscribeParksARecordItsHandlerKeepsRefusing(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		broker, err := svcqueue.NewMemory(svcqueue.MemoryConfig{
			Policy: corequeue.PolicyValue{VisibilityTimeout: time.Minute, MaxDeliveries: 3},
		})
		if err != nil {
			t.Fatalf("queue.NewMemory() = %v", err)
		}
		appendOK(t, s, record("ok-1"), record("poison"), record("ok-2"))
		rec := newRecorder(func(entries []corestream.EntryValue) error {
			for _, e := range entries {
				if string(e.Record.Payload) == "poison" {
					return errors.New("cannot handle this one")
				}
			}
			return nil
		})
		subscribe(t, s, svcstream.SubscriberConfig{
			Group: "g", Handler: rec.handle, BatchSize: 3, Retry: quickRetry,
			Park: &svcstream.ParkConfig{Broker: broker, After: 2},
		})
		rec.waitFor(t, "both good records", func(ds []delivery) bool { return len(ds) >= 2 })
		if got := payloads(rec.deliveries()); !slices.Equal(got, []string{"ok-1", "ok-2"}) {
			t.Fatalf("delivered %v, want [ok-1 ok-2]", got)
		}
		parked, err := broker.Receive(t.Context(), 4)
		if err != nil || len(parked) != 1 {
			t.Fatalf("Receive() = %d dead letters, %v, want 1", len(parked), err)
		}
		var entry corestream.EntryValue
		if err := json.Unmarshal(parked[0].Message.Payload, &entry); err != nil {
			t.Fatalf("json.Unmarshal(dead letter) = %v", err)
		}
		if string(entry.Record.Payload) != "poison" || entry.Offset != 1 {
			t.Fatalf("dead letter = %+v, want the poison record at offset 1", entry)
		}
		awaitCommitted(t, sibling[corestream.Committer](t, s), "g", 0, 3)
	})
}

// awaitCommitted waits until group's committed offset on partition is want:
// the commit follows the handler's return.
func awaitCommitted(t *testing.T, committer corestream.Committer, group string, partition int, want corestream.Offset) {
	t.Helper()
	deadline := time.Now().Add(subscribeWait)
	for {
		got, err := committer.Committed(t.Context(), group, partition)
		if err == nil && got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Committed() = %d, %v, want %d", got, err, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSubscribeFencesAPartitionToOneReaderAtATime: two subscribers of one
// group over one fence never hold one partition together, and the records
// are each read once between them.
func TestSubscribeFencesAPartitionToOneReaderAtATime(t *testing.T) {
	t.Parallel()
	forEachStream(t, corestream.PolicyValue{Partitions: 2}, func(t *testing.T, _ *clock.ManualClock, s corestream.Stream) {
		locker, err := svclock.NewMemory(svclock.MemoryConfig{TTL: time.Minute})
		if err != nil {
			t.Fatalf("lock.NewMemory() = %v", err)
		}
		var inside [2]atomic.Int32
		rec := newRecorder(func(entries []corestream.EntryValue) error {
			p := entries[0].Partition
			if inside[p].Add(1) != 1 {
				t.Errorf("two readers inside partition %d at once", p)
			}
			time.Sleep(time.Millisecond)
			inside[p].Add(-1)
			return nil
		})
		cfg := svcstream.SubscriberConfig{Group: "g", Name: "events", Fence: locker, Handler: rec.handle}
		subscribe(t, s, cfg)
		subscribe(t, s, cfg)
		for i := range 20 {
			appendOK(t, s, keyed(keyOn(t, i%2, 2), strconv.Itoa(i)))
		}
		rec.waitFor(t, "every record", func(ds []delivery) bool { return len(ds) >= 20 })
		got := payloads(rec.deliveries())
		slices.Sort(got)
		if len(slices.Compact(got)) != 20 {
			t.Fatalf("delivered %v, want each of the 20 records once", rec.deliveries())
		}
	})
}

// subscriberField returns the "field" field of err, "" when it carries none.
func subscriberField(err error) string {
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == "field" {
			return field.StringValue()
		}
	}
	return ""
}

// TestSubscribeRefusesWhatItCannotRun: each required field, a commit mode it
// does not know, a partition outside the stream or named twice, a fence
// without a name, a park without a queue.
func TestSubscribeRefusesWhatItCannotRun(t *testing.T) {
	t.Parallel()
	s := makeMemory(t, clock.System, corestream.PolicyValue{Partitions: 2})
	handler := func(context.Context, []corestream.EntryValue) error { return nil }
	locker, err := svclock.NewMemory(svclock.MemoryConfig{TTL: time.Minute})
	if err != nil {
		t.Fatalf("lock.NewMemory() = %v", err)
	}
	valid := svcstream.SubscriberConfig{Group: "g", Handler: handler, HandlerIsIdempotent: true}
	type tc struct {
		edit  func(*svcstream.SubscriberConfig)
		name  string
		field string
	}
	runCase := func(t *testing.T, c tc) {
		cfg := valid
		c.edit(&cfg)
		err := svcstream.Subscribe(t.Context(), s, cfg)
		if !errs.HasCode(err, corestream.CodeSubscriberMisconfigured) {
			t.Fatalf("Subscribe() = %v, want SUBSCRIBER_MISCONFIGURED", err)
		}
		if got := subscriberField(err); got != c.field {
			t.Fatalf("field = %v, want %s", got, c.field)
		}
	}
	for _, c := range []tc{
		{name: "no handler", field: "Handler", edit: func(c *svcstream.SubscriberConfig) { c.Handler = nil }},
		{name: "no group", field: "Group", edit: func(c *svcstream.SubscriberConfig) { c.Group = "" }},
		{name: "not idempotent", field: "HandlerIsIdempotent", edit: func(c *svcstream.SubscriberConfig) { c.HandlerIsIdempotent = false }},
		{name: "unknown mode", field: "Commit", edit: func(c *svcstream.SubscriberConfig) { c.Commit = 7 }},
		{name: "fence unnamed", field: "Name", edit: func(c *svcstream.SubscriberConfig) { c.Fence = locker }},
		{name: "park nowhere", field: "Park.Broker", edit: func(c *svcstream.SubscriberConfig) { c.Park = &svcstream.ParkConfig{} }},
		{name: "partition outside", field: "Partitions", edit: func(c *svcstream.SubscriberConfig) { c.Partitions = []int{2} }},
		{name: "partition twice", field: "Partitions", edit: func(c *svcstream.SubscriberConfig) { c.Partitions = []int{1, 1} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
