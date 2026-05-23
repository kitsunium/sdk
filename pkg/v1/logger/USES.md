<!--
  Per-package "Use cases" — INTERACTIVE tabs (HTML). The narrative
  "Goals" + "What's shipped" tables live in the Go doc comment of
  logger.go (so pkg.go.dev + README.md show them too); only the HTML
  tabs need a hand-authored file because raw HTML inside a Go doc
  comment renders as literal text on pkg.go.dev.

  Code blocks inside each tabpanel use markdown fences with blank
  lines around them so astro-expressive-code processes them at build
  time (Shiki github-dark + copy-icon button — same as Quick start).
-->

## Use cases

The logger composes a Sink (where the bytes go) with an Encoder (how
the bytes are formatted). Pick the topology that fits.

<div class="tabs" data-tabs>
<div class="tab-strip" role="tablist">
<button type="button" role="tab" id="lg-default-btn" aria-controls="lg-default" aria-selected="true" tabindex="0" class="active">Default (stderr)</button>
<button type="button" role="tab" id="lg-multiwriter-btn" aria-controls="lg-multiwriter" aria-selected="false" tabindex="-1">Multi-writer (stderr + file)</button>
<button type="button" role="tab" id="lg-file-btn" aria-controls="lg-file" aria-selected="false" tabindex="-1">File only</button>
<button type="button" role="tab" id="lg-multi-btn" aria-controls="lg-multi" aria-selected="false" tabindex="-1">Multi (custom sinks)</button>
<button type="button" role="tab" id="lg-build-btn" aria-controls="lg-build" aria-selected="false" tabindex="-1">Build (hot path)</button>
<button type="button" role="tab" id="lg-custom-btn" aria-controls="lg-custom" aria-selected="false" tabindex="-1">Custom sink</button>
</div>

<div role="tabpanel" id="lg-default" aria-labelledby="lg-default-btn">

**Best for:** bootstrap, CLI tools, container apps that already log to stderr.

```go
lg, err := logger.Default()
if err != nil { panic(err) } // returns WriterRequired only on nil Writer

ctx := context.Background()
logger.Info(ctx, lg, "service started",
    logger.String("env", "prod"),
    logger.Int("port", 8080),
)
```

</div>

<div role="tabpanel" id="lg-multiwriter" aria-labelledby="lg-multiwriter-btn" hidden>

**Best for:** "log to stderr AND to a file" — the most common ops pattern. Use the new `Writers []io.Writer` field of `Config` and `NewText` wires the fan-out for you (no `NewWithSink` dance).

```go
f, err := os.OpenFile("/var/log/myapp.log",
    os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
if err != nil { panic(err) }

lg, err := logger.NewText(logger.Config{
    Writers:  []io.Writer{os.Stderr, f}, // any number of io.Writers
    MinLevel: logger.LevelInfo,
})
// Records broadcast to BOTH writers, atomic per-line on each.
```

</div>

<div role="tabpanel" id="lg-file" aria-labelledby="lg-file-btn" hidden>

**Best for:** production daemons that ship logs to a single on-disk file (with logrotate / fluentd reading it). Same `NewText` path — just one writer.

```go
f, err := os.OpenFile("/var/log/myapp.log",
    os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
if err != nil { panic(err) }

lg, err := logger.NewText(logger.Config{
    Writer:   f,
    MinLevel: logger.LevelInfo,
})
```

</div>

<div role="tabpanel" id="lg-multi" aria-labelledby="lg-multi-btn" hidden>

**Best for:** fan-out across heterogeneous sinks — stderr + a remote syslog + a custom DB sink at once. When the branches aren't all plain `io.Writer`s, drop to `NewWithSink` and compose with `Multi`. Errors from any branch join under `FANOUT_WRITE_FAILED`.

```go
// NewWriterSink wraps any io.Writer (file, bytes.Buffer, net.Conn, …)
// as a Sink so Multi can fold it alongside custom Sinks.
fileSink, _ := logger.NewWriterSink(myFile)

lg, err := logger.NewWithSink(logger.SinkConfig{
    Sink: logger.Multi(
        logger.ConsoleStderr(),
        fileSink,
        remoteSyslogSink, // your custom Sink implementation
    ),
    Encoder: logger.TextEncoder(),
})
```

</div>

<div role="tabpanel" id="lg-build" aria-labelledby="lg-build-btn" hidden>

**Best for:** the zero-allocation hot path. The chainable builder is backed by a `sync.Pool`; steady-state per-call cost is 0 allocs once the pool is warm. Don't reuse the builder after `Send` — it returns to the recycler.

```go
logger.Build(lg, logger.LevelInfo).
    Str("env", "prod").
    Int("port", 8080).
    Send(ctx, "service started")
```

</div>

<div role="tabpanel" id="lg-custom" aria-labelledby="lg-custom-btn" hidden>

**Best for:** push to a database / external observability stack. Implement the three-method `logger.Sink` interface and plug it through `NewWithSink`.

```go
type sqlSink struct{ db *sql.DB }

func (s *sqlSink) Write(ctx context.Context, r logger.Record, p []byte) (int, error) {
    _, err := s.db.ExecContext(ctx,
        "INSERT INTO logs(level, msg, payload) VALUES($1, $2, $3)",
        r.Level, r.Message, p)
    return len(p), err
}
func (s *sqlSink) Flush(_ context.Context) error { return nil }
func (s *sqlSink) Close() error                  { return s.db.Close() }

lg, _ := logger.NewWithSink(logger.SinkConfig{Sink: &sqlSink{db: db}})
```

</div>

</div>
