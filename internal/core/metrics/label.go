// Package metrics — the label set that gives a series its identity.
package metrics

// OverflowLabelKey names the reserved label the meter attaches to the single
// aggregated series it folds observations into once an instrument has reached
// its cardinality bound. It is spelled with underscores rather than dots so it
// needs no mangling to be a legal Prometheus label name.
//
// The key is RESERVED: a caller who passes it explicitly is writing into the
// same series the meter uses for overflow, and their observations merge with
// it. That is not enforced — enforcing it would cost a comparison per label on
// the lookup path to prevent a collision nobody reaches by accident.
const OverflowLabelKey string = "sdk_metric_overflow"

// OverflowLabelValue is the only value ever stored under OverflowLabelKey. It
// is a string, not a bool, because a label value is a string everywhere the
// snapshot is going.
const OverflowLabelValue string = "true"

// LabelValue is one dimension of a series: a Key naming the dimension and the
// Value observed for it. It is the metrics twin of logger's AttrValue, and the
// unit the whole cardinality question is about.
//
// The Key is STRUCTURE and the Value is DATA. A key is written at the call
// site and is constant for the lifetime of the process ("method", "status");
// a value comes from whatever the process is measuring and varies per
// observation ("GET", "503"). That asymmetry is why an unusable key is a
// programmer error the meter panics on, while an unbounded stream of values is
// a runtime condition the meter absorbs into an overflow series.
type LabelValue struct {
	// Key names the dimension. It must be non-empty and appear at most once
	// in a label set; both are checked on every instrument fetch.
	Key string
	// Value is the observed value of the dimension. Anything is accepted,
	// including the empty string — an empty value is a legitimate reading.
	Value string
}
