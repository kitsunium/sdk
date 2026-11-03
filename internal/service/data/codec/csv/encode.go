package csv

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// quoteSpecials are the bytes encoding/csv's Writer escapes inside a quoted
// field: the quote itself is doubled, CR and LF are written as they are.
const quoteSpecials string = "\"\r\n"

// formulaPrefix is the byte the OWASP mitigation writes before a cell a
// spreadsheet would evaluate (escapeIfFormulaCell).
const formulaPrefix byte = '\''

// fieldSpec is how one cell is written: whether it gains the formula prefix,
// and whether it is quoted.
type fieldSpec struct {
	// prefixed is whether the cell is written after formulaPrefix.
	prefixed bool
	// quoted is whether the cell is written between quotes.
	quoted bool
}

// appendRecords appends records as encoding/csv's Writer.WriteAll writes them
// with UseCRLF false: each field quoted only when it must be, a quote doubled,
// one '\n' after each record. It writes into dst directly — the encoder an
// Append needs — and Marshal sizes dst with recordsLen first. escape applies
// the OWASP mitigation to every cell as escapeIfFormulaCell would.
func appendRecords(dst []byte, records [][]string, comma rune, escape bool) []byte {
	//: one record after the other, each terminated.
	for _, record := range records {
		//: the record's fields and its terminator.
		dst = appendRecord(dst, record, comma, escape)
	}
	//: the caller's prefix, then every record.
	return dst
}

// appendRecord appends one record and its '\n', as encoding/csv's
// Writer.Write writes it.
func appendRecord(dst []byte, record []string, comma rune, escape bool) []byte {
	//: fields separated by the delimiter.
	for i, field := range record {
		//: the delimiter goes between fields, as WriteRune writes it.
		if i > 0 {
			//: one rune, UTF-8 encoded.
			dst = utf8.AppendRune(dst, comma)
		}
		//: the field, prefixed and quoted as it needs.
		dst = appendField(dst, field, specFor(field, comma, escape))
	}
	//: the record terminator (UseCRLF is false).
	return append(dst, '\n')
}

// appendField appends field as spec says: after the formula prefix when it
// gains one, between quotes with every quote doubled when it is quoted.
func appendField(dst []byte, field string, spec fieldSpec) []byte {
	//: an unquoted field is its bytes, after the prefix when it has one.
	if !spec.quoted {
		//: the prefix is a byte the field does not need quoted for.
		if spec.prefixed {
			//: the mitigation's leading quote.
			dst = append(dst, formulaPrefix)
		}
		//: verbatim.
		return append(dst, field...)
	}
	//: the opening quote.
	dst = append(dst, '"')
	//: the prefix sits inside the quotes, as it is part of the escaped cell.
	if spec.prefixed {
		//: the mitigation's leading quote.
		dst = append(dst, formulaPrefix)
	}
	//: copy runs of plain bytes, doubling each quote; CR and LF stay as they are.
	for field != "" {
		//: the next quote, if any; CR and LF are copied with the run.
		i := strings.IndexByte(field, '"')
		//: no quote left: the rest is plain.
		if i < 0 {
			//: the tail of the field.
			dst = append(dst, field...)
			//: done with this field.
			break
		}
		//: the run before the quote, then the doubled quote.
		dst = append(dst, field[:i]...)
		dst = append(dst, '"', '"')
		field = field[i+1:]
	}
	//: the closing quote.
	return append(dst, '"')
}

// specFor decides how field is written: prefixed when escape is on and its
// first byte would start a formula, quoted when encoding/csv's
// fieldNeedsQuotes says so of the cell as written (prefix included).
func specFor(field string, comma rune, escape bool) fieldSpec {
	//: the OWASP mitigation, when the caller asked for it.
	prefixed := escape && field != "" && isFormulaTrigger(field[0])
	//: a prefixed cell starts with a quote mark, never a space, and is never `\.`.
	if prefixed {
		//: only a special byte inside forces the quotes.
		return fieldSpec{prefixed: true, quoted: hasSpecial(field, comma)}
	}
	//: encoding/csv's own rule.
	return fieldSpec{quoted: fieldNeedsQuotes(field, comma)}
}

// fieldNeedsQuotes is encoding/csv's Writer.fieldNeedsQuotes: a non-empty
// field is quoted when it is `\.`, holds the delimiter, a quote, CR or LF, or
// starts with a space as unicode.IsSpace reads it.
func fieldNeedsQuotes(field string, comma rune) bool {
	//: the empty field is written bare.
	if field == "" {
		return false
	}
	//: a line holding only `\.` would end a PostgreSQL COPY.
	if field == `\.` || hasSpecial(field, comma) {
		return true
	}
	//: a leading space would be trimmed by some readers.
	r1, _ := utf8.DecodeRuneInString(field)
	return unicode.IsSpace(r1)
}

// hasSpecial reports whether field holds the delimiter, a quote, CR or LF.
func hasSpecial(field string, comma rune) bool {
	//: a single-byte delimiter: one pass over the bytes, as encoding/csv does.
	if comma < utf8.RuneSelf {
		//: any special byte.
		for i := range len(field) {
			//: the delimiter or one of the three quote specials.
			if c := field[i]; c == '\n' || c == '\r' || c == '"' || c == byte(comma) {
				return true
			}
		}
		//: none found.
		return false
	}
	//: a multi-byte delimiter, searched as a rune.
	return strings.ContainsRune(field, comma) || strings.ContainsAny(field, quoteSpecials)
}

// recordsLen is the exact length appendRecords writes for records, so
// Marshal allocates its result once at its final size.
func recordsLen(records [][]string, comma rune, escape bool) int {
	//: the delimiter's encoded width.
	commaLen := utf8.RuneLen(comma)
	//: summed record by record.
	n := 0
	//: each record: its fields, the delimiters between them, its terminator.
	for _, record := range records {
		//: the terminator.
		n++
		//: the fields and their delimiters.
		for i, field := range record {
			//: a delimiter before every field but the first.
			if i > 0 {
				n += commaLen
			}
			//: the field as written.
			n += fieldLen(field, specFor(field, comma, escape))
		}
	}
	//: the exact size.
	return n
}

// fieldLen is the length appendField writes for field under spec.
func fieldLen(field string, spec fieldSpec) int {
	//: the bytes themselves.
	n := len(field)
	//: the mitigation's prefix.
	if spec.prefixed {
		n++
	}
	//: the two quotes and one more byte per doubled quote.
	if spec.quoted {
		n += 2 + strings.Count(field, `"`)
	}
	//: the written length.
	return n
}
