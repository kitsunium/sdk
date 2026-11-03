package forward_test

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"

	coreforward "github.com/kitsunium/sdk/internal/core/app/forward"
	svcforward "github.com/kitsunium/sdk/internal/service/app/forward"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// otlpLike is a batch shaped as OTLP/JSON spans are: random hex identifiers
// and timestamps, attribute keys repeated, which is what gzip works on.
func otlpLike(n int) string {
	var b strings.Builder
	b.WriteString(`{"resourceSpans":[{"scopeSpans":[{"spans":[`)
	rng := rand.New(rand.NewPCG(uint64(n), 1))
	for i := range n {
		fmt.Fprintf(&b, `{"traceId":"%016x%016x","spanId":"%016x","name":"GET /items/%d","startTimeUnixNano":"%d","attributes":[{"key":"http.method","value":{"stringValue":"GET"}},{"key":"http.status_code","value":{"intValue":"200"}}]},`,
			rng.Uint64(), rng.Uint64(), rng.Uint64(), i, 1_761_000_000_000_000_000+rng.Uint64N(1_000_000_000))
	}
	b.WriteString(`{}]}]}]}`)
	return b.String()
}

// encoded records what SendEncoded is handed: each batch and its encoding.
type encoded struct {
	calls chan [2]string
}

// send is the EncodedSender.
func (e *encoded) send(_ context.Context, batch []byte, encoding string) error {
	e.calls <- [2]string{string(batch), encoding}
	return nil
}

// await returns the next call.
func (e *encoded) await(t *testing.T) (batch, encoding string) {
	t.Helper()
	got := <-e.calls
	return got[0], got[1]
}

// gunzip decompresses a gzip batch, failing the case when it is not one.
func gunzip(t *testing.T, batch string) string {
	t.Helper()
	zr, err := gzip.NewReader(strings.NewReader(batch))
	if err != nil {
		t.Fatalf("not a gzip batch: %v", err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return string(plain)
}

// TestCompressKeepsTheBatchGzippedAndSendsItEncoded (#1051): with Compress
// the queue holds the gzip bytes — PendingBytes counts them, a fraction of
// the document — and SendEncoded receives them as they rest, labelled gzip.
func TestCompressKeepsTheBatchGzippedAndSendsItEncoded(t *testing.T) {
	enc := &encoded{calls: make(chan [2]string, 4)}
	fx := newFixture(t, svcforward.Config{Compress: true, SendEncoded: enc.send})
	doc := otlpLike(100)
	fx.enqueue(t, doc)
	stats := fx.forwarder.Stats()
	if stats.Pending != 1 || stats.PendingBytes <= 0 || stats.PendingBytes*2 > int64(len(doc)) {
		t.Fatalf("stats = %+v, want one batch resting in under half of its %d bytes", stats, len(doc))
	}
	fx.run(t)
	batch, encoding := enc.await(t)
	if encoding != svcforward.EncodingGzip || int64(len(batch)) != stats.PendingBytes {
		t.Fatalf("SendEncoded got %d bytes labelled %q, want the %d resting bytes labelled gzip", len(batch), encoding, stats.PendingBytes)
	}
	if gunzip(t, batch) != doc {
		t.Fatal("the gzip batch does not decompress to the document enqueued")
	}
	fx.awaitStats(t, "sent", func(s svcforward.StatsValue) bool { return s.Sent == 1 && s.PendingBytes == 0 })
	select {
	case got := <-fx.sender.calls:
		t.Errorf("Send was called beside SendEncoded with %q", got)
	default:
	}
}

// TestSendEncodedShipsAPlainBatchUnlabelled: without Compress a batch rests
// as it came and SendEncoded receives it with the identity encoding.
func TestSendEncodedShipsAPlainBatchUnlabelled(t *testing.T) {
	enc := &encoded{calls: make(chan [2]string, 4)}
	fx := newFixture(t, svcforward.Config{SendEncoded: enc.send})
	fx.enqueue(t, "batch-1")
	fx.run(t)
	if batch, encoding := enc.await(t); batch != "batch-1" || encoding != "" {
		t.Fatalf("SendEncoded got %q labelled %q, want batch-1 unlabelled", batch, encoding)
	}
}

// TestCompressRequiresSendEncoded: a gzip batch handed to Send would leave
// labelled as JSON, so Compress without SendEncoded is refused by name; and
// SendEncoded alone stands in for Send.
func TestCompressRequiresSendEncoded(t *testing.T) {
	broker, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Policy: testPolicy})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	send := func(context.Context, []byte) error { return nil }
	_, err = svcforward.New(context.Background(), svcforward.Config{Broker: broker, Send: send, Retryable: retryable, Compress: true})
	if !errors.Is(err, coreforward.ForwardMisconfigured) || field0(err, "field") != "SendEncoded" {
		t.Fatalf("Compress without SendEncoded = %v, want ForwardMisconfigured on SendEncoded", err)
	}
	sendEncoded := func(context.Context, []byte, string) error { return nil }
	if _, err := svcforward.New(context.Background(), svcforward.Config{Broker: broker, SendEncoded: sendEncoded, Retryable: retryable, Compress: true}); err != nil {
		t.Fatalf("SendEncoded without Send = %v, want nil", err)
	}
}

