package queue_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/clock"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// The rows BENCH.md lacked before issue #1048: what Consume adds per message
// over Receive and Ack, the verbs since ADR 0151, #474 and ADR 0183, the
// siblings of the 2026-11-02 amendment, and the SQL broker's allocations per
// verb over the fake engine.

// benchKinds are the two brokers on a real clock.
var benchKinds = []string{"mem", "file"}

// BenchmarkConsume measures one message published and handled through
// Consume — the wake, the watch and the acknowledgement included — plain,
// under AutoExtend, and with a DrainTimeout.
func BenchmarkConsume(b *testing.B) {
	variants := map[string]func(*svcqueue.ConsumerConfig){
		"plain":      func(*svcqueue.ConsumerConfig) {},
		"autoextend": func(cfg *svcqueue.ConsumerConfig) { cfg.AutoExtend = true },
		"drain":      func(cfg *svcqueue.ConsumerConfig) { cfg.DrainTimeout = time.Second },
	}
	for _, kind := range benchKinds {
		for _, name := range []string{"plain", "autoextend", "drain"} {
			b.Run(kind+"/"+name, func(b *testing.B) {
				broker := benchBroker(b, kind)
				handled := make(chan struct{}, 1)
				cfg := svcqueue.ConsumerConfig{
					Handler: func(context.Context, corequeue.DeliveryValue) error {
						handled <- struct{}{}
						return nil
					},
					HandlerIsIdempotent: true, PollInterval: time.Hour,
				}
				variants[name](&cfg)
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { done <- svcqueue.Consume(ctx, broker, cfg) }()
				payload := make([]byte, 64)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if _, err := broker.Publish(ctx, payload); err != nil {
						b.Fatalf("Publish() = %v", err)
					}
					<-handled
				}
				b.StopTimer()
				cancel()
				if err := <-done; err != nil {
					b.Fatalf("Consume() = %v", err)
				}
			})
		}
	}
}

// BenchmarkLeaseVerbs measures the verbs that end or renew one lease beside
// Ack and Nack: Extend, Release and Reject.
func BenchmarkLeaseVerbs(b *testing.B) {
	cause := errors.New("bench rejection") //nolint:err113
	verbs := map[string]func(ctx context.Context, broker corequeue.Broker, receipt corequeue.ReceiptValue) error{
		"extend": func(ctx context.Context, broker corequeue.Broker, receipt corequeue.ReceiptValue) error {
			_, err := broker.(corequeue.LeaseExtender).Extend(ctx, receipt, time.Hour)
			return err
		},
		"release": func(ctx context.Context, broker corequeue.Broker, receipt corequeue.ReceiptValue) error {
			return broker.(corequeue.Releaser).Release(ctx, receipt)
		},
		"reject": func(ctx context.Context, broker corequeue.Broker, receipt corequeue.ReceiptValue) error {
			return broker.(corequeue.Rejecter).Reject(ctx, receipt, cause)
		},
	}
	for _, kind := range benchKinds {
		for _, verb := range []string{"extend", "release", "reject"} {
			b.Run(kind+"/"+verb, func(b *testing.B) {
				broker := benchBroker(b, kind)
				ctx := context.Background()
				prime(b, broker, b.N, 64)
				receipts := leaseAll(b, broker, b.N)
				b.ReportAllocs()
				b.ResetTimer()
				for index := range b.N {
					if err := verbs[verb](ctx, broker, receipts[index]); err != nil {
						b.Fatalf("%s() = %v", verb, err)
					}
				}
			})
		}
	}
}

// BenchmarkPublishLater measures ADR 0183's two publications: PublishAt, and
// PublishOnce under a fresh key and under one its window still holds.
func BenchmarkPublishLater(b *testing.B) {
	for _, kind := range benchKinds {
		b.Run(kind+"/at", func(b *testing.B) {
			broker := benchBroker(b, kind).(corequeue.DelayedPublisher)
			at, payload := time.Now().Add(time.Hour), make([]byte, 64)
			b.ReportAllocs()
			for range b.N {
				if _, err := broker.PublishAt(context.Background(), payload, at); err != nil {
					b.Fatalf("PublishAt() = %v", err)
				}
			}
		})
		for _, duplicate := range []bool{false, true} {
			name := map[bool]string{false: "/once-fresh", true: "/once-duplicate"}[duplicate]
			b.Run(kind+name, func(b *testing.B) {
				broker := benchBroker(b, kind).(corequeue.OncePublisher)
				payload := make([]byte, 64)
				b.ReportAllocs()
				for index := range b.N {
					key := "k"
					if !duplicate {
						key = strconv.Itoa(index)
					}
					if _, _, err := broker.PublishOnce(context.Background(), key, payload, time.Hour); err != nil {
						b.Fatalf("PublishOnce() = %v", err)
					}
				}
			})
		}
	}
}

