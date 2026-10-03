// Package jsonshape — a struct field's json tag, read with the grammar the
// json/v2 engine reads it with.
package jsonshape

import (
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// reservedInName are the characters a tag name stops at: the option
// separator, the backslash, and the three quotes.
const reservedInName string = ",\\'\"`"

// tagOptions is what a json tag says about a field.
type tagOptions struct {
	// name is the member name: the tag's, else the Go field's.
	name string
	// hasName reports that the name came from the tag.
	hasName bool
	// embed, omitEmpty, omitZero and quoted are the options of those names.
	embed, omitEmpty, omitZero, quoted bool
	// cased and formatted record a valid case: or format: option, which only
	// matter as options other than embed.
	cased, formatted bool
}

// hasOthers reports options besides embed, which make json/v2 treat an
// embedded field as a member when it is named and drop them when it is not.
func (o tagOptions) hasOthers() bool {
	//: any of them.
	return o.hasName || o.omitEmpty || o.omitZero || o.quoted || o.cased || o.formatted
}

// parseTag reads field's json tag. ignored reports a field encoding/json never
// writes: `json:"-"` exactly, or an unexported field that is not embedded.
//
// A name runs to the first reserved character; a name followed by something
// other than a comma keeps only its leading identifier, and one that does not
// start with a letter or an underscore is no name at all. An option is an
// identifier; case: and format: take a value; anything malformed is skipped
// to the next comma, as the engine skips it after recording its error.
func parseTag(field reflect.StructField) (opts tagOptions, ignored bool) {
	tag := field.Tag.Get("json")
	//: the one spelling that means "never", or a field reflection cannot read
	//: and that promotes nothing.
	if tag == "-" || (!field.IsExported() && !field.Anonymous) {
		return tagOptions{}, true
	}
	opts.name = field.Name
	//: a tag that starts with a name.
	if tag != "" && !strings.HasPrefix(tag, ",") {
		tag = readName(tag, &opts)
	}
	//: the options, one per comma.
	for tag != "" {
		tag = readOption(tag, &opts)
	}
	return opts, false
}

// readName reads the name at the start of tag into opts and returns the rest.
func readName(tag string, opts *tagOptions) string {
	length := len(tag) - len(strings.TrimLeftFunc(tag, func(r rune) bool { return !strings.ContainsRune(reservedInName, r) }))
	name, valid := tag[:length], true
	//: a reserved character inside the name: only an identifier survives.
	if !strings.HasPrefix(tag[length:], ",") && length != len(tag) {
		name, length, valid = consumeOption(tag, false)
	}
	//: invalid UTF-8 is replaced, as the engine replaces it.
	if !utf8.ValidString(name) {
		name = string([]rune(name))
	}
	//: a name only when it parsed.
	if valid {
		opts.name, opts.hasName = name, true
	}
	return tag[length:]
}

// readOption reads one option from tag — its comma first — into opts and
// returns the rest. Every call but one reading a second comma in a row
// consumes something, and that one leaves the comma for the next call.
func readOption(tag string, opts *tagOptions) string {
	//: the comma before the option.
	if tag[0] == ',' {
		tag = tag[1:]
		//: a trailing comma ends the tag.
		if tag == "" {
			return ""
		}
	}
	option, length, _ := consumeOption(tag, false)
	tag = tag[length:]
	//: the options that take a value, then the flags.
	switch option {
	//: case:ignore or case:strict.
	case "case":
		return readValue(tag, false, func(value string) { opts.cased = value == "ignore" || value == "strict" })
	//: format:<value>, possibly quoted.
	case "format":
		return readValue(tag, true, func(value string) { opts.formatted = value != "" })
	//: a flag, or an option this engine does not know.
	default:
		setFlag(option, opts)
		return tag
	}
}

// setFlag records one of the four options that take no value; any other
// option is one the engine ignores.
func setFlag(option string, opts *tagOptions) {
	//: the four flags; any other option is ignored.
	switch option {
	//: the embed option.
	case "embed":
		opts.embed = true
	//: the omitempty option.
	case "omitempty":
		opts.omitEmpty = true
	//: the omitzero option.
	case "omitzero":
		opts.omitZero = true
	//: the string option.
	case "string":
		opts.quoted = true
	}
}

// readValue reads the ":value" after case or format. The value is kept — the
// tag advanced past it — only when it parses; otherwise the tag is left where
// the value starts, and the next option read skips it.
func readValue(tag string, allowQuoted bool, keep func(string)) string {
	//: no value at all.
	if !strings.HasPrefix(tag, ":") {
		return tag
	}
	tag = tag[1:]
	value, length, valid := consumeOption(tag, allowQuoted)
	//: a malformed value is left for the next option read to skip.
	if !valid {
		return tag
	}
	keep(value)
	return tag[length:]
}

// consumeOption reads the option at the start of in: an identifier, or — when
// allowQuoted — a single-quoted string. Anything else is invalid, and
// consumed up to the next comma.
func consumeOption(in string, allowQuoted bool) (option string, length int, valid bool) {
	comma := strings.IndexByte(in, ',')
	//: no comma: the rest of the tag.
	if comma < 0 {
		comma = len(in)
	}
	first, _ := utf8.DecodeRuneInString(in)
	//: by the option's first rune.
	switch {
	//: an identifier.
	case first == '_' || unicode.IsLetter(first):
		length = len(in) - len(strings.TrimLeftFunc(in, isLetterOrDigit))
		return in[:length], length, true
	//: a single-quoted string, where one is allowed.
	case first == '\'' && allowQuoted:
		return consumeQuoted(in, comma)
	//: anything else, to the next comma.
	default:
		return in[:comma], comma, false
	}
}

// consumeQuoted reads a single-quoted string: a double-quoted Go string with
// single quotes as terminators, where \' is a quote and " is literal. An
// unterminated or unparsable one is consumed up to the next comma, invalid.
func consumeQuoted(in string, comma int) (option string, length int, valid bool) {
	var converted strings.Builder
	converted.WriteByte('"')
	escaped := false
	//: rune by rune after the opening quote; offset counts from it.
	for offset, r := range in[1:] {
		//: the rune's role in the quoted string.
		switch {
		//: the rune after a backslash keeps it — unless it is a single quote.
		case escaped:
			escaped = false
			//: \' is a plain quote; any other escape stays an escape.
			if r != '\'' {
				converted.WriteByte('\\')
			}
		//: a backslash, written once the next rune says whether it stays.
		case r == '\\':
			escaped = true
			continue
		//: a double quote must be escaped in the double-quoted form.
		case r == '"':
			converted.WriteByte('\\')
		//: the closing quote, one byte after offset.
		case r == '\'':
			converted.WriteByte('"')
			unquoted, err := strconv.Unquote(converted.String())
			//: an escape Go does not know.
			if err != nil {
				return in[:comma], comma, false
			}
			return unquoted, offset + 2, true
		}
		converted.WriteRune(r)
	}
	//: never closed.
	return in[:comma], comma, false
}

// isLetterOrDigit is the rune class of an identifier's tail.
func isLetterOrDigit(r rune) bool {
	//: letters, digits, underscores.
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}
