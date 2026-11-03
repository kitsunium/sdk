package csv

import (
	"bytes"
	stdcsv "encoding/csv"
	"errors"
	"io"
	"slices"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corecsv "github.com/kitsunium/sdk/internal/core/data/codec/csv"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Package-level constants and vars are grouped per KTN-VAR-GROUP /
// KTN-CONST-ORDER: all consts first (cap-discard threshold + promotion
// shape constants), then the package-level vars (pool + Codec singleton).
const (
	// promotionHeaderCell is the single-column header label the facade
	// promotion path stamps when wrapping a non-CSV value (matches
	// pkg/v1/data/codec/promote.go csvPromotionHeader).
	promotionHeaderCell = "_json"

	// promotionRowCount is the expected row count of a promotion-wrapped
	// CSV: one header row plus one body row carrying the JSON.
	promotionRowCount int = 2

	// promotionColumnCount is the expected column count per promotion row.
	promotionColumnCount int = 1

	// promotionBufWorstCaseFactor accounts for the worst-case `"` → `""`
	// expansion in the body cell of the promotion fast-path. Body length
	// is multiplied by this factor when pre-sizing the output buffer.
	promotionBufWorstCaseFactor int = 2

	// maxReservedRecords caps the records Unmarshal reserves room for before
	// it has read one (#1098): past it the table grows by records read.
	maxReservedRecords int = 4096

	// defaultComma is the delimiter the registered codec writes and reads,
	// encoding/csv's own default.
	defaultComma rune = ','
)

// Package-level vars: the Marshal-buffer pool plus the singleton
// registered with core/data/codec at package load.
var (
	//: Codec is the CSV singleton, registered with core/data/codec at package
	//: load. Binding the registration result to a named var is more
	//: idiomatic than `var _ = codec.Register(...)` and keeps us clear of
	//: init(). The singleton has escapeFormulas=false for wire-format
	//: fidelity — consumers whose output is read by a spreadsheet
	//: application should use NewWithEscape(true) instead.
	Codec codec.Codec = codec.Register(&csvCodec{})
)

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
	// comma is the delimiter; zero means defaultComma, so the registered
	// singleton and the instances NewWithEscape builds keep encoding/csv's.
	comma rune
	// noHeader is whether a typed table is written and read without a header
	// row, its columns being the struct's fields in order.
	noHeader bool
}

// Options configure a CSV codec built by NewWithOptions. The zero Options is
// the registered codec's behaviour.
type Options struct {
	// Escape turns on the OWASP CSV-injection mitigation (CWE-1236) when
	// writing: a cell starting with '=', '+', '-', '@', TAB or CR is prefixed
	// with a single quote so a spreadsheet shows it as text. See NewWithEscape.
	Escape bool
	// Comma is the field delimiter; zero means ','. It must be a valid rune
	// other than '"', '\r', '\n' and U+FFFD, as encoding/csv requires.
	Comma rune
	// NoHeader writes and reads a table of structs without a header row: the
	// columns are the struct's fields, in order. It does not change a
	// [][]string, whose records are all data.
	NoHeader bool
}

// NewWithOptions returns a fresh CSV codec configured by o — the delimiter,
// the formula mitigation, and whether a table of structs has a header row.
// A delimiter encoding/csv refuses is OPTIONS_INVALID. The returned codec is
// not registered: Format("csv") keeps resolving to the default singleton.
func NewWithOptions(o Options) (c codec.Codec, err error) {
	//: the zero delimiter is encoding/csv's own.
	comma := o.Comma
	if comma == 0 {
		comma = defaultComma
	}
	//: encoding/csv's rule for a delimiter.
	if !validDelim(comma) {
		//: refused at construction, never at the first write.
		return nil, errs.Wrap(corecsv.OptionsInvalid, errs.WrapParams{})
	}
	//: a fresh instance, never the singleton.
	return &csvCodec{escapeFormulas: o.Escape, comma: comma, noHeader: o.NoHeader}, nil
}

// validDelim is encoding/csv's check of a delimiter.
func validDelim(r rune) bool {
	//: not a quote, a line break, the replacement character, or invalid.
	return r != 0 && r != '"' && r != '\r' && r != '\n' && utf8.ValidRune(r) && r != utf8.RuneError
}

// format is how the codec writes and reads.
func (c *csvCodec) format() format {
	//: the zero delimiter is the default one.
	comma := c.comma
	if comma == 0 {
		comma = defaultComma
	}
	//: the codec's settings.
	return format{comma: comma, escape: c.escapeFormulas, noHeader: c.noHeader}
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
// with the core/data/codec singleton registry.
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
	//: anything else must be a table of structs (#902).
	if !ok {
		//: the typed table, or VALUE_INVALID.
		return c.marshalTable(v)
	}
	//: the codec's delimiter and mitigation.
	f := c.format()
	//: promotion-shape fast-path: 2 rows × 1 column with header "_json"
	//: is the canonical shape pkg/v1/data/codec/promote.go produces for CSV.
	if !f.escape && f.comma == defaultComma && isPromotionShape(records) {
		//: dedicated fast-path emits the bytes it always emitted.
		return marshalPromotionShape(records[1][0]), nil
	}
	//: one allocation at the exact size, then the records written into it
	//: as encoding/csv's Writer writes them — the mitigation applied cell by
	//: cell when the caller opted in, never on a copy of the records.
	out := make([]byte, 0, recordsLen(records, f.comma, f.escape))
	//: the caller owns the bytes.
	return appendRecords(out, records, f.comma, f.escape), nil
}

