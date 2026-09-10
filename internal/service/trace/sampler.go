// Package trace — the three samplers, and the fraction that is refused.
package trace

import (
	"encoding/binary"
	"math"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// traceIDUniformBytes is how much of a trace id the uniform is drawn from.
const traceIDUniformBytes int = 8

// ratioThresholdBits is the width of that uniform — traceIDUniformBytes worth of
// bits minus the top one, so the comparison is unsigned-safe on every
// architecture without a cast that could sign-extend.
const ratioThresholdBits int = traceIDUniformBytes*bitsPerByte - 1

// bitsPerByte is the obvious 8, named so ratioThresholdBits derives from
// traceIDUniformBytes rather than restating it.
const bitsPerByte int = 8

// ratioSpace is the size of the uniform's range as a double: the threshold a
// fraction is scaled into. float64 carries 53 mantissa bits, so the scaling is
// exact to roughly 1 part in 2^53 — far finer than any rate a deployment states.
const ratioSpace float64 = 1 << ratioThresholdBits

// traceIDUniformOffset is where the uniform is read from: the LAST
// traceIDUniformBytes, not the first. An id that carries structure carries it at
// the front (a timestamp prefix, a machine id), and sampling on structured bits
// samples whole prefixes in or out together. The tail is the part every
// generator makes random.
const traceIDUniformOffset int = coretrace.TraceIDLen - traceIDUniformBytes

// AlwaysSample keeps every root trace. It is the right default for a
// development environment and for a low-traffic service, and it is what an
// unset TracerConfig.Sampler clamps to (see NewTracer).
func AlwaysSample(_ coretrace.SamplingParams) bool {
	//: the parameters are deliberately unread — the answer does not depend on
	//: them, and naming them would suggest otherwise.
	return true
}

// NeverSample drops every root trace. It exists so "off" has a NAME: a
// deployment that wants no tracing says NeverSample, which is greppable, rather
// than setting a rate to zero, which is indistinguishable from forgetting to set
// it (ADR 0031).
func NeverSample(_ coretrace.SamplingParams) bool {
	//: the parameters are deliberately unread, as in AlwaysSample.
	return false
}

// ParentBased returns a Sampler that HONOURS a valid parent's decision and
// consults root only when there is no parent.
//
// It is the sampler almost every deployment wants, and the reason is the whole
// design of the domain: the decision is taken once, at the root, and travels in
// the traceparent's sampled bit. A process that re-decided for an inbound
// request with a valid parent would produce a trace with a hole in the middle —
// its own spans missing from a trace the caller kept, or present in one the
// caller dropped, so the backend stores fragments of a request nobody can
// reassemble.
//
// A nil root CLAMPS to AlwaysSample rather than refusing. The nil means "the
// caller did not name a root policy", and a sampler that dropped everything
// would silently turn tracing off — the ADR 0031 failure this whole family is
// written against. Every other constructor in this file refuses instead; this
// one clamps because AlwaysSample is a description of the safe direction, not a
// number chosen on the caller's behalf.
func ParentBased(root coretrace.Sampler) coretrace.Sampler {
	//: an unnamed root policy keeps traces rather than losing them.
	if root == nil {
		//: the safe direction.
		root = AlwaysSample
	}
	//: the returned closure holds only the root policy.
	return func(params coretrace.SamplingParams) bool {
		//: a valid parent has already decided, for the whole trace.
		if params.Parent.IsValid() {
			//: honour the bit that travelled in the traceparent.
			return params.Parent.IsSampled()
		}
		//: no parent: this IS the root, so the policy applies here.
		return root(params)
	}
}

// Ratio returns a Sampler keeping a deterministic FRACTION of root traces.
//
// Deterministic on the trace id, not random per call, and that matters more than
// it looks: two independent services that both start a root span for the same
// incoming id — a retry, a fan-out re-entering the mesh — reach the same verdict,
// so a trace is never half-kept. It is the same function OpenTelemetry's
// TraceIDRatioBased specifies, over the id's last 8 bytes.
//
// # Which fractions are refused, and why zero is one of them
//
// NaN, a negative fraction and a fraction above 1 are refused because they are
// not fractions. Exactly 0 is refused for a different and more interesting
// reason, and it is the question ADR 0031 asks of every zero value: does 0 mean
// "none" or "nobody set this"?
//
// Here it means BOTH, and nothing in the type can tell them apart. A float64
// field left unset in a config struct, a JSON document missing the key, a YAML
// `rate:` with nothing after it — all three produce 0.0, and all three would
// silently disable tracing for a deployment that believed it had configured it.
// The failure is invisible: there is no error, no log line, and no telemetry —
// the absence of telemetry is the symptom.
//
// So the ambiguity is not resolved, it is REFUSED. A caller who wants no traces
// says NeverSample, which cannot be produced by forgetting anything. A caller
// who wants all of them says AlwaysSample, or Ratio(1). The one input that could
// mean two things is the one input Ratio will not accept.
func Ratio(fraction float64) (sampler coretrace.Sampler, err error) {
	//: NaN fails every ordered comparison, so it needs its own test.
	if math.IsNaN(fraction) || fraction <= 0 || fraction > 1 {
		//: the fraction is configuration, not data, so echoing it is safe and
		//: is what makes the message actionable.
		return nil, errs.Wrap(InvalidSampleRatio, errs.WrapParams{}, errs.String("ratio", formatRatio(fraction)))
	}
	//: 1 keeps everything, and the threshold below would too — but naming it
	//: skips a multiplication and a comparison on every root span.
	if fraction == 1 {
		//: the same policy, spelled as the sampler that has a name.
		return AlwaysSample, nil
	}
	//: the fraction of the uniform's range to keep — see ratioSpace.
	threshold := uint64(fraction * ratioSpace)
	//: the returned closure holds only the threshold.
	return func(params coretrace.SamplingParams) bool {
		//: the id's tail, with the top bit dropped, is a uniform in [0,2^63).
		return traceIDUniform(params.TraceID) < threshold
	}, nil
}

// traceIDUniform reads the tail of a trace id as a ratioThresholdBits-wide
// uniform, from the offset traceIDUniformOffset names.
func traceIDUniform(id coretrace.TraceID) uint64 {
	//: big-endian so the value is the same on every architecture, and >>1 to
	//: drop the top bit rather than reasoning about a signed comparison.
	return binary.BigEndian.Uint64(id[traceIDUniformOffset:]) >> 1
}

// formatRatio renders a fraction for the error field, naming the two values
// strconv would otherwise spell as an unhelpful "NaN"/"+Inf" in a log.
func formatRatio(fraction float64) string {
	//: NaN is the value a caller is least likely to have written on purpose.
	if math.IsNaN(fraction) {
		//: named, so the field says what happened.
		return "NaN"
	}
	//: everything else renders shortest-round-trip.
	return formatFloat(fraction)
}
