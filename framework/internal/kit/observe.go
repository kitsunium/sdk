// Package kit — observation: the spans and events of a running product.
package kit

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/trace"
)

// The hub's bounds: what it keeps of the traces, and how far a slow Studio
// subscriber may fall behind.
const (
	// tracesPerRoot is how many traces the hub keeps per root node.
	tracesPerRoot int = 50
	// maxTraces is how many traces the hub keeps in all.
	maxTraces int = 2000
	// maxSpansPerTrace is how many spans one trace keeps.
	maxSpansPerTrace int = 128
	// subscriberBuffer is how many events a subscriber may have unread.
	subscriberBuffer int = 256
)

// otelStatusError is STATUS_CODE_ERROR from the OpenTelemetry trace protocol.
// The SDK's public facade exports the type but not its values; the value is
// fixed by the specification, not by the SDK.
const otelStatusError trace.StatusCode = 2

// payloadLimit is how much of each side of a payload a span keeps.
const payloadLimit int = 8 << 10

var (
	// spanKinds maps kit operations to OpenTelemetry span kinds.
	spanKinds = map[string]trace.Kind{
		model.OpRequest:     trace.KindServer,
		model.OpCall:        trace.KindClient,
		model.OpPublish:     trace.KindProducer,
		model.OpDeliver:     trace.KindConsumer,
		model.OpSend:        trace.KindProducer,
		model.OpDeliverMail: trace.KindConsumer,
		model.OpDispatch:    trace.KindClient,
		model.OpAsk:         trace.KindClient,
		model.OpHandle:      trace.KindConsumer,
		model.OpConnect:     trace.KindServer,
	}

	// Frames of these packages are the machinery a call goes through, not where
	// the product made it.
	machinery = []string{
		"github.com/kitsunium/sdk/framework/kit.",
		"github.com/kitsunium/sdk/framework/internal/kit.",
		"github.com/kitsunium/sdk/framework/connectors/",
		"github.com/kitsunium/sdk/pkg/",
		"github.com/kitsunium/sdk/internal/",
		"runtime.",
	}
)

// Call sites, the product's own frames ----------------------------------

// hub is where the running product observes itself. Every unit of work a
// building block does ends here as a span; the hub turns spans into node and
// edge statistics, keeps the recent traces, and fans live events out to the
// Studio. It is only switched on in dev: production pays for nothing but the
// SDK span that carries the trace context.
type hub struct {
	enabled bool
	// onStructure is called when an edge is seen for the first time: the
	// graph's structure changed.
	onStructure func()

	mu     sync.Mutex
	seq    uint64
	nodes  map[string]*counter
	edges  map[string]*edgeCounter
	traces map[string]*traceRec
	// order lists every kept trace, oldest first; ids of dropped traces are
	// skipped when read and compacted away.
	order []string
	// byRoot keeps, per entry node, its most recent traces: a rare write is
	// not pushed out by a page polling three endpoints every two seconds.
	byRoot map[string][]string
	subs   map[chan model.Event]struct{}
	// logs is a ring of the last maxLogs records product code wrote through
	// kit.Log, in dev; logNext is where the next one goes once it is full.
	logs    []model.LogRecord
	logNext int
	logSeq  uint64
}

type counter struct {
	count, errors int64
	last          time.Time
	totalMs       float64
	maxMs         float64
}

type edgeCounter struct {
	counter
	from, to, label string
	kind            model.EdgeKind
}

type traceRec struct {
	spans []model.Span
	root  string
}

// spanStart is what a span says of the work it stands for: the node that
// does it, the node and the edge that caused it, and its operation.
type spanStart struct {
	// node is the node doing the work; from is the node that caused it.
	node, from string
	// edge is the kind of edge from to node.
	edge model.EdgeKind
	// label, op and name say what the work is, as the trace shows it.
	label, op, name string
}

// span is one unit of work in flight on a node.
type span struct {
	a     *App
	sdk   trace.Span
	s     model.Span
	start time.Time
	root  bool
	// restore is the context begin was given, in dev: end puts its pprof
	// labels back on the goroutine, so the work after the span is not
	// charged to the span's node.
	restore context.Context
}

// spanKey carries the span a context runs inside, in dev.
type spanKey struct{}

// add counts one unit of work at at, which took ms and failed or not.
func (c *counter) add(at time.Time, ms float64, failed bool) {
	c.count++
	if failed {
		c.errors++
	}
	c.last = at
	c.totalMs += ms
	c.maxMs = max(c.maxMs, ms)
}

