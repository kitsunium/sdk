package queue_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// Priorities (ADR 0054, amended 2026-11-03, issue #1045): five levels, the
// visible messages of the highest received first, then by visibility —
// strict, so a steady higher level starves a lower one, which a named case
// shows rather than hides. Every case runs on every broker: memory, file,
// and SQL with SQLConfig.Priority over the fake engine on each dialect.

// everyPriorityBroker is everyBroker with the SQL brokers keeping levels.
func everyPriorityBroker() []brokerFactory {
	return []brokerFactory{
		{name: "memory", make: makeMemory},
		{name: "file", make: makeFile},
		{name: "sql/postgres", make: makePrioritySQL(coresql.DialectPostgres)},
		{name: "sql/mysql", make: makePrioritySQL(coresql.DialectMySQL)},
		{name: "sql/sqlite", make: makePrioritySQL(coresql.DialectSQLite)},
	}
}

// makePrioritySQL returns the factory of the SQL broker on dialect with
// SQLConfig.Priority, over a fake engine whose table has the column.
func makePrioritySQL(dialect coresql.Dialect) func(*testing.T, clock.Clock, corequeue.PolicyValue) corequeue.Broker {
	return func(t *testing.T, clk clock.Clock, policy corequeue.PolicyValue) corequeue.Broker {
		t.Helper()
		engine := newSQLEngineColumns(dialect, jobsTable, true, true)
		broker, err := svcqueue.NewSQL(svcqueue.SQLConfig{
			Transactor: newSQLTransactor(t, engine, dialect), Dialect: dialect, Table: jobsTable,
			Policy: policy, Clock: clk, Trail: true, Priority: true,
		})
		if err != nil {
			t.Fatalf("NewSQL() = %v, want nil", err)
		}
		return broker
	}
}

// publishAt is the assertive form of PriorityPublisher.PublishPriority.
func publishAt(t *testing.T, broker corequeue.Broker, payload string, level corequeue.Priority) corequeue.MessageValue {
	t.Helper()
	publisher, ok := broker.(corequeue.PriorityPublisher)
	if !ok {
		t.Fatalf("%T does not implement queue.PriorityPublisher", broker)
	}
	message, err := publisher.PublishPriority(t.Context(), []byte(payload), level)
	if err != nil {
		t.Fatalf("PublishPriority(%q, %d) = %v, want nil", payload, level, err)
	}
	return message
}

// deliveredPayloads is the payloads of a batch, in its order.
func deliveredPayloads(batch []corequeue.DeliveryValue) []string {
	out := make([]string, 0, len(batch))
	for _, delivery := range batch {
		out = append(out, string(delivery.Message.Payload))
	}
	return out
}

// TestTheVisibleMessagesOfTheHighestLevelAreReceivedFirst is the issue's
// first acceptance row: three visible messages published normal, high and
// low are received high, normal, low — and the five levels in their order
// when all five are queued, whatever order they were published in.
func TestTheVisibleMessagesOfTheHighestLevelAreReceivedFirst(t *testing.T) {
	t.Parallel()
	for _, factory := range everyPriorityBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			broker := factory.make(t, clock.NewManualClock(epoch), defaultPolicy())
			publish(t, broker, "normal")
			publishAt(t, broker, "high", corequeue.PriorityHigh)
			publishAt(t, broker, "low", corequeue.PriorityLow)
			if got := strings.Join(deliveredPayloads(receive(t, broker, 10)), " "); got != "high normal low" {
				t.Fatalf("Receive = %s, want high normal low", got)
			}
			for _, level := range []corequeue.Priority{0, -2, 2, -1, 1} {
				publishAt(t, broker, fmt.Sprint(level), level)
			}
			var got []string
			for range 5 {
				got = append(got, deliveredPayloads(receive(t, broker, 1))...)
			}
			if strings.Join(got, " ") != "2 1 0 -1 -2" {
				t.Fatalf("five Receive(1) = %v, want 2 1 0 -1 -2", got)
			}
		})
	}
}

// TestAHigherMessageNotYetVisibleHoldsBackNoVisibleLowerOne is the second
// row: a high message nacked into its retry delay does not hold back a
// visible normal one, and once its delay has passed it is received first
// again — its level kept across the nack.
func TestAHigherMessageNotYetVisibleHoldsBackNoVisibleLowerOne(t *testing.T) {
	t.Parallel()
	for _, factory := range everyPriorityBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publishAt(t, broker, "high", corequeue.PriorityHigh)
			nack(t, broker, receiveOne(t, broker).Lease.Receipt, nil)
			publish(t, broker, "normal")
			if got := deliveredPayloads(receive(t, broker, 10)); len(got) != 1 || got[0] != "normal" {
				t.Fatalf("Receive while the high one waits = %v, want [normal]", got)
			}
			publish(t, broker, "normal again")
			clk.Advance(testRetryDelay)
			if got := deliveredPayloads(receive(t, broker, 1)); len(got) != 1 || got[0] != "high" {
				t.Fatalf("Receive past the delay = %v, want [high] — its level kept across the nack", got)
			}
		})
	}
}

