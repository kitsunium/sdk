package otlpengine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/app/forward"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/metrics"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
	"github.com/kitsunium/sdk/pkg/v1/proc"
)

// The queue every signal's batches wait in: a lease the forwarder renews
// for as long as an attempt and its retries take, a margin above two
// attempts, and deliveries no outage reaches — a batch's budget is its age
// (ADR 0185) —; one batch at most maxQueueMessage bytes, below the export's
// own byte bound.
const (
	leaseMargin     time.Duration = 30 * time.Second
	maxDeliveries   int           = 1 << 30
	maxQueueMessage int64         = 64 << 20
)

// The exporters' names, as an SDK refusal names them.
const (
	spanExporterName   trace.ExporterName   = "app-otlp-traces"
	metricExporterName metrics.ExporterName = "app-otlp-metrics"
)

// scopeName is the instrumentation that produces the export: the
// framework's core, as its tracer says.
const scopeName = "github.com/kitsunium/sdk/framework/internal/app"

// Enable plugs the OTLP export into the framework.
func Enable() {
	plug.OTLP.Store(&plug.OTLPDriverConfig{Open: open})
}

// open opens the export cfg describes: a queue and a forwarder per signal —
// in cfg.Dir, one subdirectory each, else in memory, and in memory too where
// the SDK's durable queue refuses the platform —, the trace batch sink in
// front of the trace forwarder, and the meter the metrics are collected
// from. Nothing is sent before Run.
//
// IFACE-PLUGIN: the framework reaches the export through plug.OTLPExporter;
// this is its implementation.
func open(ctx context.Context, cfg *plug.OTLPConfig) (plug.OTLPExporter, error) {
	clk := clockOf(cfg.Clock)
	e := &export{clock: clk, interval: cfg.Interval, settle: cfg.SettleTimeout, onError: cfg.OnError}
	for _, name := range []string{plug.OTLPTraces, plug.OTLPMetrics} {
		s, err := openSignal(ctx, cfg, clk, name)
		if err != nil {
			return nil, errors.Join(err, e.close())
		}
		e.signals = append(e.signals, s)
	}
	resource := trace.Resource{Attrs: []trace.Attr{metrics.String(trace.ServiceNameKey, cfg.Service)}}
	scope := trace.Scope{Name: scopeName, Version: cfg.Version}
	spans, err := forward.NewSpanExporter(spanExporterName, e.signals[0].forwarder)
	if err == nil {
		e.batch, err = trace.NewBatchSink(trace.BatchSinkConfig{Exporter: spans, Resource: resource, Scope: scope, Clock: clk, OnError: e.report})
	}
	if err == nil {
		e.snapshots, err = forward.NewMetricExporter(metricExporterName, e.signals[1].forwarder)
	}
	if err != nil {
		return nil, errors.Join(err, e.close())
	}
	e.meter = metrics.NewMeterWithConfig(metrics.MeterConfig{Resource: resource, Scope: scope})
	e.observeStats()
	return e, nil
}

// openSignal opens one signal's queue and its forwarder, the backlog a
// previous process left counted.
func openSignal(ctx context.Context, cfg *plug.OTLPConfig, clk clock.Timed, name string) (*signal, error) {
	send, retryable, err := senderOf(cfg, name)
	if err != nil {
		return nil, err
	}
	broker, durable, err := openQueue(cfg, clk, name)
	if err != nil {
		return nil, err
	}
	f, err := forward.New(ctx, forward.Config{
		Broker: broker, Send: send, Retryable: retryable, Clock: clk,
		MaxBatches: cfg.MaxBatches, MaxBytes: cfg.MaxBytes, WhenFull: fullPolicyOf(cfg.WhenFull),
		SendTimeout: cfg.SendTimeout, MaxAge: cfg.MaxAge,
		DrainTimeout: cfg.DrainTimeout, SettleTimeout: cfg.SettleTimeout,
	})
	if err != nil {
		return nil, errors.Join(err, closeQueue(broker))
	}
	return &signal{name: name, broker: broker, forwarder: f, durable: durable, maxBatches: cfg.MaxBatches, maxBytes: cfg.MaxBytes}, nil
}

