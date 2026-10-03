// Package kit — the decoding of a request into its typed value.
package kit

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/data/codec/strictjson"
)

// DefaultMaxBody is the largest request body an endpoint reads, unless the
// endpoint says otherwise with [MaxBody].
const DefaultMaxBody int64 = 1 << 20

var (
	// The types a request field binds from text by what they mean rather than
	// by their kind: an instant, a duration.
	timeType = reflect.TypeFor[time.Time]()
	// durationType is time.Duration, read from text as Go writes it.
	durationType = reflect.TypeFor[time.Duration]()

	// bodyRefusals are what kit says of each way a request body is refused, in
	// its words: never the body itself.
	bodyRefusals = []struct {
		sentinel error
		said     func(err error) *Error
	}{
		{strictjson.DocumentEmpty, func(error) *Error { return Invalid("the request body is empty") }},
		{strictjson.DocumentTooLarge, func(error) *Error {
			return NewError(http.StatusRequestEntityTooLarge, WireTooLarge, "the request body is too large")
		}},
		{strictjson.MediaTypeUnsupported, func(error) *Error {
			return NewError(http.StatusUnsupportedMediaType, WireUnsupported, "the request body must be application/json")
		}},
		{strictjson.MemberUnknown, func(err error) *Error {
			return Invalid("the request body has a member this endpoint does not accept, at " + pointerIn(err))
		}},
		{strictjson.ValueMismatched, func(err error) *Error {
			return Invalid("the request body has a value of the wrong type at " + pointerIn(err))
		}},
		{strictjson.DocumentUnreadable, func(error) *Error { return Invalid("the request body could not be read") }},
		{strictjson.DocumentMalformed, func(error) *Error { return Invalid("the request body is not valid JSON") }},
	}

	// scalarSetters set a field of each basic kind to what a text writes.
	scalarSetters = func() map[reflect.Kind]func(field reflect.Value, text string) error {
		setInt := func(field reflect.Value, text string) error {
			n, err := strconv.ParseInt(text, 10, field.Type().Bits())
			if err == nil {
				field.SetInt(n)
			}
			return err
		}
		setUint := func(field reflect.Value, text string) error {
			n, err := strconv.ParseUint(text, 10, field.Type().Bits())
			if err == nil {
				field.SetUint(n)
			}
			return err
		}
		setFloat := func(field reflect.Value, text string) error {
			f, err := strconv.ParseFloat(text, field.Type().Bits())
			if err == nil {
				field.SetFloat(f)
			}
			return err
		}
		return map[reflect.Kind]func(field reflect.Value, text string) error{
			reflect.String: func(field reflect.Value, text string) error {
				field.SetString(text)
				return nil
			},
			reflect.Bool: func(field reflect.Value, text string) error {
				b, err := strconv.ParseBool(text)
				if err == nil {
					field.SetBool(b)
				}
				return err
			},
			reflect.Int: setInt, reflect.Int8: setInt, reflect.Int16: setInt, reflect.Int32: setInt, reflect.Int64: setInt,
			reflect.Uint: setUint, reflect.Uint8: setUint, reflect.Uint16: setUint, reflect.Uint32: setUint, reflect.Uint64: setUint,
			reflect.Float32: setFloat, reflect.Float64: setFloat,
		}
	}()
)

// param binds one request field to a path wildcard, a query parameter, a
// header or a cookie.
type param struct {
	index  []int
	name   string
	source string
	typ    reflect.Type
}

// decoder knows how to build a request value from an HTTP request. It is
// computed once, when the endpoint is declared, so every shape problem is a
// startup error rather than a runtime surprise.
type decoder struct {
	typ     reflect.Type
	params  []param
	hasBody bool
}

