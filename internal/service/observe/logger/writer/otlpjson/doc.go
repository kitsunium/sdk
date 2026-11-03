// Package otlpjson registers the "otlpjson" writer factory (#867): the third
// OpenTelemetry signal. Each record the logger emits becomes an OTLP LogRecord —
// timeUnixNano, severityNumber and severityText from the level (the
// OpenTelemetry slog bridge's level+9), the message as the string body, the
// attributes typed (a group flattened under dotted keys), traceId and spanId
// from the record's correlation fields (ADR 0062) and nothing when it has none —,
// the Resource and the InstrumentationScope once per request; the async
// middleware batches the records, and each batch is ONE
// ExportLogsServiceRequest handed to Config.Exporter. That exporter is the
// OTLP/HTTP logs exporter NewOTLPHTTPExporter returns — posted by the shared
// sender of the other two signals: bounded read, no redirect, the same
// verdicts, gzip on demand — or the store-and-forward adapter of app/forward,
// which keeps the batch on disk while the collector is down (ADR 0185).
package otlpjson