// TestSendEncodedDrainsAGzipBacklogWithCompressOff: a backlog a previous
// process kept with Compress is still handed labelled gzip by a forwarder
// that no longer compresses, beside the plain batches it keeps itself.
func TestSendEncodedDrainsAGzipBacklogWithCompressOff(t *testing.T) {
	dir := t.TempDir()
	clk := newTimerClock()
	enc := &encoded{calls: make(chan [2]string, 4)}
	doc := otlpLike(50)
	first := newFixtureOn(t, clk, fileBroker(t, clk, dir), svcforward.Config{Compress: true, SendEncoded: enc.send})
	first.enqueue(t, doc)
	closeBroker(t, first.broker)

	second := newFixtureOn(t, clk, fileBroker(t, clk, dir), svcforward.Config{SendEncoded: enc.send})
	t.Cleanup(func() { closeBroker(t, second.broker) })
	second.enqueue(t, "plain")
	second.run(t)
	got := map[string]string{}
	for range 2 {
		batch, encoding := enc.await(t)
		if encoding == svcforward.EncodingGzip {
			batch = gunzip(t, batch)
		}
		got[batch] = encoding
	}
	if got[doc] != svcforward.EncodingGzip || got["plain"] != "" {
		t.Fatalf("encodings = %v, want the old batch gzip and the new one plain", got)
	}
	second.awaitStats(t, "both sent", func(s svcforward.StatsValue) bool { return s.Sent == 2 && s.Pending == 0 })
}

// TestTheBatchesSurviveARestartCompressed is TestTheBatchesSurviveARestart
// with Compress on: the gzip batches the first process kept are counted and
// sent by the second, as they rest.
func TestTheBatchesSurviveARestartCompressed(t *testing.T) {
	dir := t.TempDir()
	clk := newTimerClock()
	enc := &encoded{calls: make(chan [2]string, 8)}
	cfg := svcforward.Config{MaxBatches: 3, Compress: true, SendEncoded: enc.send}
	first := newFixtureOn(t, clk, fileBroker(t, clk, dir), cfg)
	for i := range 3 {
		first.enqueue(t, fmt.Sprintf("batch-%d", i))
	}
	kept := first.forwarder.Stats().PendingBytes
	closeBroker(t, first.broker)

	second := newFixtureOn(t, clk, fileBroker(t, clk, dir), cfg)
	t.Cleanup(func() { closeBroker(t, second.broker) })
	if stats := second.forwarder.Stats(); stats.Pending != 3 || stats.PendingBytes != kept {
		t.Fatalf("after the restart stats = %+v, want the three batches (%d bytes) the last process kept", stats, kept)
	}
	second.run(t)
	sent := map[string]bool{}
	for range 3 {
		batch, encoding := enc.await(t)
		if encoding != svcforward.EncodingGzip {
			t.Fatalf("a batch was handed with %q, want gzip", encoding)
		}
		sent[gunzip(t, batch)] = true
	}
	for i := range 3 {
		if !sent[fmt.Sprintf("batch-%d", i)] {
			t.Errorf("batch-%d was not sent after the restart: %v", i, sent)
		}
	}
	second.awaitStats(t, "all sent", func(s svcforward.StatsValue) bool { return s.Sent == 3 && s.Pending == 0 })
}
