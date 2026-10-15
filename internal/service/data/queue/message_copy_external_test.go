// Package queue_test — MessageReader and MessageWriter on every broker: the
// live messages listed without a lease or a count changed, and one written as
// another broker recorded it, read back as it was given and delivered no
// sooner than its instant (ADR 0178).
package queue_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// messageReader asserts the MessageReader capability and returns it.
func messageReader(t *testing.T, broker corequeue.Broker) corequeue.MessageReader {
	t.Helper()
	capability, ok := broker.(corequeue.MessageReader)
	if !ok {
		t.Fatalf("%T does not implement queue.MessageReader", broker)
	}
	return capability
}

// messageWriter asserts the MessageWriter capability and returns it.
func messageWriter(t *testing.T, broker corequeue.Broker) corequeue.MessageWriter {
	t.Helper()
	capability, ok := broker.(corequeue.MessageWriter)
	if !ok {
		t.Fatalf("%T does not implement queue.MessageWriter", broker)
	}
	return capability
}

// liveMessages lists every live message, failing the test on an error.
func liveMessages(t *testing.T, broker corequeue.Broker) []corequeue.LiveMessageValue {
	t.Helper()
	live, err := messageReader(t, broker).Messages(t.Context(), "", 100)
	if err != nil {
		t.Fatalf("Messages() = %v, want nil", err)
	}
	return live
}

// byPayload finds the live message holding payload.
func byPayload(t *testing.T, live []corequeue.LiveMessageValue, payload string) corequeue.LiveMessageValue {
	t.Helper()
	for _, m := range live {
		if string(m.Message.Payload) == payload {
			return m
		}
	}
	t.Fatalf("no live message holds %q among %+v", payload, live)
	return corequeue.LiveMessageValue{}
}

// TestTheLiveMessagesAreListedWithoutALease pins Messages on every broker: a
// queued, a retried and a leased message are listed in identifier order with
// what the broker records of each, twice the same, and the listing leases
// nothing and counts no delivery.
func TestTheLiveMessagesAreListedWithoutALease(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publish(t, broker, "retried")
			clk.Advance(time.Second)
			publish(t, broker, "leased")
			clk.Advance(time.Second)
			published := publish(t, broker, "waiting")
			retried := receiveOne(t, broker)
			nack(t, broker, retried.Lease.Receipt, nil)
			leased := receiveOne(t, broker)

			first := liveMessages(t, broker)
			if len(first) != 3 || !slices.IsSortedFunc(first, func(a, b corequeue.LiveMessageValue) int {
				return compareStrings(a.Message.ID, b.Message.ID)
			}) {
				t.Fatalf("Messages() = %+v, want three in identifier order", first)
			}
			r := byPayload(t, first, "retried")
			if r.Leased || r.Deliveries != 1 || !r.VisibleAt.Equal(epoch.Add(2*time.Second+testRetryDelay)) || !r.Message.EnqueuedAt.Equal(epoch) {
				t.Errorf("the retried message is listed as %+v", r)
			}
			l := byPayload(t, first, "leased")
			if !l.Leased || l.Deliveries != 1 || !l.VisibleAt.Equal(leased.Lease.ExpiresAt) || l.Message.ID != leased.Message.ID {
				t.Errorf("the leased message is listed as %+v, its lease expiring at %v", l, leased.Lease.ExpiresAt)
			}
			w := byPayload(t, first, "waiting")
			if w.Leased || w.Deliveries != 0 || !w.VisibleAt.Equal(published.EnqueuedAt) || w.Message.ID != published.ID {
				t.Errorf("the waiting message is listed as %+v", w)
			}
			if again := liveMessages(t, broker); !slices.EqualFunc(again, first, sameLive) {
				t.Fatalf("a second listing differs: %+v, want %+v", again, first)
			}
			if got := receiveOne(t, broker); got.Message.ID != published.ID || got.Deliveries != 1 {
				t.Fatalf("after two listings Receive gave %+v, want the waiting message on its first delivery", got)
			}
		})
	}
}

