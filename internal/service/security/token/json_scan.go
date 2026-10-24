package token

import (
	"bytes"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"

	coretoken "github.com/kitsunium/sdk/internal/core/security/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// maxIndexedMembers is how many top-level members an object index holds:
	// the claims cap, which bounds the JOSE header too. A header carrying more
	// is refused with TooLarge — the legacy reader had no such bound, and the
	// divergence is listed in CLAUDE.md.
	maxIndexedMembers int = maxClaimMembers
	// hexDigitsPerEscape is the four hex digits of a \uXXXX escape.
	hexDigitsPerEscape int = 4
	// unicodeEscapeLen is the six bytes of a \uXXXX escape.
	unicodeEscapeLen int = 2 + hexDigitsPerEscape
	// hexLetterOffset is where 'a' and 'A' sit past the ten decimal digits.
	hexLetterOffset rune = 10
	// hexRadixBits is the bits one hex digit carries.
	hexRadixBits uint = 4
	// maxFloat64Order is the decimal order of the largest float64,
	// 1.7976931348623157e308.
	maxFloat64Order int = 308
	// exponentSaturation is where an exponent stops being accumulated: far
	// past every order a float64 can hold or round to zero.
	exponentSaturation int = 1 << 20
	// highSurrogateFirst is the first UTF-16 code unit that opens a surrogate
	// pair, U+D800.
	highSurrogateFirst rune = 0xD800
	// highSurrogateLast is the last one, U+DBFF.
	highSurrogateLast rune = 0xDBFF
	// lowSurrogateFirst is the first UTF-16 code unit that closes a surrogate
	// pair, U+DC00.
	lowSurrogateFirst rune = 0xDC00
	// lowSurrogateLast is the last one, U+DFFF.
	lowSurrogateLast rune = 0xDFFF
)

// valueKind is the JSON type of one scanned value. The zero value is no
// value at all, which no successful scan reports.
type valueKind uint8

const (
	// kindNull is the literal null.
	kindNull valueKind = iota + 1
	// kindFalse is the literal false.
	kindFalse
	// kindTrue is the literal true.
	kindTrue
	// kindNumber is a number in the RFC 8259 §6 grammar.
	kindNumber
	// kindString is a string literal.
	kindString
	// kindArray is an array.
	kindArray
	// kindObject is an object.
	kindObject
)

// memberSpan locates one top-level member of a scanned object inside the
// scanned bytes: the name without its quotes, and the value as written.
type memberSpan struct {
	// nameStart / nameEnd delimit the name, quotes excluded.
	nameStart, nameEnd int32
	// valueStart / valueEnd delimit the value, whitespace excluded.
	valueStart, valueEnd int32
	// kind is the value's JSON type.
	kind valueKind
}

// objectIndex is the top-level members of one scanned object, in order. It is
// an ARRAY, sized by the member cap, so indexing allocates nothing; total
// keeps counting past the cap so the refusal can name it.
type objectIndex struct {
	// member holds the first count members.
	member [maxIndexedMembers]memberSpan
	// count is how many entries of member are populated.
	count int
	// total is how many members the object carries, indexed or not.
	total int
}

// name returns the bytes of member i's name inside raw.
func (x *objectIndex) name(raw []byte, i int) []byte {
	m := x.member[i]
	//: a view, never a copy.
	return raw[m.nameStart:m.nameEnd]
}

// value returns the bytes of member i's value inside raw.
func (x *objectIndex) value(raw []byte, i int) []byte {
	m := x.member[i]
	//: a view, never a copy.
	return raw[m.valueStart:m.valueEnd]
}

// lookup returns the index of the member called name, or -1.
func (x *objectIndex) lookup(raw []byte, name string) int {
	//: the index is small and its names unescaped: a scan is exact.
	for i := range x.count {
		//: the conversion does not escape, so it is zero-copy: no allocation.
		if bytes.Equal(x.name(raw, i), []byte(name)) {
			//: names are unique, so the first is the only one.
			return i
		}
	}
	//: absent.
	return -1
}

