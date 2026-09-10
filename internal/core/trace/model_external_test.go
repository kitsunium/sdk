package trace_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	coretrace "github.com/kitsunium/sdk/internal/core/trace"
)

// TestAttributesAreTheSameTypeAsMetrics pins ADR 0051 §Decision 2 as a compile
// and value fact rather than a claim in a comment.
//
// If `core/trace` ever grows a TWIN of the attribute model, this stops compiling.
// That matters because the twin would be invisible in review — it would encode
// identically and behave identically — and would surface only when exemplars need
// to carry a trace id onto a metric data point across a boundary that no longer
// has one type.
func TestAttributesAreTheSameTypeAsMetrics(t *testing.T) {
	//: every attribute-shaped field of this domain's model is coremetrics'
	//: type. There is no coretrace.AttrValue to be a twin of it, and this
	//: assignment is what stops compiling the day somebody adds one.
	attr := coremetrics.String("k", "v")
	span := coretrace.SpanValue{Attrs: []coremetrics.AttrValue{attr}}
	event := coretrace.EventValue{Attrs: span.Attrs}
	link := coretrace.LinkValue{Attrs: event.Attrs}
	params := coretrace.SpanParams{Attrs: link.Attrs}
	if params.Attrs[0] != attr {
		t.Error("an attribute must cross every model type unchanged")
	}
	batch := coretrace.SpansValue{Resource: coremetrics.ResourceValue{Attrs: params.Attrs}}
	if batch.Resource.Attrs[0].Key != "k" {
		t.Error("a Resource carries the same attribute type a span does")
	}
}

// TestScopeDefaultNamesTheTracePackage pins the one place reusing the metrics
// types would have produced a WRONG answer: an InstrumentationScope names the
// library that produced THIS signal, so a span batch stamped
// "…/pkg/v1/metrics" would tell a backend the metrics package emitted spans.
func TestScopeDefaultNamesTheTracePackage(t *testing.T) {
	normalized := coretrace.NormalizeScope(coremetrics.ScopeValue{})
	if normalized.Name != coretrace.DefaultScopeName {
		t.Errorf("scope name = %q, want %q", normalized.Name, coretrace.DefaultScopeName)
	}
	if normalized.Name == coremetrics.DefaultScopeName {
		t.Error("the trace scope default must not be the metrics one")
	}
	named := coretrace.NormalizeScope(coremetrics.ScopeValue{Name: "mine", Version: "1"})
	if named.Name != "mine" || named.Version != "1" {
		t.Error("a named scope is the caller's and must never be overwritten")
	}
}

