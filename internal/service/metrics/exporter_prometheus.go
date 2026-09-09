// Package metrics — Prometheus text exposition Exporter (format 0.0.4).
package metrics

import (
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// prometheusExporterName is the registered name of the default Prometheus
// exporter, which writes to stderr (ADR 0030).
const prometheusExporterName coremetrics.ExporterName = "prometheus"

// The three TYPE names the exposition format defines for the SDK's instrument
// kinds. Summary and untyped exist in the format but have no SDK instrument.
const (
	promTypeCounter   string = "counter"
	promTypeGauge     string = "gauge"
	promTypeHistogram string = "histogram"
)

// The suffixes a histogram family is spelled with, plus the reserved label that
// carries a bucket's inclusive upper bound and the mandatory last bound.
const (
	bucketSuffix     string = "_bucket"
	sumSuffix        string = "_sum"
	countSuffix      string = "_count"
	boundLabelKey    string = "le"
	positiveInfBound string = "+Inf"
	// noBound is the boundLabelKey-absent sentinel. The empty string is safe
	// as a sentinel because every bound this exporter emits is a formatted
	// float, and no formatted float is empty.
	noBound string = ""
	// reservedLabelPrefix is what Prometheus reserves for its own internal
	// labels; a label so named is dropped during relabelling, so a series
	// carrying one silently loses a dimension at the server.
	reservedLabelPrefix string = "__"
)

// Prometheus is the default Prometheus exporter, registered to write snapshots
// to stderr. Use NewPrometheusExporter for the writer a scrape actually needs.
//
// stderr, not stdout, for the reason ADR 0030 gives in full on Text: importing
// a package must never arm a writer on a stream the process may be using as a
// protocol channel. The registered instance is a diagnostic — the production
// path is NewPrometheusExporter(name, w) inside an HTTP handler, where w is the
// http.ResponseWriter the scraper is waiting on, and no default is involved.
var Prometheus = coremetrics.RegisterExporter(newPrometheusExporter(prometheusExporterName, os.Stderr))

// prometheusExporter renders a Snapshot as a Prometheus text exposition
// document: one "# TYPE" header per instrument name, then that name's series.
//
// mu serialises the single dst.Write for the same reason textExporter's does —
// core/metrics.Exporter requires concurrency safety and dst is caller-supplied.
// Rendering happens outside the lock, so a slow writer serialises callers
// without also serialising the formatting.
type prometheusExporter struct {
	mu   sync.Mutex
	name coremetrics.ExporterName
	dst  io.Writer
}

// newPrometheusExporter is the shared constructor.
func newPrometheusExporter(name coremetrics.ExporterName, dst io.Writer) *prometheusExporter {
	//: a stateless writer-bound exporter.
	return &prometheusExporter{name: name, dst: dst}
}

// NewPrometheusExporter returns a Prometheus text Exporter writing to dst under
// name. It is NOT added to the registry — bind it yourself, or hand it the
// http.ResponseWriter of a /metrics handler and call Export directly.
func NewPrometheusExporter(name coremetrics.ExporterName, dst io.Writer) coremetrics.Exporter {
	//: hand back the concrete exporter behind the interface.
	return newPrometheusExporter(name, dst)
}

// Name implements core/metrics.Exporter.
func (e *prometheusExporter) Name() coremetrics.ExporterName {
	//: the registered name.
	return e.name
}

// Export renders the whole snapshot into one buffer, then writes it once.
//
// A name the format cannot carry aborts the WHOLE document before the write, so
// a scraper never receives a partial exposition — a truncated document parses
// as a complete one, and the series it is missing look like series that stopped
// existing.
func (e *prometheusExporter) Export(snap coremetrics.SnapshotValue) error {
	//: render + validate first; nothing is written if either fails.
	buf, err := renderExposition(snap)
	//: an unrepresentable name is reported typed, with dst untouched.
	if err != nil {
		//: the sentinel already carries the code, reason and public message.
		return err
	}
	//: single write — the only remaining error surface — serialised so
	//: concurrent Exports cannot interleave partial documents into a
	//: non-atomic dst.
	e.mu.Lock()
	_, writeErr := e.dst.Write(buf)
	e.mu.Unlock()
	//: success fast-path.
	if writeErr == nil {
		//: snapshot written.
		return nil
	}
	//: wrap the writer fault with the dotted-quad code.
	return errs.Wrap(writeErr, errs.WrapParams{
		Code:    coremetrics.CodeExportFailed,
		Reason:  "EXPORT_FAILED",
		Public:  "The metrics exporter failed to ship the snapshot",
		Private: "service/metrics: Prometheus exporter writer returned an error",
	})
}

// renderExposition builds the whole document, one block per kind, each in
// sorted-name order so two exports of one snapshot are byte-identical.
func renderExposition(snap coremetrics.SnapshotValue) (doc []byte, err error) {
	//: doc starts nil and every line appends onto it (append never errors);
	//: counters first.
	doc, err = appendPromCounters(doc, snap.Counters)
	//: abort the whole document on an unrepresentable name.
	if err != nil {
		//: surface the typed refusal.
		return nil, err
	}
	//: then gauges.
	doc, err = appendPromGauges(doc, snap.Gauges)
	//: same abort.
	if err != nil {
		//: surface the typed refusal.
		return nil, err
	}
	//: then histograms, the only multi-line family.
	doc, err = appendPromHistograms(doc, snap.Histograms)
	//: same abort.
	if err != nil {
		//: surface the typed refusal.
		return nil, err
	}
	//: hand back the finished document.
	return doc, nil
}

// appendPromCounters renders each counter name as a "counter" family.
func appendPromCounters(buf []byte, groups map[string][]coremetrics.CounterValue) (doc []byte, err error) {
	//: names first, so the document is stable across runs.
	for _, name := range sortedKeys(groups) {
		//: a name that cannot be spelled refuses the export.
		if err := checkMetricName(name); err != nil {
			//: surface the typed refusal.
			return nil, err
		}
		//: the header is written ONCE per name — the property the
		//: name-keyed snapshot shape exists to make free.
		buf = appendTypeLine(buf, name, promTypeCounter)
		//: then each series, already ordered by label set by Collect.
		for _, series := range groups[name] {
			//: a label key that cannot be spelled refuses the export.
			if err := checkLabelNames(series.Labels, false); err != nil {
				//: surface the typed refusal.
				return nil, err
			}
			//: the cumulative total is an int64; a decimal integer is a
			//: valid float to the format's parser.
			buf = appendSample(buf, name, "", series.Labels,
				strconv.FormatInt(series.Value, decimalBase))
		}
	}
	//: hand back the extended buffer.
	return buf, nil
}

// appendPromGauges renders each gauge name as a "gauge" family.
func appendPromGauges(buf []byte, groups map[string][]coremetrics.GaugeValue) (doc []byte, err error) {
	//: same two-level walk as counters.
	for _, name := range sortedKeys(groups) {
		//: a name that cannot be spelled refuses the export.
		if err := checkMetricName(name); err != nil {
			//: surface the typed refusal.
			return nil, err
		}
		//: one header per name.
		buf = appendTypeLine(buf, name, promTypeGauge)
		//: one line per series.
		for _, series := range groups[name] {
			//: a label key that cannot be spelled refuses the export.
			if err := checkLabelNames(series.Labels, false); err != nil {
				//: surface the typed refusal.
				return nil, err
			}
			//: the reading is a float64 — see promFloat on the spelling.
			buf = appendSample(buf, name, "", series.Labels, promFloat(series.Value))
		}
	}
	//: hand back the extended buffer.
	return buf, nil
}

// appendPromHistograms renders each histogram name as a "histogram" family:
// the cumulative _bucket ladder, then _sum and _count.
func appendPromHistograms(buf []byte, groups map[string][]coremetrics.HistogramValue) (doc []byte, err error) {
	//: same two-level walk as counters.
	for _, name := range sortedKeys(groups) {
		//: a name that cannot be spelled refuses the export.
		if err := checkMetricName(name); err != nil {
			//: surface the typed refusal.
			return nil, err
		}
		//: the TYPE header names the BASE metric; the samples carry the
		//: _bucket / _sum / _count suffixes.
		buf = appendTypeLine(buf, name, promTypeHistogram)
		//: one family per series.
		for _, series := range groups[name] {
			//: "le" is reserved here, so a histogram gets the stricter check.
			if err := checkLabelNames(series.Labels, true); err != nil {
				//: surface the typed refusal.
				return nil, err
			}
			//: the ladder plus its two totals.
			buf = appendHistogramSeries(buf, name, series)
		}
	}
	//: hand back the extended buffer.
	return buf, nil
}

// appendHistogramSeries writes one histogram series: the cumulative _bucket
// ladder including the mandatory le="+Inf" line, then _sum and _count.
//
// The meter stores a PER-BUCKET count (Record increments exactly one slot); the
// format wants a CUMULATIVE one — bucket le="b" reports every observation ≤ b.
// The running total below is that conversion, and it is also what makes the
// ladder monotonic by construction.
//
// le="+Inf" carries the ladder's own total rather than series.Count. The two
// are equal at rest, and under a concurrent Record they can differ by the
// observations in flight, because Record bumps its bucket and the total count
// as two separate atomics and snapshot reads them at two instants. Deriving
// +Inf from Count instead would make it possible for +Inf to come out BELOW the
// bucket beneath it — a non-monotonic ladder, which is the worse violation of
// the two. No single number restores atomicity here; only a lock would, and the
// instruments are lock-free on purpose.
func appendHistogramSeries(buf []byte, name string, series coremetrics.HistogramValue) []byte {
	//: bucket i reports every observation up to and including its bound.
	var cumulative uint64
	//: the declared bounds, ascending — newHistogram sorts them.
	for i, bound := range series.Buckets {
		//: a hand-built value may carry fewer counts than bounds; a missing
		//: slot contributes zero rather than panicking on the scrape path.
		if i < len(series.Counts) {
			//: fold this bucket into the running total.
			cumulative += series.Counts[i]
		}
		//: a non-finite bound has no legal `le` spelling of its own — +Inf is
		//: already the mandatory last line, and emitting it twice would forge
		//: a duplicate series, while NaN is not an ordering at all. The
		//: observations still ride the running total, so none is lost.
		if math.IsInf(bound, 0) || math.IsNaN(bound) {
			//: skip the line, keep the count.
			continue
		}
		//: one cumulative bucket line.
		buf = appendBucket(buf, name, series.Labels, promFloat(bound),
			strconv.FormatUint(cumulative, decimalBase))
	}
	//: every slot past the declared bounds is the meter's +Inf overflow.
	for i := len(series.Buckets); i < len(series.Counts); i++ {
		//: fold the overflow into the running total.
		cumulative += series.Counts[i]
	}
	//: the +Inf bucket is MANDATORY and closes the ladder.
	buf = appendBucket(buf, name, series.Labels, positiveInfBound,
		strconv.FormatUint(cumulative, decimalBase))
	//: _sum is a float and may legitimately be NaN or ±Inf.
	buf = appendSample(buf, name, sumSuffix, series.Labels, promFloat(series.Sum))
	//: _count closes the family.
	return appendSample(buf, name, countSuffix, series.Labels,
		strconv.FormatUint(series.Count, decimalBase))
}

// appendTypeLine appends "# TYPE <name> <kind>\n".
//
// No "# HELP" line accompanies it. HELP is optional in the exposition format
// and carries a DOCSTRING; the SDK's Meter records no description for an
// instrument, so the only HELP this exporter could emit is the metric name
// repeated back or a fixed sentence restating the TYPE line. Both are
// placeholders, and a placeholder on every scrape teaches an operator nothing.
// The day Meter grows a description, HELP lands on the line above this one and
// nothing else about the document changes.
func appendTypeLine(buf []byte, name, kind string) []byte {
	//: the comment prefix the format reserves for metadata.
	buf = append(buf, "# TYPE "...)
	//: the base metric name — validated, so it needs no escaping.
	buf = append(buf, name...)
	buf = append(buf, ' ')
	//: counter / gauge / histogram.
	buf = append(buf, kind...)
	//: hand back the extended buffer.
	return append(buf, '\n')
}

// appendSample appends one sample line: "<name><suffix>{<labels>} <value>\n".
// It is the shape every family member but a bucket takes.
func appendSample(buf []byte, name, suffix string, labels []coremetrics.LabelValue, value string) []byte {
	//: the metric name, plus a family suffix for histogram members.
	buf = append(buf, name...)
	buf = append(buf, suffix...)
	//: the label set, or nothing at all when the series has none.
	buf = appendPromLabels(buf, labels, noBound)
	//: value, then terminate the line.
	buf = append(buf, ' ')
	buf = append(buf, value...)
	//: hand back the extended buffer.
	return append(buf, '\n')
}

// appendBucket appends one cumulative bucket line:
// "<name>_bucket{<labels>,le="<bound>"} <count>\n".
//
// It is its own function rather than a sixth parameter on appendSample because
// a bucket line IS a different shape: it is the only one carrying the reserved
// `le` dimension, and the only one whose value is a running total rather than
// the series' own reading.
func appendBucket(buf []byte, name string, labels []coremetrics.LabelValue, bound, count string) []byte {
	//: the base name plus the family suffix the format reserves.
	buf = append(buf, name...)
	buf = append(buf, bucketSuffix...)
	//: the series' own labels, then le last.
	buf = appendPromLabels(buf, labels, bound)
	//: the cumulative count, then terminate the line.
	buf = append(buf, ' ')
	buf = append(buf, count...)
	//: hand back the extended buffer.
	return append(buf, '\n')
}

// appendPromLabels renders {k="v",…[,le="<bound>"]}. A dimensionless series
// with no bound renders as a bare name, which the format's own examples do.
func appendPromLabels(buf []byte, labels []coremetrics.LabelValue, bound string) []byte {
	//: no braces at all when there is nothing inside them.
	if len(labels) == 0 && bound == noBound {
		//: nothing to render.
		return buf
	}
	//: open the set.
	buf = append(buf, '{')
	//: comma-separated pairs, in the snapshot's canonical order.
	for i, label := range labels {
		//: separator between pairs only.
		if i > 0 {
			//: continue the set.
			buf = append(buf, ',')
		}
		//: the key is validated against the label-name grammar, so it holds
		//: no byte the format would need an escape for.
		buf = append(buf, label.Key...)
		buf = append(buf, '=', '"')
		//: the value is data — it must not be able to close the quote.
		buf = appendEscapedValue(buf, label.Value)
		buf = append(buf, '"')
	}
	//: the reserved bucket bound goes last, after the series' own labels.
	if bound != noBound {
		//: separate it from the pairs that precede it.
		if len(labels) > 0 {
			//: continue the set.
			buf = append(buf, ',')
		}
		//: le="<bound>" — the bound is a formatted float, never escapable.
		buf = append(buf, boundLabelKey...)
		buf = append(buf, '=', '"')
		buf = append(buf, bound...)
		buf = append(buf, '"')
	}
	//: close the set.
	return append(buf, '}')
}

// promFloat spells a float64 the way the exposition format wants it.
//
// The format says a value is "a float represented as required by Go's
// ParseFloat()", and names NaN / +Inf / -Inf as valid. strconv.FormatFloat with
// 'g' and precision -1 produces exactly those three spellings, and the shortest
// round-tripping decimal otherwise — which is also what produced the published
// example values (`1.7560473e+07`, `12.47`) in the format's own documentation.
// Pinned by TestPromFloatSpellsTheFormatsOwnExamples.
func promFloat(value float64) string {
	//: shortest round-trip; NaN / +Inf / -Inf come out named.
	return strconv.FormatFloat(value, 'g', -1, floatBitSize)
}

// checkMetricName refuses an instrument name the format cannot carry.
//
// The offending name rides in a FIELD, not in the public message: Public is a
// string literal by rule 4, and the name is the one thing here that did not
// come from this package.
func checkMetricName(name string) error {
	//: the format's metric-name grammar.
	if validMetricName(name) {
		//: representable.
		return nil
	}
	//: origin wins on wrap — the sentinel keeps its code/reason/public.
	return errs.Wrap(InvalidMetricName, errs.WrapParams{}, errs.String("metric", name))
}

// checkLabelNames refuses a label set the format cannot carry. bucketBound
// additionally reserves "le", which a histogram spends on its bucket bound.
func checkLabelNames(labels []coremetrics.LabelValue, bucketBound bool) error {
	//: every key in the set must survive the wire.
	for _, label := range labels {
		//: the format's label-name grammar — no colon, unlike a metric name.
		if !validLabelName(label.Key) {
			//: origin wins on wrap; the key rides in a field.
			return errs.Wrap(InvalidLabelName, errs.WrapParams{}, errs.String("label", label.Key))
		}
		//: "__foo" is syntactically fine and silently dropped by the server.
		if strings.HasPrefix(label.Key, reservedLabelPrefix) {
			//: origin wins on wrap; the key rides in a field.
			return errs.Wrap(ReservedLabelName, errs.WrapParams{}, errs.String("label", label.Key))
		}
		//: a second "le" on a bucket line is a duplicate label name, which
		//: the format rejects outright.
		if bucketBound && label.Key == boundLabelKey {
			//: origin wins on wrap; the key rides in a field.
			return errs.Wrap(ReservedLabelName, errs.WrapParams{}, errs.String("label", label.Key))
		}
	}
	//: the whole set is representable.
	return nil
}

// validMetricName reports whether s matches [a-zA-Z_:][a-zA-Z0-9_:]*, the
// metric-name grammar of the text exposition format.
func validMetricName(s string) bool {
	//: an empty name has no first character to be legal.
	if s == "" {
		//: refuse.
		return false
	}
	//: byte-wise: the grammar is pure ASCII, so a multi-byte rune fails on
	//: its first byte and never needs decoding.
	for i := range len(s) {
		//: the first byte may not be a digit.
		if !metricNameByte(s[i], i == 0) {
			//: refuse.
			return false
		}
	}
	//: every byte is in the grammar.
	return true
}

// metricNameByte reports whether c is legal in a metric name at that position.
func metricNameByte(c byte, first bool) bool {
	//: a colon is legal in a metric name (it is what recording rules use).
	switch {
	//: the letters and the two punctuation bytes the grammar admits.
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_', c == ':':
		//: always legal.
		return true
	//: a digit cannot open a name, or it would parse as a number.
	case c >= '0' && c <= '9':
		//: legal everywhere but the first position.
		return !first
	//: everything outside the grammar.
	default:
		//: a dot, a dash, a space, a quote, any UTF-8 continuation byte.
		return false
	}
}

// validLabelName reports whether s matches [a-zA-Z_][a-zA-Z0-9_]*, the
// label-name grammar of the text exposition format. It is the metric-name
// grammar MINUS the colon, which Prometheus reserves for recording rules.
func validLabelName(s string) bool {
	//: an empty key has no first character to be legal. The meter already
	//: panics on one, so this only catches a hand-built snapshot.
	if s == "" {
		//: refuse.
		return false
	}
	//: byte-wise, same reasoning as validMetricName.
	for i := range len(s) {
		//: the first byte may not be a digit.
		if !labelNameByte(s[i], i == 0) {
			//: refuse.
			return false
		}
	}
	//: every byte is in the grammar.
	return true
}

// labelNameByte reports whether c is legal in a label name at that position.
func labelNameByte(c byte, first bool) bool {
	//: no colon here — that is the whole difference from a metric name.
	switch {
	//: the letters and the underscore the grammar admits.
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		//: always legal.
		return true
	//: a digit cannot open a name, or it would parse as a number.
	case c >= '0' && c <= '9':
		//: legal everywhere but the first position.
		return !first
	//: everything outside the grammar.
	default:
		//: anything else, including the colon a metric name may hold.
		return false
	}
}
