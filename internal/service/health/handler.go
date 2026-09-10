// Package health — hosts the three HTTP handlers. There are three because a
// single handler that read the probe out of the URL would let a routing
// mistake serve liveness on the readiness path — which is the domain's central
// failure wearing a different hat.
package health

import (
	"encoding/json"
	"net/http"
	"strconv"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// genericReason is what a probe body says about a failure carrying no errs
// Public of its own. It names a condition and never a cause: a probe endpoint
// is routinely reachable by more than whoever added it.
const genericReason string = "A health check reported a failure"

// fallbackBody is written when rendering itself fails. It is a byte literal
// rather than a marshalled value so that the one path that cannot rely on the
// encoder does not depend on it.
var fallbackBody = []byte(`{"status":"unhealthy"}`)

// NewStartupHandler serves the startup probe: 200 once every startup check has
// passed, 503 while any has not.
func NewStartupHandler(registry corehealth.Health, cfg HandlerConfig) http.Handler {
	//: one handler per probe; see the package comment.
	return &handler{registry: registry, probe: corehealth.ProbeStartup, cfg: cfg}
}

// NewReadinessHandler serves the readiness probe: 200 while the replica can
// take traffic (healthy OR degraded), 503 otherwise — including while starting
// and after Drain.
func NewReadinessHandler(registry corehealth.Health, cfg HandlerConfig) http.Handler {
	//: the only probe a dependency check can influence.
	return &handler{registry: registry, probe: corehealth.ProbeReadiness, cfg: cfg}
}

// NewLivenessHandler serves the liveness probe: 200 unless the process is
// irrecoverable.
//
// Nothing outside the process can make this return 503, because nothing
// outside the process can be registered on it — see core/health.SelfCheck.
func NewLivenessHandler(registry corehealth.Health, cfg HandlerConfig) http.Handler {
	//: the only probe whose failure restarts anything.
	return &handler{registry: registry, probe: corehealth.ProbeLiveness, cfg: cfg}
}

// handler serves exactly one probe.
type handler struct {
	// registry answers the probe.
	registry corehealth.Health
	// probe is fixed at construction and is never read from the request: a
	// route cannot redirect a liveness handler onto readiness.
	probe corehealth.Probe
	// cfg carries the body's verbosity.
	cfg HandlerConfig
}

// ServeHTTP answers one probe request.
//
// It writes to the ResponseWriter and to nowhere else — not stdout, which may
// be the process's protocol channel, and not stderr (ADR 0030). A caller who
// wants a probe logged wires Config.OnReport, which sees the whole error.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	//: a probe is a read. Anything else is either a mistake or somebody
	//: exploring, and both deserve the same flat answer.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		//: answered in full; no probe is run for a verb this endpoint refuses.
		return
	}
	//: the query string is deliberately not read. An endpoint that changes
	//: what it does based on a parameter is an endpoint an unauthenticated
	//: caller can steer.
	report := h.registry.Probe(r.Context(), h.probe)
	body := h.render(report)
	header := w.Header()
	header.Set("Content-Type", "application/json; charset=utf-8")
	//: a cached probe response is a probe that lies — an intermediary
	//: replaying yesterday's 200 is indistinguishable from a healthy replica.
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(statusCode(report.Status))
	//: net/http discards the body of a HEAD itself, so there is no second
	//: branch to keep in step with the first. The write error is dropped on
	//: purpose: a probe has nowhere to report a client that hung up — the
	//: response IS the report, and ADR 0030 forbids writing it anywhere else.
	_, _ = w.Write(body)
}

// statusCode maps an aggregate to the only thing most callers read.
func statusCode(status corehealth.Status) int {
	//: degraded SERVES — that is what marking a check non-critical means, and
	//: a 503 here would make the flag decorative.
	if status.Serving() {
		//: keep the traffic.
		return http.StatusOK
	}
	//: the conventional "not me, try another replica".
	return http.StatusServiceUnavailable
}

// render turns a report into the bytes that leave the process.
func (h *handler) render(report corehealth.ReportValue) []byte {
	body := bodyValue{Status: report.Status.String()}
	//: the aggregate alone is the default; see HandlerConfig.Detail.
	if h.cfg.Detail {
		body.Checks = renderChecks(report)
	}
	encoded, err := json.Marshal(body)
	//: the shape is closed and every field is a string, an int64 or a bool,
	//: so this cannot fail — but a probe that panicked while rendering its own
	//: answer would be the worst possible failure mode for this endpoint.
	if err != nil {
		//: a fixed, honest, conservative body.
		return fallbackBody
	}
	//: the whole response.
	return encoded
}

// renderChecks projects each result onto the wire shape.
func renderChecks(report corehealth.ReportValue) []checkValue {
	checks := make([]checkValue, 0, len(report.Results))
	//: registration order, exactly as the report carries it — two identical
	//: probes must render identically.
	for _, result := range report.Results {
		check := checkValue{
			Name:     result.Name,
			Status:   result.Status.String(),
			TookMs:   result.Took.Milliseconds(),
			AgeMs:    result.Age(report.At).Milliseconds(),
			TimedOut: result.TimedOut,
			Cached:   result.Cached,
		}
		//: a passing check has nothing to explain, and Reason is spelled
		//: omitempty so leaving it unset drops the member from the body
		//: entirely. Asking publicReason for the public half of a nil error
		//: would be asking a question with no answer.
		if result.Err != nil {
			check.Reason = publicReason(result.Err)
		}
		checks = append(checks, check)
	}
	//: one entry per contributing check.
	return checks
}

// publicReason is the ONLY path by which anything derived from a check's error
// reaches the wire. It always names something: err is a FAILURE, which its one
// caller has already established, so there is no "no reason" answer to give.
//
// errs.PublicOf walks to the deepest *errs.Error and returns its wire-safe
// half — which the SDK validated at construction: at most 120 runes, no
// newline, and written by whoever declared the sentinel to be read by a
// stranger. A chain with no *errs.Error in it yields "", and that becomes the
// SDK's own generic string rather than err.Error(): a driver message carries
// hosts, ports and occasionally credentials, and trimming one is a guess about
// where the secret is.
func publicReason(err error) string {
	//: the deepest declared-public half, if the chain has one.
	if public := kerrs.PublicOf(err); public != "" {
		//: the caller's own wire-safe message, or an SDK sentinel's.
		return public
	}
	//: no declared public half — say nothing about the cause.
	return genericReason
}
