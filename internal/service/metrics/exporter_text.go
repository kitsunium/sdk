// Package metrics — stdlib text Exporter (one metric per line).
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

// textExporter renders a Snapshot as newline-delimited "kind name value" lines.
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
	//: counters in sorted-name order for deterministic output.
	for _, name := range sortedKeys(snap.Counters) {
		//: "counter <name> <value>".
		buf = appendLine(buf, "counter ", name, strconv.FormatInt(snap.Counters[name], decimalBase))
	}
	//: gauges next.
	for _, name := range sortedKeys(snap.Gauges) {
		//: "gauge <name> <value>".
		buf = appendLine(buf, "gauge ", name, strconv.FormatFloat(snap.Gauges[name], 'g', -1, floatBitSize))
	}
	//: histograms emit their observation count (full buckets via the Snapshot API).
	for _, name := range sortedKeys(snap.Histograms) {
		//: "histogram_count <name> <count>".
		buf = appendLine(buf, "histogram_count ", name, strconv.FormatUint(snap.Histograms[name].Count, decimalBase))
	}
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

// appendLine appends "prefix name value\n" to buf and returns the grown slice.
func appendLine(buf []byte, prefix, name, value string) []byte {
	//: concatenate the four parts plus the newline onto buf.
	buf = append(buf, prefix...)
	buf = append(buf, name...)
	buf = append(buf, ' ')
	buf = append(buf, value...)
	buf = append(buf, '\n')
	//: hand back the extended buffer.
	return buf
}

// sortedKeys returns the map keys in ascending order for stable output.
func sortedKeys[V any](m map[string]V) []string {
	//: maps.Keys yields an unordered iterator; slices.Sorted materialises it sorted.
	return slices.Sorted(maps.Keys(m))
}
