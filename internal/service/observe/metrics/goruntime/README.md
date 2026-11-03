# goruntime

Package `goruntime` publishes the Go runtime's own instruments on a metrics
meter (#868), under the OpenTelemetry Go runtime semantic conventions:
`RuntimeInstruments` registers `go.memory.*`, `go.goroutine.count`,
`go.processor.limit`, `go.config.gogc` and, on a meter that takes an
observable histogram, `go.schedule.duration` and `go.gc.duration`. It reads
`runtime/metrics`, so it stands apart from the metrics domain, which
`framework/app` imports. See `CLAUDE.md`.
