package otlpengine

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/forward"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/concur/group"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/observe/metrics"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
)

// The instruments the framework records of each unit of work it observed,
// and those it reports of its own export (issue #483). A node and an
// operation are the graph's, bounded by what the product declares.
const (
	runsMetric     = "app.node.runs"
	failuresMetric = "app.node.failures"
	durationMetric = "app.node.duration"

	pendingMetric        = "app.otlp.pending"
	pendingBytesMetric   = "app.otlp.pending_bytes"
	sentMetric           = "app.otlp.sent"
	retriesMetric        = "app.otlp.retries"
	droppedMetric        = "app.otlp.dropped"
	expiredMetric        = "app.otlp.expired"
	deadLetteredMetric   = "app.otlp.dead_lettered"
	lastRetryAfterMetric = "app.otlp.last_retry_after"
)

// The attributes of the instruments.
const (
	nodeAttr   = "app.node"
	opAttr     = "app.op"
	signalAttr = "app.otlp.signal"
)

// durationBuckets are the duration histogram's bounds, in seconds: the
// OpenTelemetry semantic conventions' advice for http.server.request.duration.
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}

// export is an app's export, open: a queue and a forwarder per signal, the
// trace batch sink in front of the trace forwarder, the meter the metrics
// are collected from, and the instruments of each node and operation.
type export struct {
	clock     clock.Timed
	signals   []*signal
	batch     *trace.BatchSink
	snapshots metrics.ContextExporter
	meter     metrics.FullMeter
	// product is the product's own meter (plug.OTLPExporter.Meter),
	// collected beside meter.
	product  metrics.FullMeter
	interval time.Duration
	settle   time.Duration
	onError  func(err error)
	// instruments are the instruments of each node and operation, made at
	// their first unit of work.
	instruments sync.Map
}

// signal is one signal's queue and its forwarder, and the bounds it holds.
type signal struct {
	name       string
	broker     queue.Broker
	forwarder  *forward.Forwarder
	maxBytes   int64
	maxBatches int
	durable    bool
}

// nodeInstruments are the instruments of one node and operation.
type nodeInstruments struct {
	runs, failures metrics.Counter
	duration       metrics.Histogram
}

// SpanSink is the trace batch sink's, as the app's tracer takes it.
func (e *export) SpanSink() plug.OTLPSinkValue { return plug.OTLPSinkValue{Sink: e.batch.Sink()} }

// Meter is the product's own meter, as the core takes it back.
func (e *export) Meter() plug.OTLPMeterValue { return plug.OTLPMeterValue{Meter: e.product} }

// Record counts one unit of work on node: one run, one failure when it
// failed, and how long it took.
func (e *export) Record(node, op string, took time.Duration, failed bool) {
	key := node + "\x00" + op
	got, ok := e.instruments.Load(key)
	if !ok {
		attrs := []metrics.Attr{metrics.String(nodeAttr, node), metrics.String(opAttr, op)}
		got, _ = e.instruments.LoadOrStore(key, &nodeInstruments{
			runs:     e.meter.Counter(runsMetric, attrs...),
			failures: e.meter.Counter(failuresMetric, attrs...),
			duration: e.meter.Histogram(durationMetric, durationBuckets, attrs...),
		})
	}
	in, _ := got.(*nodeInstruments)
	in.runs.Inc()
	if failed {
		in.failures.Inc()
	}
	in.duration.RecordDuration(took)
}

// Stats is what each signal's queue holds, has sent and has lost.
func (e *export) Stats() []plug.OTLPStatsValue {
	out := make([]plug.OTLPStatsValue, 0, len(e.signals))
	for _, s := range e.signals {
		st := s.forwarder.Stats()
		out = append(out, plug.OTLPStatsValue{
			LastRetryAfterAt: st.LastRetryAfterAt, Signal: s.name,
			PendingBytes: st.PendingBytes, MaxBytes: s.maxBytes, LastRetryAfter: st.LastRetryAfter,
			Pending: st.Pending, MaxBatches: s.maxBatches,
			Enqueued: st.Enqueued, Sent: st.Sent, Retries: st.Retries,
			Dropped: st.Dropped, Expired: st.Expired, DeadLettered: st.DeadLettered,
			Durable: s.durable,
		})
	}
	return out
}

