// Package trace — the live, recording span.
package trace

import (
	"slices"
	"sync"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
)

// span is one recording core/trace.Span.
//
// Every method takes the mutex, and that is a deliberate trade rather than an
// oversight: a span is written from the goroutine doing the work it measures,
// and a handler that fans out to five goroutines and annotates the same span
// from all five is the ordinary case, not the exotic one. A lock-free design
// would need a lock-free slice append, which is a bigger claim than a mutex held
// for the length of an append.
//
// The mutex covers exactly the fields that mutate. The identity fields —
// context, parent, name, kind, start — are written once in newSpan and never
// again, so they are read without it.
type span struct {
	// tracer owns the sink and the clock this span reports through.
	tracer *Tracer
	// context is this span's own identity. Immutable after construction.
	context coretrace.SpanContextValue
	// parent is the context this span descends from, invalid for a root.
	parent coretrace.SpanContextValue
	// name is the operation label. Immutable after construction.
	name string
	// kind is the resolved SpanKind. Immutable after construction.
	kind coretrace.SpanKind
	// start is the instant the span opened. Immutable after construction.
	start time.Time
	// links are the links supplied at Start. Immutable after construction.
	links []coretrace.LinkValue

	// mu guards everything below it.
	mu sync.Mutex
	// attrs accumulate; a repeated Key replaces rather than duplicates.
	attrs []coremetrics.AttrValue
	// events accumulate in the order they were recorded.
	events []coretrace.EventValue
	// status is the recorded outcome.
	status coretrace.StatusValue
	// ended is what makes End idempotent.
	ended bool
	// end is the instant the span closed.
	end time.Time
}

// newSpan builds a recording span. It normalises the attributes ONCE here — the
// same SortAttrs the metrics domain uses, so an unusable attribute set panics at
// the call site that wrote it rather than at the first export.
func newSpan(tracer *Tracer, context, parent coretrace.SpanContextValue, name string, params coretrace.SpanParams) *span {
	//: an unset StartTime means "now", which is what all but a replaying
	//: caller wants (ADR 0031 §clamp).
	start := params.StartTime
	//: the clock is the tracer's, so a test can move it.
	if start.IsZero() {
		//: stamp it now.
		start = tracer.cfg.Clock.Now()
	}
	//: links are copied so the caller's slice cannot change under the span.
	return &span{
		tracer:  tracer,
		context: context,
		parent:  parent,
		name:    name,
		kind:    params.Kind.Resolved(),
		start:   start,
		links:   normalizeLinks(params.Links),
		attrs:   coremetrics.SortAttrs(params.Attrs),
	}
}

// SpanContext implements core/trace.Span.
func (s *span) SpanContext() coretrace.SpanContextValue {
	//: immutable after construction, so no lock is taken.
	return s.context
}

// SetAttrs implements core/trace.Span. A repeated Key REPLACES the previous
// value rather than appending a second entry.
//
// Replacing is what the OTel API specifies, and it is also the only behaviour
// that keeps the attribute set a set: two `http.response.status_code` entries on
// one span are a payload a backend renders arbitrarily, and the arbitrary choice
// differs between backends.
func (s *span) SetAttrs(attrs ...coremetrics.AttrValue) {
	//: nothing to record.
	if len(attrs) == 0 {
		//: no lock taken for a no-op.
		return
	}
	//: validate + sort the incoming set before taking the lock — SortAttrs
	//: panics on an unusable set, and panicking under a held mutex would
	//: leave the span locked for every other goroutine annotating it.
	incoming := sortedIncoming(attrs)
	//: merge under the lock.
	s.mu.Lock()
	defer s.mu.Unlock()
	//: a span that has ended is closed to further writes; the value is
	//: already on its way to an exporter, so a late write would either race
	//: or be silently lost — being explicitly ignored is the honest one.
	if s.ended {
		//: dropped.
		return
	}
	//: merge each incoming attribute, replacing on Key.
	for _, attr := range incoming {
		//: the set is sorted by Key, so the position is a binary search.
		at, found := slices.BinarySearchFunc(s.attrs, attr, coremetrics.CompareAttrKey)
		//: an existing Key is overwritten in place.
		if found {
			//: replace.
			s.attrs[at] = attr
			//: next.
			continue
		}
		//: a new Key is inserted where the ordering puts it.
		s.attrs = slices.Insert(s.attrs, at, attr)
	}
}

// sortedIncoming returns attrs in the canonical order, VALIDATED, cloning only
// when a clone buys something.
//
// SortAttrs always clones, and its reason is ownership: it SORTS in place, so it
// must not sort the caller's array. A single attribute is already sorted, so
// there is nothing to sort and nothing to protect — and the merge below only
// ever COPIES elements out of this slice, never keeps it. The validation still
// runs, still on this goroutine, still outside the lock.
//
// One attribute is not a corner case: it is what ServerMiddleware does with the
// response status on every request, and what almost every hand-written
// `span.SetAttrs(...)` at an instrumentation site does.
func sortedIncoming(attrs []coremetrics.AttrValue) []coremetrics.AttrValue {
	//: more than one attribute has an order to establish, and establishing it
	//: means owning the array first.
	if len(attrs) > 1 {
		//: clone + sort + validate.
		return coremetrics.SortAttrs(attrs)
	}
	//: exactly one — SetAttrs already returned on zero. Validate it with the
	//: same call SortAttrs would have made, on a set that is trivially sorted.
	coremetrics.ValidateAttrs(attrs)
	//: the caller's array, read and never retained.
	return attrs
}

