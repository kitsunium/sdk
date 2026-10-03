# otlp

Package `otlp` is the OTLP machinery the metrics and trace signals share, written
once: the proto3-JSON scalars (`Int64`, `Uint64`, `Double`), the `KeyValue`,
`AnyValue`, `ResourceMessage` and `ScopeMessage` messages, the single-document
`Marshal`, the newline-delimited `Stream`, the lenient `DecodeRejected`, and the
OTLP/HTTP `Sender` — endpoint refusal, a redirect-refusing default client on a
pool of its own, bounded reads and drain, and the three verdicts. It declares
no error code: each signal passes a `SignalSpec` carrying its sentinels, wraps and
field names, so every error leaves under the calling signal's code and wording.
Internal to `internal/service`; used by `internal/service/observe/metrics` and
`internal/service/observe/trace`. ADR 0048, ADR 0051. See `CLAUDE.md`.
