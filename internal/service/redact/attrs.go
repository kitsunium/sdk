// Package redact — log attributes rendered as display text.
package redact

import (
	"fmt"
	"iter"
	"strconv"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
)

// Unencodable is the text an attribute holding a value encoding/json refuses
// is rendered as.
const Unencodable string = "[unencodable]"

// floatFormat and floatBits render a float attribute the way strconv's
// shortest round-trip form does.
const (
	floatFormat byte = 'g'
	floatBits   int  = 64
	floatShort  int  = -1
	decimal     int  = 10
)

// Attrs renders log attributes as display text, one (key, text) pair at a
// time, in order: a group is flattened into dotted keys ("db.user"), and each
// text is cut to maxBytes (raised to MinBytes).
//
//   - An attribute — or a group — whose DOTTED key the Redactor recognises is
//     yielded once, as Placeholder: a group named "session" is one redacted
//     pair, not one per member.
//   - A string has the credentials of its URLs replaced; a number, a bool, a
//     duration and a time (UTC, RFC 3339 with nanoseconds) are written as Go
//     writes them.
//   - Anything else — what logger.Any carried — is an error rendered by
//     Config.Error, a fmt.Stringer by its String, or otherwise the value's
//     JSON through Value, so its declared secrets are replaced too;
//     Unencodable when encoding/json refuses it.
//
// It is an iterator so the caller bounds the COUNT: a record carrying a group
// of ten thousand attributes costs the caller exactly as many pairs as it
// ranges over before it breaks.
func (r *Redactor) Attrs(attrs []corelogger.AttrValue, maxBytes int) iter.Seq2[string, string] {
	limit := bound(maxBytes)
	//: nothing is rendered until the caller ranges.
	return func(yield func(key, text string) bool) {
		r.walk(attrs, "", limit, yield)
	}
}

// walk yields every attribute under prefix, and reports false once the
// caller stopped ranging.
func (r *Redactor) walk(
	attrs []corelogger.AttrValue, prefix string, limit int, yield func(key, text string) bool,
) bool {
	//: in the order the record carries them.
	for index := range attrs {
		attr := &attrs[index]
		key := dotted(prefix, attr.Key)
		//: a secret's name hides whatever it holds, a whole group included.
		if key != "" && r.Name(key) {
			//: one redacted pair.
			if !yield(key, Placeholder) {
				//: the caller stopped.
				return false
			}
			continue
		}
		//: a group contributes its members, under its key.
		if attr.Value.Kind() == corelogger.KindGroup {
			//: flattened.
			if !r.walk(attr.Value.Group(), key, limit, yield) {
				//: the caller stopped.
				return false
			}
			continue
		}
		//: one rendered pair.
		if !yield(key, r.render(attr, limit)) {
			//: the caller stopped.
			return false
		}
	}
	//: all of them.
	return true
}

// dotted joins a group's key and a member's, skipping an empty one: an
// unnamed group inlines its members, as log/slog does.
func dotted(prefix, key string) string {
	switch {
	//: no enclosing group.
	case prefix == "":
		//: the key alone.
		return key
	//: an unnamed member of a named group.
	case key == "":
		//: the group's key.
		return prefix
	//: the ordinary case.
	default:
		//: joined.
		return prefix + "." + key
	}
}

// render returns one non-group attribute's value as display text, cut to
// limit like every other text Attrs hands out: a timestamp or a long duration
// is as much a text as a string is.
func (r *Redactor) render(attr *corelogger.AttrValue, limit int) string {
	//: formatted, then scrubbed and cut by the one function that bounds text.
	return r.Text(r.format(attr, limit), limit)
}

// format returns one non-group attribute's value as text, uncut.
func (r *Redactor) format(attr *corelogger.AttrValue, limit int) string {
	value := attr.Value
	switch value.Kind() {
	case corelogger.KindString:
		//: the string itself: render scrubs and cuts it.
		return value.String()
	case corelogger.KindInt64:
		//: base ten.
		return strconv.FormatInt(value.Int64(), decimal)
	case corelogger.KindUint64:
		//: base ten.
		return strconv.FormatUint(value.Uint64(), decimal)
	case corelogger.KindFloat64:
		//: the shortest form that reads back.
		return strconv.FormatFloat(value.Float64(), floatFormat, floatShort, floatBits)
	case corelogger.KindBool:
		//: true or false.
		return strconv.FormatBool(value.Bool())
	case corelogger.KindDuration:
		//: as Go spells it.
		return value.Duration().String()
	case corelogger.KindTime:
		//: one zone for every record.
		return value.Time().UTC().Format(time.RFC3339Nano)
	default:
		//: whatever logger.Any carried.
		return r.renderAny(attr, limit)
	}
}

// renderAny renders the opaque value logger.Any put in an attribute.
func (r *Redactor) renderAny(attr *corelogger.AttrValue, limit int) string {
	opaque := attr.Value.Any()
	switch typed := opaque.(type) {
	case nil:
		//: as JSON spells it.
		return "null"
	case string:
		//: like any string attribute.
		return r.Text(typed, limit)
	case error:
		//: as the caller chose to show errors, then scrubbed and cut.
		return r.Text(r.errorText(typed), limit)
	case fmt.Stringer:
		//: the type's own rendering, scrubbed and cut.
		return r.Text(typed.String(), limit)
	}
	redacted, err := r.Value(opaque, limit)
	//: encoding/json refused it.
	if err != nil {
		//: the marker, never the value.
		return Unencodable
	}
	//: its JSON, with its declared secrets replaced, within the bound.
	return string(redacted.JSON)
}

// errorText renders an error with the caller's renderer, or its own text.
func (r *Redactor) errorText(err error) string {
	//: the caller's choice, when it made one.
	if r.error != nil {
		//: e.g. only the wire-safe half.
		return r.error(err)
	}
	//: the error's own text; Text scrubs and cuts it.
	return err.Error()
}
