// Package metrics — the typed attribute that gives a series its identity.
package metrics

import (
	"cmp"
	"encoding/binary"
	"math"
	"slices"
	"strconv"
	"strings"
)

// OverflowAttrKey names the reserved attribute the meter attaches to the single
// aggregated series it folds observations into once an instrument has reached
// its cardinality bound. It is spelled with underscores rather than dots so it
// needs no mangling to be a legal Prometheus label name — unlike every
// OTel-conventional key, which is dotted.
//
// The key is RESERVED: a caller who passes it explicitly with a BOOL value is
// writing into the same series the meter uses for overflow, and their
// observations merge with it. That is not enforced — enforcing it would cost a
// comparison per attribute on the lookup path to prevent a collision nobody
// reaches by accident.
const OverflowAttrKey string = "sdk_metric_overflow"

// The two identity bytes a bool attribute encodes to, plus the strconv
// parameters the canonical text rendering uses.
const (
	boolTrue    byte = 1
	boolFalse   byte = 0
	decimalBase int  = 10
	floatFmt    byte = 'g'
	floatPrec   int  = -1
	floatBits   int  = 64
)

// AttrKind discriminates the type of an AttrValue's value. It is the OpenTelemetry
// attribute model: a value is a string, a bool, a signed 64-bit integer or a
// double — never a string that happens to spell one of the others.
type AttrKind uint8

const (
	// AttrKindInvalid is the zero value, and it names no type. An AttrValue
	// carrying it was built by a struct literal rather than by a
	// constructor, so its value was never set; the meter refuses it rather
	// than inventing an empty string for it (ADR 0031).
	AttrKindInvalid AttrKind = iota
	// AttrKindString marks a string-valued attribute.
	AttrKindString
	// AttrKindBool marks a bool-valued attribute.
	AttrKindBool
	// AttrKindInt64 marks a signed 64-bit integer attribute.
	AttrKindInt64
	// AttrKindFloat64 marks an IEEE-754 double attribute.
	AttrKindFloat64
)

// AttrValue is one dimension of a series: a Key naming the dimension and a TYPED
// value observed for it.
//
// The Key is STRUCTURE and the value is DATA. A key is written at the call site
// and is constant for the lifetime of the process ("http.request.method"); a
// value comes from whatever the process is measuring and varies per observation
// ("GET", 503, true). That asymmetry is why an unusable key is a programmer
// error the meter panics on, while an unbounded stream of values is a runtime
// condition the meter absorbs into an overflow series.
//
// The value is unexported and reachable only through Kind and the four typed
// accessors, so the only way to build a usable AttrValue is one of the four
// constructors — String, Bool, Int64, Float64. A struct literal can still set
// Key, and the resulting AttrKindInvalid is refused rather than silently read
// as an empty string.
type AttrValue struct {
	// Key names the dimension. It must be non-empty and appear at most once
	// in an attribute set; both are checked on every instrument fetch.
	Key string
	// kind discriminates which of num/str carries the value.
	kind AttrKind
	// num carries a bool, an int64 or the IEEE-754 bits of a float64.
	num uint64
	// str carries a string value, and only a string value.
	str string
}

// String returns a string-valued AttrValue. It is the shortest of the four to write
// because a string dimension is the overwhelmingly common one.
func String(key, value string) AttrValue {
	//: the string rides in its own field; num stays zero.
	return AttrValue{Key: key, kind: AttrKindString, str: value}
}

// Bool returns a bool-valued AttrValue.
func Bool(key string, value bool) AttrValue {
	//: false is the zero of num, so only true needs writing.
	if value {
		//: one is the canonical true.
		return AttrValue{Key: key, kind: AttrKindBool, num: 1}
	}
	//: zero is the canonical false.
	return AttrValue{Key: key, kind: AttrKindBool}
}

// Int64 returns a signed-integer AttrValue.
func Int64(key string, value int64) AttrValue {
	//: two's-complement round-trip through uint64 is exact.
	return AttrValue{Key: key, kind: AttrKindInt64, num: uint64(value)}
}

// Float64 returns a double AttrValue.
//
// A float is a poor dimension — it is a measurement, not a category — and the
// identity encoding keys it on its IEEE-754 BIT PATTERN, so -0.0 and +0.0 name
// two different series and two NaNs with different payloads name two more. The
// kind exists because the OTel attribute model has it and an OTLP payload can
// carry it, not because a metric wants it.
func Float64(key string, value float64) AttrValue {
	//: the bit pattern is what makes the encoding injective.
	return AttrValue{Key: key, kind: AttrKindFloat64, num: math.Float64bits(value)}
}

