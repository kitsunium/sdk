// Package queue_test — DeadLetterWriter on every broker: a dead letter written
// as another broker recorded it reads back as it was given, is decided like any
// other, is never delivered, and a value no store keeps as given is refused
// before anything is written (ADR 0178).
package queue_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// aboveInt32 is one past the largest count a durable broker records. It is a
// variable so the conversion to int compiles on 32 bits too, where it wraps
// negative and is refused all the same.
var aboveInt32 int64 = math.MaxInt32 + 1

// writer asserts the DeadLetterWriter capability and returns it.
func writer(t *testing.T, broker corequeue.Broker) corequeue.DeadLetterWriter {
	t.Helper()
	capability, ok := broker.(corequeue.DeadLetterWriter)
	if !ok {
		t.Fatalf("%T does not implement queue.DeadLetterWriter", broker)
	}
	return capability
}

// recordedElsewhere are dead letters as another broker recorded them: a death
// by expired leases with no code, a cause that was no SDK error, an SDK
// error's three halves and its trail, an empty payload, and bytes that are no
// text with the longest trail a broker keeps (issue #500).
func recordedElsewhere() []corequeue.DeadLetterValue {
	enqueued := epoch.Add(-48 * time.Hour)
	return []corequeue.DeadLetterValue{
		{
			Message:  corequeue.MessageValue{Payload: []byte("crashed every consumer"), EnqueuedAt: enqueued},
			FailedAt: enqueued.Add(time.Hour), Deliveries: 5, Reason: "LEASE_EXPIRED",
		},
		{
			Message:  corequeue.MessageValue{Payload: []byte("a stdlib cause"), EnqueuedAt: enqueued.Add(time.Second)},
			FailedAt: enqueued.Add(2 * time.Hour), Deliveries: 3,
		},
		{
			Message:  corequeue.MessageValue{Payload: []byte(`{"order":42}`), EnqueuedAt: enqueued.Add(2 * time.Second)},
			FailedAt: enqueued.Add(3 * time.Hour), Deliveries: 1,
			Reason: "NOT_RETRYABLE", Cause: "The message cannot be processed and was dead-lettered without a retry",
			Code: uint32(corequeue.CodeNotRetryable), Trail: []uint32{0x00_05_01_03, uint32(corequeue.CodeNotRetryable)},
		},
		{
			Message:  corequeue.MessageValue{EnqueuedAt: enqueued.Add(3 * time.Second)},
			FailedAt: enqueued.Add(4 * time.Hour), Deliveries: 2, Reason: "UNREPORTED",
		},
		{
			Message:  corequeue.MessageValue{Payload: []byte{0, 0xff, '\n', '\n', 0x80}, EnqueuedAt: enqueued.Add(4 * time.Second)},
			FailedAt: enqueued.Add(5 * time.Hour), Deliveries: 7, Reason: "CUSTOM", Code: 0x00_05_01_02,
			Trail: []uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, math.MaxUint32},
		},
	}
}

// sameDeadLetter reports what differs between got and want, the identifier
// aside: a written dead letter keeps everything else as given.
func sameDeadLetter(got, want corequeue.DeadLetterValue) string {
	switch {
	case !bytes.Equal(got.Message.Payload, want.Message.Payload):
		return "the payload"
	case !got.Message.EnqueuedAt.Equal(want.Message.EnqueuedAt):
		return "EnqueuedAt"
	case !got.FailedAt.Equal(want.FailedAt):
		return "FailedAt"
	case got.Deliveries != want.Deliveries:
		return "Deliveries"
	case got.Reason != want.Reason:
		return "Reason"
	case got.Cause != want.Cause:
		return "Cause"
	case got.Code != want.Code:
		return "Code"
	case !slices.Equal(got.Trail, want.Trail) || (want.Trail == nil) != (got.Trail == nil):
		return "Trail"
	}
	return ""
}