// TestASteadyHigherLevelStarvesTheLevelBelow is the one non-guarantee,
// shown on purpose as TestASingleRetryReordersTheStream shows the reorder:
// strict levels age nothing, so while a high message is published before
// every Receive, a low one queued first is never received; it is received
// once the stream stops.
func TestASteadyHigherLevelStarvesTheLevelBelow(t *testing.T) {
	t.Parallel()
	for _, factory := range everyPriorityBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			publishAt(t, broker, "starved", corequeue.PriorityLow)
			for round := range 20 {
				clk.Advance(time.Millisecond)
				publishAt(t, broker, "steady", corequeue.PriorityNormal)
				delivery := receiveOne(t, broker)
				if string(delivery.Message.Payload) != "steady" {
					t.Fatalf("round %d delivered %q, want the higher level's message", round, delivery.Message.Payload)
				}
				if err := broker.Ack(t.Context(), delivery.Lease.Receipt); err != nil {
					t.Fatalf("Ack() = %v", err)
				}
			}
			if got := deliveredPayloads(receive(t, broker, 10)); len(got) != 1 || got[0] != "starved" {
				t.Fatalf("Receive once the stream stopped = %v, want [starved]", got)
			}
		})
	}
}

// TestALevelOutsideTheSetIsRefused pins the closed set: a level past either
// end is QUEUE_MISCONFIGURED and publishes nothing, on every broker; so is a
// copy carrying one.
func TestALevelOutsideTheSetIsRefused(t *testing.T) {
	t.Parallel()
	for _, factory := range everyPriorityBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			broker := factory.make(t, clock.NewManualClock(epoch), defaultPolicy())
			publisher := broker.(corequeue.PriorityPublisher)
			for _, level := range []corequeue.Priority{corequeue.PriorityLowest - 1, corequeue.PriorityHighest + 1} {
				if _, err := publisher.PublishPriority(t.Context(), []byte("x"), level); !errs.HasCode(err, corequeue.CodeQueueMisconfigured) {
					t.Fatalf("PublishPriority(level %d) = %v, want QUEUE_MISCONFIGURED", level, err)
				}
			}
			live := corequeue.LiveMessageValue{VisibleAt: epoch, Message: corequeue.MessageValue{EnqueuedAt: epoch}, Priority: 3}
			if _, err := broker.(corequeue.MessageWriter).PutMessage(t.Context(), live); !errs.HasCode(err, corequeue.CodeInvalidMessage) {
				t.Fatalf("PutMessage(level 3) = %v, want INVALID_MESSAGE", err)
			}
			receiveNone(t, broker)
		})
	}
}

// TestALevelIsKeptAcrossARejectionAReplayAListingAndACopy follows one high
// message through the paths that move it: rejected, it is a dead letter at
// its level; replayed, it is received before a normal message; listed, it
// says its level; copied into another broker with its level, it is received
// there first.
func TestALevelIsKeptAcrossARejectionAReplayAListingAndACopy(t *testing.T) {
	t.Parallel()
	for _, factory := range everyPriorityBroker() {
		t.Run(factory.name, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewManualClock(epoch)
			broker := factory.make(t, clk, defaultPolicy())
			high := publishAt(t, broker, "high", corequeue.PriorityHigh)
			if err := broker.(corequeue.Rejecter).Reject(t.Context(), receiveOne(t, broker).Lease.Receipt, nil); err != nil {
				t.Fatalf("Reject() = %v", err)
			}
			dead := deadLetters(t, broker, 10)
			if len(dead) != 1 || dead[0].Priority != corequeue.PriorityHigh {
				t.Fatalf("DeadLetters = %+v, want the high message at its level", dead)
			}
			publish(t, broker, "normal")
			if err := broker.(corequeue.DeadLetterManager).ReplayDeadLetter(t.Context(), high.ID); err != nil {
				t.Fatalf("ReplayDeadLetter() = %v", err)
			}
			live, err := broker.(corequeue.MessageReader).Messages(t.Context(), "", 10)
			if err != nil || len(live) != 2 {
				t.Fatalf("Messages = %d, %v, want 2", len(live), err)
			}
			for _, message := range live {
				if want := map[string]corequeue.Priority{"high": 1, "normal": 0}[string(message.Message.Payload)]; message.Priority != want {
					t.Fatalf("Messages lists %q at level %d, want %d", message.Message.Payload, message.Priority, want)
				}
			}
			copied := factory.make(t, clk, defaultPolicy())
			for _, message := range live {
				message.Message.ID = ""
				if _, putErr := copied.(corequeue.MessageWriter).PutMessage(t.Context(), message); putErr != nil {
					t.Fatalf("PutMessage() = %v", putErr)
				}
			}
			for _, target := range []corequeue.Broker{broker, copied} {
				if got := deliveredPayloads(receive(t, target, 10)); strings.Join(got, " ") != "high normal" {
					t.Fatalf("Receive = %v, want [high normal]", got)
				}
			}
		})
	}
}