// Kind reports which type the attribute's value has.
func (a AttrValue) Kind() AttrKind {
	//: the discriminant every reader switches on.
	return a.kind
}

// Str returns the string value, and the empty string for every other kind.
func (a AttrValue) Str() string {
	//: only AttrKindString ever populates str.
	return a.str
}

// Bool returns the bool value, and false for every other kind.
func (a AttrValue) Bool() bool {
	//: a non-zero num on a bool attribute is true.
	return a.kind == AttrKindBool && a.num != 0
}

// Int64 returns the integer value, and zero for every other kind.
func (a AttrValue) Int64() int64 {
	//: a foreign kind must not leak a float's bit pattern as an integer.
	if a.kind != AttrKindInt64 {
		//: documented zero.
		return 0
	}
	//: two's-complement round-trip back.
	return int64(a.num)
}

// Float64 returns the double value, and zero for every other kind.
func (a AttrValue) Float64() float64 {
	//: a foreign kind must not leak an integer as a denormal float.
	if a.kind != AttrKindFloat64 {
		//: documented zero.
		return 0
	}
	//: decode the stored bit pattern.
	return math.Float64frombits(a.num)
}

// AppendIdentity appends an INJECTIVE binary encoding of the attribute's value
// — its kind tag followed by fixed-width or length-prefixed bytes — to dst.
//
// The kind tag is what keeps String("v", "1") and Int64("v", 1) apart: without
// it the two would encode identically and two series carrying different
// dimensions would silently accumulate into one. It lives here rather than in
// the meter so that every Meter implementation inherits the same injectivity
// instead of re-deriving it.
func (a AttrValue) AppendIdentity(dst []byte) []byte {
	//: the tag opens the value and discriminates every case below.
	dst = append(dst, byte(a.kind))
	//: one encoding per kind; each is self-delimiting.
	switch a.kind {
	//: a string is the only variable-length case, so it carries a length.
	case AttrKindString:
		//: length prefix, then the raw bytes it bounds.
		dst = binary.AppendUvarint(dst, uint64(len(a.str)))
		//: no escaping is needed — the length already delimits them.
		return append(dst, a.str...)
	//: a bool is one byte, which is already fixed width.
	case AttrKindBool:
		//: canonical one-or-zero, never the raw num.
		if a.num != 0 {
			//: true.
			return append(dst, boolTrue)
		}
		//: false.
		return append(dst, boolFalse)
	//: an int64 and a float64 are both eight fixed bytes of num.
	case AttrKindInt64, AttrKindFloat64:
		//: big-endian so the bytes read the same on every architecture, and
		//: FIXED WIDTH so it needs no length prefix of its own.
		return binary.BigEndian.AppendUint64(dst, a.num)
	//: an invalid attribute never reaches here — the meter refuses it first.
	default:
		//: the tag alone, so a hand-built value still encodes injectively.
		return dst
	}
}

// AppendText appends the canonical TEXT rendering of the attribute's value.
//
// Only AttrKindString can produce a byte an exposition format would have to
// escape: a bool renders as "true"/"false", and an integer or a double renders
// through strconv, so all three are drawn from [0-9a-zA-Z+-.] alone. An
// exporter can therefore escape the string case and append the other three
// verbatim.
func (a AttrValue) AppendText(dst []byte) []byte {
	//: one rendering per kind.
	switch a.kind {
	//: a string is its own text.
	case AttrKindString:
		//: verbatim — the caller escapes it if its format needs that.
		return append(dst, a.str...)
	//: a bool renders as the two words every wire format spells it with.
	case AttrKindBool:
		//: strconv agrees with JSON, OTLP and the Prometheus convention.
		return strconv.AppendBool(dst, a.num != 0)
	//: an integer renders in base ten.
	case AttrKindInt64:
		//: two's-complement round-trip back before formatting.
		return strconv.AppendInt(dst, int64(a.num), decimalBase)
	//: a double renders shortest-round-trip, NaN and ±Inf named.
	case AttrKindFloat64:
		//: the same spelling the Prometheus exposition format asks for.
		return strconv.AppendFloat(dst, math.Float64frombits(a.num), floatFmt, floatPrec, floatBits)
	//: an invalid attribute has no value to render.
	default:
		//: nothing, rather than a forged empty string.
		return dst
	}
}

