//go:build integration

// The JetStream broker (third-party/nats/queue) against a real nats-server
// with JetStream: the queue conformance rows on the wall clock — the server
// keeps AckWait, so a row sleeps where it lets a lease lapse —, the race of
// two consumers over a thousand messages, and Nats-Msg-Id deduplication.
// Run from the e2e module:
//
//	cd e2e && GOWORK=off go test -tags integration -race ./integration/queue/nats/
//
// NATS_URL names a running server (started with -js) instead of a container.
package nats_test

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"

	"github.com/kitsunium/sdk/e2e/integration/internal/servers"
	"github.com/kitsunium/sdk/e2e/integration/queue/brokerrows"
	natsqueue "github.com/kitsunium/sdk/third-party/nats/queue"
)

// streamSeq names one stream per row.
var streamSeq atomic.Int64

func connect(t *testing.T) *nats.Conn {
	t.Helper()
	conn, err := nats.Connect(servers.NATS(t))
	if err != nil {
		t.Skipf("nats unavailable: %v", err)
	}
	t.Cleanup(conn.Close)
	return conn
}

func config(conn *nats.Conn, policy corequeue.PolicyValue) natsqueue.Config {
	name := "Q" + strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.FormatInt(streamSeq.Add(1), 10)
	return natsqueue.Config{
		Conn: conn, Stream: name, Subject: name + ".work", DeadLetterSubject: name + ".dead", Consumer: "workers",
		Policy: policy, Clock: clock.System, Timeout: 5 * time.Second, CreateStream: true,
	}
}

func TestIntegration_JetStreamBroker_conformance(t *testing.T) {
	conn := connect(t)
	brokerrows.Run(t, func(t *testing.T, policy corequeue.PolicyValue) brokerrows.Env {
		t.Helper()
		cfg := config(conn, policy)
		build := func() corequeue.Broker {
			broker, err := natsqueue.NewJetStream(cfg)
			if err != nil {
				t.Fatalf("NewJetStream = %v", err)
			}
			return broker
		}
		return brokerrows.Env{Broker: build(), Advance: time.Sleep, Sibling: build}
	})
}

func TestIntegration_JetStreamBroker_publishOnceDeduplicates(t *testing.T) {
	conn := connect(t)
	cfg := config(conn, brokerrows.Policy())
	broker, err := natsqueue.NewJetStream(cfg)
	if err != nil {
		t.Fatalf("NewJetStream = %v", err)
	}
	once := broker.(corequeue.OncePublisher)
	for i, want := range []bool{true, false} {
		_, published, err := once.PublishOnce(t.Context(), "order-42", []byte("charge"), natsqueue.DefaultDuplicateWindow)
		if err != nil || published != want {
			t.Fatalf("PublishOnce #%d = (%v, %v), want (%v, nil)", i, published, err, want)
		}
	}
	if _, _, err := once.PublishOnce(t.Context(), "k", nil, time.Hour); err == nil {
		t.Fatal("PublishOnce with a window that is not the stream's = nil, want QUEUE_MISCONFIGURED")
	}
	js, err := jetstream.New(conn)
	if err != nil {
		t.Fatalf("jetstream.New = %v", err)
	}
	stream, err := js.Stream(t.Context(), cfg.Stream)
	if err != nil {
		t.Fatalf("Stream = %v", err)
	}
	info, err := stream.Info(t.Context())
	if err != nil || info.State.Msgs != 1 {
		t.Fatalf("StreamInfo.State.Msgs = %v (%v), want 1", info, err)
	}
}

func TestIntegration_JetStreamBroker_missingStreamIsTyped(t *testing.T) {
	conn := connect(t)
	cfg := config(conn, brokerrows.Policy())
	cfg.CreateStream = false
	broker, err := natsqueue.NewJetStream(cfg)
	if err != nil {
		t.Fatalf("NewJetStream = %v", err)
	}
	if _, err := broker.Receive(t.Context(), 1); !errs.HasCode(err, natsqueue.CodeJetStreamStreamMissing) {
		t.Fatalf("Receive on a missing stream = %v, want STREAM_MISSING", err)
	}
}