// stats is what the counter says, for the model; nil when it counted nothing.
func (c *counter) stats() *model.Stats {
	if c == nil || c.count == 0 {
		return nil
	}
	last := c.last
	return &model.Stats{
		Count:  c.count,
		Errors: c.errors,
		LastAt: &last,
		AvgMs:  round2(c.totalMs / float64(c.count)),
		MaxMs:  round2(c.maxMs),
	}
}

// newHub is an empty hub; a disabled one observes nothing.
func newHub(enabled bool) *hub {
	return &hub{
		enabled: enabled,
		nodes:   map[string]*counter{},
		edges:   map[string]*edgeCounter{},
		traces:  map[string]*traceRec{},
		byRoot:  map[string][]string{},
		subs:    map[chan model.Event]struct{}{},
	}
}

// record takes a finished span into account and streams it. root says the
// span started a trace in this process: no parent, or a parent elsewhere.
func (h *hub) record(s model.Span, root bool) {
	if !h.enabled {
		return
	}
	end := s.Start.Add(time.Duration(s.Ms * float64(time.Millisecond)))
	failed := s.Status == model.StatusError
	h.mu.Lock()
	// A transaction is part of its node's run, not a run of its own: its
	// span joins the trace and leaves the node's counters as they are.
	if s.Op != model.OpTransaction {
		c := h.nodes[s.Node]
		if c == nil {
			c = &counter{}
			h.nodes[s.Node] = c
		}
		c.add(end, s.Ms, failed)
	}
	newEdge := h.countEdge(&s, end, failed)
	if s.TraceID != "" {
		h.keepSpan(&s, root)
	}
	h.mu.Unlock()
	h.publish(model.Event{Type: model.EventSpan, Time: end, Span: &s})
	if newEdge && h.onStructure != nil {
		h.onStructure()
	}
}

// countEdge counts the span on the edge it travelled, when it travelled one,
// and reports whether that edge was new. The caller holds h.mu.
func (h *hub) countEdge(s *model.Span, end time.Time, failed bool) bool {
	if s.From == "" || s.Edge == "" {
		return false
	}
	id := model.EdgeID(s.From, s.Edge, s.Node, s.Label)
	e, known := h.edges[id]
	if !known {
		e = &edgeCounter{from: s.From, to: s.Node, label: s.Label, kind: s.Edge}
		h.edges[id] = e
	}
	e.add(end, s.Ms, failed)
	return !known
}

// keepSpan adds the span to its trace — a new one evicting the oldest —, and
// a root span's trace to the ones of its node. The caller holds h.mu.
func (h *hub) keepSpan(s *model.Span, root bool) {
	t := h.traces[s.TraceID]
	if t == nil {
		t = &traceRec{}
		h.traces[s.TraceID] = t
		h.order = append(h.order, s.TraceID)
		h.evict()
	}
	if len(t.spans) < maxSpansPerTrace {
		t.spans = append(t.spans, *s)
	}
	if !root || t.root != "" {
		return
	}
	t.root = s.Node
	ring := append(h.byRoot[s.Node], s.TraceID)
	if len(ring) > tracesPerRoot {
		delete(h.traces, ring[0])
		ring = ring[1:]
	}
	h.byRoot[s.Node] = ring
}

// evict drops the oldest traces beyond the global bound, and compacts the
// order once dropped ids dominate it. The caller holds h.mu.
func (h *hub) evict() {
	for len(h.traces) > maxTraces && len(h.order) > 0 {
		delete(h.traces, h.order[0])
		h.order = h.order[1:]
	}
	if len(h.order) > 2*len(h.traces)+64 {
		kept := h.order[:0]
		for _, id := range h.order {
			if _, ok := h.traces[id]; ok {
				kept = append(kept, id)
			}
		}
		h.order = kept
	}
}

