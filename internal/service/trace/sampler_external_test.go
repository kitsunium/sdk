package trace_test

import (
	"errors"
	"math"
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/trace"
)

// TestRatioRefusesZeroBecauseZeroIsAmbiguous is the ADR 0031 question, answered.
//
// Does a sampling rate of 0 mean "sample nothing" or "nobody configured this"? It
// means BOTH, and nothing in a float64 can tell them apart: an unset struct
// field, a JSON document missing the key and a `rate:` with nothing after it all
// produce 0.0. A sampler that honoured it would disable tracing for a deployment
// that believed it had configured it, and the symptom is the ABSENCE of telemetry
// — no error, no log, nothing to alert on.
//
// So the ambiguity is refused rather than resolved. NeverSample is the name for
// none, and it cannot be produced by forgetting anything.
func TestRatioRefusesZeroBecauseZeroIsAmbiguous(t *testing.T) {
	sampler, err := svctrace.Ratio(0)
	if !errors.Is(err, svctrace.InvalidSampleRatio) {
		t.Fatalf("Ratio(0) must be refused, got %v", err)
	}
	if sampler != nil {
		t.Error("a refused ratio must return no sampler — not an inert one")
	}
}

// TestRatioRefusalsAndAcceptances walks the whole domain of the parameter.
func TestRatioRefusalsAndAcceptances(t *testing.T) {
	refused := []struct {
		name     string
		fraction float64
	}{
		{"zero", 0},
		{"negative", -0.5},
		{"above one", 1.0001},
		{"NaN", math.NaN()},
		{"+Inf", math.Inf(1)},
		{"-Inf", math.Inf(-1)},
	}
	for _, tc := range refused {
		t.Run("refused/"+tc.name, func(t *testing.T) {
			if _, err := svctrace.Ratio(tc.fraction); !errors.Is(err, svctrace.InvalidSampleRatio) {
				t.Fatalf("Ratio(%v) must be refused, got %v", tc.fraction, err)
			}
		})
	}
	for _, fraction := range []float64{0.0001, 0.5, 1} {
		t.Run("accepted", func(t *testing.T) {
			if _, err := svctrace.Ratio(fraction); err != nil {
				t.Fatalf("Ratio(%v): %v", fraction, err)
			}
		})
	}
}

// TestRatioIsDeterministicOnTheTraceID pins why the decision is a function of the
// id rather than a coin flip.
//
// Two independent services that both start a root span for the same id — a retry,
// a fan-out re-entering the mesh — must reach the SAME verdict, or a trace is
// half-kept. A random sampler cannot promise that; this one does, and the promise
// is testable.
func TestRatioIsDeterministicOnTheTraceID(t *testing.T) {
	sampler, err := svctrace.Ratio(0.5)
	if err != nil {
		t.Fatalf("Ratio: %v", err)
	}
	id, err := svctrace.NewTraceID()
	if err != nil {
		t.Fatalf("NewTraceID: %v", err)
	}
	params := coretrace.SamplingParams{TraceID: id}
	first := sampler(params)
	for range 64 {
		if sampler(params) != first {
			t.Fatal("the same trace id produced two different verdicts")
		}
	}
}

// TestRatioSplitsTheIDSpaceRoughlyAsAsked checks the threshold arithmetic over a
// large sample. It is a statistical assertion with a generous band: the point is
// that a ratio of 0.25 is not 0, not 1 and not 0.5 — a mis-scaled threshold fails
// by an order of magnitude, not by a few percent.
func TestRatioSplitsTheIDSpaceRoughlyAsAsked(t *testing.T) {
	sampler, err := svctrace.Ratio(0.25)
	if err != nil {
		t.Fatalf("Ratio: %v", err)
	}
	const draws int = 20000
	kept := 0
	for range draws {
		id, idErr := svctrace.NewTraceID()
		if idErr != nil {
			t.Fatalf("NewTraceID: %v", idErr)
		}
		if sampler(coretrace.SamplingParams{TraceID: id}) {
			kept++
		}
	}
	ratio := float64(kept) / float64(draws)
	if ratio < 0.22 || ratio > 0.28 {
		t.Errorf("kept %.3f of draws, want ~0.25 — the threshold is mis-scaled", ratio)
	}
}

