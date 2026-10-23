package app

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/health"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
	"github.com/kitsunium/sdk/pkg/v1/observe/metrics"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The export while the app runs (otlp.go): a lifecycle component, started
// before every other one that may produce a span and so stopped after them;
// the tracer's sink, which hands every span that ends to the export's batch
// sink; one record per unit of work for the metrics; a readiness check that
// is not critical; and its stop, inside the lifecycle's budget, which the
// export's drain and settle widen (componentStopBudget).
//
// The readiness check says degraded — never unready — once the export's
// loop ended while the app runs (a queue's storage failed: nothing is sent
// until the next start), while a signal's queue is above 80% of either
// bound, or since it last answered a batch was lost: dropped, expired or
// dead-lettered. Telemetry is not what the
// product serves: taking an instance out of rotation because its collector
// is down would turn an observability outage into an outage of the
// product, every instance at once. The operator reads the state on the
// readiness probe, in the log, and in the export's own metrics.

// The places a queue of the export lies in, as the runtime state says them.
const (
	otlpStoreMemory    = "memory"
	otlpStoreDirectory = "directory"
	otlpStoreDatabase  = "database:"
)

// otlpRunning is the export of a running app: the subsystem's export, the
// tracer's sink into it, and the loop that sends.
type otlpRunning struct {
	ex   plug.OTLPExporter
	sink trace.SpanSink
	// sample decides whether a trace this process starts is kept, as the
	// setting otlp-sample-ratio says; nil keeps every one.
	sample trace.Sampler
	// meter is the product's own meter (Meter), collected into the same
	// metrics stream; nil when the export hands none.
	meter metrics.FullMeter
	// store is where the queues lie, as the runtime state says it:
	// "database:<name>", "directory" or "memory" (a queue the platform
	// keeps in memory is said so by its stats).
	store string
	// cancel ends the loop; done is closed once it returned, err — written
	// under mu before done closes — what it returned.
	cancel context.CancelFunc
	done   chan struct{}
	ended  sync.Once
	err    error
	// mu guards err and lost, the batches lost when readiness last
	// answered.
	mu   sync.Mutex
	lost map[string]uint64
}

// otlpLinked reports whether the program imports framework/app/otlp.
func otlpLinked() bool { return plug.OTLP.Load() != nil }

// otlpDeclared is where the app declared its export, nil when it declares
// none.
func otlpDeclared(a *App) *pos {
	if a.opts.otlp == nil {
		return nil
	}
	return &a.opts.otlp.decl
}

// otlpSink is the tracer's sink when the app declares an export: every span
// that ends goes to the export while it runs, nowhere otherwise. nil for an
// app that declares none, whose tracer then records nothing.
func (a *App) otlpSink() trace.SpanSink {
	if a.opts.otlp == nil {
		return nil
	}
	return func(span trace.SpanData) {
		if r := a.otlpRun.Load(); r != nil && r.sink != nil {
			r.sink(span)
		}
	}
}

// otlpSampler is the tracer's sampler when the app declares an export: a
// parent's decision honoured, and a trace this process starts kept as the
// running export's ratio says — every one while none runs, which records
// nothing. nil for an app that declares none: the SDK's default, whose
// spans no sink receives.
func (a *App) otlpSampler() trace.Sampler {
	if a.opts.otlp == nil {
		return nil
	}
	return trace.ParentBased(a.otlpSampleRoot)
}

// otlpSampleRoot decides whether a trace this process starts is kept: the
// running export's ratio, every one without it. Without an export it costs
// one atomic load.
func (a *App) otlpSampleRoot(p trace.SamplingParams) bool {
	if r := a.otlpRun.Load(); r != nil && r.sample != nil {
		return r.sample(p)
	}
	return true
}

// otlpRootSampler is the sampler of the traces this process starts for
// ratio, which the setting's check holds in [0, 1]: nil — every one — at 1,
// none at 0, the SDK's ratio on the trace's identifier between.
func otlpRootSampler(ratio float64) trace.Sampler {
	switch {
	case ratio >= 1:
		return nil
	case ratio <= 0:
		return trace.NeverSample
	}
	s, err := trace.Ratio(ratio)
	if err != nil {
		return nil
	}
	return s
}