// TestTheLiveMessagesAreListedPageByPage pins resuming after an identifier,
// and InvalidBatchSize for a batch that is not positive.
func TestTheLiveMessagesAreListedPageByPage(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			for _, payload := range []string{"a", "b", "c", "d", "e"} {
				publish(t, broker, payload)
				clk.Advance(time.Millisecond)
			}
			reader := messageReader(t, broker)
			var paged []string
			after := ""
			for {
				page, err := reader.Messages(t.Context(), after, 2)
				if err != nil {
					t.Fatalf("Messages(%q, 2) = %v", after, err)
				}
				if len(page) == 0 {
					break
				}
				for _, m := range page {
					paged = append(paged, string(m.Message.Payload))
				}
				after = page[len(page)-1].Message.ID
			}
			if !slices.Equal(paged, []string{"a", "b", "c", "d", "e"}) {
				t.Fatalf("the pages hold %v, want every message once, in order", paged)
			}
			if _, err := reader.Messages(t.Context(), "", 0); !errs.HasCode(err, corequeue.CodeInvalidBatchSize) {
				t.Fatalf("Messages(max 0) = %v, want InvalidBatchSize", err)
			}
		})
	}
}

// TestAWrittenMessageReadsBackAsItWasGiven pins PutMessage on every broker:
// the message reads back as given under a fresh identifier, is delivered no
// sooner than its instant, and counts its next delivery from the count given.
func TestAWrittenMessageReadsBackAsItWasGiven(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			given := []corequeue.LiveMessageValue{
				{Message: corequeue.MessageValue{Payload: []byte("due now"), EnqueuedAt: epoch.Add(-time.Hour)}, VisibleAt: epoch.Add(-time.Minute), Deliveries: 2},
				{Message: corequeue.MessageValue{Payload: []byte{0, 0xff, '\n'}, EnqueuedAt: epoch.Add(-2 * time.Hour)}, VisibleAt: epoch.Add(time.Hour)},
				{Message: corequeue.MessageValue{EnqueuedAt: epoch.Add(-3 * time.Hour)}, VisibleAt: epoch.Add(2 * time.Hour), Deliveries: 4},
			}
			ids := map[string]corequeue.LiveMessageValue{}
			for _, live := range given {
				id, err := messageWriter(t, broker).PutMessage(t.Context(), live)
				if err != nil || id == "" {
					t.Fatalf("PutMessage(%q) = %q, %v", live.Message.Payload, id, err)
				}
				if _, twice := ids[id]; twice {
					t.Fatalf("PutMessage minted %q twice", id)
				}
				ids[id] = live
			}
			for _, got := range liveMessages(t, broker) {
				want, found := ids[got.Message.ID]
				if !found {
					t.Fatalf("Messages() lists %q, an identifier PutMessage never answered", got.Message.ID)
				}
				if got.Leased || got.Deliveries != want.Deliveries || !got.VisibleAt.Equal(want.VisibleAt) ||
					!got.Message.EnqueuedAt.Equal(want.Message.EnqueuedAt) || !bytes.Equal(got.Message.Payload, want.Message.Payload) {
					t.Errorf("message %q reads back as %+v, want %+v", got.Message.ID, got, want)
				}
				delete(ids, got.Message.ID)
			}
			if len(ids) != 0 {
				t.Fatalf("Messages() missed %d written messages", len(ids))
			}
			now := receiveOne(t, broker)
			if string(now.Message.Payload) != "due now" || now.Deliveries != 3 {
				t.Fatalf("Receive gave %+v, want the message already due, on its third delivery", now)
			}
			receiveNone(t, broker)
			clk.Advance(time.Hour)
			if later := receiveOne(t, broker); !bytes.Equal(later.Message.Payload, []byte{0, 0xff, '\n'}) || later.Deliveries != 1 {
				t.Fatalf("Receive at its instant gave %+v", later)
			}
		})
	}
}