// jsonScan is a strict RFC 8259 reader over one byte slice, with one integer
// of position. It decodes nothing and allocates nothing: it validates, and
// reports where things are.
type jsonScan struct {
	// raw is the text being scanned.
	raw []byte
	// pos is the offset of the next unread byte.
	pos int
	// maxDepth bounds nesting; the depth pre-pass already enforced it, and
	// this is the scanner's own guard against a pre-pass it does not trust.
	maxDepth int
}

// scanObject validates raw as exactly one JSON object and indexes its
// top-level members into idx.
//
// It is the one reader every claims object and every JOSE header goes through,
// so the refusal set is one set. A top-level member name carrying a backslash
// is refused with Malformed: an escape is a second spelling of a name, and
// "\u0065xp" read as "exp" is how a duplicate slips past a check made on the
// spelling. A repeated top-level name is refused with DuplicateMember the
// moment it is read (RFC 8725 §2.6). Past the member cap, the object is still
// read to its end, so malformed text is still Malformed, and is then refused
// with TooLarge; duplicates are not looked for past the cap, which is what
// bounds the cost of a hostile payload.
func scanObject(raw []byte, maxDepth int, idx *objectIndex) error {
	s := jsonScan{raw: raw, maxDepth: maxDepth}
	idx.count, idx.total = 0, 0
	s.skipSpace()
	//: a claims object or a header is an object and nothing else.
	if !s.consume('{') {
		//: not a JSON object at all.
		return coretoken.Malformed
	}
	//: the members, if any; the closing brace was consumed when it returns.
	if err := s.topMembers(idx); err != nil {
		//: propagate Malformed / DuplicateMember / TooDeep.
		return err
	}
	s.skipSpace()
	//: one value, then nothing: trailing bytes are another document.
	if s.pos != len(s.raw) {
		//: refused, as encoding/json refuses them.
		return coretoken.Malformed
	}
	//: a syntactically valid object over the cap is still too large.
	if idx.total > maxIndexedMembers {
		//: name the limit, never the members.
		return errs.Wrap(coretoken.TooLarge, errs.WrapParams{},
			errs.Int("limit", maxIndexedMembers))
	}
	//: indexed.
	return nil
}

// topMembers reads the top-level members after the opening brace, through
// the closing one.
func (s *jsonScan) topMembers(idx *objectIndex) error {
	s.skipSpace()
	//: the empty object.
	if s.consume('}') {
		//: no member.
		return nil
	}
	//: one member per iteration; a comma continues, a brace ends.
	for {
		//: the name, the separator and the value.
		if err := s.topMember(idx); err != nil {
			//: propagate the first refusal.
			return err
		}
		s.skipSpace()
		//: another member follows.
		if s.consume(',') {
			continue
		}
		//: the end of the object.
		if s.consume('}') {
			//: read through.
			return nil
		}
		//: anything else is not JSON.
		return coretoken.Malformed
	}
}

// topMember reads one top-level member and indexes it.
func (s *jsonScan) topMember(idx *objectIndex) error {
	s.skipSpace()
	//: a member starts with its name.
	if s.peek() != '"' {
		//: no name, no member.
		return coretoken.Malformed
	}
	nameStart := s.pos + 1
	escaped, serr := s.str()
	//: an unreadable name.
	if serr != nil {
		//: propagate.
		return serr
	}
	//: a second spelling of a name is refused, not decoded.
	if escaped {
		//: the backslash rule.
		return coretoken.Malformed
	}
	name := s.raw[nameStart : s.pos-1]
	//: duplicates are looked for among the indexed names only.
	if idx.total < maxIndexedMembers && idx.hasName(s.raw, name) {
		//: refuse; never "keep the last one".
		return coretoken.DuplicateMember
	}
	s.skipSpace()
	//: a name is followed by its separator.
	if !s.consume(':') {
		//: refused.
		return coretoken.Malformed
	}
	s.skipSpace()
	valueStart := s.pos
	kind, verr := s.value(1)
	//: an unreadable value.
	if verr != nil {
		//: propagate.
		return verr
	}
	//: index while there is room; count always.
	if idx.count < maxIndexedMembers {
		idx.member[idx.count] = memberSpan{
			nameStart: int32(nameStart), nameEnd: int32(nameStart + len(name)),
			valueStart: int32(valueStart), valueEnd: int32(s.pos), kind: kind,
		}
		idx.count++
	}
	idx.total++
	//: indexed.
	return nil
}