// CompareAttrKey orders two attributes by Key alone. A package-level function
// value, not a closure, so passing it to slices.SortFunc allocates nothing.
func CompareAttrKey(a, b AttrValue) int {
	//: Key alone decides the order; duplicates are refused, not tie-broken.
	return strings.Compare(a.Key, b.Key)
}

// CompareAttrValue orders two attribute VALUES totally: kind first, then the
// value within the kind. It never allocates.
//
// The ordering distinguishes exactly the pairs AppendIdentity distinguishes,
// which is what a Collect-time sort needs: if two values that name two
// different series compared equal, their relative order would depend on the map
// iteration that produced them and a snapshot would stop rendering identically
// twice in a row. That is why a double falls back to its BIT PATTERN when the
// numeric comparison ties — +0.0 and -0.0, and two NaNs with different
// payloads, are distinct series and must be distinct here too.
func CompareAttrValue(a, b AttrValue) int {
	//: a value of one kind is never a value of another.
	if a.kind != b.kind {
		//: the tag ordering, which matches AppendIdentity's leading byte.
		return cmp.Compare(a.kind, b.kind)
	}
	//: within a kind, compare the payload that kind actually uses.
	switch a.kind {
	//: a string orders lexicographically.
	case AttrKindString:
		//: str is the only populated field.
		return strings.Compare(a.str, b.str)
	//: a double orders numerically, then by bits to break every tie.
	case AttrKindFloat64:
		//: the ordering a reader expects, NaN sorting below everything.
		if c := cmp.Compare(math.Float64frombits(a.num), math.Float64frombits(b.num)); c != 0 {
			//: ordered numerically.
			return c
		}
		//: ±0 and two NaN payloads tie above and are separated here.
		return cmp.Compare(a.num, b.num)
	//: an integer orders as a signed integer, not as its two's-complement bits.
	case AttrKindInt64:
		//: decode both sides before comparing.
		return cmp.Compare(int64(a.num), int64(b.num))
	//: a bool (and the invalid kind, which never reaches a snapshot) orders
	//: on num, so false precedes true.
	default:
		//: raw payload comparison.
		return cmp.Compare(a.num, b.num)
	}
}

// ValidateAttrs panics when sorted cannot name a series: an empty Key, a value
// no constructor ever set, or the same Key twice.
//
// It runs on the ALREADY SORTED set so duplicates are adjacent and the check
// costs one comparison per attribute. Panicking is the same call the meter
// makes for a cross-kind name reuse, and it is safe for the same reason: an
// attribute key and an attribute KIND are both structure, written at the call
// site, so they are wrong on the first call or never. The alternative is a
// series no exporter can emit — Prometheus and OTLP both reject an empty
// attribute name — failing far away, inside the component the SDK told the
// caller to stop thinking about.
func ValidateAttrs(sorted []AttrValue) {
	//: walk once; the set is sorted, so a duplicate sits next to its twin.
	for i, attr := range sorted {
		//: an empty key names no dimension.
		if attr.Key == "" {
			//: fail at the call site that wrote it.
			panic(InvalidAttribute.Error())
		}
		//: a value no constructor set is not a value.
		if attr.kind == AttrKindInvalid {
			//: same refusal — the set is unusable either way.
			panic(InvalidAttribute.Error())
		}
		//: the same key twice means the set is not a set.
		if i > 0 && sorted[i-1].Key == attr.Key {
			//: same refusal.
			panic(InvalidAttribute.Error())
		}
	}
}

// SortAttrs returns a sorted, validated, owned copy of attrs — the cold-path
// form of what the meter does with a stack buffer on every fetch. Resource and
// Scope use it once at construction, never per observation.
func SortAttrs(attrs []AttrValue) []AttrValue {
	//: an empty set stays nil, which is what "no dimensions" is spelled as.
	if len(attrs) == 0 {
		//: nothing to own.
		return nil
	}
	//: own the backing array before ordering it — the caller keeps theirs.
	sorted := slices.Clone(attrs)
	slices.SortFunc(sorted, CompareAttrKey)
	//: refuse an unusable set here rather than at the first export.
	ValidateAttrs(sorted)
	//: hand back the canonical set.
	return sorted
}