// TestPutMessageRefusesWhatNoBrokerKeepsAsGiven pins the refusals every
// broker makes alike, each naming its field, and that a refused message
// writes nothing.
func TestPutMessageRefusesWhatNoBrokerKeepsAsGiven(t *testing.T) {
	t.Parallel()
	valid := corequeue.LiveMessageValue{
		Message: corequeue.MessageValue{Payload: []byte("ok"), EnqueuedAt: epoch}, VisibleAt: epoch, Deliveries: 1,
	}
	cases := []struct {
		change func(*corequeue.LiveMessageValue)
		field  string
	}{
		{field: "Message.ID", change: func(m *corequeue.LiveMessageValue) { m.Message.ID = "0000000000000000001-00000000000000aa" }},
		{field: "Leased", change: func(m *corequeue.LiveMessageValue) { m.Leased = true }},
		{field: "Deliveries", change: func(m *corequeue.LiveMessageValue) { m.Deliveries = -1 }},
		{field: "Deliveries", change: func(m *corequeue.LiveMessageValue) { m.Deliveries = int(aboveInt32) }},
		{field: "Message.EnqueuedAt", change: func(m *corequeue.LiveMessageValue) { m.Message.EnqueuedAt = time.Time{} }},
		{field: "VisibleAt", change: func(m *corequeue.LiveMessageValue) { m.VisibleAt = time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{field: "VisibleAt", change: func(m *corequeue.LiveMessageValue) { m.VisibleAt = time.Unix(-1, 0) }},
	}
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			policy := defaultPolicy()
			policy.MaxMessageBytes = 16
			broker := factory.make(t, clock.NewManualClock(epoch), policy)
			for _, testCase := range cases {
				live := valid
				testCase.change(&live)
				_, err := messageWriter(t, broker).PutMessage(t.Context(), live)
				if !errs.HasCode(err, corequeue.CodeInvalidMessage) {
					t.Errorf("PutMessage with a bad %s = %v, want CodeInvalidMessage", testCase.field, err)
					continue
				}
				if named := fieldOf(err, "field"); named != testCase.field {
					t.Errorf("PutMessage with a bad %s named the field %q", testCase.field, named)
				}
			}
			tooLarge := valid
			tooLarge.Message.Payload = bytes.Repeat([]byte("x"), 17)
			if _, err := messageWriter(t, broker).PutMessage(t.Context(), tooLarge); !errs.HasCode(err, corequeue.CodeMessageTooLarge) {
				t.Errorf("PutMessage with a payload above the bound = %v, want CodeMessageTooLarge", err)
			}
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := messageWriter(t, broker).PutMessage(cancelled, valid); !errors.Is(err, context.Canceled) {
				t.Errorf("PutMessage on a cancelled context = %v, want context.Canceled", err)
			}
			if left := liveMessages(t, broker); len(left) != 0 {
				t.Fatalf("a refused message was written: %+v", left)
			}
		})
	}
}

// TestSQLAWrittenMessageExistsIfAndOnlyIfItsTransactionCommits pins that
// PutMessage joins the transaction its context carries (ADR 0151), and wakes
// the consumers only once it has committed.
func TestSQLAWrittenMessageExistsIfAndOnlyIfItsTransactionCommits(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		waker, _ := fx.broker.(corequeue.Waker)
		before := waker.Wake().Signal
		live := corequeue.LiveMessageValue{Message: corequeue.MessageValue{Payload: []byte("moved"), EnqueuedAt: epoch}, VisibleAt: epoch, Deliveries: 1}
		errRolledBack := errors.New("the copy failed further on")
		err := inTransaction(t, fx, func(ctx context.Context) error {
			if _, putErr := messageWriter(t, fx.broker).PutMessage(ctx, live); putErr != nil {
				t.Fatalf("PutMessage() inside the transaction = %v", putErr)
			}
			return errRolledBack
		})
		if !errors.Is(err, errRolledBack) {
			t.Fatalf("Transact() = %v, want the function's own error", err)
		}
		if fx.engine.rows() != 0 {
			t.Fatalf("a rolled-back PutMessage left %d rows", fx.engine.rows())
		}
		assertOpen(t, before, "a rolled-back PutMessage")
		err = inTransaction(t, fx, func(ctx context.Context) error {
			_, putErr := messageWriter(t, fx.broker).PutMessage(ctx, live)
			return putErr
		})
		if err != nil {
			t.Fatalf("Transact() = %v, want nil", err)
		}
		assertClosed(t, before, "the commit of a PutMessage")
		if got := receiveOne(t, fx.broker); string(got.Message.Payload) != "moved" || got.Deliveries != 2 {
			t.Fatalf("Receive gave %+v, want the committed message on its second delivery", got)
		}
	})
}

// sameLive reports whether two listings of one message agree.
func sameLive(a, b corequeue.LiveMessageValue) bool {
	return a.Message.ID == b.Message.ID && a.Leased == b.Leased && a.Deliveries == b.Deliveries &&
		a.VisibleAt.Equal(b.VisibleAt) && a.Message.EnqueuedAt.Equal(b.Message.EnqueuedAt) &&
		bytes.Equal(a.Message.Payload, b.Message.Payload)
}

// compareStrings is strings.Compare, the order the listing promises.
func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