// marshalTable writes a slice of structs: a header row from the columns,
// then one record per row (#902).
func (c *csvCodec) marshalTable(v any) (encoded []byte, err error) {
	//: the rows and their columns, or VALUE_INVALID.
	t, terr := tableOf(v)
	//: not a table of structs.
	if terr != nil {
		return nil, terr
	}
	//: an estimate of the size; append grows past it.
	out, aerr := appendTable(make([]byte, 0, (t.rows.Len()+1)*rowEstimate), t, c.format())
	//: a field that does not write itself.
	if aerr != nil {
		return nil, aerr
	}
	//: the caller owns the bytes.
	return out, nil
}

// valueInvalid is the refusal of a value or a target that is not a records
// matrix; private names the call that refused it.
func valueInvalid(private string) error {
	//: the dedicated sentinel's code, reason and public text.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    corecsv.CodeCSVValueInvalid,
		Reason:  "VALUE_INVALID",
		Public:  "CSV codec requires a [][]string value",
		Private: private,
	})
}

// isFormulaTrigger reports whether b is a byte that a spreadsheet app
// interprets as the start of a formula. The set includes the classic
// trigger bytes (=, +, -, @) plus the two control characters (TAB, CR)
// that some spreadsheets treat as formula prefixes after whitespace trim.
func isFormulaTrigger(b byte) bool {
	//: explicit byte comparison avoids switch-case-comment verbosity.
	return b == '=' || b == '+' || b == '-' || b == '@' || b == '\t' || b == '\r'
}

// Append encodes records into CSV bytes and appends them to dst.
// Implements the optional codec.Appender interface so hot-path callers
// (multi-table exports, paginated dumps) can write CSV records into a
// recycled buffer: the records are written into dst itself, grown once to
// the exact size when it lacks the room, with no intermediate encoding.
// On a refusal dst is returned as it was given.
func (c *csvCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: a forwarder small enough to inline, as Append always was.
	return c.appendValue(dst, v)
}

// appendValue is Append's body: the type gate, then the records written in
// place.
func (c *csvCodec) appendValue(dst []byte, v any) (appended []byte, err error) {
	//: the same type gate as Marshal.
	records, ok := extractRecords(v)
	//: the codec's delimiter and mitigation.
	f := c.format()
	//: a table of structs (#902), written in place.
	if !ok {
		//: the rows and their columns, or VALUE_INVALID: dst untouched.
		t, terr := tableOf(v)
		if terr != nil {
			return dst, terr
		}
		//: written after the caller's prefix.
		out, aerr := appendTable(dst, t, f)
		//: a field that does not write itself: dst as it was given.
		if aerr != nil {
			return dst, aerr
		}
		//: the caller's prefix, then the table.
		return out, nil
	}
	//: the promotion shape, as Marshal writes it.
	if !f.escape && f.comma == defaultComma && isPromotionShape(records) {
		//: appended in place.
		return appendPromotionShape(dst, records[1][0]), nil
	}
	//: room for the whole encoding, then the records written in place.
	dst = slices.Grow(dst, recordsLen(records, f.comma, f.escape))
	//: the caller's prefix, then the records.
	return appendRecords(dst, records, f.comma, f.escape), nil
}

// isPromotionShape reports whether records is the exact 2-row × 1-col
// shape pkg/v1/data/codec/promote.go produces — i.e. header == "_json" and
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
// 2-row × 1-col promotion shape into a buffer of its own; see
// appendPromotionShape.
func marshalPromotionShape(body string) []byte {
	//: pre-size: header + \n + opening " + worst-case body + closing " + \n.
	out := make([]byte, 0, len(promotionHeaderCell)+1+1+promotionBufWorstCaseFactor*len(body)+1+1)
	//: the caller owns the bytes.
	return appendPromotionShape(out, body)
}

// appendPromotionShape appends the 2-row × 1-col promotion shape to dst. The
// body cell always contains JSON (with `{`, `"`, `,` etc.) so it is always
// quoted; the only escape needed is `"` → `""` per the double-quote-doubling
// rule. Saves csv.Writer construction + per-row Write loop + Flush.
func appendPromotionShape(dst []byte, body string) []byte {
	//: literal header row + line terminator, then the opening quote.
	dst = append(dst, promotionHeaderCell...)
	dst = append(dst, '\n', '"')
	//: per-byte walk: `"` doubles, everything else passes through.
	for i := range len(body) {
		//: double-quote escape.
		if body[i] == '"' {
			//: emit the doubled quote.
			dst = append(dst, '"', '"')
			//: next input byte.
			continue
		}
		//: plain byte passthrough — CSV doesn't escape anything else
		//: inside a quoted field except embedded `"`.
		dst = append(dst, body[i])
	}
	//: closing double-quote of the body cell, then the line terminator.
	return append(dst, '"', '\n')
}