// otlpRecord counts a unit of work that ended for the export's metrics.
// Without an export it costs one atomic load.
func (a *App) otlpRecord(sp *span) {
	r := a.otlpRun.Load()
	if r == nil {
		return
	}
	r.ex.Record(sp.s.Node, sp.s.Op, a.clock.Since(sp.start), sp.s.Status == model.StatusError)
}

// addOTLP adds the export's component and its readiness check, when the
// app declares one.
func (a *App) addOTLP() error {
	if a.opts.otlp == nil {
		return nil
	}
	if err := a.health.AddReadiness(health.ReadinessCheck{Name: otlpComponent, NonCritical: true, Check: a.otlpReady}); err != nil {
		return err
	}
	return a.component(otlpComponent, "", a.startOTLP, a.stopOTLP)
}

// startOTLP opens the export and starts the loop that sends: nothing while
// its endpoint is empty and no sender replaces the network.
//
// Goroutine lifecycle: one, which ends when stopOTLP cancels its context and
// the export has flushed and closed its queues; done, closed once, says so.
func (a *App) startOTLP(ctx context.Context) error {
	cfg, err := a.otlpConfig(ctx)
	if err != nil || cfg == nil {
		return err
	}
	a.warnOTLPFileQueuesLeft(cfg)
	driver := plug.OTLP.Load()
	if driver == nil {
		return errs.New(CodeSubsystemMissing, "SUBSYSTEM_NOT_IMPORTED", subsystemPublic,
			"app: an OTLP export needs the otlp subsystem, which the program does not import: "+otlpPackage,
			errs.String("packages", otlpPackage))
	}
	ex, err := driver.Open(ctx, cfg)
	if err != nil {
		return failure(CodeOTLPExport, "OTLP_OPEN", "the OTLP export cannot be opened", err)
	}
	sink, _ := ex.SpanSink().Sink.(trace.SpanSink)
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	meter, _ := ex.Meter().Meter.(metrics.FullMeter)
	r := &otlpRunning{
		ex: ex, sink: sink, sample: otlpRootSampler(resolvedSetting(a, a.opts.otlp.sampleRatio)), meter: meter, store: otlpStoreOf(a, cfg),
		cancel: cancel, done: make(chan struct{}), lost: map[string]uint64{},
	}
	for _, st := range ex.Stats() {
		if cfg.Dir != "" && !st.Durable {
			a.problem("", say("otlp.memory", "signal", st.Signal))
		}
		if st.Pending > 0 {
			logger.Info(ctx, a.log, "the OTLP export resumes a backlog", logger.String("signal", st.Signal), logger.Int("batches", st.Pending))
		}
	}
	go func() {
		defer r.ended.Do(func() { close(r.done) })
		err := ex.Run(runCtx)
		r.mu.Lock()
		r.err = err
		r.mu.Unlock()
	}()
	a.otlpRun.Store(r)
	return nil
}

// stopOTLP stops the export: what it holds in memory flushed into its
// queues, the batch being sent given its drain, the queues closed — within
// the component's budget. A batch still queued in memory is lost, and said.
func (a *App) stopOTLP(ctx context.Context) error {
	r := a.otlpRun.Swap(nil)
	if r == nil {
		return nil
	}
	r.cancel()
	select {
	case <-r.done:
	case <-ctx.Done():
		return failure(CodeOTLPExport, "OTLP_STOP", "the OTLP export did not stop within its budget", ctx.Err())
	}
	for _, st := range r.ex.Stats() {
		if !st.Durable && st.Pending > 0 {
			logger.Warn(ctx, a.log, "the OTLP export lost the batches it held in memory", logger.String("signal", st.Signal), logger.Int("batches", st.Pending))
		}
	}
	r.mu.Lock()
	err := r.err
	r.mu.Unlock()
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Warn(ctx, a.log, "the OTLP export stopped with an error", logger.String("error", errs.PublicOf(err)))
	}
	return nil
}

