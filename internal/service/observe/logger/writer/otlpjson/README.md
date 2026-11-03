# otlpjson

Package `otlpjson` registers the `"otlpjson"` logger writer (#867): every
record becomes an OTLP `LogRecord` — the time, the severity (`level+9`, the
OpenTelemetry slog bridge's mapping), the message as the body, the typed
attributes (a group flattened under dotted keys), `traceId`/`spanId` when the
record was emitted inside a span — and each batch the async middleware
gathers is ONE OTLP/JSON `ExportLogsServiceRequest` handed to
`Config.Exporter`: the OTLP/HTTP logs exporter `NewOTLPHTTPExporter`
returns (the shared sender of the metrics and trace exporters, `/v1/logs`),
or `app/forward`'s `NewLogExporter`, which keeps it in a durable queue. Codes
`0.3.104.*` live in `internal/core/observe/logger/writer/otlpjson`. See
`CLAUDE.md`.
