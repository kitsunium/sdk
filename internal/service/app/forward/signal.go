package forward

import (
	"context"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
	svctrace "github.com/kitsunium/sdk/internal/service/observe/trace"
)

// spanExporter is the producer half for the trace signal: each batch
// encoded once, as the OTLP/HTTP exporter would post it, and kept.
type spanExporter struct {
	// forwarder keeps the encoded batches.
	forwarder *Forwarder
	// name is the exporter's name.
	name coretrace.ExporterName
}

// metricExporter is the producer half for the metrics signal.
type metricExporter struct {
	// forwarder keeps the encoded snapshots.
	forwarder *Forwarder
	// name is the exporter's name.
	name coremetrics.ExporterName
}

// NewSpanExporter returns a trace ContextSpanExporter that encodes each
// batch as an OTLP/JSON ExportTraceServiceRequest and keeps it in forwarder,
// returning once it is durable — behind a trace BatchSink, an export costs
// one publication instead of a round trip to the collector. The forwarder's
// Send is the OTLP/HTTP exporter's ExportDocument. A nil forwarder is
// refused with ForwardMisconfigured.
func NewSpanExporter(name coretrace.ExporterName, forwarder *Forwarder) (coretrace.ContextSpanExporter, error) {
	//: nothing to keep the batches in.
	if forwarder == nil {
		//: named by argument.
		return nil, misconfigured("forwarder", "missing")
	}
	//: bound.
	return &spanExporter{forwarder: forwarder, name: name}, nil
}

// NewMetricExporter returns a metrics ContextExporter that encodes each
// snapshot as an OTLP/JSON ExportMetricsServiceRequest and keeps it in
// forwarder, returning once it is durable. The forwarder's Send is the
// OTLP/HTTP exporter's ExportDocument. A nil forwarder is refused with
// ForwardMisconfigured.
func NewMetricExporter(name coremetrics.ExporterName, forwarder *Forwarder) (coremetrics.ContextExporter, error) {
	//: nothing to keep the snapshots in.
	if forwarder == nil {
		//: named by argument.
		return nil, misconfigured("forwarder", "missing")
	}
	//: bound.
	return &metricExporter{forwarder: forwarder, name: name}, nil
}

// Name implements core/observe/trace.SpanExporter.
func (e *spanExporter) Name() coretrace.ExporterName {
	//: the configured name.
	return e.name
}

// Export is ExportContext under a context that never ends.
func (e *spanExporter) Export(spans coretrace.SpansValue) error {
	//: the forwarder's own bounds still apply.
	return e.ExportContext(context.Background(), spans)
}

// ExportContext encodes spans and keeps them; an empty batch keeps nothing.
func (e *spanExporter) ExportContext(ctx context.Context, spans coretrace.SpansValue) error {
	//: nothing to keep.
	if spans.IsEmpty() {
		//: no publication.
		return nil
	}
	doc, err := svctrace.EncodeOTLPJSON(spans)
	//: an unencodable batch is never kept.
	if err != nil {
		//: the encoder's typed refusal.
		return err
	}
	//: kept, or refused by the bounds.
	return e.forwarder.Enqueue(ctx, doc)
}

// Name implements core/observe/metrics.Exporter.
func (e *metricExporter) Name() coremetrics.ExporterName {
	//: the configured name.
	return e.name
}

// Export is ExportContext under a context that never ends.
func (e *metricExporter) Export(snap coremetrics.SnapshotValue) error {
	//: the forwarder's own bounds still apply.
	return e.ExportContext(context.Background(), snap)
}

// ExportContext encodes snap and keeps it.
func (e *metricExporter) ExportContext(ctx context.Context, snap coremetrics.SnapshotValue) error {
	doc, err := svcmetrics.EncodeOTLPJSON(snap)
	//: an unrepresentable snapshot is never kept.
	if err != nil {
		//: the encoder's typed refusal.
		return err
	}
	//: kept, or refused by the bounds.
	return e.forwarder.Enqueue(ctx, doc)
}

// logExporter is the producer half for the logs signal: the "otlpjson"
// writer has encoded the batch already, so the document is kept as it is.
type logExporter struct {
	// forwarder keeps the documents.
	forwarder *Forwarder
}

// NewLogExporter returns a logger DocumentExporter that keeps each
// OTLP/JSON logs document the "otlpjson" writer hands it in forwarder,
// returning once it is durable — the logs signal's adapter, as
// NewSpanExporter and NewMetricExporter are the other two's; the writer
// has encoded the batch already. The forwarder's Send is the OTLP/HTTP logs
// exporter's ExportDocument. A nil forwarder is refused with
// ForwardMisconfigured (#867).
func NewLogExporter(forwarder *Forwarder) (corelogger.DocumentExporter, error) {
	//: nothing to keep the documents in.
	if forwarder == nil {
		//: named by argument.
		return nil, misconfigured("forwarder", "missing")
	}
	//: bound.
	return &logExporter{forwarder: forwarder}, nil
}

// ExportDocument keeps doc, or refuses it by the forwarder's bounds; an
// empty document keeps nothing.
func (e *logExporter) ExportDocument(ctx context.Context, doc []byte) error {
	//: Enqueue copies what it publishes, so doc is not retained.
	return e.forwarder.Enqueue(ctx, doc)
}
