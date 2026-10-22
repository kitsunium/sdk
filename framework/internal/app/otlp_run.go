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

// otlpRunning is the export of a running app: the subsystem's export, the
// tracer's sink into it, and the loop that sends.
type otlpRunning struct {
	ex   plug.OTLPExporter
	sink trace.SpanSink
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
	r := &otlpRunning{ex: ex, sink: sink, cancel: cancel, done: make(chan struct{}), lost: map[string]uint64{}}
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
	if send := e.send; send != nil {
		cfg.Send = func(ctx context.Context, b plug.OTLPBatchValue) error {
			return send(ctx, OTLPBatchValue{Signal: b.Signal, Document: b.Document})
		}
		cfg.Retryable = otlpRetryable
	}
	return cfg, nil
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

// backlogged reports whether a signal's queue is above the readiness's share
// of either of its bounds.
func backlogged(st *plug.OTLPStatsValue) bool {
	return st.MaxBatches > 0 && float64(st.Pending) > otlpReadinessBacklog*float64(st.MaxBatches) ||
		st.MaxBytes > 0 && float64(st.PendingBytes) > otlpReadinessBacklog*float64(st.MaxBytes)
}