// AddEvent implements core/trace.Span, stamping the event with the tracer's
// clock.
func (s *span) AddEvent(name string, attrs ...coremetrics.AttrValue) {
	//: normalise outside the lock, for the reason SetAttrs does.
	event := coretrace.EventValue{
		Time:  s.tracer.cfg.Clock.Now(),
		Name:  name,
		Attrs: coremetrics.SortAttrs(attrs),
	}
	//: append under the lock.
	s.mu.Lock()
	defer s.mu.Unlock()
	//: an ended span is closed to further writes.
	if s.ended {
		//: dropped.
		return
	}
	//: events keep the order they were recorded in.
	s.events = append(s.events, event)
}

// SetStatus implements core/trace.Span. The LAST call wins, and a message is
// kept only on StatusError — StatusValue.Resolved applies that at End.
func (s *span) SetStatus(code coretrace.StatusCode, message string) {
	//: record under the lock.
	s.mu.Lock()
	defer s.mu.Unlock()
	//: an ended span is closed to further writes.
	if s.ended {
		//: dropped.
		return
	}
	//: the last verdict is the one that ships.
	s.status = coretrace.StatusValue{Code: code, Message: message}
}

// End implements core/trace.Span: it closes the span and hands the immutable
// SpanValue to the tracer's sink.
//
// It is IDEMPOTENT. A second End is ignored rather than exporting the span
// twice, because the common way to get one is a `defer span.End()` beside an
// explicit End on an early return — a duplicate span is a duplicate row in every
// backend and the duplicate is the one nobody notices.
//
// The sink is called OUTSIDE the lock. It is caller-supplied, it may be slow,
// and holding a span's mutex across it would serialise every annotation on a
// span that is already finished.
func (s *span) End() {
	//: close under the lock and take the value out with it.
	value, first := s.finish()
	//: a second End does nothing at all.
	if !first {
		//: already exported.
		return
	}
	//: the sink runs on this goroutine, without the span's lock held.
	s.tracer.cfg.Sink(value)
}

// finish marks the span ended and builds its immutable value. It reports false
// when the span had already ended.
func (s *span) finish() (value coretrace.SpanValue, first bool) {
	//: everything below mutates, so the lock covers the whole transition.
	s.mu.Lock()
	defer s.mu.Unlock()
	//: idempotence: only the first End produces a value.
	if s.ended {
		//: no value, and the caller does not call the sink.
		return coretrace.SpanValue{}, false
	}
	//: the transition itself.
	s.ended = true
	s.end = s.tracer.cfg.Clock.Now()
	//: the exported value OWNS its slices, and it owns them by TRANSFER rather
	//: than by copy: the span hands the arrays over and drops its own
	//: references, so there is exactly one holder afterwards — the same
	//: guarantee a clone gives, for no allocation.
	//:
	//: This was `slices.Clone` on both, and the reason given was "a sink that
	//: held the value while a late (ignored) write reallocated would otherwise
	//: observe a tear". That event cannot happen: every write site above
	//: returns on `s.ended` under this same mutex, which the line above just
	//: set, so an ignored write reallocates nothing. The clone was defending
	//: against its own description of an impossibility, and it cost one
	//: allocation and 240 B on every sampled span — 7.9 % of a traced HTTP
	//: request (BENCH.md §1.4).
	//:
	//: Nilling is what makes the transfer real rather than a rename. It is
	//: also what a reader should check first if a post-End write is ever
	//: allowed: this line is the one that would have to go back to a clone.
	attrs, events := s.attrs, s.events
	//: the span is ended and reads neither again; giving them up is what makes
	//: the value the sole owner.
	s.attrs, s.events = nil, nil
	//: links were never cloned and never mutated after construction.
	return coretrace.SpanValue{
		Context:   s.context,
		Parent:    s.parent,
		Name:      s.name,
		Kind:      s.kind,
		StartTime: s.start,
		EndTime:   s.end,
		Attrs:     attrs,
		Events:    events,
		Links:     s.links,
		Status:    s.status.Resolved(),
	}, true
}

// normalizeLinks sorts each link's attributes and drops links that name no span.
//
// A link whose context is invalid is dropped rather than emitted: OTLP would
// carry an all-zero trace-id, which W3C Trace Context declares invalid and the
// OTLP encoder here refuses outright — so keeping it would turn one bad link
// into a whole payload nobody can export.
func normalizeLinks(links []coretrace.LinkValue) []coretrace.LinkValue {
	//: the common case is no links at all.
	if len(links) == 0 {
		//: nil, which is what "no links" is spelled as.
		return nil
	}
	//: at most one entry per supplied link.
	kept := make([]coretrace.LinkValue, 0, len(links))
	//: one link at a time, in the order the caller wrote them.
	for _, link := range links {
		//: a link that names no span is not a link.
		if link.IsValid() {
			//: normalised, so its attributes are sorted and owned.
			kept = append(kept, link.Normalized())
		}
	}
	//: an all-invalid set collapses to no links at all.
	if len(kept) == 0 {
		//: nil rather than an empty slice, so the encoder omits the field.
		return nil
	}
	//: the surviving links.
	return kept
}
