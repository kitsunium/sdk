# Notes — <!-- updated: 2026-11-03T12:00:00Z -->

Reference moved out of `CLAUDE.md` (ADR 0253), unchanged and under the same section titles. `CLAUDE.md` keeps what to obey; this file holds the rest.

## Contents

```
doc.go         — the package comment; kit writes it from design/observe/logger.yaml
                 (ADR 0167)
gen_facade.go  — kit's (ADR 0166), from design/observe/logger.yaml's facade:: every
                 alias, re-export and forwarder the files below name, and what
                 builder.go, caller.go, kind.go, levelvar.go and memory.go held before
                 their package comments joined their neighbours' — Builder alias,
                 Build, LogAttrs (slice overload), Kind alias + its nine constants,
                 WithCaller, Leveler / LevelVar aliases, NewLevelVar, ParseLevel
                 (strict; never echoes its input), RecordSnapshot / MemorySink
                 aliases, NewMemorySink (the test sink), BatchSink alias (#807),
                 LevelAmbiguous (0.3.1.5, re-exported, #1015); the logs signal
                 (#867): DocumentExporter / EncodedDocumentExporter aliases,
                 OTLPConfig (the "otlpjson" writer's Config) / OTLPHTTPConfig
                 aliases, NewOTLPHTTPExporter, OTLPRetryable — importing this
                 package registers the "otlpjson" writer
logger.go      — Logger / Attr / Level aliases, 4 Level constants, Config struct,
                 NewText, Default, DefaultMulti (console + one file, through NewMulti),
                 Debug|Info|Warn|Error emission helpers,
                 String|Int|Bool|Float64|Int64|Uint64|Duration|Time|Any Attr constructors
sink.go        — Sink / Record / Encoder aliases, SinkConfig struct (MinLevel, or Level —
                 a live Leveler such as a LevelVar, #1015; both set is
                 LevelAmbiguous), NewWithSink + handlerFor,
                 Multi fan-out helper, LevelGate (a per-branch floor over
                 writer/levelgate.Floor — ADR 0132), NewWriterSink (an io.Writer as
                 a Sink), ConsoleStderr|ConsoleStdout, TextEncoder
                 (ConsoleConfig{} / StreamStderr is the zero value — ADR 0030)
writer.go      — WriterName / *Config / ConsoleStream aliases, StreamStderr|StreamStdout,
                 CredentialProvider / CredentialValue aliases + NewCredentialValue,
                 WriterSpec, NewMulti (named writers; the Logger it returns owns them
                 and is an io.Closer)
fromconfig.go  — Format alias, FromConfig (build a Logger from a config blob),
                 FromConfigWithLevel (the same, gated on a caller's LevelVar the
                 topology's level seeds, #1015), + parseLevel / decodeTopology /
                 resolveSinks / topologySinkConfig helpers (ADR 0014 §D5)
topology.go    — TopologyConfig DTO (Level + Writers)
writer_entry.go — WriterEntryConfig DTO (Name + raw Options map)
tracecontext.go — TraceContext / TraceContextSource aliases + TraceContextFromContext,
                 the ONLY bridge between the logging and tracing domains (ADR 0062)
version.go     — Version var (ldflags injection point), FrameworkVersion()
gen_codes.go   — CodeWriterRequired / CodeSinkConfigRequired / CodeWriterSpecInvalid
                 / CodeTopologyInvalid (range 1.1.0.*)
gen_codes.go   — WriterRequired / SinkConfigRequired / WriterSpecInvalid /
                 TopologyInvalid sentinels (errs.Define)
encoder.go     — NewTextEncoder / NewJSONEncoder (one JSON object per line, groups
                 flattened to dotted keys)
witherror.go   — WithError: an error as error.code / error.reason / error.public /
                 error.trail.<n> Attrs, or one error.message for a non-SDK error
BENCH.md       — the emit-path benchmarks (logger_bench_test.go), then a separate
                 trace-correlation run (ADR 0062), then the #1014 / #1015 run
                 (enabled_bench_test.go: the Leveler gate's cost)
USES.md        — the interactive use-case tabs a Go doc comment cannot render
*_test.go      — *_external_test.go suites, fromconfig / witherror internal suites,
                 the two `!race` *_integration_test.go differential allocation gates
                 (fanout, tracecontext), and the design contracts (ADR 0165):
                 perf_fixtures_test.go, hand-written, and gen_perf_test.go, kit's —
                 Build's floor of one allocation an emit and an extraction that
                 allocates nothing — all run by the alloc lane
```

`README.md` is the consumer-facing quickstart (text-on-stderr example, custom sink topology, Builder hot path).