// hasName reports whether an indexed member is already called name.
func (x *objectIndex) hasName(raw, name []byte) bool {
	//: at most maxIndexedMembers comparisons, most ending at the length.
	for i := range x.count {
		//: exact bytes; names carry no escape, so bytes are names.
		if bytes.Equal(x.name(raw, i), name) {
			//: seen.
			return true
		}
	}
	//: new.
	return false
}

// value reads one value at depth and reports its kind.
func (s *jsonScan) value(depth int) (kind valueKind, err error) {
	//: the first byte decides the grammar.
	switch c := s.peek(); {
	//: a string.
	case c == '"':
		_, err = s.str()
		//: the string's verdict.
		return kindString, err
	//: an object.
	case c == '{':
		//: one level deeper.
		return kindObject, s.container(depth, '}')
	//: an array.
	case c == '[':
		//: one level deeper.
		return kindArray, s.container(depth, ']')
	//: a number.
	case c == '-' || isDigit(c):
		//: the RFC 8259 §6 grammar, checked before anybody parses it.
		return kindNumber, s.number()
	//: the three literals, or nothing JSON.
	default:
		//: true, false, null, or refused.
		return s.literalValue(c)
	}
}

// literalValue reads the literal that c opens and reports its kind.
func (s *jsonScan) literalValue(c byte) (kind valueKind, err error) {
	//: the three literals.
	switch c {
	//: true.
	case 't':
		//: the whole word.
		return kindTrue, s.literal("true")
	//: false.
	case 'f':
		//: the whole word.
		return kindFalse, s.literal("false")
	//: null.
	case 'n':
		//: the whole word.
		return kindNull, s.literal("null")
	//: nothing else starts a value.
	default:
		//: not JSON.
		return 0, coretoken.Malformed
	}
}

// container reads a nested object or array whose opening byte is at pos,
// through closer. Names inside it are read as strings: escapes and repeats
// are the top level's concern, as they were for the legacy reader.
func (s *jsonScan) container(depth int, closer byte) error {
	//: the scanner's own guard, behind the pre-pass.
	if depth >= s.maxDepth {
		//: name the limit, never the payload.
		return errs.Wrap(coretoken.TooDeep, errs.WrapParams{},
			errs.Int("limit", s.maxDepth))
	}
	s.pos++
	s.skipSpace()
	//: the empty container.
	if s.consume(closer) {
		//: read through.
		return nil
	}
	//: one element per iteration.
	for {
		//: an object element is a name, a separator and a value.
		if err := s.element(depth, closer == '}'); err != nil {
			//: propagate.
			return err
		}
		s.skipSpace()
		//: another element follows.
		if s.consume(',') {
			s.skipSpace()
			continue
		}
		//: the end of the container.
		if s.consume(closer) {
			//: read through.
			return nil
		}
		//: anything else is not JSON.
		return coretoken.Malformed
	}
}

// element reads one array element, or one object member when member is set.
func (s *jsonScan) element(depth int, member bool) error {
	//: a member is preceded by its name and separator.
	if member {
		//: a member starts with its name.
		if s.peek() != '"' {
			//: no name, no member.
			return coretoken.Malformed
		}
		//: escapes are allowed in a nested name.
		if _, err := s.str(); err != nil {
			//: propagate.
			return err
		}
		s.skipSpace()
		//: the separator.
		if !s.consume(':') {
			//: refused.
			return coretoken.Malformed
		}
		s.skipSpace()
	}
	_, err := s.value(depth + 1)
	//: the value's verdict.
	return err
}

