// Package metrics — the exportable sum point: the shape a Counter and an
// UpDownCounter share. The metric envelope that tells them apart lives beside
// SnapshotValue, whose field it is.
package metrics

// SumValue is one series of a sum metric: the attribute set that identifies it
// plus its value. Monotonicity and temporality are NOT here — they belong to
// the metric, not to the point, which is the OTel data model's own split.
type SumValue struct {
	// Attrs is the series' attribute set, sorted by Key; nil for the
	// dimensionless series.
	//
	// The slice ALIASES the meter's own copy — it is not cloned per Collect.
	// The meter never mutates an attribute set after the series is created,
	// so a reader is safe; a writer would corrupt every future snapshot.
	// Cloning instead would cost one allocation per series per collection,
	// i.e. it would scale the cost of scraping with cardinality, which is
	// the exact axis the cardinality bound exists to contain.
	Attrs []AttrValue
	// Value is the series' total: cumulative since the meter started, or the
	// delta since the previous collection, according to
	// SumMetricValue.Temporality.
	Value int64
}
