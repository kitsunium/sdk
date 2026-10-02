// Package toml — the decode: a parsed tree written into a Go value. Untyped
// targets (map[string]any, any, []any) are built natively; typed ones through
// reflection, guided by the cached typeInfo of each type.
package toml

import (
	"reflect"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The problems a decode refuses a target for.
const (
	// problemTarget: Unmarshal was not handed a non-nil pointer.
	problemTarget string = "the target is not a non-nil pointer"
	// problemMismatch: a value the target's type cannot hold.
	problemMismatch string = "the value does not fit the target's type"
	// problemOverflow: a number outside the target's range.
	problemOverflow string = "the number does not fit the target's type"
	// problemMapKey: a key the map's key type cannot hold.
	problemMapKey string = "the key does not fit the map's key type"
	// problemText: a value the target's UnmarshalText refused.
	problemText string = "the target's UnmarshalText refused the value"
	// problemEmbedded: a nil pointer to an unexported embedded struct.
	problemEmbedded string = "a nil pointer to an unexported embedded struct cannot be allocated"
	// problemNotThisKind: text that is not the date or time a local type
	// reads.
	problemNotThisKind string = "the text is not the date or time this type reads"
)

// The names a refusal gives the kinds of TOML value.
var kindNames = [...]string{
	kindTable:         "table",
	kindArrayOfTables: "array of tables",
	kindArray:         "array",
	kindString:        "string",
	kindInteger:       "integer",
	kindFloat:         "float",
	kindBool:          "boolean",
	kindDateTime:      "offset date-time",
	kindLocalDateTime: "local date-time",
	kindLocalDate:     "local date",
	kindLocalTime:     "local time",
}

// decoder writes one parsed tree into Go values.
type decoder struct {
	// p holds the tree.
	p *parser
}

// unmarshal parses data and decodes it into v.
func unmarshal(data []byte, v any) error {
	//: a document past the cap is refused before it is read.
	if len(data) > maxDocumentBytes {
		//: refused, naming the bound.
		return errs.Wrap(UnmarshalFailed, errs.WrapParams{},
			errs.String(fieldProblem, problemTooLarge), errs.Int(fieldLimit, maxDocumentBytes))
	}
	rv := reflect.ValueOf(v)
	//: the target must be somewhere to write.
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		//: refused, naming the type.
		return errs.Wrap(UnmarshalFailed, errs.WrapParams{},
			errs.String(fieldProblem, problemTarget), errs.String(fieldType, typeName(rv)))
	}
	p := parserPool.Get()
	defer parserPool.Put(p)
	//: the whole document is checked before anything is written.
	if err := p.parse(data); err != nil {
		//: refused, located.
		return err
	}
	d := decoder{p: p}
	//: the root table into the target.
	return d.root(v, rv.Elem())
}

// typeName names the type of v for a refusal.
func typeName(v reflect.Value) string {
	//: a nil interface has no type.
	if !v.IsValid() {
		//: nil.
		return "nil"
	}
	//: the type.
	return v.Type().String()
}

// root decodes the root table into the target, natively for the two untyped
// targets every config source and catalogue loader passes.
func (d *decoder) root(v any, target reflect.Value) error {
	switch m := v.(type) {
	//: map[string]any, the configuration decode.
	case *map[string]any:
		//: a nil map is created; an existing one is merged into.
		if *m == nil {
			*m = d.anyTable(rootNode)
		} else {
			d.mergeTable(rootNode, *m)
		}
		//: decoded.
		return nil
	//: a bare any.
	case *any:
		*m = d.anyInto(rootNode, *m)
		//: decoded.
		return nil
	//: a typed target.
	default:
		return d.value(rootNode, target)
	}
}

// anyInto returns the table n as an untyped value, merged into existing when
// existing is already a map[string]any.
func (d *decoder) anyInto(n int32, existing any) any {
	//: a map already there is merged into.
	if m, ok := existing.(map[string]any); ok && m != nil {
		d.mergeTable(n, m)
		//: the same map.
		return m
	}
	//: a fresh map.
	return d.anyTable(n)
}

