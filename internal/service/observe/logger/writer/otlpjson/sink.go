package otlpjson

import (
	"context"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	coreotlpjson "github.com/kitsunium/sdk/internal/core/observe/logger/writer/otlpjson"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// otlpSink encodes each batch the async drainer hands it into one OTLP/JSON
// document and gives it to the exporter. The document buffer and the
// one-record scratch are reused under mu, so the steady-state path allocates
// nothing of its own.
type otlpSink struct {
	// exporter receives each document.
	exporter corelogger.DocumentExporter
	// resource and scope are rendered once per document.
	resource otlp.ResourceMessage
	scope    otlp.ScopeMessage
	// mu serialises encoding into doc and the export of it.
	mu sync.Mutex
	// doc is the reused document buffer; one is the scratch Write uses.
	doc []byte
	one [1]corelogger.RecordEvent
}

// newSink builds the terminal sink of cfg.
func newSink(cfg *Config) *otlpSink {
	//: the identity rendered once, here.
	return &otlpSink{exporter: cfg.Exporter, resource: otlp.ResourceOf(cfg.Resource), scope: otlp.ScopeOf(cfg.Scope)}
}

// Write exports r as a document of one record. The async drainer reaches it
// only when it holds a single record; a backlog goes to WriteBatch.
func (s *otlpSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	s.mu.Lock()
	s.one[0] = r
	err = s.export(ctx, s.one[:])
	s.one[0] = corelogger.RecordEvent{}
	s.mu.Unlock()
	//: the record was not delivered.
	if err != nil {
		return 0, err
	}
	//: the encoded line's length, as every sink reports.
	return len(p), nil
}

// WriteBatch exports recs as ONE document: delivered whole or not at all,
// so a refusal reports none of them written — the async drainer then
// reports the failure once per record.
func (s *otlpSink) WriteBatch(ctx context.Context, recs []corelogger.RecordEvent, _ [][]byte) (written int, err error) {
	s.mu.Lock()
	err = s.export(ctx, recs)
	s.mu.Unlock()
	//: none of the batch was delivered.
	if err != nil {
		return 0, err
	}
	//: every record.
	return len(recs), nil
}

// export encodes recs into the reused buffer and hands it to the exporter.
// The caller holds s.mu.
func (s *otlpSink) export(ctx context.Context, recs []corelogger.RecordEvent) error {
	//: a cancelled context exports nothing.
	if ctx != nil && ctx.Err() != nil {
		return wrapCancelled(ctx.Err())
	}
	s.doc = appendRequest(s.doc[:0], s.resource, s.scope, recs)
	//: the exporter's verdict, under its own code.
	return s.exporter.ExportDocument(ctx, s.doc)
}

// Flush is a no-op: every batch is exported as it arrives.
func (s *otlpSink) Flush(ctx context.Context) error {
	//: a cancelled context is still reported.
	if ctx != nil && ctx.Err() != nil {
		return wrapCancelled(ctx.Err())
	}
	//: nothing held.
	return nil
}

// Close releases nothing: the exporter is the caller's.
func (s *otlpSink) Close() error {
	//: nothing owned.
	return nil
}

// wrapCancelled is a cancelled context under the transient verdict: the
// batch was not sent and a later attempt may be.
func wrapCancelled(cause error) error {
	//: retryable, as a transport fault is.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    coreotlpjson.CodeOTLPLogsExportUnavailable,
		Reason:  "OTLP_LOGS_EXPORT_UNAVAILABLE",
		Public:  "The OTLP collector is unreachable or overloaded",
		Private: "service/observe/logger/writer/otlpjson: the context ended before the batch was exported",
	})
}
