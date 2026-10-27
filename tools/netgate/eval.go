// Package main — the evaluation: each gate of the table held to the samples,
// criterion by criterion, and the report that names the commit measured.
package main

import (
	"errors"
	"fmt"
)

const (
	// verdictPass: the bound meets the threshold.
	verdictPass string = "pass"
	// verdictFail: the bound misses it.
	verdictFail string = "fail"
	// verdictMissing: a side has no samples, or too few.
	verdictMissing string = "missing"
	// verdictInactive: the gate is not turned on; its criteria are reported.
	verdictInactive string = "inactive"
)

var (
	// errBadRatio reports a ratio sample at or below zero: no CPU a request
	// or datagrams a receive make one.
	errBadRatio = errors.New("a ratio sample at or below zero")
	// errUnknownOp reports a criterion whose Op is neither opAtMost nor
	// opAtLeast.
	errUnknownOp = errors.New("unknown op")
)

// criterionEntity is one metric and one threshold of a gate.
type criterionEntity struct {
	// Metric is the unit the bench reports.
	Metric string `json:"metric"`
	// Op is opAtMost or opAtLeast.
	Op string `json:"op"`
	// Threshold is the bound's limit: the per-round ratio B/A when Ratio is
	// set, the metric's own value on B otherwise.
	Threshold float64 `json:"threshold"`
	// Ratio judges the pair's per-round ratios, B over A; unset, B alone.
	Ratio bool `json:"ratio"`
	// Reported is judged and written in the report, and never settles the
	// gate's verdict: a metric the owner keeps measured but not gated.
	Reported bool `json:"reported,omitempty"`
}

// gateEntity is one step's gate.
type gateEntity struct {
	// Step is the plan's step.
	Step string `json:"step"`
	// Load is the load both engines run.
	Load string `json:"load"`
	// A is the reference engine's label.
	A string `json:"a"`
	// B is the label of the engine the step builds.
	B string `json:"b"`
	// Criteria must all pass.
	Criteria []criterionEntity `json:"criteria"`
	// AllocBudget names the allocation budget the step is also held to,
	// outside the bench, when it has one.
	AllocBudget string `json:"alloc_budget,omitempty"`
	// Active gates fail the merge; an inactive one is reported only, with
	// the samples it found, until the step that builds B turns it on.
	Active bool `json:"active"`
	// Pending says what has to land before the gate is turned on.
	Pending string `json:"pending,omitempty"`
}

// summaryEntity is a sample set's median and the confidence interval around
// it (stats.go).
type summaryEntity struct {
	// N is the number of samples.
	N int `json:"n"`
	// Lo is the interval's lower bound.
	Lo float64 `json:"lo"`
	// Median is the sample median.
	Median float64 `json:"median"`
	// Hi is the interval's upper bound.
	Hi float64 `json:"hi"`
}

// criterionResult is one criterion judged.
type criterionResult struct {
	criterionEntity
	// A summarises engine A's own values, when the rounds reported them.
	A *summaryEntity `json:"a,omitempty"`
	// B summarises engine B's own values.
	B *summaryEntity `json:"b,omitempty"`
	// RatioSummary summarises the per-round ratios, B over A, on a ratio
	// criterion: the samples the bound is read from.
	RatioSummary *summaryEntity `json:"ratio_summary,omitempty"`
	// Bound is the value the threshold was held to.
	Bound float64 `json:"bound"`
	// Verdict is verdictPass, verdictFail or verdictMissing.
	Verdict string `json:"verdict"`
	// Reason says why, in a line.
	Reason string `json:"reason"`
}

// gateResult is one gate judged.
type gateResult struct {
	gateEntity
	// Verdict is the gate's: an inactive gate is verdictInactive whatever its
	// criteria found.
	Verdict string `json:"verdict"`
	// Measured holds each criterion's result, in the table's order.
	Measured []criterionResult `json:"measured"`
}

// reportEntity is what one evaluation writes, tied to the commit measured.
type reportEntity struct {
	// SHA is the commit the samples were measured on.
	SHA string `json:"sha"`
	// Confidence is the interval's level.
	Confidence float64 `json:"confidence"`
	// MinSamples is the plan's n.
	MinSamples int `json:"min_samples"`
	// AllocExclusions name the paths a 0-alloc budget does not count.
	AllocExclusions []string `json:"alloc_exclusions"`
	// Gates holds each gate's result, in the table's order.
	Gates []gateResult `json:"gates"`
	// Passed is false when any active gate did not pass.
	Passed bool `json:"passed"`
}

