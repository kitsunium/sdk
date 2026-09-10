// Package trace — SpanKind: the span's relationship to its neighbours.
package trace

// SpanKind says how a span relates to its parent and its children, which is what
// lets a backend assemble a latency waterfall instead of a flat list.
//
// The five values and their integers are
// opentelemetry/proto/trace/v1.Span.SpanKind, copied from the schema so the OTLP
// encoder emits the integer directly (OTLP/JSON encodes an enum as its NUMBER,
// never as its name).
type SpanKind int32

// The five kinds, in the schema's own declaration order: iota reproduces
// SPAN_KIND_UNSPECIFIED = 0 through SPAN_KIND_CONSUMER = 5 exactly, which is
// what the OTLP encoder emits as the enum's integer.
const (
	// SpanKindUnspecified is SPAN_KIND_UNSPECIFIED (0). It is the zero value,
	// and the schema's own comment says an implementation should set INTERNAL
	// instead — so it CLAMPS rather than travels (ADR 0031 §clamp): a span
	// created with no stated kind is doing work inside this process, which is
	// what INTERNAL means. Nothing this SDK emits carries it.
	SpanKindUnspecified SpanKind = iota
	// SpanKindInternal is SPAN_KIND_INTERNAL (1): work inside one process,
	// with no remote counterpart.
	SpanKindInternal
	// SpanKindServer is SPAN_KIND_SERVER (2): handling an inbound synchronous
	// request. Its parent is normally a remote CLIENT span.
	SpanKindServer
	// SpanKindClient is SPAN_KIND_CLIENT (3): making an outbound synchronous
	// request and waiting for it.
	SpanKindClient
	// SpanKindProducer is SPAN_KIND_PRODUCER (4): enqueuing a message whose
	// handling happens later, and elsewhere.
	SpanKindProducer
	// SpanKindConsumer is SPAN_KIND_CONSUMER (5): handling a message that was
	// enqueued earlier. Its parent finished long before it started, which is
	// why it is not simply a SERVER span.
	SpanKindConsumer
)

// Resolved returns the kind a span actually carries: SpanKindUnspecified clamps
// to SpanKindInternal, every declared value is honoured, and anything else — a
// cast, since there is no other way to produce one — clamps too.
//
// It clamps where Temporality refuses (ADR 0044 §Decision 3), and the asymmetry
// applies the clamp-or-refuse question honestly: an unstated temporality changes what a
// NUMBER means, so no default can be chosen for the caller; an unstated kind
// changes only how a span is drawn, and the specification itself names the
// default. A clamp is right exactly when the SDK is not substituting judgement.
func (k SpanKind) Resolved() SpanKind {
	//: every value the schema declares is honoured as written.
	switch k {
	//: the four remote kinds, plus the explicit internal one.
	case SpanKindInternal, SpanKindServer, SpanKindClient, SpanKindProducer, SpanKindConsumer:
		//: as the caller stated it.
		return k
	//: SpanKindUnspecified, or an integer cast into the type.
	default:
		//: the schema's own recommended default.
		return SpanKindInternal
	}
}

// String renders the kind as its OpenTelemetry name, for a text rendering and
// for a test failure that has to be readable. It is never what goes on the wire:
// OTLP/JSON carries the integer.
func (k SpanKind) String() string {
	//: one name per declared value.
	switch k {
	//: in schema order.
	case SpanKindInternal:
		//: work inside this process.
		return "INTERNAL"
	//: inbound.
	case SpanKindServer:
		//: handling a request.
		return "SERVER"
	//: outbound.
	case SpanKindClient:
		//: making a request.
		return "CLIENT"
	//: enqueue.
	case SpanKindProducer:
		//: publishing a message.
		return "PRODUCER"
	//: dequeue.
	case SpanKindConsumer:
		//: handling a message.
		return "CONSUMER"
	//: the zero value and every cast.
	default:
		//: named, so a reader sees the unresolved state rather than a number.
		return "UNSPECIFIED"
	}
}