// newDecoder plans the decoding of t for a route whose path has the given
// wildcards. It returns every problem it finds, not just the first.
func newDecoder(t reflect.Type, wildcards []string) (*decoder, []phrase) {
	dec := &decoder{typ: t}
	if t.Kind() != reflect.Struct {
		dec.hasBody = true
		if len(wildcards) > 0 {
			return dec, []phrase{say("decode.not-struct", "type", t, "wildcards", wildcards)}
		}
		return dec, nil
	}
	var problems []phrase
	bound := map[string]bool{}
	for _, f := range reflect.VisibleFields(t) {
		if problem := dec.field(t, &f, wildcards, bound); !problem.empty() {
			problems = append(problems, problem)
		}
	}
	for _, w := range wildcards {
		if !bound[w] {
			problems = append(problems, say("decode.unbound-wildcard", "name", w))
		}
	}
	return dec, problems
}

// field plans the decoding of the field f of t: a parameter from where its
// tag says, bound[name] when it binds a wildcard, or a part of the body. It
// returns what is wrong with it, empty when nothing is.
func (d *decoder) field(t reflect.Type, f *reflect.StructField, wildcards []string, bound map[string]bool) phrase {
	if embedsStruct(f) {
		return phrase{} // its fields are visited, promoted
	}
	in, name := paramTag(*f)
	if in == "" {
		d.hasBody = d.hasBody || (f.IsExported() && f.Tag.Get("json") != "-")
		return phrase{}
	}
	if problem := paramProblem(t, f, in, name, wildcards); !problem.empty() {
		return problem
	}
	bound[name] = bound[name] || in == "path"
	d.params = append(d.params, param{index: f.Index, name: name, source: in, typ: f.Type})
	return phrase{}
}

// paramProblem is what is wrong with the field f of t, read from in under
// name; empty when kit can bind it.
func paramProblem(t reflect.Type, f *reflect.StructField, in, name string, wildcards []string) phrase {
	switch {
	case throughPointer(t, f.Index):
		return say("decode.pointer", "field", f.Name, "in", in, "name", name)
	case !f.IsExported():
		return say("decode.unexported", "field", f.Name, "in", in, "name", name)
	case !paramKind(f.Type):
		return say("decode.kind", "field", f.Name, "type", f.Type, "in", in)
	case in == "path" && !slices.Contains(wildcards, name):
		return say("decode.unknown-wildcard", "field", f.Name, "name", name)
	default:
		return phrase{}
	}
}

// throughPointer reports whether the field at index is reached through an
// embedded pointer, which a request value leaves nil.
func throughPointer(t reflect.Type, index []int) bool {
	for _, i := range index[:len(index)-1] {
		f := t.Field(i)
		if f.Type.Kind() == reflect.Pointer {
			return true
		}
		t = f.Type
	}
	return false
}

// paramKind reports whether a field of type t can be parsed from text.
func paramKind(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == timeType || t == durationType {
		return true
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Slice:
		return t.Elem().Kind() == reflect.String
	default:
		// Every other kind is not read from text.
	}
	return false
}

// decode builds the request value. The body is decoded first, strictly —
// unknown members, duplicate names and trailing data are refused — and then
// every path, query, header and cookie field is set from its own source, or
// zeroed when the source is absent. A body therefore never decides a field
// the route owns: PUT /todos/A with {"ID":"B"} still addresses A.
func (d *decoder) decode[T any](w http.ResponseWriter, r *http.Request, maxBody int64, out *T) error {
	v := reflect.ValueOf(out).Elem()
	if d.hasBody && r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		if err := d.decodeBody(w, r, maxBody, out); err != nil {
			return err
		}
	}
	return d.setParams(r, v)
}

// setParams sets every path, query, header and cookie field of v from its
// source, zeroing the ones whose source is absent. A cookie field is always
// optional: a request without the cookie leaves it zero.
func (d *decoder) setParams(r *http.Request, v reflect.Value) error {
	for _, p := range d.params {
		field := v.FieldByIndex(p.index)
		raw := p.values(r)
		field.SetZero()
		if len(raw) == 0 {
			continue
		}
		if err := setText(field, raw); err != nil {
			return Invalid(fmt.Sprintf("%s parameter %q is not a valid %s", p.source, p.name, describeKind(p.typ)))
		}
	}
	return nil
}

