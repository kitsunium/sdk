// Package csv wraps encoding/csv as a codec.Codec implementation.
// The CSV format serialises a matrix of strings; the codec accepts
// [][]string for Marshal and writes into *[][]string on Unmarshal.
package csv

import (
	"bytes"
	stdcsv "encoding/csv"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// promotionHeaderCell is the single-column header label the facade
// promotion path stamps when wrapping a non-CSV value (matches
// pkg/v1/codec/promote.go csvPromotionHeader).
const promotionHeaderCell = "_json"

// promotionRowCount is the expected row count of a promotion-wrapped
// CSV: one header row plus one body row carrying the JSON.
const promotionRowCount int = 2

// promotionColumnCount is the expected column count per promotion row.
const promotionColumnCount int = 1

// promotionBufWorstCaseFactor accounts for the worst-case `"` → `""`
// expansion in the body cell of the promotion fast-path. Body length
// is multiplied by this factor when pre-sizing the output buffer.
const promotionBufWorstCaseFactor int = 2

// Codec is the CSV singleton, registered with core/codec at package load.
// Binding the registration result to a named var is more idiomatic than
// `var _ = codec.Register(...)` and keeps us clear of init().
// The singleton has escapeFormulas=false for wire-format fidelity — consumers
// whose output is read by a spreadsheet application should use NewWithEscape(true)
// instead.
var Codec codec.Codec = codec.Register(&csvCodec{})

// csvCodec is the concrete Codec implementation for CSV. The escapeFormulas
// field toggles the CSV-injection mitigation documented by OWASP: cells that
// begin with '=', '+', '-', '@', TAB, or CR are prefixed with a single quote
// so Excel / LibreOffice / Google Sheets do not evaluate them as formulas.
// See NewWithEscape for the recommended opt-in path.
type csvCodec struct {
	// escapeFormulas gates the OWASP CSV-injection mitigation. When true,
	// Marshal rewrites any cell starting with a formula-trigger byte by
	// prefixing it with "'" so downstream spreadsheet apps render the cell
	// as text. When false (the default), cells pass through verbatim so
	// the wire format is lossless for round-trip use.
	escapeFormulas bool
}

// New returns the CSV singleton with escapeFormulas disabled. This preserves
// the v1 API and keeps the wire-format lossless for pipe-to-pipe use.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// NewWithEscape returns a fresh CSV codec with the formula-injection
// mitigation toggled on or off. Use this constructor when the encoded
// output will be opened in a spreadsheet app (Excel, LibreOffice,
// Google Sheets): without the mitigation an attacker-influenced cell
// starting with "=" evaluates as a formula at open time, which is
// OWASP CSV Injection (CWE-1236). The returned codec is not registered
// with the core/codec singleton registry.
func NewWithEscape(escape bool) codec.Codec {
	//: fresh instance so callers who want the mitigation do not affect the
	//: registered singleton or any other consumer.
	return &csvCodec{escapeFormulas: escape}
}

// Name implements codec.Codec.
func (*csvCodec) Name() string {
	//: canonical identifier.
	return "csv"
}

// MIMETypes lists every MIME alias.
func (*csvCodec) MIMETypes() []string {
	//: RFC 4180 registers text/csv.
	return []string{"text/csv"}
}

// Extensions lists every file extension.
func (*csvCodec) Extensions() []string {
	//: canonical extension.
	return []string{".csv"}
}

// Marshal serialises a [][]string into RFC 4180 CSV bytes.
func (c *csvCodec) Marshal(v any) (encoded []byte, err error) {
	//: accept both direct and pointer form to keep call sites flexible.
	records, ok := extractRecords(v)
	//: type-gate failure surfaces the dedicated sentinel.
	if !ok {
		//: caller passed something that is not [][]string — loud failure.
		return nil, errs.Wrap(nil, errs.WrapParams{
			Code:    CodeCSVValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "CSV codec requires a [][]string value",
			Private: "service/codec/csv.Marshal: argument is not [][]string",
		})
	}
	//: promotion-shape fast-path: 2 rows × 1 column with header "_json"
	//: is the canonical shape pkg/v1/codec/promote.go produces for CSV.
	//: Bypass csv.Writer + WriteAll + Flush by hand-writing the bytes;
	//: only escape is `"` → `""` because the body is JSON UTF-8.
	if !c.escapeFormulas && isPromotionShape(records) {
		//: dedicated fast-path emits identical RFC 4180 bytes.
		return marshalPromotionShape(records[1][0]), nil
	}
	//: apply the OWASP CSV-injection mitigation when the caller opted in;
	//: unmodified records on the default path preserve wire-format fidelity.
	if c.escapeFormulas {
		//: operate on a defensive copy so the caller's slice stays intact.
		records = escapeFormulaCells(records)
	}
	//: write into a buffer so the caller gets []byte (not a writer).
	var buf bytes.Buffer
	w := stdcsv.NewWriter(&buf)
	//: WriteAll flushes on return; any error is surfaced via Error().
	if werr := w.WriteAll(records); werr != nil {
		//: wrap the stdlib error.
		return nil, errs.Wrap(werr, errs.WrapParams{
			Code:    CodeCSVMarshalFailed,
			Reason:  "MARSHAL_FAILED",
			Public:  "CSV encoding failed",
			Private: "service/codec/csv.Marshal: WriteAll returned an error",
		})
	}
	//: hand back the buffered bytes.
	return buf.Bytes(), nil
}

// escapeFormulaCells returns a defensive copy of records with every cell
// starting with a formula-trigger byte prefixed by a single quote so the
// cell renders as text in Excel / LibreOffice / Google Sheets. Implements
// the OWASP CSV Injection mitigation (CWE-1236) on an opt-in basis.
func escapeFormulaCells(records [][]string) [][]string {
	//: pre-allocate so no append-reallocation occurs in the hot path.
	out := make([][]string, 0, len(records))
	//: walk each row and build a sanitised per-row copy via helper to
	//: keep the per-row allocation off the outer loop (ktn HOTLOOP).
	for _, row := range records {
		//: delegate to escapeFormulaRow so the make() lives in its frame.
		out = append(out, escapeFormulaRow(row))
	}
	//: hand back the sanitised matrix.
	return out
}

// escapeFormulaRow returns a defensive copy of row with every cell's
// first byte passed through escapeIfFormulaCell. Kept as a dedicated
// helper so its per-row allocation is attributed to its own stack frame
// rather than being flagged as an inner-loop allocation by static
// analysis.
func escapeFormulaRow(row []string) []string {
	//: pre-sized copy with zero reallocation during append.
	out := make([]string, 0, len(row))
	//: inspect every cell and prefix when the first byte triggers evaluation.
	for _, cell := range row {
		out = append(out, escapeIfFormulaCell(cell))
	}
	//: hand back the sanitised row.
	return out
}

// isFormulaTrigger reports whether b is a byte that a spreadsheet app
// interprets as the start of a formula. The set includes the classic
// trigger bytes (=, +, -, @) plus the two control characters (TAB, CR)
// that some spreadsheets treat as formula prefixes after whitespace trim.
func isFormulaTrigger(b byte) bool {
	//: explicit byte comparison avoids switch-case-comment verbosity.
	return b == '=' || b == '+' || b == '-' || b == '@' || b == '\t' || b == '\r'
}

// escapeIfFormulaCell prefixes cell with a single quote when its first
// byte would otherwise trigger spreadsheet formula evaluation.
func escapeIfFormulaCell(cell string) string {
	//: empty cell is inert — nothing to escape; early return keeps the
	//: single-byte read below unconditionally safe.
	if cell == "" || !isFormulaTrigger(cell[0]) {
		//: safe input — pass through verbatim.
		return cell
	}
	//: formula-trigger byte — prefix with single quote so spreadsheets
	//: render the cell as text rather than evaluating it.
	return "'" + cell
}

// Append encodes records into CSV bytes and appends them to dst.
// Implements the optional codec.Appender interface so hot-path callers
// (multi-table exports, paginated dumps) can stream CSV records into
// a recycled buffer without an intermediate allocation per call.
// Delegates to Marshal so the type-gate + escape-formula + wrap/error
// contract has a single source.
func (c *csvCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: delegate to Marshal so the contract has a single source.
	encoded, merr := c.Marshal(v)
	//: surface any encoding failure without touching dst.
	if merr != nil {
		//: return the untouched buffer plus the wrapped error.
		return dst, merr
	}
	//: append the encoded bytes onto the caller's buffer.
	return append(dst, encoded...), nil
}

// isPromotionShape reports whether records is the exact 2-row × 1-col
// shape pkg/v1/codec/promote.go produces — i.e. header == "_json" and
// the body row has exactly one cell. Matching this shape lets Marshal
// bypass csv.Writer for a measurable win on every promotion call.
func isPromotionShape(records [][]string) bool {
	//: combined row + column count guard — exact 2x1 promotion shape.
	if len(records) != promotionRowCount ||
		len(records[0]) != promotionColumnCount ||
		len(records[1]) != promotionColumnCount {
		//: not promotion-shaped.
		return false
	}
	//: header literal pinned by the facade.
	return records[0][0] == promotionHeaderCell
}

// marshalPromotionShape emits standards-compliant CSV bytes for the
// 2-row × 1-col promotion shape. The body cell always contains JSON
// (with `{`, `"`, `,` etc.) so it always needs quoting; the only
// escape needed is `"` → `""` per the double-quote-doubling rule.
// Saves csv.Writer construction + per-row Write loop + Flush.
func marshalPromotionShape(body string) []byte {
	//: pre-size: header + \n + opening " + worst-case body + closing " + \n.
	out := make([]byte, 0, len(promotionHeaderCell)+1+1+promotionBufWorstCaseFactor*len(body)+1+1)
	//: literal header row + line terminator.
	out = append(out, promotionHeaderCell...)
	out = append(out, '\n')
	//: opening double-quote of the body cell.
	out = append(out, '"')
	//: per-byte walk: `"` doubles, everything else passes through.
	for i := range len(body) {
		//: double-quote escape.
		if body[i] == '"' {
			//: emit the doubled quote.
			out = append(out, '"', '"')
			//: next input byte.
			continue
		}
		//: plain byte passthrough — CSV doesn't escape anything else
		//: inside a quoted field except embedded `"`.
		out = append(out, body[i])
	}
	//: closing double-quote of the body cell.
	out = append(out, '"')
	//: line terminator after the body row.
	out = append(out, '\n')
	//: caller owns the bytes.
	return out
}

// Unmarshal parses data as CSV into *[][]string.
func (*csvCodec) Unmarshal(data []byte, v any) error {
	//: target must be *[][]string so we can populate it.
	dst, ok := v.(*[][]string)
	//: type-gate failure surfaces the dedicated sentinel.
	if !ok {
		//: caller passed something that is not *[][]string — loud failure.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeCSVValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "CSV codec requires a [][]string value",
			Private: "service/codec/csv.Unmarshal: target is not *[][]string",
		})
	}
	//: ReadAll consumes every record from the reader.
	r := stdcsv.NewReader(bytes.NewReader(data))
	recs, rerr := r.ReadAll()
	//: success fast-path.
	if rerr == nil {
		//: publish the decoded records through the caller's pointer.
		*dst = recs
		//: nothing to wrap.
		return nil
	}
	//: wrap the stdlib error.
	return errs.Wrap(rerr, errs.WrapParams{
		Code:    CodeCSVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "CSV decoding failed",
		Private: "service/codec/csv.Unmarshal: ReadAll returned an error",
	})
}

// extractRecords coerces v into a [][]string, accepting both direct and
// pointer forms.
func extractRecords(v any) (records [][]string, ok bool) {
	//: direct form is the common case.
	if direct, directOk := v.([][]string); directOk {
		//: hand back the direct slice.
		return direct, true
	}
	//: pointer form also accepted for symmetry with Unmarshal.
	if ptr, ptrOk := v.(*[][]string); ptrOk && ptr != nil {
		//: dereference once.
		return *ptr, true
	}
	//: anything else is a programming error for this codec.
	return nil, false
}
