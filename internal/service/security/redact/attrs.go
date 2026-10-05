package redact

import (
	"fmt"
	"iter"
	"strconv"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	coreredact "github.com/kitsunium/sdk/internal/core/security/redact"
)

// floatFormat and floatBits render a float attribute the way strconv's
// shortest round-trip form does.
const (
	floatFormat byte = 'g'
	floatBits   int  = 64
	floatShort  int  = -1
	decimal     int  = 10
)

// attrs is Redactor.Attrs's body: decl_gen.go writes Redactor.Attrs, from the
// design, as one call of it.
func (r *Redactor) attrs(attrs []corelogger.AttrValue, maxBytes int) iter.Seq2[string, string] {
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
			if !yield(key, coreredact.Placeholder) {
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
		return coreredact.Unencodable
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