// otlpConfig is the export as the start resolved its settings and its
// secret; nil while it is off: no endpoint, and no sender of the product's.
func (a *App) otlpConfig(ctx context.Context) (*plug.OTLPConfig, error) {
	e := a.opts.otlp
	endpoint := resolvedSetting(a, e.endpoint)
	if endpoint == "" && e.send == nil {
		logger.Info(ctx, a.log, "the OTLP export is off: "+variableOf(appPrefix(a.name), otlpPrefix+knobOTLPEndpoint)+" is not set")
		return nil, nil
	}
	headers, err := a.otlpHeaders(ctx)
	if err != nil {
		return nil, err
	}
	cfg := &plug.OTLPConfig{
		Clock: pluggedClock{Timed: a.clock}, Headers: headers,
		Service: a.name, Version: frameworkVersion(), Endpoint: endpoint,
		WhenFull: resolvedSetting(a, e.whenFull), MaxBytes: int64(resolvedSetting(a, e.maxBytes)),
		MaxBatches: resolvedSetting(a, e.maxBatches), MaxAge: resolvedSetting(a, e.maxAge),
		SendTimeout: resolvedSetting(a, e.sendTimeout), Interval: resolvedSetting(a, e.interval),
		DrainTimeout: resolvedSetting(a, e.drain), SettleTimeout: resolvedSetting(a, e.settle),
		OnError: func(err error) {
			logger.Warn(context.Background(), a.log, "the OTLP export could not keep or close what it holds", logger.String("error", errs.PublicOf(err)))
		},
	}
	if a.dataDir != "" && !a.opts.memory {
		cfg.Dir = filepath.Join(a.dataDir, otlpDataDir)
	}
	if err := a.otlpOnDatabase(ctx, cfg); err != nil {
		return nil, err
	}
	if send := e.send; send != nil {
		cfg.Send = func(ctx context.Context, b plug.OTLPBatchValue) error {
			err := send(ctx, OTLPBatchValue{Signal: b.Signal, Document: b.Document})
			if err != nil && !otlpRetryable(err) {
				// A refusal for good is dead-lettered with this code and
				// sentence, which `otlp dead-letters` shows (issue #536);
				// the product's own error stays its cause.
				return failure(CodeOTLPExport, "OTLP_REFUSED", "the product's OTLP sender refused the batch for good", err)
			}
			return err
		}
		cfg.Retryable = otlpRetryable
	}
	return cfg, nil
}

// otlpOnDatabase places the export's queues on the database otlp-database
// names (issue #537): one table per signal, which the framework's own
// migrations made when the database started. Nothing when the setting is
// empty; refused when it names no database the app declares, or when the
// program does not import framework/app/queue, whose SQL broker keeps them.
// A database that is not open — the app in memory, or in dev without its
// URL — leaves the queues where they were, and says so.
func (a *App) otlpOnDatabase(ctx context.Context, cfg *plug.OTLPConfig) error {
	name := resolvedSetting(a, a.opts.otlp.database)
	if name == "" {
		return nil
	}
	d := a.otlpDatabase()
	if d == nil {
		return failure(CodeOTLPExport, "OTLP_DATABASE", "the OTLP export's database is none the app declares", nil,
			errs.String("database", name))
	}
	if !queuesLinked() {
		return queuesMissing()
	}
	r := a.runOf(d)
	if a.opts.memory || r == nil || !r.open() || r.database() == nil {
		logger.Warn(ctx, a.log, "the OTLP export's database is not open: its queues are kept apart from it", logger.String("database", name))
		return nil
	}
	cfg.Dir, cfg.Database = "", r.database()
	cfg.Tables = map[string]string{OTLPTraces: otlpTable(OTLPTraces), OTLPMetrics: otlpTable(OTLPMetrics)}
	return nil
}

