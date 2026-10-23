package app

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
)

// The product's OTLP export (issue #483, ADR 0185): [OTLP] declares that
// the app forwards its traces — every span the framework starts — and the
// metrics the framework derives from them to an OpenTelemetry collector over
// OTLP/HTTP, or to the product's own sender ([OTLPSender]). It is a channel
// of the product's, apart from framework/telemetry's (ADR 0149), which
// reports numbers to a tool attached on a private socket and is left as it
// is. The export is a subsystem (ADR 0179): a program that declares one
// imports framework/app/otlp, which plugs the SDK's app/forward in, and a
// program that declares none links nothing of it.
//
// Each signal waits in a queue of its own — under <data>/.otlp/<signal> when
// the app keeps its data in a directory, so a backlog outlives a restart;
// in memory otherwise, and where the SDK's durable queue refuses the
// platform — and is sent by a forwarder that retries a collector that is
// down on a backoff, waits a Retry-After in full, dead-letters a batch the
// collector refuses for good, and drops one that outlived its age. Its
// tuning are settings the framework declares for the app, the code's values
// their defaults, which config/<env>.*, <PRODUCT>_<NAME> and app.Set
// override like any setting (ADR 0097):
//
//	otlp-endpoint      the collector's base URL ("": the export is off)
//	otlp-max-batches   the batches each signal's queue holds (10000)
//	otlp-max-bytes     the bytes each signal's queue holds (256 MiB)
//	otlp-when-full     drop-oldest, drop-newest or block (drop-oldest)
//	otlp-max-age       how long a batch may wait to be sent (24h)
//	otlp-send-timeout  one attempt's bound (10s)
//	otlp-interval      how often the metrics are collected (1m)
//	otlp-drain         how long the batch being sent at a stop may finish (5s)
//	otlp-settle        each acknowledgement's and the stop's flush's bound (5s)
//	otlp-sample-ratio  the share of the traces started here that are kept (1)
//
// and the headers every request carries — a collector's Authorization — are
// the framework's secret otlp-headers, <PRODUCT>_OTLP_HEADERS or its _FILE
// twin, "key=value,key=value" as OTEL_EXPORTER_OTLP_HEADERS spells them.

// The settings' knobs, each named otlp-<knob>.
const (
	otlpPrefix          = "otlp-"
	knobOTLPEndpoint    = "endpoint"
	knobOTLPMaxBatches  = "max-batches"
	knobOTLPMaxBytes    = "max-bytes"
	knobOTLPWhenFull    = "when-full"
	knobOTLPMaxAge      = "max-age"
	knobOTLPSendTimeout = "send-timeout"
	knobOTLPInterval    = "interval"
	knobOTLPDrain       = "drain"
	knobOTLPSettle      = "settle"
	knobOTLPSampleRatio = "sample-ratio"
	otlpHeadersSecret   = "otlp-headers"
	varOTLPHeaders      = "OTLP_HEADERS"
	otlpPackage         = "github.com/kitsunium/sdk/framework/app/otlp"
	otlpDataDir         = ".otlp"
	otlpNode            = "otlp"
	otlpComponent       = "otlp"
)

// The export's defaults, the settings' when the code names none, and the
// share of a bound past which the readiness says the export is behind.
const (
	otlpDefaultBatches   int           = 10_000
	otlpDefaultBytes     int           = 256 << 20
	otlpDefaultMaxAge    time.Duration = 24 * time.Hour
	otlpDefaultTimeout   time.Duration = 10 * time.Second
	otlpDefaultInterval  time.Duration = time.Minute
	otlpDefaultDrain     time.Duration = 5 * time.Second
	otlpDefaultSettle    time.Duration = 5 * time.Second
	otlpDefaultRatio     float64       = 1
	otlpReadinessBacklog float64       = 0.8
)

// The two signals an export forwards, as an [OTLPBatchValue] names them.
const (
	// OTLPTraces names the trace signal: an OTLP/JSON
	// ExportTraceServiceRequest.
	OTLPTraces string = plug.OTLPTraces
	// OTLPMetrics names the metrics signal: an OTLP/JSON
	// ExportMetricsServiceRequest.
	OTLPMetrics string = plug.OTLPMetrics
)