// TestSpanKindResolvedClampsToInternal pins the schema's own recommended default,
// and the asymmetry with metrics' Temporality, which REFUSES its unset value.
//
// An unstated temporality changes what a NUMBER means, so no default can be
// chosen on the caller's behalf. An unstated kind changes only how a span is
// drawn, and the specification names the default. A clamp is right exactly when
// the SDK is not substituting judgement (ADR 0031).
func TestSpanKindResolvedClampsToInternal(t *testing.T) {
	cases := []struct {
		in   coretrace.SpanKind
		want coretrace.SpanKind
	}{
		{coretrace.SpanKindUnspecified, coretrace.SpanKindInternal},
		{coretrace.SpanKindInternal, coretrace.SpanKindInternal},
		{coretrace.SpanKindServer, coretrace.SpanKindServer},
		{coretrace.SpanKindClient, coretrace.SpanKindClient},
		{coretrace.SpanKindProducer, coretrace.SpanKindProducer},
		{coretrace.SpanKindConsumer, coretrace.SpanKindConsumer},
		{coretrace.SpanKind(99), coretrace.SpanKindInternal},
		{coretrace.SpanKind(-1), coretrace.SpanKindInternal},
	}
	for _, tc := range cases {
		if got := tc.in.Resolved(); got != tc.want {
			t.Errorf("SpanKind(%d).Resolved() = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestStatusResolvedDropsAMessageWithoutAnError pins the schema's "message …
// SHOULD be used only if the code is ERROR".
//
// It is enforced rather than trusted because a message hanging off an OK status
// renders as an error in one backend's UI and vanishes in another's — a
// disagreement nobody notices until two people are looking at the same trace.
func TestStatusResolvedDropsAMessageWithoutAnError(t *testing.T) {
	cases := []struct {
		name string
		in   coretrace.StatusValue
		want coretrace.StatusValue
	}{
		{"error keeps its message", coretrace.StatusValue{Code: coretrace.StatusError, Message: "boom"}, coretrace.StatusValue{Code: coretrace.StatusError, Message: "boom"}},
		{"ok drops its message", coretrace.StatusValue{Code: coretrace.StatusOK, Message: "fine"}, coretrace.StatusValue{Code: coretrace.StatusOK}},
		{"unset drops its message", coretrace.StatusValue{Message: "orphan"}, coretrace.StatusValue{}},
		{"a cast clamps to unset", coretrace.StatusValue{Code: coretrace.StatusCode(7)}, coretrace.StatusValue{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Resolved(); got != tc.want {
				t.Errorf("Resolved() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestSpanValueDurationAndRoot pins the two derived facts, including the ones
// that must NOT produce a plausible wrong answer.
func TestSpanValueDurationAndRoot(t *testing.T) {
	start := time.Unix(0, 1_000_000)
	span := coretrace.SpanValue{StartTime: start, EndTime: start.Add(time.Second)}
	if got := span.Duration(); got != time.Second {
		t.Errorf("Duration = %v, want 1s", got)
	}
	if !span.IsRoot() {
		t.Error("a span with the zero parent context is a root")
	}
	unended := coretrace.SpanValue{StartTime: start}
	if got := unended.Duration(); got != 0 {
		t.Errorf("an unended span reports %v; it must report 0, not a negative interval", got)
	}
	reversed := coretrace.SpanValue{StartTime: start.Add(time.Second), EndTime: start}
	if got := reversed.Duration(); got != 0 {
		t.Errorf("an end before its start reports %v; it must report 0", got)
	}
}

// TestSpanContextValidityAndSampling pins the two normative validity rules and
// the fact that the sampled bit is the only sampling question anything asks.
func TestSpanContextValidityAndSampling(t *testing.T) {
	valid, err := coretrace.ParseTraceParent(specHeader)
	if err != nil {
		t.Fatalf("ParseTraceParent: %v", err)
	}
	if !valid.IsValid() || !valid.IsSampled() {
		t.Error("the specification example is a valid, sampled context")
	}
	noTrace := valid
	noTrace.TraceID = coretrace.TraceID{}
	if noTrace.IsValid() {
		t.Error("an all-zero trace-id is invalid (§3.2.2.3)")
	}
	noSpan := valid
	noSpan.SpanID = coretrace.SpanID{}
	if noSpan.IsValid() {
		t.Error("an all-zero span-id is invalid (§3.2.2.4)")
	}
}

// TestContextCarriesAnInvalidContextDeliberately pins why an invalid context is
// STORED rather than skipped: it is how "this scope has no trace" is expressed,
// and it must SHADOW an outer one rather than silently re-parenting children onto
// a trace that ended.
func TestContextCarriesAnInvalidContextDeliberately(t *testing.T) {
	outer, err := coretrace.ParseTraceParent(specHeader)
	if err != nil {
		t.Fatalf("ParseTraceParent: %v", err)
	}
	ctx := coretrace.ContextWithSpanContext(context.Background(), outer)
	if coretrace.SpanContextFromContext(ctx).TraceID != outer.TraceID {
		t.Fatal("the stored context was not read back")
	}
	shadowed := coretrace.ContextWithSpanContext(ctx, coretrace.SpanContextValue{})
	if coretrace.SpanContextFromContext(shadowed).IsValid() {
		t.Error("an invalid context must shadow the outer one, not be skipped")
	}
	if coretrace.SpanContextFromContext(context.Background()).IsValid() {
		t.Error("a context with no trace yields the invalid zero value")
	}
}

// stubExporter is a SpanExporter that records what it was handed.
type stubExporter struct {
	name  coretrace.ExporterName
	fail  error
	spans coretrace.SpansValue
}

// Name implements core/trace.SpanExporter.
func (e *stubExporter) Name() coretrace.ExporterName { return e.name }

// Export implements core/trace.SpanExporter.
func (e *stubExporter) Export(spans coretrace.SpansValue) error {
	e.spans = spans
	return e.fail
}

// TestExporterRegistry pins the registry's three behaviours: lookup, a sorted
// listing, and the typed verdict for a name nothing registered.
func TestExporterRegistry(t *testing.T) {
	first := &stubExporter{name: "zz-model-test"}
	second := &stubExporter{name: "aa-model-test", fail: errors.New("sink refused")}
	coretrace.RegisterExporter(first)
	coretrace.RegisterExporter(second)
	if found, ok := coretrace.LookupExporter(first.name); !ok || found != first {
		t.Error("a registered exporter must be reachable by name")
	}
	names := coretrace.AvailableExporters()
	if !slices.Contains(names, first.name) || !slices.Contains(names, second.name) {
		t.Errorf("AvailableExporters = %v, missing a registered name", names)
	}
	if !slices.IsSorted(names) {
		t.Errorf("AvailableExporters = %v, want a sorted listing", names)
	}
	if err := coretrace.Export("nothing-registers-this", coretrace.SpansValue{}); !errors.Is(err, coretrace.UnknownExporter) {
		t.Errorf("want UnknownExporter, got %v", err)
	}
	if err := coretrace.Export(second.name, coretrace.SpansValue{}); !errors.Is(err, coretrace.ExportFailed) {
		t.Errorf("an exporter fault must wrap as ExportFailed, got %v", err)
	}
}

// TestRegisterExporterIsIdempotentButRefusesACollision pins the boot-time
// behaviour: re-registering the same instance is a no-op (a package-level var can
// be evaluated once), while a DIFFERENT exporter on a taken name is a wiring bug
// that must fail loudly, at boot, rather than silently shadowing.
func TestRegisterExporterIsIdempotentButRefusesACollision(t *testing.T) {
	exporter := &stubExporter{name: "collision-model-test"}
	coretrace.RegisterExporter(exporter)
	coretrace.RegisterExporter(exporter)
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Error("a distinct exporter on a taken name must panic at boot")
		}
	}()
	coretrace.RegisterExporter(&stubExporter{name: exporter.name})
}