// TestRatioOneIsAlwaysSample pins the shortcut: a ratio of 1 is the policy that
// already has a name, so it takes that name and skips a multiply and a compare on
// every root span.
func TestRatioOneIsAlwaysSample(t *testing.T) {
	sampler, err := svctrace.Ratio(1)
	if err != nil {
		t.Fatalf("Ratio: %v", err)
	}
	for range 32 {
		id, idErr := svctrace.NewTraceID()
		if idErr != nil {
			t.Fatalf("NewTraceID: %v", idErr)
		}
		if !sampler(coretrace.SamplingParams{TraceID: id}) {
			t.Fatal("Ratio(1) must keep every trace")
		}
	}
}

// TestParentBasedHonoursTheParentAndConsultsRootOnly pins the sampler almost
// every deployment wants, in both directions.
func TestParentBasedHonoursTheParentAndConsultsRootOnly(t *testing.T) {
	sampled, err := coretrace.ParseTraceParent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if err != nil {
		t.Fatalf("ParseTraceParent: %v", err)
	}
	unsampled, err := coretrace.ParseTraceParent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00")
	if err != nil {
		t.Fatalf("ParseTraceParent: %v", err)
	}
	var rootCalls int
	counting := func(params coretrace.SamplingParams) bool {
		rootCalls++
		return true
	}
	sampler := svctrace.ParentBased(counting)

	if !sampler(coretrace.SamplingParams{Parent: sampled}) {
		t.Error("a sampled parent must be honoured")
	}
	if sampler(coretrace.SamplingParams{Parent: unsampled}) {
		t.Error("an unsampled parent must be honoured — re-deciding puts a hole in the trace")
	}
	if rootCalls != 0 {
		t.Errorf("the root policy ran %d times for spans that HAD a parent", rootCalls)
	}
	if !sampler(coretrace.SamplingParams{}) || rootCalls != 1 {
		t.Errorf("the root policy must run exactly once for a root span, ran %d times", rootCalls)
	}
}

// TestParentBasedClampsANilRootToAlwaysSample pins the one clamp in this family.
//
// Every other constructor here refuses; this one clamps, because AlwaysSample is
// a description of the SAFE direction rather than a number chosen on the caller's
// behalf. A nil that dropped everything would silently turn tracing off, which is
// exactly the failure the Ratio(0) refusal exists to prevent.
func TestParentBasedClampsANilRootToAlwaysSample(t *testing.T) {
	if !svctrace.ParentBased(nil)(coretrace.SamplingParams{}) {
		t.Error("a nil root policy must keep traces, not lose them")
	}
}

// TestAlwaysAndNeverSample pins the two named endpoints.
func TestAlwaysAndNeverSample(t *testing.T) {
	if !svctrace.AlwaysSample(coretrace.SamplingParams{}) {
		t.Error("AlwaysSample must keep every trace")
	}
	if svctrace.NeverSample(coretrace.SamplingParams{}) {
		t.Error("NeverSample must drop every trace")
	}
}

// TestNewIdentifiersAreValidAndDistinct pins that the generators never hand back
// the all-zero value the specification declares invalid — a traceparent carrying
// one is a header every conforming receiver must ignore.
func TestNewIdentifiersAreValidAndDistinct(t *testing.T) {
	seen := make(map[coretrace.TraceID]struct{}, 128)
	for range 128 {
		id, err := svctrace.NewTraceID()
		if err != nil {
			t.Fatalf("NewTraceID: %v", err)
		}
		if !id.IsValid() {
			t.Fatal("NewTraceID returned the all-zero id, which is invalid by specification")
		}
		if _, dup := seen[id]; dup {
			t.Fatal("NewTraceID repeated an id")
		}
		seen[id] = struct{}{}
	}
	span, err := svctrace.NewSpanID()
	if err != nil {
		t.Fatalf("NewSpanID: %v", err)
	}
	if !span.IsValid() {
		t.Fatal("NewSpanID returned the all-zero id, which also spells 'no parent'")
	}
}