// What an export's full queue does with a new batch: the setting
// otlp-when-full spells it.
const (
	// OTLPDropOldest, the default: the oldest batches not being sent are
	// taken out, counted dropped, until the new one fits.
	OTLPDropOldest OTLPFullPolicy = "drop-oldest"
	// OTLPDropNewest: the new batch is refused, counted dropped.
	OTLPDropNewest OTLPFullPolicy = "drop-newest"
	// OTLPBlock: the span or the snapshot waits for room a few seconds, then
	// is refused, counted dropped.
	OTLPBlock OTLPFullPolicy = "block"
)

// OTLPFullPolicy is what an export's full queue does with a new batch.
type OTLPFullPolicy string

// OTLPBatchValue is one batch as the product's own sender receives it
// ([OTLPSender]): its signal, [OTLPTraces] or [OTLPMetrics], and its
// OTLP/JSON document, which a collector takes as it is.
type OTLPBatchValue struct {
	// Signal is OTLPTraces or OTLPMetrics.
	Signal string
	// Document is the OTLP/JSON export request.
	Document []byte
}

// otlpExport is what app.OTLP declared: where, its code's values, and the
// settings that tune it.
type otlpExport struct {
	decl pos
	send func(ctx context.Context, batch OTLPBatchValue) error

	endpoint    *SettingService[string]
	maxBatches  *SettingService[int]
	maxBytes    *SettingService[int]
	whenFull    *SettingService[string]
	maxAge      *SettingService[time.Duration]
	sendTimeout *SettingService[time.Duration]
	interval    *SettingService[time.Duration]
	drain       *SettingService[time.Duration]
	settle      *SettingService[time.Duration]
	sampleRatio *SettingService[float64]
}

// otlpOptions are an export's code values, the settings' defaults.
type otlpOptions struct {
	send        func(ctx context.Context, batch OTLPBatchValue) error
	endpoint    string
	whenFull    OTLPFullPolicy
	maxBytes    int
	maxBatches  int
	maxAge      time.Duration
	sendTimeout time.Duration
	sampleRatio float64
}

// otlpOption configures an export.
type otlpOption func(o *otlpOptions)

// openTelemetryConfigure sets the option on what it configures.
func (f otlpOption) openTelemetryConfigure(o *otlpOptions) { f(o) }

// OTLP declares that the app forwards its traces and the metrics the
// framework derives from them to an OpenTelemetry collector over OTLP/HTTP:
// the spans the framework starts — the share otlp-sample-ratio keeps —, and
// per node and operation its runs, failures and durations (app.node.runs,
// app.node.failures, app.node.duration), with what the export itself holds
// and lost (app.otlp.*) and the product's own instruments ([Meter]). The
// export is on once its endpoint is set — the setting
// otlp-endpoint, [OTLPEndpoint] its default — or once [OTLPSender] replaces
// the network; a CLI command never exports. A program that declares it
// imports framework/app/otlp, or its start is refused, naming the import:
//
//	import _ "github.com/kitsunium/sdk/framework/app/otlp"
//
//	var App = app.NewApp("shop", catalog.Service).With(
//		app.OTLP(app.OTLPEndpoint("http://collector:4318")),
//	)
//
// Every value given here is the default of the setting that tunes it, which
// config/<env>.* and <PRODUCT>_OTLP_<KNOB> override. Given twice, the last
// wins.
//
//go:noinline
func OTLP(opts ...OpenTelemetryConfigurer) AppConfigurer {
	o := otlpOptions{
		whenFull: OTLPDropOldest, maxBatches: otlpDefaultBatches, maxBytes: otlpDefaultBytes,
		maxAge: otlpDefaultMaxAge, sendTimeout: otlpDefaultTimeout, sampleRatio: otlpDefaultRatio,
	}
	for _, opt := range opts {
		if opt != nil {
			opt.openTelemetryConfigure(&o)
		}
	}
	e := &otlpExport{decl: callerPos(), send: o.send}
	e.declareSettings(&o)
	return appOption(func(ao *appOptions) { ao.otlp = e })
}