// warnOTLPFileQueuesLeft says, at the start, that the export's queues moved
// from the data directory onto a database and left batches behind (issue
// #580): they wait in the file queues under <data>/.otlp, which nothing
// sends now, until `otlp migrate` moves them — logged and shown as a
// problem, as a consumer's queue left behind is (consumer.file-queue-left).
func (a *App) warnOTLPFileQueuesLeft(cfg *plug.OTLPConfig) {
	if cfg.Database == nil || a.dataDir == "" || a.opts.memory {
		return
	}
	database := resolvedSetting(a, a.opts.otlp.database)
	for _, signal := range []string{OTLPTraces, OTLPMetrics} {
		if fileQueueHolds(otlpFileQueue(a.dataDir, signal)) {
			a.problem(otlpNode, say("otlp.file-queue-left", "signal", signal, "database", database, "product", a.name))
		}
	}
}

// otlpFileQueue is the directory of signal's file queue under the data
// directory data, where the export keeps it without otlp-database.
func otlpFileQueue(data, signal string) string {
	return filepath.Join(data, otlpDataDir, signal)
}

// otlpDatabase is the database otlp-database names, nil when it names none
// the app declares, or nothing.
func (a *App) otlpDatabase() *database {
	name := resolvedSetting(a, a.opts.otlp.database)
	for _, d := range a.opts.databases {
		if name != "" && d.name == name {
			return d
		}
	}
	return nil
}

// otlpTable is the table of signal's queue on the export's database:
// app_otlp__traces, app_otlp__metrics.
func otlpTable(signal string) string { return tableName(otlpTableService, signal, "") }

// otlpTables are the export's tables on d, when d is the database
// otlp-database names and the app keeps its data out of memory: each
// signal's queue, the SDK's SQL broker's one table, and its trail column
// (store_sql.go's frameworkTables, as a consumer's queue).
func (a *App) otlpTables(d *database) []frameworkTable {
	if a.opts.otlp == nil || a.opts.memory || a.otlpDatabase() != d {
		return nil
	}
	var out []frameworkTable
	for _, signal := range []string{OTLPTraces, OTLPMetrics} {
		table := otlpTable(signal)
		out = append(out, frameworkTable{name: table, node: otlpNode, queue: true},
			frameworkTable{name: table + trailSuffix, node: otlpNode, queue: true, trailOf: table})
	}
	return out
}

// otlpHeaders reads the secret otlp-headers; none when it is set nowhere.
func (a *App) otlpHeaders(ctx context.Context) (map[string]string, error) {
	v, _, err := a.frameworkSecret(ctx, otlpHeadersSecret)
	switch {
	case errors.Is(err, secret.NotFound):
		return nil, nil
	case err != nil:
		return nil, failure(CodeOTLPExport, "OTLP_HEADERS", "the OTLP export's headers cannot be read", err)
	}
	headers, ok := parseOTLPHeaders(v.Value.RevealString())
	if !ok {
		return nil, failure(CodeOTLPExport, "OTLP_HEADERS", "the OTLP export's headers are not key=value pairs joined by commas", nil,
			errs.String("variable", a.variable(varOTLPHeaders)))
	}
	return headers, nil
}

// otlpHeaderSettings is <PRODUCT>_OTLP_HEADERS, when the app declares an
// export. A collector's credentials live in it: whether it is set, and
// where, is all that shows.
func (a *App) otlpHeaderSettings(ctx context.Context) []model.Setting {
	if a.opts.otlp == nil {
		return nil
	}
	_, from, err := a.frameworkSecret(ctx, otlpHeadersSecret)
	if err != nil {
		from = ""
	}
	return []model.Setting{{Name: a.variable(varOTLPHeaders), Secret: true, From: settingFrom(from)}}
}

// otlpRetryable reports whether a failure of the product's own sender may
// be cured by a later attempt: all but a refusal the caller must fix, a 4xx
// other than 408 and 429.
func otlpRetryable(err error) bool {
	status, _ := describe(err)
	return status >= http.StatusInternalServerError || status == http.StatusRequestTimeout || status == http.StatusTooManyRequests
}