// openQueue opens one signal's queue: the SDK's durable queue in its
// subdirectory of cfg.Dir, and whether it is durable; a memory queue without
// a directory, and where the durable queue refuses the platform — an export
// never stops the product it observes.
func openQueue(cfg *plug.OTLPConfig, clk clock.Timed, name string) (queue.Broker, bool, error) {
	policy := queue.Policy{
		VisibilityTimeout: 2*cfg.SendTimeout + leaseMargin,
		MaxDeliveries:     maxDeliveries,
		MaxMessageBytes:   int(min(cfg.MaxBytes, maxQueueMessage)),
	}
	if cfg.Dir != "" {
		if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
			return nil, false, err
		}
		b, err := queue.NewFile(queue.FileConfig{Clock: clk, Dir: filepath.Join(cfg.Dir, name), Policy: policy})
		if !errors.Is(err, proc.UnsupportedPlatform) {
			return b, err == nil, err
		}
	}
	b, err := queue.NewMemory(queue.MemoryConfig{Clock: clk, Policy: policy})
	return b, false, err
}

// senderOf is how one signal's batches are shipped, and how a failure is
// classified: the product's own sender when it gave one, else the signal's
// OTLP/HTTP exporter at the endpoint, the signal's path appended, whose
// ExportDocument posts a stored document as it is.
func senderOf(cfg *plug.OTLPConfig, name string) (forward.Sender, func(error) bool, error) {
	if own := cfg.Send; own != nil {
		retryable := cfg.Retryable
		if retryable == nil {
			retryable = func(error) bool { return true }
		}
		return func(ctx context.Context, batch []byte) error {
			return own(ctx, plug.OTLPBatchValue{Signal: name, Document: batch})
		}, retryable, nil
	}
	base := strings.TrimRight(cfg.Endpoint, "/")
	if name == plug.OTLPTraces {
		ex, err := trace.NewOTLPHTTPExporter(spanExporterName, trace.OTLPHTTPConfig{Endpoint: base + trace.OTLPTracesPath, Headers: cfg.Headers, Timeout: cfg.SendTimeout})
		if err != nil {
			return nil, nil, err
		}
		doc, ok := ex.(trace.DocumentExporter)
		if !ok {
			return nil, nil, lacks(name)
		}
		return doc.ExportDocument, trace.OTLPRetryable, nil
	}
	ex, err := metrics.NewOTLPHTTPExporter(metricExporterName, metrics.OTLPHTTPConfig{Endpoint: base + metrics.OTLPMetricsPath, Headers: cfg.Headers, Timeout: cfg.SendTimeout})
	if err != nil {
		return nil, nil, err
	}
	doc, ok := ex.(metrics.DocumentExporter)
	if !ok {
		return nil, nil, lacks(name)
	}
	return doc.ExportDocument, metrics.OTLPRetryable, nil
}

// lacks is the refusal of an OTLP/HTTP exporter that cannot post a stored
// document.
func lacks(name string) error {
	return errs.Wrap(forward.Misconfigured, errs.WrapParams{}, errs.String("signal", name), errs.String("problem", "no DocumentExporter"))
}

// fullPolicyOf is the setting's spelling as the forwarder's policy; the
// setting's check refused any other.
func fullPolicyOf(name string) forward.FullPolicy {
	switch name {
	case plug.OTLPDropNewest:
		return forward.DropNewest
	case plug.OTLPBlock:
		return forward.Block
	}
	return forward.DropOldest
}

// closeQueue releases what a queue holds — a durable queue's directory —;
// nothing for one that holds none.
func closeQueue(b queue.Broker) error {
	if c, ok := b.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}