// Unmarshal parses data as CSV into *[][]string.
func (c *csvCodec) Unmarshal(data []byte, v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: the untyped target: every record, as it is.
	dst, ok := v.(*[][]string)
	//: anything else must be a pointer to a slice of structs (#902).
	if !ok {
		//: the typed target, or VALUE_INVALID.
		t, terr := rowsTargetOf(v)
		if terr != nil {
			return terr
		}
		//: the header maps the columns, each record a row.
		return unmarshalTable(data, t, c.format())
	}
	//: rent a *bytes.Reader from the shared pool, positioned at data.
	br := scratch.AcquireReader(data)
	//: hand-rolled Read loop replaces stdcsv.Reader.ReadAll: it pre-
	//: sizes the outer [][]string from the wire newline count + uses
	//: ReuseRecord to amortise per-record slice growth on the stdlib
	//: side. ReuseRecord returns slices that alias the reader's
	//: internal backing array; slices.Clone per record gives the
	//: caller an independent []string while keeping the strings
	//: (which are immutable and freshly allocated by the reader)
	//: shared by reference.
	recs, rerr := readAllPreSized(br, data, c.format().comma)
	//: hand the reader back to the shared pool.
	scratch.ReleaseReader(br)
	//: success fast-path.
	if rerr == nil {
		//: publish the decoded records through the caller's pointer.
		*dst = recs
		//: nothing to wrap.
		return nil
	}
	//: wrap the stdlib error.
	return errs.Wrap(rerr, errs.WrapParams{
		Code:    corecsv.CodeCSVUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "CSV decoding failed",
		Private: "service/data/codec/csv.Unmarshal: ReadAll returned an error",
	}, parsePosition(rerr)...)
}

// readAllPreSized is the stdcsv.Reader.ReadAll equivalent with two
// pre-allocation tricks: the outer [][]string is sized from the wire's
// non-empty lines (recordsHint, capped at maxReservedRecords — #1098), and
// ReuseRecord lets the stdlib reader avoid re-allocating the per-record
// []string backing array on every call. Each record is cloned before storage because
// ReuseRecord aliases — the strings inside are fresh (immutable)
// but the slice header isn't.
func readAllPreSized(br *bytes.Reader, data []byte, comma rune) (records [][]string, err error) {
	//: stdcsv.NewReader is the wire-parsing primitive; no Reset method
	//: exists so we allocate a fresh one per call (16 ns micro-cost).
	r := stdcsv.NewReader(br)
	//: the codec's delimiter.
	r.Comma = comma
	//: ReuseRecord = true tells the reader to alias the per-record
	//: []string across Read calls, eliminating the geometric grow
	//: cascade inside reader.go:441-445 ReadAll uses.
	r.ReuseRecord = true
	//: pre-allocate the outer slice from the non-empty lines, at most
	//: maxReservedRecords; append grows it past that by records read.
	records = make([][]string, 0, recordsHint(data))
	//: read records one at a time so we can clone the aliased slice.
	for {
		//: stdlib Read returns the next record OR io.EOF / parse error.
		record, rerr := r.Read()
		//: clean stream end terminates the loop.
		if errors.Is(rerr, io.EOF) {
			//: every record consumed.
			return records, nil
		}
		//: any other failure surfaces verbatim.
		if rerr != nil {
			//: caller wraps it.
			return nil, rerr
		}
		//: ReuseRecord aliases — clone the slice header (strings
		//: inside are fresh per Read, so cloning the []string is
		//: enough; the underlying strings stay caller-owned).
		records = append(records, slices.Clone(record))
	}
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

// recordsHint is the room Unmarshal reserves for data's records: its lines
// holding a byte but their terminator — encoding/csv skips the others — and
// at most maxReservedRecords. A quoted field spanning lines makes it an upper
// bound, never a lower one; 1 MiB of newlines reserves nothing (#1098).
func recordsHint(data []byte) int {
	//: counted line by line, up to the cap.
	n := 0
	//: each line, the last one with or without its terminator.
	for len(data) > 0 && n < maxReservedRecords {
		//: the line's length, its '\n' included when it has one.
		end := bytes.IndexByte(data, '\n') + 1
		//: no terminator: the rest is the last line.
		if end == 0 {
			end = len(data)
		}
		//: "\n" and "\r\n" are the empty lines encoding/csv skips.
		if line := data[:end]; !bytes.Equal(line, []byte{'\n'}) && !bytes.Equal(line, []byte("\r\n")) {
			n++
		}
		//: the next line.
		data = data[end:]
	}
	//: the reservation.
	return n
}