// OTLPEndpoint is the collector's base URL — "http://collector:4318" —, each
// signal's path (/v1/traces, /v1/metrics) appended to it: the default of
// the setting otlp-endpoint. An absolute http or https URL with a host; the
// empty default leaves the export off.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OTLPEndpoint(url string) OpenTelemetryConfigurer {
	return otlpOption(func(o *otlpOptions) { o.endpoint = url })
}

// OTLPBounds bounds each signal's queue: at most maxBatches batches and
// maxBytes bytes — the defaults of the settings otlp-max-batches (10000)
// and otlp-max-bytes (256 MiB). A batch larger than maxBytes on its own is
// dropped.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OTLPBounds(maxBatches, maxBytes int) OpenTelemetryConfigurer {
	return otlpOption(func(o *otlpOptions) { o.maxBatches, o.maxBytes = maxBatches, maxBytes })
}

// OTLPWhenFull is what a full queue does with a new batch — the default of
// the setting otlp-when-full: [OTLPDropOldest] (the default),
// [OTLPDropNewest] or [OTLPBlock].
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OTLPWhenFull(p OTLPFullPolicy) OpenTelemetryConfigurer {
	return otlpOption(func(o *otlpOptions) { o.whenFull = p })
}

// OTLPMaxAge is how long after it was queued a batch may still be sent — the
// default of the setting otlp-max-age (24h): one older is dropped unsent,
// counted expired. A collector's Retry-After is waited in full, but never
// past it.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OTLPMaxAge(d time.Duration) OpenTelemetryConfigurer {
	return otlpOption(func(o *otlpOptions) { o.maxAge = d })
}

// OTLPSendTimeout bounds one attempt to send a batch — the default of the
// setting otlp-send-timeout (10s, OTEL_EXPORTER_OTLP_TIMEOUT's).
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OTLPSendTimeout(d time.Duration) OpenTelemetryConfigurer {
	return otlpOption(func(o *otlpOptions) { o.sendTimeout = d })
}

// OTLPSender replaces the network: each batch is handed to send instead of
// being posted to a collector — the product's own sender, a bridge to
// another transport. The queue, its bounds, its age and its retries stay
// the framework's. A failure send returns is tried again on the backoff —
// after the delay it carries when it is a resilience.RetryAfter's —, but for
// a refusal the caller must fix, an [Error] of a 4xx other than 408 and 429,
// which dead-letters the batch. The export is then on whatever otlp-endpoint
// says. A nil send keeps the network.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OTLPSender(send func(ctx context.Context, batch OTLPBatchValue) error) OpenTelemetryConfigurer {
	return otlpOption(func(o *otlpOptions) { o.send = send })
}

// OTLPSampleRatio is the share of the traces the app starts that the export
// keeps — the default of the setting otlp-sample-ratio (1, every one): from
// 0, none, to 1, every one, decided on the trace's identifier, so every
// service that sees the same trace decides alike. A trace another process
// started is kept or not as that process decided, whatever the ratio: a span
// is never cut off from its parent. The metrics the framework derives count
// every unit of work, kept or not.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func OTLPSampleRatio(ratio float64) OpenTelemetryConfigurer {
	return otlpOption(func(o *otlpOptions) { o.sampleRatio = ratio })
}

// declareSettings declares the export's settings, the code's values their
// defaults.
func (e *otlpExport) declareSettings(o *otlpOptions) {
	e.endpoint = otlpSetting(e, knobOTLPEndpoint, o.endpoint, checkOTLPEndpoint())
	e.maxBatches = otlpSetting(e, knobOTLPMaxBatches, o.maxBatches, checkAtLeastOne())
	e.maxBytes = otlpSetting(e, knobOTLPMaxBytes, o.maxBytes, checkAtLeastOne())
	e.whenFull = otlpSetting(e, knobOTLPWhenFull, string(o.whenFull), checkOTLPWhenFull())
	e.maxAge = otlpSetting(e, knobOTLPMaxAge, o.maxAge, checkPositive())
	e.sendTimeout = otlpSetting(e, knobOTLPSendTimeout, o.sendTimeout, checkPositive())
	e.interval = otlpSetting(e, knobOTLPInterval, otlpDefaultInterval, checkPositive())
	e.drain = otlpSetting(e, knobOTLPDrain, otlpDefaultDrain, checkWait())
	e.settle = otlpSetting(e, knobOTLPSettle, otlpDefaultSettle, checkPositive())
	e.sampleRatio = otlpSetting(e, knobOTLPSampleRatio, o.sampleRatio, checkOTLPSampleRatio())
}