// values returns what the request carries for the parameter, in order.
func (p *param) values(r *http.Request) []string {
	switch p.source {
	case "path":
		if s := r.PathValue(p.name); s != "" {
			return []string{s}
		}
	case "query":
		return r.URL.Query()[p.name]
	case "header":
		return r.Header.Values(p.name)
	case "cookie":
		var out []string
		for _, c := range r.CookiesNamed(p.name) {
			out = append(out, c.Value)
		}
		return out
	}
	return nil
}

// present reports whether the request carries at least one of the
// decoder's parameters.
func (d *decoder) present(r *http.Request) bool {
	for _, p := range d.params {
		if len(p.values(r)) > 0 {
			return true
		}
	}
	return false
}

// decodeBody decodes r's JSON body into out, at most maxBody bytes.
func (d *decoder) decodeBody[T any](w http.ResponseWriter, r *http.Request, maxBody int64, out *T) error {
	return readJSON(w, r, out, maxBody, true)
}

// readJSON decodes the JSON body of r into v with the SDK's strict decoder —
// a JSON media type, at most limit bytes, exactly one value, no member v does
// not declare, no duplicate name — and says what it refused in kit's words,
// never quoting the body. An empty body leaves v as it is when optional.
func readJSON[T any](w http.ResponseWriter, r *http.Request, v *T, limit int64, optional bool) error {
	err := strictjson.DecodeRequest(w, r, v, limit)
	if err == nil || (optional && errors.Is(err, strictjson.DocumentEmpty)) {
		return nil
	}
	for _, refusal := range bodyRefusals {
		if errors.Is(err, refusal.sentinel) {
			return refusal.said(err).Wrap(err)
		}
	}
	return err
}

// pointerIn is where a refused body went wrong, as a JSON Pointer built from
// the body's own member names: a location, never a value.
func pointerIn(err error) string {
	if at, _ := strictjson.PointerOf(err); at != "" {
		return clip(at)
	}
	return "/"
}

// setText parses raw into field, honouring the field's bit size.
func setText(field reflect.Value, raw []string) error {
	switch field.Kind() {
	case reflect.Pointer:
		p := reflect.New(field.Type().Elem())
		if err := setText(p.Elem(), raw); err != nil {
			return err
		}
		field.Set(p)
		return nil
	case reflect.Slice:
		s := reflect.MakeSlice(field.Type(), len(raw), len(raw))
		for i, r := range raw {
			s.Index(i).SetString(r)
		}
		field.Set(s)
		return nil
	default:
		return setOne(field, raw[0])
	}
}

// setOne sets field, a single value, to what text writes: an instant, a
// duration, or a basic kind.
func setOne(field reflect.Value, text string) error {
	switch field.Type() {
	case timeType:
		t, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return err
		}
		field.Set(reflect.ValueOf(t))
		return nil
	case durationType:
		d, err := time.ParseDuration(text)
		if err != nil {
			return err
		}
		field.SetInt(int64(d))
		return nil
	default:
		return setScalar(field, text)
	}
}

// setScalar sets field, of a basic kind, to what text writes.
func setScalar(field reflect.Value, text string) error {
	set, ok := scalarSetters[field.Kind()]
	if !ok {
		return errors.ErrUnsupported
	}
	return set(field, text)
}

// describeKind is how a request field of type t is written, for an error that
// refuses one.
func describeKind(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t {
	case timeType:
		return "RFC 3339 time"
	case durationType:
		return "duration"
	}
	switch t.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return t.Kind().String()
	case reflect.Float32, reflect.Float64:
		return "number"
	default:
		// Every other kind is not read from text.
	}
	return "value"
}