// BenchmarkListings measures the reads a copy or an operator makes: a page of
// a hundred live messages, the first hundred through AllMessages, a page of a
// hundred dead letters, and Stats, over a backlog of a thousand.
func BenchmarkListings(b *testing.B) {
	for _, kind := range benchKinds {
		broker, dir := benchBrokerIn(b, kind)
		ctx := context.Background()
		prime(b, broker, 1000, 64)
		deadReceipts := leaseAll(b, broker, 200)
		for _, receipt := range deadReceipts {
			if err := broker.(corequeue.Rejecter).Reject(ctx, receipt, nil); err != nil {
				b.Fatalf("Reject() = %v", err)
			}
		}
		b.Run(kind+"/messages-page100", func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if page, err := broker.(corequeue.MessageReader).Messages(ctx, "", 100); err != nil || len(page) != 100 {
					b.Fatalf("Messages() = %d, %v", len(page), err)
				}
			}
		})
		b.Run(kind+"/allmessages-first100", func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				n := 0
				for _, err := range broker.(corequeue.MessageLister).AllMessages(ctx, "") {
					if err != nil {
						b.Fatalf("AllMessages() = %v", err)
					}
					if n++; n == 100 {
						break
					}
				}
			}
		})
		b.Run(kind+"/deadletters-page100", func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				if page, err := broker.(corequeue.DeadLetterReader).DeadLetters(ctx, 100); err != nil || len(page) != 100 {
					b.Fatalf("DeadLetters() = %d, %v", len(page), err)
				}
			}
		})
		// stats-depth1000-listed reads the three directories on every call —
		// each touched off the clock first, as a busy queue's are; -quiet is
		// a queue nobody touched for longer than the margin, its listings
		// reused (issue #811).
		b.Run(kind+"/stats-depth1000-listed", func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				touchStates(b, dir, time.Now())
				if _, err := broker.(corequeue.Counter).Stats(ctx); err != nil {
					b.Fatalf("Stats() = %v", err)
				}
			}
		})
		b.Run(kind+"/stats-depth1000-quiet", func(b *testing.B) {
			touchStates(b, dir, time.Now().Add(-time.Hour))
			b.ReportAllocs()
			for range b.N {
				if _, err := broker.(corequeue.Counter).Stats(ctx); err != nil {
					b.Fatalf("Stats() = %v", err)
				}
			}
		})
	}
}

// BenchmarkIdlePoll measures the Receive an idle consumer makes on every poll
// interval over a queue that holds depth messages an hour from visible and
// that nobody has touched for longer than the file broker's margin: it finds
// nothing, and on the file broker reads no directory (issue #811).
func BenchmarkIdlePoll(b *testing.B) {
	for _, kind := range benchKinds {
		for _, depth := range []int{0, 1000} {
			b.Run(kind+"/depth"+strconv.Itoa(depth), func(b *testing.B) {
				broker, dir := benchBrokerIn(b, kind)
				ctx := context.Background()
				later := broker.(corequeue.DelayedPublisher)
				b.StopTimer()
				for range depth {
					if _, err := later.PublishAt(ctx, make([]byte, 64), time.Now().Add(time.Hour)); err != nil {
						b.Fatalf("PublishAt() = %v", err)
					}
				}
				touchStates(b, dir, time.Now().Add(-time.Hour))
				b.StartTimer()
				b.ReportAllocs()
				for range b.N {
					if batch, err := broker.Receive(ctx, 1); err != nil || len(batch) != 0 {
						b.Fatalf("Receive() = %d, %v", len(batch), err)
					}
				}
			})
		}
	}
}

// benchBrokerIn is benchBroker, with the file broker's directory — empty for
// the memory broker.
func benchBrokerIn(b *testing.B, kind string) (corequeue.Broker, string) {
	b.Helper()
	if kind != "file" {
		return benchBroker(b, kind), ""
	}
	dir := b.TempDir()
	broker, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: dir, Policy: benchPolicy()})
	if err != nil {
		b.Fatalf("NewFile() = %v", err)
	}
	return broker, dir
}