// Run sends each signal's queued batches and queues the metrics every
// interval until ctx ends. Then it flushes the spans the batch sink holds
// and one last snapshot into the queues — each bounded by the settle —,
// stops the forwarders, which let the batch being sent finish within their
// drain, and closes the queues: what is still queued is the next process's.
//
// Goroutine lifecycle: one per forwarder and one collecting the metrics, in
// one group; Run waits for all of them before it returns.
func (e *export) Run(ctx context.Context) error {
	sending, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	g, gctx := group.NewJoined(sending, len(e.signals)+1)
	for _, s := range e.signals {
		g.Go(s.forwarder.Run)
	}
	collecting, stopCollecting := context.WithCancel(gctx)
	defer stopCollecting()
	stop := context.AfterFunc(ctx, stopCollecting)
	defer stop()
	g.Go(func(context.Context) error { e.collect(collecting); return nil })
	<-collecting.Done()
	flushErr := e.flush()
	cancel()
	return errors.Join(g.Wait(), flushErr, e.close())
}

// collect queues a snapshot of the meter every interval, on the app's
// clock, until ctx ends; a snapshot its queue refused is reported.
func (e *export) collect(ctx context.Context) {
	if e.interval <= 0 {
		return
	}
	t := e.clock.NewTicker(e.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-t.C():
			if !open {
				return
			}
			if err := e.queueSnapshots(ctx); err != nil {
				e.report(err)
			}
		}
	}
}

// report hands err to the core, which logs it.
func (e *export) report(err error) {
	if e.onError != nil {
		e.onError(err)
	}
}

// flush hands what the export holds in memory to the queues: the spans the
// batch sink holds, then one last snapshot, each within the settle on the
// app's clock.
func (e *export) flush() error {
	ctx, cancel := e.bounded()
	defer cancel()
	err := e.batch.Shutdown(ctx)
	ctx2, cancel2 := e.bounded()
	defer cancel2()
	return errors.Join(err, e.queueSnapshots(ctx2))
}

// queueSnapshots queues a snapshot of the framework's meter, then one of
// the product's when it holds any instrument: one batch each, the product's
// under its own scope.
func (e *export) queueSnapshots(ctx context.Context) error {
	err := e.snapshots.ExportContext(ctx, e.meter.Collect())
	if e.product == nil {
		return err
	}
	snap := e.product.Collect()
	if len(snap.Sums)+len(snap.Gauges)+len(snap.Histograms) == 0 {
		return err
	}
	return errors.Join(err, e.snapshots.ExportContext(ctx, snap))
}

// bounded is a context that ends once the settle passed on the app's clock.
//
// Goroutine lifecycle: one, which ends when the timer fires or the returned
// cancel is called.
func (e *export) bounded() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	timer := e.clock.NewTimer(max(e.settle, time.Millisecond))
	go func() {
		defer timer.Stop()
		select {
		case <-timer.C():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// close releases every queue the export opened.
func (e *export) close() error {
	var errs []error
	for _, s := range e.signals {
		errs = append(errs, closeQueue(s.broker))
	}
	return errors.Join(errs...)
}

// observeStats reports the export's own counts, per signal, at each
// collection: what it holds, has sent and has lost, and the last delay a
// collector imposed.
func (e *export) observeStats() {
	gauge := func(name string, read func(st *plug.OTLPStatsValue) float64) {
		e.meter.ObservableGauge(name, func(observe metrics.ObserveFloat64) {
			for _, st := range e.Stats() {
				observe(read(&st), metrics.String(signalAttr, st.Signal))
			}
		})
	}
	counter := func(name string, read func(st *plug.OTLPStatsValue) uint64) {
		e.meter.ObservableCounter(name, func(observe metrics.ObserveInt64) {
			for _, st := range e.Stats() {
				observe(int64(read(&st)), metrics.String(signalAttr, st.Signal))
			}
		})
	}
	gauge(pendingMetric, func(st *plug.OTLPStatsValue) float64 { return float64(st.Pending) })
	gauge(pendingBytesMetric, func(st *plug.OTLPStatsValue) float64 { return float64(st.PendingBytes) })
	gauge(lastRetryAfterMetric, func(st *plug.OTLPStatsValue) float64 { return st.LastRetryAfter.Seconds() })
	counter(sentMetric, func(st *plug.OTLPStatsValue) uint64 { return st.Sent })
	counter(retriesMetric, func(st *plug.OTLPStatsValue) uint64 { return st.Retries })
	counter(droppedMetric, func(st *plug.OTLPStatsValue) uint64 { return st.Dropped })
	counter(expiredMetric, func(st *plug.OTLPStatsValue) uint64 { return st.Expired })
	counter(deadLetteredMetric, func(st *plug.OTLPStatsValue) uint64 { return st.DeadLettered })
}
