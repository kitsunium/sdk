// Package metrics — stdlib text Exporter (one series per line).
package metrics

import (
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"sync"

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

// textExporter renders a Snapshot as newline-delimited
// "kind name{labels} value" lines.
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
	//: one block per kind, each in sorted-name order for deterministic output.
	buf = appendCounters(buf, snap.Counters)
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

// appendCounters renders every counter series as "counter <name>{…} <total>".
func appendCounters(buf []byte, groups map[string][]coremetrics.CounterValue) []byte {
	//: names first, so the block is stable across runs.
	for _, name := range sortedKeys(groups) {
		//: then each series, already ordered by label set by Collect.
		for _, series := range groups[name] {
			//: the cumulative total is an int64.
			buf = appendSeriesLine(buf, "counter ", name, series.Labels,
				strconv.FormatInt(series.Value, decimalBase))
		}
	}
	//: hand back the extended buffer.
	return buf
}

// appendGauges renders every gauge series as "gauge <name>{…} <reading>".
func appendGauges(buf []byte, groups map[string][]coremetrics.GaugeValue) []byte {
	//: same two-level walk as counters.
	for _, name := range sortedKeys(groups) {
		//: one line per series.
		for _, series := range groups[name] {
			//: the reading is a float64.
			buf = appendSeriesLine(buf, "gauge ", name, series.Labels,
				strconv.FormatFloat(series.Value, 'g', -1, floatBitSize))
		}
	}
	//: hand back the extended buffer.
	return buf
}

// appendHistograms renders each histogram series' observation count. The full
// bucket layout is reachable through the Snapshot API — this exporter is a
// diagnostic, not a wire format.
func appendHistograms(buf []byte, groups map[string][]coremetrics.HistogramValue) []byte {
	//: same two-level walk as counters.
	for _, name := range sortedKeys(groups) {
		//: one line per series.
		for _, series := range groups[name] {
			//: observation count only.
			buf = appendSeriesLine(buf, "histogram_count ", name, series.Labels,
				strconv.FormatUint(series.Count, decimalBase))
		}
	}
	//: hand back the extended buffer.
	return buf
}

// appendSeriesLine appends "prefix name{k=\"v\",…} value\n" to buf.
func appendSeriesLine(buf []byte, prefix, name string, labels []coremetrics.LabelValue, value string) []byte {
	//: kind marker then the instrument name.
	buf = append(buf, prefix...)
	buf = append(buf, name...)
	//: the label set, or nothing at all when the series has none.
	buf = appendLabels(buf, labels)
	//: value, then terminate the line.
	buf = append(buf, ' ')
	buf = append(buf, value...)
	//: hand back the extended buffer.
	return append(buf, '\n')
}

// appendLabels renders {k="v",…}. A dimensionless series renders as a bare
// name, so every pre-label line is byte-identical to what it always was.
func appendLabels(buf []byte, labels []coremetrics.LabelValue) []byte {
	//: no braces at all for the dimensionless series.
	if len(labels) == 0 {
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
		//: key is structure — no escaping needed, it is validated non-empty.
		buf = append(buf, label.Key...)
		buf = append(buf, '=', '"')
		//: value is data — it must not be able to close the quote.
		buf = appendEscapedValue(buf, label.Value)
		buf = append(buf, '"')
	}
	//: close the set.
	return append(buf, '}')
}

// appendEscapedValue writes a label value with backslash, quote and newline
// escaped.
//
// A label value is data — a URL, a header, a user-supplied tenant id. Without
// escaping, a value containing a quote or a newline forges a line that a
// reader of this output parses as another series, which turns a metrics dump
// into an injection surface. The three escapes are the Prometheus text
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