// TestAWrittenDeadLetterReadsBackAsItWasGiven pins PutDeadLetter on every
// broker: each dead letter reads back with its payload, instants, count,
// reason, cause and code — code 0 included — under a fresh identifier the
// broker minted, distinct from every other.
func TestAWrittenDeadLetterReadsBackAsItWasGiven(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			broker := factory.make(t, clock.NewManualClock(epoch), defaultPolicy())
			given := recordedElsewhere()
			minted := map[string]corequeue.DeadLetterValue{}
			for _, dead := range given {
				id, err := writer(t, broker).PutDeadLetter(t.Context(), dead)
				if err != nil {
					t.Fatalf("PutDeadLetter(%q) = %v, want nil", dead.Message.Payload, err)
				}
				if id == "" {
					t.Fatalf("PutDeadLetter(%q) minted an empty identifier", dead.Message.Payload)
				}
				if _, twice := minted[id]; twice {
					t.Fatalf("PutDeadLetter minted %q twice", id)
				}
				minted[id] = dead
			}
			read := deadLetters(t, broker, 100)
			if len(read) != len(given) {
				t.Fatalf("DeadLetters() returned %d records, want %d", len(read), len(given))
			}
			for _, got := range read {
				want, found := minted[got.Message.ID]
				if !found {
					t.Fatalf("DeadLetters() returned %q, an identifier PutDeadLetter never answered", got.Message.ID)
				}
				if differs := sameDeadLetter(got, want); differs != "" {
					t.Errorf("dead letter %q: %s differs: got %+v, want %+v", got.Message.ID, differs, got, want)
				}
			}
		})
	}
}

// TestAWrittenDeadLetterIsDecidedLikeAnyOther pins that a written dead letter
// is never delivered, that a replay queues it under the identifier it was
// written under with its count reset, and that a deletion removes it.
func TestAWrittenDeadLetterIsDecidedLikeAnyOther(t *testing.T) {
	t.Parallel()
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			broker := factory.make(t, clock.NewManualClock(epoch), defaultPolicy())
			given := recordedElsewhere()
			replayed, err := writer(t, broker).PutDeadLetter(t.Context(), given[0])
			if err != nil {
				t.Fatalf("PutDeadLetter() = %v, want nil", err)
			}
			deleted, err := writer(t, broker).PutDeadLetter(t.Context(), given[2])
			if err != nil {
				t.Fatalf("PutDeadLetter() = %v, want nil", err)
			}
			receiveNone(t, broker)

			if err := manager(t, broker).DeleteDeadLetter(t.Context(), deleted); err != nil {
				t.Fatalf("DeleteDeadLetter(%q) = %v, want nil", deleted, err)
			}
			if err := manager(t, broker).ReplayDeadLetter(t.Context(), replayed); err != nil {
				t.Fatalf("ReplayDeadLetter(%q) = %v, want nil", replayed, err)
			}
			if left := deadLetters(t, broker, 10); len(left) != 0 {
				t.Fatalf("decided dead letters are still in the store: %+v", left)
			}
			again := receiveOne(t, broker)
			if again.Message.ID != replayed || !bytes.Equal(again.Message.Payload, given[0].Message.Payload) {
				t.Errorf("replayed %+v, want %q under %q", again.Message, given[0].Message.Payload, replayed)
			}
			if again.Deliveries != 1 {
				t.Errorf("the replayed message counted %d, want 1", again.Deliveries)
			}
		})
	}
}