// str reads a string literal whose opening quote is at pos, through its
// closing quote, and reports whether it carried an escape. It refuses an
// unescaped control byte, an unknown escape, and a lone surrogate.
func (s *jsonScan) str() (escaped bool, err error) {
	s.pos++
	//: byte by byte; UTF-8 validity was checked over the whole text first.
	for s.pos < len(s.raw) {
		c := s.raw[s.pos]
		//: the three bytes that end the plain run.
		switch {
		//: the closing quote.
		case c == '"':
			s.pos++
			//: read through.
			return escaped, nil
		//: an escape.
		case c == '\\':
			escaped = true
			n, eerr := escapeLen(s.raw[s.pos:])
			//: an escape this grammar does not have.
			if eerr != nil {
				//: refused.
				return true, eerr
			}
			s.pos += n
		//: a raw control byte (RFC 8259 §7).
		case c < firstPrintable:
			//: refused.
			return escaped, coretoken.Malformed
		//: plain content.
		default:
			s.pos++
		}
	}
	//: the text ended inside a string.
	return escaped, coretoken.Malformed
}

// escapeLen validates the escape at the start of esc and returns its length.
// A high surrogate must be followed at once by an escaped low one; a lone
// surrogate of either half is refused, where encoding/json would have
// replaced it with U+FFFD — two spellings of one string.
func escapeLen(esc []byte) (n int, err error) {
	//: a backslash needs a byte after it.
	if len(esc) < 2 {
		//: truncated.
		return 0, coretoken.Malformed
	}
	//: the eight escapes of RFC 8259 §7.
	switch esc[1] {
	//: the two-byte escapes.
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		//: fixed length.
		return 2, nil
	//: the code-unit escape.
	case 'u':
		//: validated below.
	//: anything else.
	default:
		//: not an escape.
		return 0, coretoken.Malformed
	}
	unit, ok := hexUnit(esc)
	//: four hex digits are required, and a low surrogate cannot open a pair.
	if !ok || isLowSurrogate(unit) {
		//: refused, or a lone low half.
		return 0, coretoken.Malformed
	}
	//: anything but a high surrogate stands alone.
	if unit < highSurrogateFirst || unit > highSurrogateLast {
		//: one code unit.
		return unicodeEscapeLen, nil
	}
	//: a high surrogate needs its low half, escaped, right after it.
	if len(esc) < 2*unicodeEscapeLen || esc[unicodeEscapeLen] != '\\' {
		//: lone high half.
		return 0, coretoken.Malformed
	}
	low, lok := hexUnit(esc[unicodeEscapeLen:])
	//: the second escape must be a low surrogate.
	if !lok || !isLowSurrogate(low) {
		//: lone high half.
		return 0, coretoken.Malformed
	}
	//: a pair.
	return 2 * unicodeEscapeLen, nil
}

// isLowSurrogate reports whether unit is the UTF-16 code unit that closes a
// surrogate pair.
func isLowSurrogate(unit rune) bool {
	//: U+DC00 to U+DFFF.
	return unit >= lowSurrogateFirst && unit <= lowSurrogateLast
}

// hexUnit reads the \uXXXX escape at the start of esc.
func hexUnit(esc []byte) (unit rune, ok bool) {
	//: the escape is six bytes: backslash, u, four digits.
	if len(esc) < unicodeEscapeLen || esc[0] != '\\' || esc[1] != 'u' {
		//: not a \u escape.
		return 0, false
	}
	//: four digits, most significant first.
	for _, c := range esc[2:unicodeEscapeLen] {
		digit, dok := hexValue(c)
		//: a non-hex byte.
		if !dok {
			//: refused.
			return 0, false
		}
		unit = unit<<hexRadixBits | digit
	}
	//: one UTF-16 code unit.
	return unit, true
}

// hexValue reads one hex digit, in either case.
func hexValue(c byte) (digit rune, ok bool) {
	//: three ranges.
	switch {
	//: decimal.
	case isDigit(c):
		//: 0-9.
		return rune(c - '0'), true
	//: lowercase.
	case c >= 'a' && c <= 'f':
		//: 10-15.
		return rune(c-'a') + hexLetterOffset, true
	//: uppercase.
	case c >= 'A' && c <= 'F':
		//: 10-15.
		return rune(c-'A') + hexLetterOffset, true
	//: anything else.
	default:
		//: not a digit.
		return 0, false
	}
}