// publish stamps an event and hands it to every subscriber. A subscriber
// that does not keep up loses events rather than slowing the product down:
// the stream is a view, never a dependency.
func (h *hub) publish(e model.Event) {
	if !h.enabled {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	e.Seq = h.seq
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// subscribe returns a stream of events and the function that ends it.
func (h *hub) subscribe() (<-chan model.Event, func()) {
	ch := make(chan model.Event, subscriberBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// nodeStats returns the observed statistics of a node.
func (h *hub) nodeStats(id string) *model.Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nodes[id].stats()
}

// observedEdges returns every edge seen at runtime.
func (h *hub) observedEdges() []model.Edge {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]model.Edge, 0, len(h.edges))
	for id, e := range h.edges {
		out = append(out, model.Edge{ID: id, From: e.from, To: e.to, Kind: e.kind, Label: e.label, Observed: e.stats()})
	}
	return out
}

// recentTraces returns up to limit traces, newest first; with a root, only
// the traces that entered the product through that node; with a node, only
// those that went through it — a delivery runs in the trace of the request
// that published, never as a root of its own.
func (h *hub) recentTraces(limit int, root, node string) []model.Trace {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := h.order
	if root != "" {
		ids = h.byRoot[root]
	}
	out := make([]model.Trace, 0, min(limit, len(ids)))
	for i := len(ids) - 1; i >= 0 && len(out) < limit; i-- {
		t, ok := h.traces[ids[i]]
		if !ok || node != "" && !slices.ContainsFunc(t.spans, func(s model.Span) bool { return s.Node == node }) {
			continue
		}
		out = append(out, assemble(ids[i], t.spans))
	}
	return out
}

// trace returns one trace.
func (h *hub) trace(id string) (model.Trace, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.traces[id]
	if !ok {
		return model.Trace{}, false
	}
	return assemble(id, t.spans), true
}

// assemble orders a trace's spans and summarizes it.
func assemble(id string, spans []model.Span) model.Trace {
	spans = slices.Clone(spans)
	slices.SortStableFunc(spans, func(a, b model.Span) int { return a.Start.Compare(b.Start) })
	t := model.Trace{TraceID: id, Status: model.StatusOK, Spans: spans}
	if len(spans) == 0 {
		return t
	}
	ids := map[string]bool{}
	for _, s := range spans {
		ids[s.SpanID] = true
	}
	root := spans[0]
	for _, s := range spans {
		if s.ParentID == "" || !ids[s.ParentID] {
			root = s
			break
		}
	}
	t.Root, t.Name, t.Start = root.Node, root.Name, root.Start
	var end time.Time
	for _, s := range spans {
		e := s.Start.Add(time.Duration(s.Ms * float64(time.Millisecond)))
		if e.After(end) {
			end = e
		}
		if s.Status == model.StatusError {
			t.Status = model.StatusError
		}
	}
	t.Ms = round2(float64(end.Sub(spans[0].Start)) / float64(time.Millisecond))
	return t
}

// spanOf returns the span ctx runs inside, in dev, or nil.
func spanOf(ctx context.Context) *span {
	sp, _ := ctx.Value(spanKey{}).(*span)
	return sp
}

// begin starts a span for work done on st.node, caused by st.from over an
// edge of st.edge's kind. The returned context runs inside st.node.
func (a *App) begin(ctx context.Context, st *spanStart) (context.Context, *span) {
	parent := trace.SpanContextFromContext(ctx)
	kind, ok := spanKinds[st.op]
	if !ok {
		kind = trace.KindInternal
	}
	ctx, sdk := a.tracer.Start(ctx, st.name, trace.SpanParams{Kind: kind})
	sc := sdk.SpanContext()
	sp := &span{a: a, sdk: sdk, start: a.clock.Now(), root: startsTrace(parent, st.op)}
	sp.s = model.Span{
		TraceID: sc.TraceID.String(),
		SpanID:  sc.SpanID.String(),
		Node:    st.node,
		From:    st.from,
		Edge:    st.edge,
		Label:   st.label,
		Op:      st.op,
		Name:    st.name,
		Start:   sp.start.UTC(),
	}
	if parent.IsValid() {
		sp.s.ParentID = parent.SpanID.String()
	}
	sp.user(ctx)
	if a.hub.enabled && st.from != "" {
		a.callSite(sp)
	}
	return a.inside(ctx, sp, st.node), sp
}

// startsTrace reports whether a span of op under parent starts a trace: when
// it has no parent, or a parent from another process. A delivery's parent —
// a message's, a mail's, a queued command's — crossed a queue, not a
// process: its trace belongs to whoever published.
func startsTrace(parent trace.SpanContext, op string) bool {
	if !parent.IsValid() {
		return true
	}
	return parent.Remote && op != model.OpDeliver && op != model.OpDeliverMail && op != model.OpHandle
}

// inside is ctx running inside node, with the span sp. In dev the goroutine
// is the node's while the span lasts: a CPU sample taken now — or in a
// goroutine started now, which inherits the label — is charged to it; end
// restores the labels ctx carries.
func (a *App) inside(ctx context.Context, sp *span, node string) context.Context {
	out := withNode(ctx, node, a)
	if !a.hub.enabled {
		return out
	}
	sp.restore = ctx
	out = pprof.WithLabels(out, pprof.Labels(labelNode, node))
	pprof.SetGoroutineLabels(out)
	return context.WithValue(out, spanKey{}, sp)
}

// detailed reports whether the span keeps details — payloads — for the
// Studio: in dev only.
func (sp *span) detailed() bool { return sp.a.hub.enabled }

// user records the authenticated caller ctx acts for.
func (sp *span) user(ctx context.Context) {
	if uid, ok := UserID(ctx); ok {
		sp.s.User = string(uid)
	}
}

// request records what went in: a decoded request, a delivered message.
func (sp *span) request[V any](v V) {
	raw, cut := redactValue(v, payloadLimit)
	sp.payload(raw, nil, cut)
}

// replied records what an endpoint answered: its response, or the error
// body.
func (sp *span) replied[R any](resp R, err error, noBody bool) {
	var raw json.RawMessage
	var cut bool
	switch {
	case err != nil:
		_, body := describe(err)
		raw, cut = redactValue(wireError{Error: body}, payloadLimit)
	case !noBody:
		raw, cut = redactValue(resp, payloadLimit)
	}
	sp.payload(nil, raw, cut)
}

// payload keeps the span's input and output, cut when they were too long.
func (sp *span) payload(in, out json.RawMessage, cut bool) {
	if in == nil && out == nil {
		return
	}
	if sp.s.Payload == nil {
		sp.s.Payload = &model.Payload{}
	}
	if in != nil {
		sp.s.Payload.Request = in
	}
	if out != nil {
		sp.s.Payload.Response = out
	}
	sp.s.Payload.Truncated = sp.s.Payload.Truncated || cut
}

// attr adds an allow-listed attribute.
func (sp *span) attr(key, value string) {
	if sp.s.Attrs == nil {
		sp.s.Attrs = map[string]string{}
	}
	sp.s.Attrs[key] = value
}

// end finishes the span. Only what describe says is wire-safe about err
// travels: the code and the public message, never err.Error().
//
// Only a failure of the product is an error: what describe answers with a
// 5xx. A refusal the caller has to fix — not found, invalid, conflict,
// unauthenticated, rate limited — keeps its code and stays ok, as
// OpenTelemetry leaves a server span unset for a 4xx: a store lookup that
// finds nothing, or a request for a task that does not exist, is not the
// product breaking.
func (sp *span) end(err error) {
	sp.s.Ms = round2(float64(sp.a.clock.Now().Sub(sp.start)) / float64(time.Millisecond))
	sp.s.Status = model.StatusOK
	if err != nil {
		status, body := describe(err)
		sp.s.Code = body.Code
		if status >= http.StatusInternalServerError {
			sp.s.Status, sp.s.Error = model.StatusError, body.Message
			sp.sdk.SetStatus(otelStatusError, body.Message)
		}
	}
	sp.sdk.End()
	if sp.restore != nil {
		pprof.SetGoroutineLabels(sp.restore)
	}
	sp.a.report(sp, err)
	sp.a.hub.record(sp.s, sp.root)
}

// callSite records, as code.filepath and code.lineno, the line of the
// product's code that made the call the span stands for: the first frame
// outside kit, the SDK and the runtime, when it lies in the product's module.
// Only in dev: two rows of a sequence can make the same call — one in each
// branch of an if — and the line is what tells the Studio which one ran.
func (a *App) callSite(sp *span) {
	var pcs [24]uintptr
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs[:])])
	for {
		f, more := frames.Next()
		if f.Function != "" && !slices.ContainsFunc(machinery, func(p string) bool { return strings.HasPrefix(f.Function, p) }) {
			if src := a.source(&pos{file: f.File, line: f.Line}); src != nil && filepath.IsLocal(filepath.FromSlash(src.File)) {
				sp.attr("code.filepath", src.File)
				sp.attr("code.lineno", strconv.Itoa(src.Line))
			}
			return
		}
		if !more {
			return
		}
	}
}

// round2 rounds f to two decimals.
func round2(f float64) float64 {
	return math.Round(f*100) / 100
}
