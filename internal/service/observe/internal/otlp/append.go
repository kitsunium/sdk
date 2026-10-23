package otlp

import "unicode/utf8"

// hexDigits spells the low nibble of an escaped byte or line separator.
const hexDigits string = "0123456789abcdef"

// The JSON literals an appender writes for a bool; null is jsonNull, the
// literal the response decoder reads.
const (
	jsonTrue  string = "true"
	jsonFalse string = "false"
)

// The byte ranges AppendString treats specially: below spaceByte a byte is a
// control character JSON requires escaped; lineSeparator and paragraphSeparator
// are escaped unconditionally, as encoding/json does.
const (
	spaceByte          byte = 0x20
	lineSeparator      rune = 0x2028
	paragraphSeparator rune = 0x2029
	nibbleShift        int  = 4
	nibbleMask         byte = 0x0f
)

// AppendString appends s to dst as a JSON string, byte for byte what Marshal
// renders for it: '"' and '\\' escaped, the five short control escapes (\b,
// \f, \n, \r, \t), every other byte below 0x20 as \u00xx, an invalid UTF-8
// byte replaced by U+FFFD written as it is, U+2028 and U+2029 written as their
// six-character escapes — and, because Marshal disables HTML escaping, '<', '>'
// and '&' left as they are.
func AppendString(dst []byte, s string) []byte {
	//: the opening quote.
	dst = append(dst, '"')
	//: start marks the first byte not yet copied; runs of safe bytes are
	//: copied at once rather than byte by byte.
	start := 0
	//: walk the string once.
	for i := 0; i < len(s); {
		//: ASCII: safe unless it is a quote, a backslash or a control byte.
		if b := s[i]; b < utf8.RuneSelf {
			//: the common case, kept in the run.
			if b >= spaceByte && b != '"' && b != '\\' {
				//: next byte.
				i++
				continue
			}
			//: flush the run, then the escape.
			dst = append(dst, s[start:i]...)
			dst = appendEscapedByte(dst, b)
			i++
			start = i
			continue
		}
		//: a multi-byte sequence: kept in the run, or flushed and rewritten.
		var size int
		dst, size, start = appendMultiByte(dst, s, i, start)
		i += size
	}
	//: the last run and the closing quote.
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// appendMultiByte handles the sequence that starts at s[i], a byte at or above
// utf8.RuneSelf, for AppendString: it returns dst, the sequence's size and the
// start of the run still to copy. A valid rune stays in the run; an invalid
// byte and the two separators JSONP cannot carry flush the run first, as
// encoding/json does — the byte replaced by U+FFFD, the separators escaped.
func appendMultiByte(dst []byte, s string, i, start int) (rendered []byte, size, runStart int) {
	//: decoded to tell valid from invalid.
	c, size := utf8.DecodeRuneInString(s[i:])
	//: an invalid byte is replaced by the replacement character, unescaped.
	if c == utf8.RuneError && size == 1 {
		//: flush the run, then the replacement.
		dst = append(dst, s[start:i]...)
		return utf8.AppendRune(dst, utf8.RuneError), size, i + size
	}
	//: the two separators are escaped.
	if c == lineSeparator || c == paragraphSeparator {
		//: flush the run, then the six-character escape.
		dst = append(dst, s[start:i]...)
		return append(dst, '\\', 'u', '2', '0', '2', hexDigits[c&rune(nibbleMask)]), size, i + size
	}
	//: any other valid rune stays in the run.
	return dst, size, start
}

// appendEscapedByte appends the escape of one ASCII byte AppendString does not
// keep as it is.
func appendEscapedByte(dst []byte, b byte) []byte {
	//: the short escapes first, then the generic one.
	switch b {
	//: the two structural characters.
	case '\\', '"':
		//: a backslash before the character itself.
		return append(dst, '\\', b)
	//: backspace.
	case '\b':
		//: its short escape.
		return append(dst, '\\', 'b')
	//: form feed.
	case '\f':
		//: its short escape.
		return append(dst, '\\', 'f')
	//: line feed.
	case '\n':
		//: its short escape.
		return append(dst, '\\', 'n')
	//: carriage return.
	case '\r':
		//: its short escape.
		return append(dst, '\\', 'r')
	//: tab.
	case '\t':
		//: its short escape.
		return append(dst, '\\', 't')
	//: every other control byte.
	default:
		//: \u00 and two hex digits.
		return append(dst, '\\', 'u', '0', '0', hexDigits[b>>nibbleShift], hexDigits[b&nibbleMask])
	}
}

// AppendKey appends the member name key and its colon to dst, preceded by a
// comma unless the member is the first of its object — which an appender
// knows by the '{' it just wrote. key is a schema name: written as it is, with
// no escaping, so it must be one.
func AppendKey(dst []byte, key string) []byte {
	//: a member after another one is separated from it.
	if len(dst) > 0 && dst[len(dst)-1] != '{' {
		//: the separator.
		dst = append(dst, ',')
	}
	//: "key":
	dst = append(dst, '"')
	dst = append(dst, key...)
	return append(dst, '"', ':')
}

// AppendBool appends v as the JSON literal true or false.
func AppendBool(dst []byte, v bool) []byte {
	//: the literal.
	if v {
		//: true.
		return append(dst, jsonTrue...)
	}
	//: false.
	return append(dst, jsonFalse...)
}

// AppendNull appends the JSON literal null — what Marshal writes for a nil
// slice or a nil pointer its member does not omit.
func AppendNull(dst []byte) []byte {
	//: the literal.
	return append(dst, jsonNull...)
}

// AppendJSON appends the integer to dst as a quoted decimal string — what its
// MarshalJSON returns, without the slice.
func (v Int64) AppendJSON(dst []byte) []byte {
	//: quote, digits, quote — no escaping is possible inside a decimal.
	dst = append(dst, '"')
	dst = appendInt(dst, int64(v))
	return append(dst, '"')
}

// AppendJSON appends the integer to dst as a quoted decimal string — what its
// MarshalJSON returns, without the slice.
func (v Uint64) AppendJSON(dst []byte) []byte {
	//: quote, digits, quote.
	dst = append(dst, '"')
	dst = appendUint(dst, uint64(v))
	return append(dst, '"')
}

// AppendJSON appends the double to dst shortest-round-trip, or its quoted name
// when it is not finite — what its MarshalJSON returns, without the slice.
func (v Double) AppendJSON(dst []byte) []byte {
	//: a value that is not a number has a name rather than a rendering.
	return appendDouble(dst, float64(v))
}

// AppendJSON appends the oneof to dst: the one case that is set, emitted even
// at its zero, or {} when none is.
func (v AnyValue) AppendJSON(dst []byte) []byte {
	//: the members in field-number order, each omitted when nil.
	dst = append(dst, '{')
	//: stringValue (1).
	if v.StringValue != nil {
		//: a string, escaped.
		dst = AppendKey(dst, "stringValue")
		dst = AppendString(dst, *v.StringValue)
	}
	//: boolValue (2).
	if v.BoolValue != nil {
		//: a literal.
		dst = AppendKey(dst, "boolValue")
		dst = AppendBool(dst, *v.BoolValue)
	}
	//: intValue (3).
	if v.IntValue != nil {
		//: a decimal string.
		dst = AppendKey(dst, "intValue")
		dst = v.IntValue.AppendJSON(dst)
	}
	//: doubleValue (4).
	if v.DoubleValue != nil {
		//: a number or a name.
		dst = AppendKey(dst, "doubleValue")
		dst = v.DoubleValue.AppendJSON(dst)
	}
	//: close the message.
	return append(dst, '}')
}

// AppendJSON appends the attribute to dst as {"key":…,"value":{…}}.
func (kv KeyValue) AppendJSON(dst []byte) []byte {
	//: key (1) then value (2).
	dst = append(dst, '{')
	dst = AppendKey(dst, "key")
	dst = AppendString(dst, kv.Key)
	dst = AppendKey(dst, "value")
	dst = kv.Value.AppendJSON(dst)
	return append(dst, '}')
}

// AppendKeyValues appends attrs to dst as a JSON array — [] when it is empty,
// for a caller whose member is not omitted; an omitted member is the caller's
// to skip.
func AppendKeyValues(dst []byte, attrs []KeyValue) []byte {
	//: the opening bracket.
	dst = append(dst, '[')
	//: one message per attribute, comma-separated.
	for i := range attrs {
		//: every attribute after the first is separated from the previous one.
		if i > 0 {
			//: the separator.
			dst = append(dst, ',')
		}
		dst = attrs[i].AppendJSON(dst)
	}
	//: the closing bracket.
	return append(dst, ']')
}

// AppendJSON appends the resource to dst, its attributes omitted when there
// are none.
func (m ResourceMessage) AppendJSON(dst []byte) []byte {
	//: attributes (1), omitted when empty.
	dst = append(dst, '{')
	if len(m.Attributes) > 0 {
		//: the repeated KeyValue.
		dst = AppendKey(dst, "attributes")
		dst = AppendKeyValues(dst, m.Attributes)
	}
	//: close the message.
	return append(dst, '}')
}

// AppendJSON appends the scope to dst, each field omitted when it is empty.
func (m ScopeMessage) AppendJSON(dst []byte) []byte {
	//: name (1) and version (2), each omitted when empty.
	dst = append(dst, '{')
	if m.Name != "" {
		//: the scope's name.
		dst = AppendKey(dst, "name")
		dst = AppendString(dst, m.Name)
	}
	//: version (2).
	if m.Version != "" {
		//: the scope's version.
		dst = AppendKey(dst, "version")
		dst = AppendString(dst, m.Version)
	}
	//: close the message.
	return append(dst, '}')
}