// number reads a number in the RFC 8259 §6 grammar: an optional minus, an
// integer part without a leading zero, an optional fraction and exponent,
// each with at least one digit. strconv would accept more — "0x1p3", "Inf",
// "1_0" — which is why nothing reaches it unchecked.
//
// A number whose magnitude a float64 cannot hold is refused too, wherever it
// sits: the legacy reader walked every token with encoding/json, which reads
// each number as a float64 and refused "1e400" at any depth. Underflow is not
// refused — strconv rounds it to zero without an error, as encoding/json did.
func (s *jsonScan) number() error {
	start := s.pos
	s.consume('-')
	intStart := s.pos
	//: the integer part: a lone zero, or a non-zero digit and more.
	if !s.consume('0') && !s.digits() {
		//: no integer part.
		return coretoken.Malformed
	}
	intDigits := s.pos - intStart
	fracStart, fracEnd := s.pos, s.pos
	//: an optional fraction, with at least one digit.
	if s.consume('.') {
		fracStart = s.pos
		//: "1." is not a number.
		if !s.digits() {
			//: refused.
			return coretoken.Malformed
		}
		fracEnd = s.pos
	}
	exponent, err := s.exponent()
	//: "1e" is not a number.
	if err != nil {
		//: refused.
		return err
	}
	//: the range, as a float64 would see it.
	return s.checkMagnitude(start, intStart, intDigits, s.raw[fracStart:fracEnd], exponent)
}

// exponent reads an optional exponent and returns its value, saturated far
// past any float64's range so a thousand-digit exponent cannot overflow it.
func (s *jsonScan) exponent() (value int, err error) {
	//: no exponent at all.
	if !s.consume('e') && !s.consume('E') {
		//: zero.
		return 0, nil
	}
	negative := false
	//: the sign is optional.
	if !s.consume('+') {
		negative = s.consume('-')
	}
	start := s.pos
	//: the digits are not.
	if !s.digits() {
		//: refused.
		return 0, coretoken.Malformed
	}
	//: most significant first, saturated.
	for _, c := range s.raw[start:s.pos] {
		//: past the saturation point nothing changes the verdict.
		if value < exponentSaturation {
			value = value*decimalBase + int(c-'0')
		}
	}
	//: the sign, last.
	if negative {
		value = -value
	}
	//: the exponent.
	return value, nil
}

// checkMagnitude refuses a number whose magnitude is past the largest
// float64. The number is d.ddd × 10^order with 1 ≤ d.ddd < 10: an order up to
// 307 always fits, one from 309 never does, and only order 308 is close
// enough to need strconv's own verdict.
func (s *jsonScan) checkMagnitude(start, intStart, intDigits int, frac []byte, exponent int) error {
	var order int
	//: a non-zero integer part sets the order by its length.
	if s.raw[intStart] != '0' {
		order = intDigits - 1 + exponent
	} else {
		lead := 0
		//: below one, the order is the position of the first non-zero digit.
		for lead < len(frac) && frac[lead] == '0' {
			lead++
		}
		//: every digit is zero: the value is zero, whatever the exponent.
		if lead == len(frac) {
			//: in range.
			return nil
		}
		order = exponent - lead - 1
	}
	//: the three bands.
	switch {
	//: always a float64.
	case order < maxFloat64Order:
		//: in range.
		return nil
	//: never one.
	case order > maxFloat64Order:
		//: refused, as encoding/json refused it.
		return coretoken.Malformed
	//: the edge: strconv decides, as it did for encoding/json.
	default:
		//: only the rare edge number pays a conversion.
		if _, perr := strconv.ParseFloat(string(s.raw[start:s.pos]), 64); perr != nil {
			//: out of range.
			return coretoken.Malformed
		}
		//: in range.
		return nil
	}
}

