<!-- updated: 2026-11-03T15:00:00Z -->
# internal/core/observe/logger/writer/otlpjson/

## Purpose

The codes and sentinels of the OTLP/JSON log writer, whose engine is
`internal/service/observe/logger/writer/otlpjson` (#867). The range
`0.3.104.*` was allocated to that engine and is declared here since ADR 0160
§2 puts every code of a service package in the core package at the same path:
the engine returns these sentinels and declares none. They are the logs
signal's words for the four verdicts the shared OTLP/HTTP sender reaches, as
`0.3.45.*` is the metrics signal's.

Nothing else lives here: the writer registers a `Factory` in
`internal/core/observe/logger/writer`, and its exporter implements
`internal/core/observe/logger`'s `DocumentExporter`.

## Surface

| Code | Constant | Sentinel | Reason |
|---|---|---|---|
| 0.3.104.1 | `CodeOTLPLogsEndpointInvalid` | `OTLPLogsEndpointInvalid` | `OTLP_LOGS_ENDPOINT_INVALID` — refused at construction |
| 0.3.104.2 | `CodeOTLPLogsExportRejected` | `OTLPLogsExportRejected` | `OTLP_LOGS_EXPORT_REJECTED` — permanent: a 4xx outside the retryable four, 415 included, or an unbuildable request |
| 0.3.104.3 | `CodeOTLPLogsExportUnavailable` | `OTLPLogsExportUnavailable` | `OTLP_LOGS_EXPORT_UNAVAILABLE` — retryable: transport fault, timeout, 429/502/503/504, a cancelled context |
| 0.3.104.4 | `CodeOTLPLogsPartialSuccess` | `OTLPLogsPartialSuccess` | `OTLP_LOGS_PARTIAL_SUCCESS` — records the collector refused; never retried |

`gen_codes.go` is kit's, from `design/observe/logger/writer/otlpjson.yaml`
(ADR 0164); the range is `design/sdk.yaml`'s `codes.ranges`.

## Do NOT

- Add a code in the engine: it belongs here, then `kit gen`.
- Echo a record or an endpoint into a sentinel's text (secret gate).