// touchStates sets the mtime of the file broker's three state directories at
// dir, off the clock; nothing for the memory broker.
func touchStates(b *testing.B, dir string, at time.Time) {
	b.Helper()
	if dir == "" {
		return
	}
	b.StopTimer()
	for _, state := range []string{"ready", "inflight", "dead"} {
		if err := os.Chtimes(filepath.Join(dir, state), at, at); err != nil {
			b.Fatalf("Chtimes(%s) = %v", state, err)
		}
	}
	b.StartTimer()
}

// BenchmarkPublishMany measures a batch of n published in one call, per
// message, beside Publish's per-message figure.
func BenchmarkPublishMany(b *testing.B) {
	for _, kind := range benchKinds {
		for _, size := range []int{1, 16, 256} {
			b.Run(kind+"/"+strconv.Itoa(size), func(b *testing.B) {
				broker := benchBroker(b, kind).(corequeue.BatchPublisher)
				payloads := make([][]byte, size)
				for index := range payloads {
					payloads[index] = make([]byte, 64)
				}
				b.ReportAllocs()
				for range b.N {
					if _, err := broker.PublishMany(context.Background(), payloads); err != nil {
						b.Fatalf("PublishMany() = %v", err)
					}
				}
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*size), "ns/msg")
			})
		}
	}
}

// BenchmarkAckMany measures a hundred leases acknowledged in one call, per
// message, beside Ack's per-message figure.
func BenchmarkAckMany(b *testing.B) {
	const batch = 100
	for _, kind := range benchKinds {
		b.Run(kind+"/100", func(b *testing.B) {
			broker := benchBroker(b, kind)
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				prime(b, broker, batch, 64)
				receipts := leaseAll(b, broker, batch)
				if _, err := broker.(corequeue.Acker).AckMany(ctx, receipts); err != nil {
					b.Fatalf("AckMany() = %v", err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*batch), "ns/msg")
		})
	}
}

// BenchmarkSQL measures the SQL broker's own cost per verb on PostgreSQL's
// statements over the fake engine: the allocations and the statements are
// the broker's, the nanoseconds the fake's. The real engines' figures are
// e2e/integration/sql's.
func BenchmarkSQL(b *testing.B) {
	clk := clock.System
	verbs := []struct {
		name string
		run  func(b *testing.B, broker corequeue.Broker)
	}{
		{"publish", func(b *testing.B, broker corequeue.Broker) {
			if _, err := broker.Publish(context.Background(), make([]byte, 64)); err != nil {
				b.Fatalf("Publish() = %v", err)
			}
		}},
		{"roundtrip", func(b *testing.B, broker corequeue.Broker) {
			ctx := context.Background()
			if _, err := broker.Publish(ctx, make([]byte, 64)); err != nil {
				b.Fatalf("Publish() = %v", err)
			}
			batch, err := broker.Receive(ctx, 1)
			if err != nil || len(batch) != 1 {
				b.Fatalf("Receive() = %d, %v", len(batch), err)
			}
			if err := broker.Ack(ctx, batch[0].Lease.Receipt); err != nil {
				b.Fatalf("Ack() = %v", err)
			}
		}},
		{"publishmany16", func(b *testing.B, broker corequeue.Broker) {
			payloads := make([][]byte, 16)
			for index := range payloads {
				payloads[index] = make([]byte, 64)
			}
			if _, err := broker.(corequeue.BatchPublisher).PublishMany(context.Background(), payloads); err != nil {
				b.Fatalf("PublishMany() = %v", err)
			}
		}},
		{"stats", func(b *testing.B, broker corequeue.Broker) {
			if _, err := broker.(corequeue.Counter).Stats(context.Background()); err != nil {
				b.Fatalf("Stats() = %v", err)
			}
		}},
	}
	for _, verb := range verbs {
		b.Run("postgres/"+verb.name, func(b *testing.B) {
			engine := newSQLEngine(coresql.DialectPostgres, jobsTable, true)
			broker, err := svcqueue.NewSQL(svcqueue.SQLConfig{
				Transactor: newSQLTransactor(b, engine, coresql.DialectPostgres), Dialect: coresql.DialectPostgres, Table: jobsTable,
				Policy: benchPolicy(), Clock: clk, Trail: true,
			})
			if err != nil {
				b.Fatalf("NewSQL() = %v", err)
			}
			b.ReportAllocs()
			for range b.N {
				verb.run(b, broker)
				//: the fake copies its table per statement: emptied off the
				//: clock, so the figure stays the broker's and not the fake's.
				b.StopTimer()
				if _, err := broker.(corequeue.Purger).Purge(context.Background()); err != nil {
					b.Fatalf("Purge() = %v", err)
				}
				b.StartTimer()
			}
		})
	}
}