// otlpSetting declares one setting of the export: otlp-<knob>.
func otlpSetting[T SettingValue](e *otlpExport, knob string, def T, check func(any) phrase) *SettingService[T] {
	s := NewSettingService(def)
	s.name, s.decl, s.check = otlpPrefix+knob, e.decl, check
	s.id = otlpNode + "/setting/" + knob
	return s
}

// settingDecls are the export's settings, in the table's order.
func (e *otlpExport) settingDecls() []settingDecl {
	return []settingDecl{e.endpoint, e.maxBatches, e.maxBytes, e.whenFull, e.maxAge, e.sendTimeout, e.interval, e.drain, e.settle, e.sampleRatio}
}

// otlpStopBudget is the most the export's stop takes, nothing without an
// export: the flush of what it holds in memory and of one last snapshot, a
// settle each, then the batch being sent, its drain and its
// acknowledgement's settle.
func (a *App) otlpStopBudget() time.Duration {
	e := a.opts.otlp
	if e == nil {
		return 0
	}
	return resolvedSetting(a, e.drain) + 3*resolvedSetting(a, e.settle)
}

// otlpSettings are the export's settings, but for one whose name the
// product's settings take: the export's problem (otlpProblems).
func (a *App) otlpSettings(byName map[string]settingDecl) []settingDecl {
	if a.opts.otlp == nil {
		return nil
	}
	var out []settingDecl
	for _, d := range a.opts.otlp.settingDecls() {
		if _, taken := byName[d.base().name]; !taken {
			out = append(out, d)
		}
	}
	return out
}

// otlpProblems are what stops a start in the app's export: a setting of
// its own a product's setting already names.
func (a *App) otlpProblems() []model.Diagnostic {
	e := a.opts.otlp
	if e == nil {
		return nil
	}
	taken := map[string]bool{}
	for _, d := range a.settingDecls() {
		taken[d.base().key()] = true
	}
	var out []model.Diagnostic
	for _, d := range e.settingDecls() {
		if name := d.base().name; taken[name] {
			out = append(out, diagnosticOf("error", otlpNode, a.source(&e.decl), say("otlp.setting-taken", "setting", name)))
		}
	}
	return out
}

// checkOTLPEndpoint refuses an endpoint that is not empty and not an
// absolute http or https URL with a host and no query nor fragment: the
// signals' paths are appended to it.
func checkOTLPEndpoint() func(v any) phrase {
	return judged(func(s string) bool {
		if s == "" {
			return true
		}
		u, err := url.Parse(s)
		return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.RawQuery == "" && u.Fragment == "" && u.User == nil
	}, func() phrase { return say("otlp.endpoint") })
}

// checkOTLPWhenFull refuses a full policy the export does not know.
func checkOTLPWhenFull() func(v any) phrase {
	return judged(func(s string) bool {
		switch OTLPFullPolicy(s) {
		case OTLPDropOldest, OTLPDropNewest, OTLPBlock:
			return true
		}
		return false
	}, func() phrase { return say("otlp.when-full") })
}

// checkOTLPSampleRatio refuses a ratio outside [0, 1], NaN included.
func checkOTLPSampleRatio() func(v any) phrase {
	return judged(func(r float64) bool { return r >= 0 && r <= 1 }, func() phrase { return say("otlp.sample-ratio") })
}

// parseOTLPHeaders reads the secret otlp-headers: "key=value" pairs joined
// by commas, each value percent-decoded, as OTEL_EXPORTER_OTLP_HEADERS
// spells them.
func parseOTLPHeaders(raw string) (map[string]string, bool) {
	out := map[string]string{}
	for pair := range strings.SplitSeq(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, found := strings.Cut(pair, "=")
		key = strings.TrimSpace(key)
		decoded, err := url.PathUnescape(strings.TrimSpace(value))
		if !found || key == "" || err != nil {
			return nil, false
		}
		out[key] = decoded
	}
	return out, true
}