// TestTheFileBrokerReadsTheNamesWrittenBeforePriorities is the persisted
// format's compatibility row: a 4-field queued name, a 5-field in-flight name
// and a 3-field dead name, spelled as the grammar of ee1f48ba2 spelled them —
// by hand here, not through the renderers —, are a queued message at the
// normal level, a lease its receipt acknowledges, and a dead letter at the
// normal level; and a normal message published now is named exactly as
// before, while a high one carries a leading digit.
func TestTheFileBrokerReadsTheNamesWrittenBeforePriorities(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clock.NewManualClock(epoch)
	broker := requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy(), Clock: clk})
	at := epoch.Add(-time.Minute).UnixNano()
	queued := fmt.Sprintf("%019d.%019d.%s.%03d.msg", at, at, "0123456789abcdef", 0)
	leased := fmt.Sprintf("%019d.%019d.%s.%03d.%s.msg", epoch.Add(time.Hour).UnixNano(), at, "fedcba9876543210", 1, "0a0b0c0d")
	plant(t, filepath.Join(dir, "ready", queued), []byte("old queued"))
	plant(t, filepath.Join(dir, "inflight", leased), []byte("old leased"))
	record, ok := svcqueue.EncodeDeadForTest(corequeue.DeadLetterValue{
		Message:  corequeue.MessageValue{ID: fmt.Sprintf("%019d-%s", at, "00112233445566ff"), EnqueuedAt: time.Unix(0, at), Payload: []byte("old dead")},
		FailedAt: epoch, Deliveries: 3, Reason: "LEASE_EXPIRED",
	})
	if !ok {
		t.Fatal("EncodeDeadForTest refused the record")
	}
	plant(t, filepath.Join(dir, "dead", fmt.Sprintf("%019d.%s.%03d.dead", at, "00112233445566ff", 3)), record)

	live, err := broker.(corequeue.MessageReader).Messages(t.Context(), "", 10)
	if err != nil || len(live) != 2 || live[0].Priority != corequeue.PriorityNormal || live[1].Priority != corequeue.PriorityNormal {
		t.Fatalf("Messages over the old names = %+v, %v, want two at the normal level", live, err)
	}
	publishAt(t, broker, "high", corequeue.PriorityHigh)
	if got := deliveredPayloads(receive(t, broker, 10)); strings.Join(got, " ") != "high old queued" {
		t.Fatalf("Receive = %v, want [high, old queued] — the old name read as normal", got)
	}
	if err := broker.Ack(t.Context(), corequeue.ReceiptValue(leased)); err != nil {
		t.Fatalf("Ack(an old receipt) = %v, want nil", err)
	}
	dead := deadLetters(t, broker, 10)
	if len(dead) != 1 || dead[0].Priority != corequeue.PriorityNormal || string(dead[0].Message.Payload) != "old dead" {
		t.Fatalf("DeadLetters over an old name = %+v, want it at the normal level", dead)
	}

	publish(t, broker, "normal")
	publishAt(t, broker, "low", corequeue.PriorityLow)
	entries, readErr := os.ReadDir(filepath.Join(dir, "ready"))
	if readErr != nil || len(entries) != 2 {
		t.Fatalf("ready/ = %d entries, %v, want 2", len(entries), readErr)
	}
	low, normal := entries[0].Name(), entries[1].Name()
	if strings.HasPrefix(normal, "3.") {
		low, normal = normal, low
	}
	if !strings.HasPrefix(low, "3.") || strings.Count(low, ".") != 5 || strings.Count(normal, ".") != 4 {
		t.Fatalf("ready/ = %s %s, want a low name opening with its digit and a normal name in the old grammar", low, normal)
	}
}

// plant writes a file as another process, or an earlier release, left it.
func plant(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile(%s) = %v", path, err)
	}
}

// TestTheSQLBrokerWithoutPriorityRefusesALevelItCannotKeep pins
// SQLConfig.Priority unset: the normal level is published as Publish
// publishes it, any other is QUEUE_MISCONFIGURED, and nothing is sent.
func TestTheSQLBrokerWithoutPriorityRefusesALevelItCannotKeep(t *testing.T) {
	t.Parallel()
	fixture := newSQLFixture(t, coresql.DialectPostgres, clock.NewManualClock(epoch), defaultPolicy())
	publisher := fixture.broker.(corequeue.PriorityPublisher)
	if _, err := publisher.PublishPriority(t.Context(), []byte("high"), corequeue.PriorityHigh); !errs.HasCode(err, corequeue.CodeQueueMisconfigured) {
		t.Fatalf("PublishPriority(high) without the column = %v, want QUEUE_MISCONFIGURED", err)
	}
	if _, err := publisher.PublishPriority(t.Context(), []byte("normal"), corequeue.PriorityNormal); err != nil {
		t.Fatalf("PublishPriority(normal) without the column = %v, want nil", err)
	}
	if roles := fixture.engine.roles(); len(roles) != 1 || roles[0] != "insert" {
		t.Fatalf("statements = %v, want one insert — Publish's own", roles)
	}
}
