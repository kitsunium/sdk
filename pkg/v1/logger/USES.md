<!--
  See pkg/v1/codec/USES.md for the authoring rule (one tab per
  variant, "Best for:" + 5-10 lines Go). Injected by sync-versions
  AFTER the narrative and BEFORE the API reference dump.
-->

## Use cases

The logger composes a Sink (where the bytes go) with an Encoder
(how the bytes are formatted). Pick the topology that fits.

<div class="tabs" data-tabs>
<div class="tab-strip" role="tablist">
<button type="button" role="tab" id="lg-default-btn" aria-controls="lg-default" aria-selected="true" tabindex="0" class="active">Default (stderr)</button>
<button type="button" role="tab" id="lg-file-btn" aria-controls="lg-file" aria-selected="false" tabindex="-1">File sink</button>
<button type="button" role="tab" id="lg-multi-btn" aria-controls="lg-multi" aria-selected="false" tabindex="-1">Multi (fan-out)</button>
<button type="button" role="tab" id="lg-build-btn" aria-controls="lg-build" aria-selected="false" tabindex="-1">Build (hot path)</button>
<button type="button" role="tab" id="lg-custom-btn" aria-controls="lg-custom" aria-selected="false" tabindex="-1">Custom sink</button>
</div>

<div role="tabpanel" id="lg-default" aria-labelledby="lg-default-btn">
<p><strong>Best for:</strong> bootstrap, CLI tools, container apps that already log to stderr.</p>
<pre><code class="language-go">lg, err := logger.Default()
if err != nil { panic(err) } // returns WriterRequired only on nil Writer

ctx := context.Background()
logger.Info(ctx, lg, "service started",
    logger.String("env", "prod"),
    logger.Int("port", 8080),
)</code></pre>
</div>

<div role="tabpanel" id="lg-file" aria-labelledby="lg-file-btn" hidden>
<p><strong>Best for:</strong> production daemons that ship logs to disk (with logrotate / fluentd reading the file).</p>
<pre><code class="language-go">f, _ := os.OpenFile("/var/log/myapp.log",
    os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)

lg, err := logger.NewWithSink(logger.SinkConfig{
    Sink:    fileSinkAroundWriter(f), // your adapter
    Encoder: logger.TextEncoder(),
})</code></pre>
</div>

<div role="tabpanel" id="lg-multi" aria-labelledby="lg-multi-btn" hidden>
<p><strong>Best for:</strong> fan-out — stream to stderr <em>and</em> to a remote aggregator at the same time. Errors from any branch are joined under <code>FANOUT_WRITE_FAILED</code>.</p>
<pre><code class="language-go">lg, err := logger.NewWithSink(logger.SinkConfig{
    Sink: logger.Multi(
        logger.ConsoleStderr(),
        remoteSyslogSink, // your custom sink
    ),
    Encoder: logger.TextEncoder(),
})</code></pre>
</div>

<div role="tabpanel" id="lg-build" aria-labelledby="lg-build-btn" hidden>
<p><strong>Best for:</strong> the zero-allocation hot path. The chainable builder is backed by a <code>sync.Pool</code>; steady-state per-call cost is 0 allocs once the pool is warm. Don't reuse the builder after <code>Send</code> — it returns to the recycler.</p>
<pre><code class="language-go">logger.Build(lg, logger.LevelInfo).
    Str("env", "prod").
    Int("port", 8080).
    Send(ctx, "service started")</code></pre>
</div>

<div role="tabpanel" id="lg-custom" aria-labelledby="lg-custom-btn" hidden>
<p><strong>Best for:</strong> push to a database / external observability stack. Implement the three-method <code>logger.Sink</code> interface and plug it through <code>NewWithSink</code>.</p>
<pre><code class="language-go">type sqlSink struct{ db *sql.DB }

func (s *sqlSink) Write(ctx context.Context, r logger.Record, p []byte) (int, error) {
    _, err := s.db.ExecContext(ctx,
        "INSERT INTO logs(level, msg, payload) VALUES($1, $2, $3)",
        r.Level, r.Message, p)
    return len(p), err
}
func (s *sqlSink) Flush(_ context.Context) error { return nil }
func (s *sqlSink) Close() error                  { return s.db.Close() }

lg, _ := logger.NewWithSink(logger.SinkConfig{Sink: &amp;sqlSink{db: db}})</code></pre>
</div>

</div>