// TestPutDeadLetterRefusesWhatNoStoreKeepsAsGiven pins the refusals every
// broker makes alike, each naming its field, and that a refused dead letter
// writes nothing.
func TestPutDeadLetterRefusesWhatNoStoreKeepsAsGiven(t *testing.T) {
	t.Parallel()
	valid := recordedElsewhere()[2]
	cases := []struct {
		change func(*corequeue.DeadLetterValue)
		field  string
	}{
		{field: "Message.ID", change: func(d *corequeue.DeadLetterValue) { d.Message.ID = "0000000000000000001-00000000000000aa" }},
		{field: "Deliveries", change: func(d *corequeue.DeadLetterValue) { d.Deliveries = 0 }},
		{field: "Deliveries", change: func(d *corequeue.DeadLetterValue) { d.Deliveries = -1 }},
		{field: "Deliveries", change: func(d *corequeue.DeadLetterValue) { d.Deliveries = int(aboveInt32) }},
		{field: "Message.EnqueuedAt", change: func(d *corequeue.DeadLetterValue) { d.Message.EnqueuedAt = time.Time{} }},
		{field: "Message.EnqueuedAt", change: func(d *corequeue.DeadLetterValue) { d.Message.EnqueuedAt = time.Unix(-1, 0) }},
		{field: "FailedAt", change: func(d *corequeue.DeadLetterValue) { d.FailedAt = time.Time{} }},
		{field: "FailedAt", change: func(d *corequeue.DeadLetterValue) { d.FailedAt = time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC) }},
		{field: "Reason", change: func(d *corequeue.DeadLetterValue) { d.Reason = "TWO\nLINES" }},
		{field: "Reason", change: func(d *corequeue.DeadLetterValue) { d.Reason = strings.Repeat("R", 121) }},
		{field: "Cause", change: func(d *corequeue.DeadLetterValue) { d.Cause = "a carriage\rreturn" }},
		{field: "Cause", change: func(d *corequeue.DeadLetterValue) { d.Cause = strings.Repeat("é", 121) }},
		{field: "Trail", change: func(d *corequeue.DeadLetterValue) { d.Trail = make([]uint32, 17) }},
	}
	for _, factory := range everyBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			policy := defaultPolicy()
			policy.MaxMessageBytes = 16
			broker := factory.make(t, clock.NewManualClock(epoch), policy)
			for _, testCase := range cases {
				dead := valid
				testCase.change(&dead)
				_, err := writer(t, broker).PutDeadLetter(t.Context(), dead)
				if !errs.HasCode(err, corequeue.CodeInvalidDeadLetter) {
					t.Errorf("PutDeadLetter with a bad %s = %v, want CodeInvalidDeadLetter", testCase.field, err)
					continue
				}
				if named := fieldOf(err, "field"); named != testCase.field {
					t.Errorf("PutDeadLetter with a bad %s named the field %q", testCase.field, named)
				}
			}
			tooLarge := valid
			tooLarge.Message.Payload = bytes.Repeat([]byte("x"), 17)
			if _, err := writer(t, broker).PutDeadLetter(t.Context(), tooLarge); !errs.HasCode(err, corequeue.CodeMessageTooLarge) {
				t.Errorf("PutDeadLetter with a payload above the bound = %v, want CodeMessageTooLarge", err)
			}
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := writer(t, broker).PutDeadLetter(cancelled, valid); !errors.Is(err, context.Canceled) {
				t.Errorf("PutDeadLetter on a cancelled context = %v, want context.Canceled", err)
			}
			if left := deadLetters(t, broker, 10); len(left) != 0 {
				t.Fatalf("a refused dead letter was written: %+v", left)
			}
		})
	}
}

// fieldOf returns the string value of err's field key, or "".
func fieldOf(err error, key string) string {
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}

// TestSQLAWrittenDeadLetterExistsIfAndOnlyIfItsTransactionCommits pins that
// PutDeadLetter joins the transaction its context carries (ADR 0151): rolled
// back, nothing was written; committed, the dead letter is there.
func TestSQLAWrittenDeadLetterExistsIfAndOnlyIfItsTransactionCommits(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, fx *sqlFixture, _ *clock.ManualClock) {
		dead := recordedElsewhere()[0]
		errRolledBack := errors.New("the copy failed further on")
		err := inTransaction(t, fx, func(ctx context.Context) error {
			if _, putErr := writer(t, fx.broker).PutDeadLetter(ctx, dead); putErr != nil {
				t.Fatalf("PutDeadLetter() inside the transaction = %v", putErr)
			}
			return errRolledBack
		})
		if !errors.Is(err, errRolledBack) {
			t.Fatalf("Transact() = %v, want the function's own error", err)
		}
		if fx.engine.rows() != 0 {
			t.Fatalf("a rolled-back PutDeadLetter left %d rows", fx.engine.rows())
		}
		var id string
		err = inTransaction(t, fx, func(ctx context.Context) error {
			var putErr error
			id, putErr = writer(t, fx.broker).PutDeadLetter(ctx, dead)
			return putErr
		})
		if err != nil {
			t.Fatalf("Transact() = %v, want nil", err)
		}
		read := deadLetters(t, fx.broker, 10)
		if len(read) != 1 || read[0].Message.ID != id {
			t.Fatalf("DeadLetters() = %+v, want the committed %q", read, id)
		}
	})
}