// digits consumes a run of decimal digits and reports whether there was one.
func (s *jsonScan) digits() bool {
	start := s.pos
	//: as many as there are.
	for s.pos < len(s.raw) && isDigit(s.raw[s.pos]) {
		s.pos++
	}
	//: at least one.
	return s.pos > start
}

// literal consumes word, or refuses.
func (s *jsonScan) literal(word string) error {
	//: the exact bytes, nothing else.
	if len(s.raw)-s.pos < len(word) || !bytes.Equal(s.raw[s.pos:s.pos+len(word)], []byte(word)) {
		//: refused.
		return coretoken.Malformed
	}
	s.pos += len(word)
	//: read.
	return nil
}

// skipSpace consumes the four whitespace bytes of RFC 8259 §2.
func (s *jsonScan) skipSpace() {
	//: space, tab, line feed, carriage return — nothing else.
	for s.pos < len(s.raw) {
		//: the four bytes.
		switch s.raw[s.pos] {
		//: whitespace.
		case ' ', '\t', '\n', '\r':
			s.pos++
		//: anything else ends the run.
		default:
			//: done.
			return
		}
	}
}

// peek returns the next byte, or 0 at the end.
func (s *jsonScan) peek() byte {
	//: 0 never starts or continues a JSON token.
	if s.pos >= len(s.raw) {
		//: end of text.
		return 0
	}
	//: the next byte.
	return s.raw[s.pos]
}

// consume advances past c when it is the next byte.
func (s *jsonScan) consume(c byte) bool {
	//: only the expected byte.
	if s.peek() != c || s.pos >= len(s.raw) {
		//: left in place.
		return false
	}
	s.pos++
	//: consumed.
	return true
}

// isDigit reports whether c is a decimal digit.
func isDigit(c byte) bool {
	//: ASCII 0-9.
	return c >= '0' && c <= '9'
}

// appendJSONString appends the decoded content of lit, a string literal the
// scanner already validated, quotes included.
func appendJSONString(dst, lit []byte) []byte {
	body := lit[1 : len(lit)-1]
	//: plain runs are copied whole; each escape is decoded on its own.
	for i := 0; i < len(body); {
		//: a plain run, up to the next backslash.
		if body[i] != '\\' {
			j := i + 1
			//: find the run's end.
			for j < len(body) && body[j] != '\\' {
				j++
			}
			dst = append(dst, body[i:j]...)
			i = j
			continue
		}
		r, n := decodeEscape(body[i:])
		dst = utf8.AppendRune(dst, r)
		i += n
	}
	//: the decoded octets.
	return dst
}

// decodeEscape decodes the validated escape at the start of esc.
func decodeEscape(esc []byte) (decoded rune, n int) {
	//: the two-byte escapes map to one byte each.
	switch esc[1] {
	//: backspace.
	case 'b':
		//: U+0008.
		return '\b', 2
	//: form feed.
	case 'f':
		//: U+000C.
		return '\f', 2
	//: line feed.
	case 'n':
		//: U+000A.
		return '\n', 2
	//: carriage return.
	case 'r':
		//: U+000D.
		return '\r', 2
	//: tab.
	case 't':
		//: U+0009.
		return '\t', 2
	//: a code unit, or a pair of them.
	case 'u':
		unit, _ := hexUnit(esc)
		//: a validated high surrogate has its low half after it.
		if utf16.IsSurrogate(unit) {
			low, _ := hexUnit(esc[unicodeEscapeLen:])
			//: the pair's code point.
			return utf16.DecodeRune(unit, low), 2 * unicodeEscapeLen
		}
		//: one code point.
		return unit, unicodeEscapeLen
	//: quote, backslash and solidus stand for themselves.
	default:
		//: the escaped byte.
		return rune(esc[1]), 2
	}
}