// otlpReady is the export's readiness, which is not critical: degraded
// once its loop ended while the app runs — a queue's storage failed, and
// nothing is sent until the next start —, while a signal's queue is above
// 80% of either bound, or since it last answered a batch was lost.
func (a *App) otlpReady(_ context.Context) error {
	r := a.otlpRun.Load()
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.done:
		reason := "its loop ended"
		if r.err != nil {
			reason += ": " + errs.PublicOf(r.err)
		}
		return Unavailable("the OTLP export stopped sending until the next start: " + reason)
	default:
	}
	var problems []string
	for _, st := range r.ex.Stats() {
		lost := st.Dropped + st.Expired + st.DeadLettered
		if lost > r.lost[st.Signal] {
			problems = append(problems, st.Signal+": "+strconv.FormatUint(lost-r.lost[st.Signal], 10)+" batches lost ("+
				strconv.FormatUint(st.Dropped, 10)+" dropped, "+strconv.FormatUint(st.Expired, 10)+" expired, "+
				strconv.FormatUint(st.DeadLettered, 10)+" dead-lettered)")
		}
		r.lost[st.Signal] = lost
		if backlogged(&st) {
			problems = append(problems, st.Signal+": "+strconv.Itoa(st.Pending)+" batches, "+strconv.FormatInt(st.PendingBytes, 10)+" bytes waiting")
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return Unavailable("the OTLP export is behind: " + strings.Join(problems, "; "))
}

// otlpStoreOf is where cfg places the export's queues.
func otlpStoreOf(a *App, cfg *plug.OTLPConfig) string {
	switch {
	case cfg.Database != nil:
		return otlpStoreDatabase + resolvedSetting(a, a.opts.otlp.database)
	case cfg.Dir != "":
		return otlpStoreDirectory
	}
	return otlpStoreMemory
}

// describeOTLP is the export as the runtime state shows it (issue #535):
// off while none runs; else each signal's queue against its bounds, what
// it sent and lost, and the last Retry-After; stopped once its loop ended.
// It reads the stats and never answers the readiness, whose losses it
// would consume: the problem it says is the loop's end or a queue's backlog.
func (a *App) describeOTLP() *model.Export {
	if a.opts.otlp == nil {
		return nil
	}
	r := a.otlpRun.Load()
	if r == nil {
		return &model.Export{State: model.ExportOff}
	}
	out := &model.Export{State: model.ExportRunning}
	var backlog []string
	for _, st := range r.ex.Stats() {
		out.Signals = append(out.Signals, exportSignal(r.store, &st))
		if backlogged(&st) {
			backlog = append(backlog, st.Signal+": "+strconv.Itoa(st.Pending)+" batches, "+strconv.FormatInt(st.PendingBytes, 10)+" bytes waiting")
		}
	}
	if len(backlog) > 0 {
		out.Problem = "the OTLP export is behind: " + strings.Join(backlog, "; ")
	}
	select {
	case <-r.done:
		out.State, out.Problem = model.ExportStopped, "the OTLP export stopped sending until the next start"
		r.mu.Lock()
		if r.err != nil {
			out.Problem += ": " + errs.PublicOf(r.err)
		}
		r.mu.Unlock()
	default:
	}
	return out
}

// exportSignal is one signal's stats as the runtime state shows them; a
// queue that is not durable lies in memory, wherever the export asked.
func exportSignal(store string, st *plug.OTLPStatsValue) model.ExportSignal {
	if !st.Durable {
		store = otlpStoreMemory
	}
	out := model.ExportSignal{
		Signal: st.Signal, Store: store, Durable: st.Durable,
		Pending: st.Pending, PendingBytes: st.PendingBytes, MaxBatches: st.MaxBatches, MaxBytes: st.MaxBytes,
		Sent: st.Sent, Retries: st.Retries, Dropped: st.Dropped, Expired: st.Expired, DeadLettered: st.DeadLettered,
		LastRetryAfterMs: st.LastRetryAfter.Milliseconds(),
	}
	if !st.LastRetryAfterAt.IsZero() {
		out.LastRetryAfterAt = new(st.LastRetryAfterAt.UTC())
	}
	return out
}

// backlogged reports whether a signal's queue is above the readiness's share
// of either of its bounds.
func backlogged(st *plug.OTLPStatsValue) bool {
	return st.MaxBatches > 0 && float64(st.Pending) > otlpReadinessBacklog*float64(st.MaxBatches) ||
		st.MaxBytes > 0 && float64(st.PendingBytes) > otlpReadinessBacklog*float64(st.MaxBytes)
}
