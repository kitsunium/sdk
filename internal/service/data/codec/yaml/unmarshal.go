// Package yaml — decoding: the source checked, the document parsed, values built.
package yaml

import (
	"bytes"
	"math"
	"reflect"
	"unicode/utf8"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The byte order marks of the encodings YAML allows besides UTF-8, which the
// subset refuses, and the line breaks the source is normalised with.
var (
	// bomUTF16BigEndian opens UTF-16 big-endian text.
	bomUTF16BigEndian = []byte{0xFE, 0xFF}
	// bomUTF16LittleEndian opens UTF-16 (or UTF-32) little-endian text.
	bomUTF16LittleEndian = []byte{0xFF, 0xFE}
	// bomUTF32BigEndian opens UTF-32 big-endian text.
	bomUTF32BigEndian = []byte{0x00, 0x00, 0xFE, 0xFF}
	// carriageReturnLineFeed is a carriage return and a line feed, one line
	// break.
	carriageReturnLineFeed = []byte("\r\n")
	// lineFeed is the line break the parser reads.
	lineFeed = []byte("\n")
	// carriageReturn is a lone carriage return, one line break.
	carriageReturn = []byte("\r")
)

// decoder turns a parsed document into Go values.
type decoder struct {
	// p holds the document's arena.
	p *parser
	// hooks counts the UnmarshalYAML calls in progress, so a hook that keeps
	// decoding into itself is refused instead of recursing forever.
	hooks int
}

// decodeDocument parses data as one document of the subset and decodes it
// into v, a non-nil pointer. firstLine is the line number of data's first
// line, so a document read from a stream is located in the stream. An empty
// document leaves v unchanged.
func decodeDocument(data []byte, v any, firstLine int) error {
	target := reflect.ValueOf(v)
	//: a target the decoder can write through.
	if target.Kind() != reflect.Pointer || target.IsNil() {
		//: refused before reading.
		return errs.Wrap(UnmarshalFailed, errs.WrapParams{}, errs.String("detail", "the target must be a non-nil pointer"))
	}
	src, err := prepareSource(data, firstLine)
	//: not UTF-8, or a control character.
	if err != nil {
		//: refused.
		return err
	}
	p := acquireParser(src)
	defer releaseParser(p)
	p.line = firstLine
	root, err := p.parseDocument()
	//: a syntax error, a refused construct, or nothing to decode.
	if err != nil || root == noNode {
		//: refused, or the target is left as it was.
		return err
	}
	d := decoder{p: p}
	//: the untyped targets, without reflection.
	if handled, uerr := d.decodeUntyped(root, v); handled {
		//: decoded, or refused.
		return uerr
	}
	//: anything else, by reflection.
	return d.decode(root, target.Elem())
}

// decodeUntyped decodes node root into *map[string]any or *any without
// reflection, and reports whether v was one of them.
func (d *decoder) decodeUntyped(root int32, v any) (bool, error) {
	switch target := v.(type) {
	//: the path config and i18n take.
	case *map[string]any:
		//: straight into the map.
		return true, d.intoStringMap(root, target)
	//: an untyped value.
	case *any:
		value, err := d.toAny(root)
		//: decoded, unless refused.
		if err == nil {
			*target = value
		}
		//: decoded, or refused.
		return true, err
	//: a typed target.
	default:
		//: by reflection.
		return false, nil
	}
}

// prepareSource checks data is text YAML can hold — UTF-8, no control
// character but tab and line breaks, no character YAML 1.1 and 1.2 read
// differently — and returns it as a string with every line break normalised
// to "\n". The check runs before the parse, so the parser never meets a byte
// it has to second-guess.
func prepareSource(data []byte, firstLine int) (string, error) {
	//: a UTF-16 or UTF-32 byte order mark.
	if bytes.HasPrefix(data, bomUTF16BigEndian) || bytes.HasPrefix(data, bomUTF16LittleEndian) ||
		bytes.HasPrefix(data, bomUTF32BigEndian) {
		//: only UTF-8 is read.
		return "", syntaxError(firstLine, 1, "the document is not UTF-8")
	}
	//: the first forbidden character.
	if off, detail, found := forbiddenCharacter(data); found {
		//: refused where it is.
		return "", sourceError(data, off, firstLine, detail)
	}
	//: no carriage return: the bytes as they are.
	if bytes.IndexByte(data, '\r') < 0 {
		//: one copy.
		return string(data), nil
	}
	//: CRLF and a lone CR are both one line break.
	return string(bytes.ReplaceAll(bytes.ReplaceAll(data, carriageReturnLineFeed, lineFeed), carriageReturn, lineFeed)), nil
}

// forbiddenCharacter returns the offset of the first character of data the
// subset refuses, why, and whether there is one.
func forbiddenCharacter(data []byte) (int, string, bool) {
	//: every character.
	for i := 0; i < len(data); {
		r, size := rune(data[i]), 1
		//: beyond ASCII.
		if r >= utf8.RuneSelf {
			r, size = utf8.DecodeRune(data[i:])
		}
		//: one the subset refuses.
		if detail, forbidden := forbiddenRune(r, size, i); forbidden {
			//: its offset.
			return i, detail, true
		}
		i += size
	}
	//: none.
	return 0, "", false
}

// forbiddenRune reports whether the character r, encoded in size bytes at
// byte offset off, is refused, and why.
func forbiddenRune(r rune, size, off int) (string, bool) {
	switch {
	//: not UTF-8.
	case r == utf8.RuneError && size == 1:
		//: refused.
		return "the document is not valid UTF-8", true
	//: U+FEFF anywhere but first: libyaml skips one where a line starts and
	//: keeps one elsewhere as text, so a reader cannot tell which a raw one is.
	case r == runeByteOrderMark && off > 0:
		//: refused; \uFEFF writes it unambiguously.
		return "a byte order mark may only open the document; write \\uFEFF in a double-quoted scalar", true
	//: U+0085, U+2028, U+2029: line breaks to YAML 1.1 (and yaml.v3), text to
	//: YAML 1.2 — so a reader cannot tell which a raw one is.
	case isAmbiguousBreak(r):
		//: refused; \N, \L and \P write them unambiguously.
		return "U+0085, U+2028 and U+2029 must be escaped (\\N, \\L, \\P) in a double-quoted scalar", true
	//: a control character other than tab and the line breaks, or a non-character.
	case isForbiddenControl(r):
		//: refused.
		return "a control character must be escaped in a double-quoted scalar", true
	//: anything else.
	default:
		//: allowed.
		return "", false
	}
}

// isAmbiguousBreak reports whether r is a line break to YAML 1.1 and text to
// YAML 1.2.
func isAmbiguousBreak(r rune) bool {
	//: NEL, LS, PS.
	return r == runeNextLine || r == runeLineSeparator || r == runeParagraphSeparator
}

// isForbiddenControl reports whether r is a control character a document
// cannot hold raw — any but tab and the line breaks — or a non-character.
func isForbiddenControl(r rune) bool {
	//: tab and the line breaks are text's own.
	if r == '\t' || r == '\n' || r == '\r' {
		//: allowed.
		return false
	}
	//: a control character, or U+FFFE, U+FFFF.
	return isControl(r) || r == runeNonCharacterFFFE || r == runeNonCharacterFFFF
}

// sourceError is a syntax error at byte i of data, whose first line is
// firstLine.
func sourceError(data []byte, i, firstLine int, detail string) error {
	before := data[:i]
	line := firstLine + bytes.Count(before, lineFeed)
	lineStart := bytes.LastIndexByte(before, '\n') + 1
	//: the line and the character column.
	return syntaxError(line, utf8.RuneCount(before[lineStart:])+1, detail)
}

// node returns the node at index i.
func (d *decoder) node(i int32) *node {
	//: the arena entry.
	return &d.p.nodes[i]
}

// mismatch is UnmarshalFailed at node n: a value its target cannot hold. It
// names the Go type and never the value.
func (d *decoder) mismatch(n *node, detail string, t reflect.Type) error {
	fields := append(at(int(n.line), d.p.columnOf(int(n.off))), errs.String("detail", detail))
	//: the target's type, when there is one.
	if t != nil {
		fields = append(fields, errs.String("type", t.String()))
	}
	//: the sentinel is the origin.
	return errs.Wrap(UnmarshalFailed, errs.WrapParams{}, fields...)
}

// refuse is a refused construct at node n.
func (d *decoder) refuse(sentinel *errs.Error, n *node) error {
	//: at the node.
	return refused(sentinel, int(n.line), d.p.columnOf(int(n.off)))
}

// describe names what node n is, for a refusal.
func describe(n *node) string {
	switch {
	//: a mapping.
	case n.kind == kindMapping:
		//: its name.
		return "a mapping"
	//: a sequence.
	case n.kind == kindSequence:
		//: its name.
		return "a sequence"
	//: a quoted or block scalar is a string.
	case n.style != stylePlain:
		//: its name.
		return "a string"
	//: a plain scalar.
	default:
		//: by the core schema.
		return describeResolved(resolvePlain(n.value).kind)
	}
}

// describeResolved names a plain scalar's reading by the core schema.
func describeResolved(kind resolvedKind) string {
	switch kind {
	//: null.
	case resolvedNull:
		//: its name.
		return "null"
	//: a boolean.
	case resolvedBool:
		//: its name.
		return "a boolean"
	//: an integer.
	case resolvedInt, resolvedUint, resolvedLeadingZero:
		//: its name.
		return "an integer"
	//: a float.
	case resolvedFloat:
		//: its name.
		return "a float"
	//: out of range.
	case resolvedOutOfRange:
		//: its name.
		return "a number out of range"
	//: a string.
	default:
		//: its name.
		return "a string"
	}
}

// toAny decodes node i into the untyped value it is: nil, bool, int (int64
// when an int is narrower, uint64 past int64), float64, string, []any or
// map[string]any — a mapping keyed by each key's text.
func (d *decoder) toAny(i int32) (any, error) {
	n := d.node(i)
	switch n.kind {
	//: a sequence.
	case kindSequence:
		//: its entries.
		return d.sequenceAny(n)
	//: a mapping.
	case kindMapping:
		out := make(map[string]any, n.count/2)
		//: its entries.
		return out, d.fillStringMap(n, out)
	//: a scalar.
	case kindScalar:
		//: a quoted or block scalar is a string.
		if n.style != stylePlain {
			//: its text.
			return n.value, nil
		}
		//: a plain scalar, by the core schema.
		return d.plainAny(n)
	//: no other kind exists.
	default:
		//: refused rather than guessed.
		return nil, d.mismatch(n, "an unknown node kind", nil)
	}
}

// sequenceAny decodes the sequence n into a []any.
func (d *decoder) sequenceAny(n *node) ([]any, error) {
	out := make([]any, n.count)
	//: every entry.
	for j := range n.count {
		value, err := d.toAny(d.p.child(n, j))
		//: refused.
		if err != nil {
			return nil, err
		}
		out[j] = value
	}
	//: the slice.
	return out, nil
}

// fillStringMap decodes the mapping n's entries into out, keyed by each key's
// text.
func (d *decoder) fillStringMap(n *node, out map[string]any) error {
	//: every entry: a key's text, then its value.
	for j := int32(0); j < n.count; j += 2 {
		value, err := d.toAny(d.p.child(n, j+1))
		//: refused.
		if err != nil {
			return err
		}
		out[d.node(d.p.child(n, j)).value] = value
	}
	//: filled.
	return nil
}

// plainAny decodes the plain scalar n by the core schema.
func (d *decoder) plainAny(n *node) (any, error) {
	r := resolvePlain(n.value)
	switch r.kind {
	//: null.
	case resolvedNull:
		//: nil.
		return nil, nil
	//: a boolean.
	case resolvedBool:
		//: bool.
		return r.b, nil
	//: an integer that fits an int64.
	case resolvedInt:
		//: int when it fits, as yaml.v3 decoded it.
		return intAny(r.i), nil
	//: an integer past int64.
	case resolvedUint:
		//: uint64.
		return r.u, nil
	//: a float.
	case resolvedFloat:
		//: float64.
		return r.f, nil
	//: a string.
	case resolvedString:
		//: its text.
		return n.value, nil
	//: 0644, or a number past 64 bits.
	default:
		//: refused.
		return nil, d.refuseResolved(n, r.kind, nil)
	}
}

// intAny returns i as an int when it fits one, as an int64 otherwise — on a
// 32-bit platform.
func intAny(i int64) any {
	//: fits an int.
	if i >= math.MinInt && i <= math.MaxInt {
		//: int.
		return int(i)
	}
	//: int64.
	return i
}

// refuseResolved refuses a plain scalar the core schema reads as a leading
// zero integer — by name — or as a number past 64 bits.
func (d *decoder) refuseResolved(n *node, kind resolvedKind, t reflect.Type) error {
	//: 0644: octal or decimal, depending on who reads it.
	if kind == resolvedLeadingZero {
		//: refused by name.
		return d.refuse(LeadingZeroRefused, n)
	}
	//: a number past 64 bits.
	return d.mismatch(n, "a number no 64-bit Go value holds", t)
}

// intoStringMap decodes node i into *target, merging into an existing map
// as yaml.v3 does.
func (d *decoder) intoStringMap(i int32, target *map[string]any) error {
	n := d.node(i)
	//: null empties the map.
	if n.isNull() {
		*target = nil
		//: decoded.
		return nil
	}
	//: only a mapping fills a map.
	if n.kind != kindMapping {
		//: refused.
		return d.mismatch(n, describe(n)+" cannot be decoded into a map", stringMapType)
	}
	//: a nil map is made.
	if *target == nil {
		*target = make(map[string]any, n.count/2)
	}
	//: the entries.
	return d.fillStringMap(n, *target)
}