// mergeTable writes the children of table t into m: a table into a table
// already there is merged, anything else replaces.
func (d *decoder) mergeTable(t int32, m map[string]any) {
	nodes := d.p.nodes
	//: each child.
	for c := nodes[t].first; c != noNode; c = nodes[c].next {
		key := d.keyString(c)
		//: a table meeting a map is merged into it.
		if nodes[c].kind == kindTable {
			m[key] = d.anyInto(c, m[key])
			continue
		}
		m[key] = d.anyValue(c)
	}
}

// keyString returns the key of node c as a string, never one aliasing the
// parser's buffers, which the next document reuses.
func (d *decoder) keyString(c int32) string {
	//: interned: a key decoded before is not allocated again.
	return d.p.internKey(d.p.keyBytes(&d.p.nodes[c]))
}

// anyTable returns table t as a map[string]any.
func (d *decoder) anyTable(t int32) map[string]any {
	nodes := d.p.nodes
	m := make(map[string]any, nodes[t].count)
	//: each child, in document order.
	for c := nodes[t].first; c != noNode; c = nodes[c].next {
		m[d.keyString(c)] = d.anyValue(c)
	}
	//: the table.
	return m
}

// anyArray returns array n, static or of tables, as a []any.
func (d *decoder) anyArray(n int32) []any {
	nodes := d.p.nodes
	out := make([]any, 0, nodes[n].count)
	//: each element, in order.
	for c := nodes[n].first; c != noNode; c = nodes[c].next {
		out = append(out, d.anyValue(c))
	}
	//: the array.
	return out
}

// anyValue returns node n as the untyped Go value a TOML value decodes to:
// map[string]any, []any, string, int64, float64, bool, time.Time,
// LocalDateTime, LocalDate or LocalTime.
func (d *decoder) anyValue(n int32) any {
	node := &d.p.nodes[n]
	switch node.kind {
	//: a table.
	case kindTable:
		return d.anyTable(n)
	//: an array of either kind.
	case kindArray, kindArrayOfTables:
		return d.anyArray(n)
	//: a string, copied out of the parser's buffers.
	case kindString:
		return string(d.p.textBytes(node))
	//: every other kind is a scalar.
	default:
		return d.scalarAny(node)
	}
}

// fail returns UNMARSHAL_FAILED for node n, naming problem, the node's dotted
// key and line, and the kind of value and the Go type when they are known.
func (d *decoder) fail(n int32, problem string, target reflect.Type) error {
	line, _ := position(d.p.data, int(d.p.nodes[n].at))
	fields := []errs.FieldValue{
		errs.String(fieldProblem, problem),
		errs.String(fieldKey, d.keyPath(n)),
		errs.Int(fieldLine, line),
		errs.String(fieldTOML, kindNames[d.p.nodes[n].kind]),
	}
	//: the Go type, when the refusal concerns one.
	if target != nil {
		fields = append(fields, errs.String(fieldType, target.String()))
	}
	//: the sentinel's code and messages, with the location as fields.
	return errs.Wrap(UnmarshalFailed, errs.WrapParams{}, fields...)
}

// keyPath returns the dotted key of node n, its array elements left out.
func (d *decoder) keyPath(n int32) string {
	var parts []string
	//: up to the root.
	for c := n; c != noNode && c != rootNode; c = d.p.nodes[c].parent {
		node := &d.p.nodes[c]
		//: an array element has no key of its own.
		if parentKind := d.p.nodes[node.parent].kind; parentKind == kindArray || parentKind == kindArrayOfTables {
			continue
		}
		parts = append(parts, d.quotedKey(c))
	}
	//: root first.
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	//: the dotted key.
	return strings.Join(parts, ".")
}

// quotedKey returns the key of node c as it would be written.
func (d *decoder) quotedKey(c int32) string {
	//: bare when it can be, quoted otherwise.
	return quoteKey(d.keyString(c))
}

// quoteKey returns key as it would be written: bare when it can be, quoted
// otherwise.
func quoteKey(key string) string {
	//: a bare key needs no quotes.
	if isBareKey(key) {
		//: as is.
		return key
	}
	//: quoted, with the escapes a basic string needs.
	return string(appendBasicString(nil, key))
}
