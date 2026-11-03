package otlpjson

import corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"

// otlpSink takes a backlog from the async drainer as one document, and the
// OTLP/HTTP exporter ships it (#867).
var (
	_ corelogger.BatchSink               = (*otlpSink)(nil)
	_ corelogger.DocumentExporter        = (*otlpHTTPExporter)(nil)
	_ corelogger.EncodedDocumentExporter = (*otlpHTTPExporter)(nil)
)