// jsonStringEqual reports whether the validated string literal lit decodes to
// want, without decoding it anywhere: it compares as it reads, so a literal of
// any length is compared without a buffer and without an allocation. It is
// not constant-time; the strings it compares are a configured issuer or
// audience, never a secret.
func jsonStringEqual(lit []byte, want string) bool {
	body := lit[1 : len(lit)-1]
	var unit [utf8.UTFMax]byte
	w := 0
	//: byte by byte for plain content, escape by escape otherwise.
	for i := 0; i < len(body); {
		piece := body[i : i+1]
		consumed := 1
		//: an escape stands for the octets of its code point.
		if body[i] == '\\' {
			r, n := decodeEscape(body[i:])
			piece = utf8.AppendRune(unit[:0], r)
			consumed = n
		}
		//: the next octets of want must be exactly these.
		if len(want)-w < len(piece) || !bytes.Equal(piece, []byte(want[w:w+len(piece)])) {
			//: a difference, or want ran out.
			return false
		}
		w += len(piece)
		i += consumed
	}
	//: equal only if want is fully consumed too.
	return w == len(want)
}

// arrayStrings walks the elements of a validated JSON array, each of which
// must be a string. It allocates nothing: each call to next reports a view of
// the next element's literal, quotes included.
type arrayStrings struct {
	// s reads the array's bytes.
	s jsonScan
}

// newArrayStrings starts a walk over the validated array arr.
func newArrayStrings(arr []byte) arrayStrings {
	walk := arrayStrings{s: jsonScan{raw: arr, pos: 1}}
	walk.s.skipSpace()
	//: positioned on the first element, or on the closing bracket.
	return walk
}

// next returns the next element's literal. ok is false at the end of the
// array; err is Malformed for an element that is not a string — a null
// included, where the legacy reader decoded one as "".
func (a *arrayStrings) next() (lit []byte, ok bool, err error) {
	//: the end of the array.
	if a.s.peek() == ']' {
		//: walked through.
		return nil, false, nil
	}
	//: every member of "aud" is a string.
	if a.s.peek() != '"' {
		//: refused.
		return nil, false, malformed("aud is neither a string nor an array of strings")
	}
	start := a.s.pos
	//: the scanner validated it already; this only finds its end.
	if _, serr := a.s.str(); serr != nil {
		//: unreachable over validated text, kept typed.
		return nil, false, serr
	}
	lit = a.s.raw[start:a.s.pos]
	a.s.skipSpace()
	a.s.consume(',')
	a.s.skipSpace()
	//: one element.
	return lit, true, nil
}

// decodeText returns the decoded content of the validated string literal lit
// as a string. A literal without an escape is converted once; one with
// escapes is decoded into a stack buffer first.
func decodeText(lit []byte) string {
	body := lit[1 : len(lit)-1]
	//: the common case: nothing to decode.
	if bytes.IndexByte(body, '\\') < 0 {
		//: one conversion.
		return string(body)
	}
	var buf [coretoken.ClaimsBufTextCap]byte
	//: decoded, then converted.
	return string(appendJSONString(buf[:0], lit))
}

// decodeTextInto decodes the validated string literal lit into dst, and
// reports how many octets it wrote. ok is false when the decoded text does not
// fit dst — which is never written past, never truncated into, and never
// grown.
func decodeTextInto(dst, lit []byte) (n int, ok bool) {
	body := lit[1 : len(lit)-1]
	//: plain runs are copied whole; each escape is decoded on its own.
	for i := 0; i < len(body); {
		//: a plain run, up to the next backslash.
		if body[i] != '\\' {
			j := i + 1
			//: find the run's end.
			for j < len(body) && body[j] != '\\' {
				j++
			}
			//: the run must fit whole.
			if len(dst)-n < j-i {
				//: refused, not cut.
				return 0, false
			}
			n += copy(dst[n:], body[i:j])
			i = j
			continue
		}
		r, consumed := decodeEscape(body[i:])
		//: the code point must fit whole.
		if len(dst)-n < utf8.RuneLen(r) {
			//: refused, not cut.
			return 0, false
		}
		n += utf8.EncodeRune(dst[n:], r)
		i += consumed
	}
	//: decoded.
	return n, true
}
