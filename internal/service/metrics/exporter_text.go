// Package metrics — stdlib text Exporter (one series per line, under a header
// per instrument name).
package metrics

import (
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// textExporterName is the registered name of the default text exporter, which
// writes to stderr (ADR 0030).
const textExporterName coremetrics.ExporterName = "text"

// decimalBase / floatBitSize are the strconv formatting parameters.
const (
	decimalBase  int = 10
	floatBitSize int = 64
)

// The three kind words and the two monotonicity words the header line uses.
const (
	textKindSum       string = "sum"
	textKindGauge     string = "gauge"
	textKindHistogram string = "histogram"
	textMonotonic     string = "monotonic"
	textNonMonotonic  string = "non_monotonic"
)

// Text is the default text exporter, registered to write snapshots to stderr.
// Use NewTextExporter for a custom writer/name (not added to the registry).
//
// The destination is stderr, not stdout, because importing a package must never
// arm a writer on a stream the process may be using as a protocol channel. A
// daemon that speaks JSON-RPC on stdout (an MCP server in stdio mode), a filter
// that pipes structured output downstream, any `cmd | jq` — each of them is
// corrupted by a single Export("text", ...) if the default writer is stdout,
// and the import that armed it is invisible at the call site. Diagnostics
// belong on stderr; a caller who genuinely wants stdout asks for it by name
// with NewTextExporter(name, os.Stdout). ADR 0030.
var Text = coremetrics.RegisterExporter(newTextExporter(textExporterName, os.Stderr))

// textExporter renders a Snapshot as a newline-delimited diagnostic document.
//
// Unlike the Prometheus exporter it is LOSSLESS about the model: it prints the
// Resource, the Scope, the collection window and each metric's temporality and
// monotonicity, because those are exactly the facts a caller cannot otherwise
// see and the reason this file exists at all. The grammar is
//
//	# resource <k>=<v> …
//	# scope name="…" [version="…"]
//	# window start="…" end="…"
//	# metric <name> <sum|gauge|histogram> [<temporality>] [monotonic|non_monotonic]
//	<name>{<k>=<v>,…} <value>
//
// with one "# metric" header per instrument name followed by that name's
// series — one pass over the snapshot, because the snapshot is keyed by name.
// A string attribute value is quoted and escaped; a bool, integer or double is
// printed bare, so the ATTRIBUTE'S TYPE is visible in the output rather than
// flattened into a string the way a wire format would flatten it.
//
// mu serialises the single dst.Write. core/metrics.Exporter documents that
// implementations MUST be safe for concurrent use, and dst is caller-supplied:
// os.Stdout tolerates concurrent writes on most platforms, but a bytes.Buffer
// or a plain os.File does not. Rendering happens outside the lock — only the
// write is guarded — so a slow writer serialises callers without also
// serialising the formatting work.
type textExporter struct {
	mu   sync.Mutex
	name coremetrics.ExporterName
	dst  io.Writer
}

// newTextExporter is the shared constructor.
func newTextExporter(name coremetrics.ExporterName, dst io.Writer) *textExporter {
	//: a stateless writer-bound exporter.
	return &textExporter{name: name, dst: dst}
}

// NewTextExporter returns a text Exporter writing to dst under name. It is NOT
// added to the registry — bind it yourself or call Export directly.
func NewTextExporter(name coremetrics.ExporterName, dst io.Writer) coremetrics.Exporter {
	//: hand back the concrete exporter behind the interface.
	return newTextExporter(name, dst)
}

// Name implements core/metrics.Exporter.
func (e *textExporter) Name() coremetrics.ExporterName {
	//: the registered name.
	return e.name
}

// Export renders the whole snapshot into one buffer, then writes it once so the
// only error surface is the single dst.Write.
func (e *textExporter) Export(snap coremetrics.SnapshotValue) error {
	//: accumulate every line into a single byte buffer (append never errors).
	var buf []byte
	//: the payload-level facts first — they qualify every line beneath them.
	buf = appendResourceLine(buf, snap.Resource)
	buf = appendScopeLine(buf, snap.Scope)
	buf = appendWindowLine(buf, snap.StartTime, snap.Time)
	//: then one block per kind, each in sorted-name order for stable output.
	buf = appendSums(buf, snap.Sums)
	buf = appendGauges(buf, snap.Gauges)
	buf = appendHistograms(buf, snap.Histograms)
	//: single write — the only error surface — serialised so concurrent
	//: Exports cannot interleave partial lines into a non-atomic dst.
	e.mu.Lock()
	_, err := e.dst.Write(buf)
	e.mu.Unlock()
	//: success fast-path.
	if err == nil {
		//: snapshot written.
		return nil
	}
	//: wrap the writer fault with the dotted-quad code.
	return errs.Wrap(err, errs.WrapParams{
		Code:    coremetrics.CodeExportFailed,
		Reason:  "EXPORT_FAILED",
		Public:  "The metrics exporter failed to ship the snapshot",
		Private: "service/metrics: text exporter writer returned an error",
	})
}

// appendResourceLine renders the producer's attributes, carried once.
func appendResourceLine(buf []byte, resource coremetrics.ResourceValue) []byte {
	//: the header word.
	buf = append(buf, "# resource"...)
	//: then every attribute, space-separated, in the Resource's own order.
	for _, attr := range resource.Attrs {
		//: one space per pair keeps the line shell-greppable.
		buf = append(buf, ' ')
		buf = appendAttrPair(buf, attr)
	}
	//: terminate the line.
	return append(buf, '\n')
}

// appendScopeLine renders the instrumentation scope. Version is omitted when
// empty rather than printed as an empty string, because the specification makes
// it optional and an empty one says nothing.
func appendScopeLine(buf []byte, scope coremetrics.ScopeValue) []byte {
	//: name is always present — a Meter normalises it.
	buf = append(buf, "# scope name="...)
	buf = appendQuoted(buf, scope.Name)
	//: an absent version is absent, not blank.
	if scope.Version != "" {
		//: append the optional field.
		buf = append(buf, " version="...)
		buf = appendQuoted(buf, scope.Version)
	}
	//: terminate the line.
	return append(buf, '\n')
}

// appendWindowLine renders the interval every point below covers. Under
// cumulative temporality start repeats across collections; under delta it
// advances, which is the difference the two words on the metric lines name.
func appendWindowLine(buf []byte, start, end time.Time) []byte {
	//: RFC3339 with nanoseconds — sortable, unambiguous, and what OTLP's
	//: unix-nano fields render back to.
	buf = append(buf, "# window start=\""...)
	buf = start.AppendFormat(buf, time.RFC3339Nano)
	buf = append(buf, "\" end=\""...)
	buf = end.AppendFormat(buf, time.RFC3339Nano)
	//: terminate the line.
	return append(buf, "\"\n"...)
}

// appendSums renders each sum name as a header plus one line per series.
func appendSums(buf []byte, metrics map[string]coremetrics.SumMetricValue) []byte {
	//: names first, so the block is stable across runs.
	for _, name := range sortedKeys(metrics) {
		metric := metrics[name]
		//: monotonicity is the field that tells a Counter from an
		//: UpDownCounter, and it is the whole reason both are one kind here.
		monotonicity := textNonMonotonic
		//: a monotonic sum never decreases.
		if metric.Monotonic {
			//: the counter case.
			monotonicity = textMonotonic
		}
		//: one header per name.
		buf = appendMetricHeader(buf, name, textKindSum, metric.Temporality.String(), monotonicity)
		//: then each series, already ordered by attribute set by Collect.
		for _, point := range metric.Points {
			//: the total is an int64.
			buf = appendSeriesLine(buf, name, point.Attrs,
				strconv.FormatInt(point.Value, decimalBase))
		}
	}
	//: hand back the extended buffer.
	return buf
}

// appendGauges renders each gauge name as a header plus one line per series. A
// gauge has neither temporality nor monotonicity, so its header carries neither.
func appendGauges(buf []byte, metrics map[string]coremetrics.GaugeMetricValue) []byte {
	//: same two-level walk as sums.
	for _, name := range sortedKeys(metrics) {
		//: one header per name, kind word only.
		buf = appendMetricHeader(buf, name, textKindGauge, "", "")
		//: one line per series.
		for _, point := range metrics[name].Points {
			//: the reading is a float64.
			buf = appendSeriesLine(buf, name, point.Attrs,
				strconv.FormatFloat(point.Value, 'g', -1, floatBitSize))
		}
	}
	//: hand back the extended buffer.
	return buf
}

// appendHistograms renders each histogram series' observation count. The full
// bucket layout is reachable through the Snapshot API — this exporter is a
// diagnostic, not a wire format.
func appendHistograms(buf []byte, metrics map[string]coremetrics.HistogramMetricValue) []byte {
	//: same two-level walk as sums.
	for _, name := range sortedKeys(metrics) {
		metric := metrics[name]
		//: one header per name; a histogram has a temporality, no monotonicity.
		buf = appendMetricHeader(buf, name, textKindHistogram, metric.Temporality.String(), "")
		//: one line per series.
		for _, point := range metric.Points {
			//: observation count only.
			buf = appendSeriesLine(buf, name, point.Attrs,
				strconv.FormatUint(point.Count, decimalBase))
		}
	}
	//: hand back the extended buffer.
	return buf
}

// appendMetricHeader appends "# metric <name> <kind>[ <temporality>][ <mono>]".
// An empty qualifier is omitted rather than printed blank, which is what makes
// the gauge header shorter than the sum header instead of ragged.
func appendMetricHeader(buf []byte, name, kind, temporality, monotonicity string) []byte {
	//: the header word plus the instrument it qualifies.
	buf = append(buf, "# metric "...)
	buf = append(buf, name...)
	buf = append(buf, ' ')
	buf = append(buf, kind...)
	//: a gauge has no window to name.
	if temporality != "" {
		//: delta or cumulative.
		buf = append(buf, ' ')
		buf = append(buf, temporality...)
	}
	//: only a sum has a monotonicity.
	if monotonicity != "" {
		//: monotonic or non_monotonic.
		buf = append(buf, ' ')
		buf = append(buf, monotonicity...)
	}
	//: terminate the line.
	return append(buf, '\n')
}

// appendSeriesLine appends "name{k=v,…} value\n" to buf.
func appendSeriesLine(buf []byte, name string, attrs []coremetrics.AttrValue, value string) []byte {
	//: the instrument name opens the line.
	buf = append(buf, name...)
	//: the attribute set, or nothing at all when the series has none.
	buf = appendAttrs(buf, attrs)
	//: value, then terminate the line.
	buf = append(buf, ' ')
	buf = append(buf, value...)
	//: hand back the extended buffer.
	return append(buf, '\n')
}

// appendAttrs renders {k=v,…}. A dimensionless series renders as a bare name.
func appendAttrs(buf []byte, attrs []coremetrics.AttrValue) []byte {
	//: no braces at all for the dimensionless series.
	if len(attrs) == 0 {
		//: nothing to render.
		return buf
	}
	//: open the set.
	buf = append(buf, '{')
	//: comma-separated pairs, in the snapshot's canonical order.
	for i, attr := range attrs {
		//: separator between pairs only.
		if i > 0 {
			//: continue the set.
			buf = append(buf, ',')
		}
		//: key, then the typed value.
		buf = appendAttrPair(buf, attr)
	}
	//: close the set.
	return append(buf, '}')
}

// appendAttrPair renders one k=v pair with the value's TYPE visible: a string
// is quoted and escaped, a bool / integer / double is printed bare.
//
// That asymmetry is the point of this exporter. Every wire format the SDK
// targets flattens a bool into the two letters "true"; a diagnostic that did
// the same would hide the one thing the typed attribute model added.
func appendAttrPair(buf []byte, attr coremetrics.AttrValue) []byte {
	//: the key is structure — validated non-empty, so it needs no escaping.
	buf = append(buf, attr.Key...)
	buf = append(buf, '=')
	//: only a string can carry a byte that would break the line.
	if attr.Kind() == coremetrics.AttrKindString {
		//: quoted and escaped.
		return appendQuoted(buf, attr.Str())
	}
	//: a bool / integer / double renders from [0-9a-zA-Z+-.] alone.
	return attr.AppendText(buf)
}

// appendQuoted renders "…" with backslash, quote and newline escaped inside.
func appendQuoted(buf []byte, value string) []byte {
	//: open the quote.
	buf = append(buf, '"')
	//: the value is data — it must not be able to close it.
	buf = appendEscapedValue(buf, value)
	//: close the quote.
	return append(buf, '"')
}

// appendEscapedValue writes a string value with backslash, quote and newline
// escaped.
//
// A string attribute value is data — a URL, a header, a user-supplied tenant
// id. Without escaping, a value containing a quote or a newline forges a line
// that a reader of this output parses as another series, which turns a metrics
// dump into an injection surface. The three escapes are the Prometheus text
// convention, so the output stays readable by the same eyes and tools.
func appendEscapedValue(buf []byte, value string) []byte {
	//: byte-wise: the escapes are all ASCII and UTF-8 is pass-through.
	for i := range len(value) {
		//: escape the three characters that would break the line.
		switch char := value[i]; char {
		//: a literal backslash doubles, or it would eat the next character.
		case '\\':
			//: emit the escaped form.
			buf = append(buf, '\\', '\\')
		//: a quote would close the value early and start a bare token.
		case '"':
			//: emit the escaped form.
			buf = append(buf, '\\', '"')
		//: a newline would forge a whole line a reader parses as a series.
		case '\n':
			//: emit the escaped form.
			buf = append(buf, '\\', 'n')
		//: every other byte is safe inside a quoted value.
		default:
			//: copy verbatim.
			buf = append(buf, char)
		}
	}
	//: hand back the extended buffer.
	return buf
}

// sortedKeys returns the map keys in ascending order for stable output.
func sortedKeys[V any](m map[string]V) []string {
	//: maps.Keys yields an unordered iterator; slices.Sorted materialises it sorted.
	return slices.Sorted(maps.Keys(m))
}