// evaluate judges every gate on the samples.
func evaluate(table []gateEntity, s samplesEntity, sha string) *reportEntity {
	rep := &reportEntity{SHA: sha, Confidence: confidence, MinSamples: minSamples, AllocExclusions: allocExclusions, Passed: true}
	//: each gate on its own.
	for i := range table {
		res := judgeGate(&table[i], s)
		//: only an active gate can fail the merge.
		if table[i].Active && res.Verdict != verdictPass {
			rep.Passed = false
		}
		rep.Gates = append(rep.Gates, res)
	}
	//: the whole table judged.
	return rep
}

// judgeGate judges each criterion of g; the gate passes when every one does,
// a Reported criterion aside.
func judgeGate(g *gateEntity, s samplesEntity) gateResult {
	res := gateResult{gateEntity: *g, Verdict: verdictPass}
	pair := s[seriesKey{Load: g.Load, A: g.A, B: g.B}]
	//: every criterion, none short-circuited, so the report is whole.
	for _, c := range g.Criteria {
		m := judge(c, pair)
		//: the first failure or gap of a gated criterion settles the verdict.
		if !c.Reported && m.Verdict != verdictPass && res.Verdict == verdictPass {
			res.Verdict = m.Verdict
		}
		res.Measured = append(res.Measured, m)
	}
	//: an inactive gate is reported, never failing.
	if !g.Active {
		res.Verdict = verdictInactive
	}
	//: judged.
	return res
}

// judge holds one criterion to a pair's rounds: on a ratio criterion the
// interval of the per-round ratios, B over A, each measured with both
// engines side by side so a drift of the runner cancels within its round; on
// any other, the interval of B's own values. Each engine's own values are
// summarised beside it when the rounds reported them.
func judge(c criterionEntity, pair map[string][]float64) criterionResult {
	res := criterionResult{criterionEntity: c}
	//: A's values, reported beside the bound, never judged.
	if sa, err := summarize(pair[aUnit+c.Metric], confidence); err == nil {
		res.A = &sa
	}
	//: B's values, the same.
	if sb, err := summarize(pair[bUnit+c.Metric], confidence); err == nil {
		res.B = &sb
	}
	unit := bUnit + c.Metric
	//: a ratio criterion reads the rounds' ratios.
	if c.Ratio {
		unit = ratioUnit + c.Metric
	}
	xs := pair[unit]
	sum, err := summarize(xs, confidence)
	//: too few rounds is no verdict.
	if err != nil {
		res.Verdict, res.Reason = verdictMissing, fmt.Sprintf("%d samples of %s: %v", len(xs), unit, err)
		//: missing.
		return res
	}
	//: the ratios' own interval, which a ratio of two intervals is not.
	if c.Ratio {
		res.RatioSummary = &sum
		//: a ratio no engine can produce.
		if sum.Lo <= 0 {
			res.Verdict, res.Reason = verdictMissing, fmt.Sprintf("%s: %v", unit, errBadRatio)
			//: missing.
			return res
		}
	}
	//: the bound that can fail the threshold.
	switch c.Op {
	//: the worst the rounds allow is the upper bound.
	case opAtMost:
		res.Bound = sum.Hi
		res.Verdict = verdictOf(sum.Hi <= c.Threshold)
	//: the worst is the lower bound.
	case opAtLeast:
		res.Bound = sum.Lo
		res.Verdict = verdictOf(sum.Lo >= c.Threshold)
	//: a table error.
	default:
		res.Verdict, res.Reason = verdictFail, fmt.Sprintf("%s: %v %q", c.Metric, errUnknownOp, c.Op)
		//: failed.
		return res
	}
	res.Reason = fmt.Sprintf("%s bound %.4g %s %.4g (median %.4g)", unit, res.Bound, c.Op, c.Threshold, sum.Median)
	//: judged.
	return res
}

// verdictOf maps a met threshold to its verdict.
func verdictOf(met bool) string {
	//: met.
	if met {
		//: pass.
		return verdictPass
	}
	//: missed.
	return verdictFail
}
