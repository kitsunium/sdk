// Package logger: kind.go declares the Kind enum that discriminates the union
// payload carried by Value. Handlers switch on Kind to select the matching
// accessor (String, Int64, Float64, …) instead of paying the cost of an
// `any` type assertion at every render call.
package logger

// KindAny tags a Value whose payload is an unconstrained interface; handlers
// SHOULD render it via fmt.Sprintf or a "?" placeholder when the concrete
// type is not in their format table. It is also the zero-value of Kind.
const KindAny Kind = 0

// KindBool tags a Value carrying a boolean payload.
const KindBool Kind = 1

// KindDuration tags a Value carrying a time.Duration payload.
const KindDuration Kind = 2

// KindFloat64 tags a Value carrying a float64 payload.
const KindFloat64 Kind = 3

// KindInt64 tags a Value carrying an int64 payload (also covers int / int32).
const KindInt64 Kind = 4

// KindString tags a Value carrying a string payload.
const KindString Kind = 5

// KindTime tags a Value carrying a time.Time payload.
const KindTime Kind = 6

// KindUint64 tags a Value carrying an unsigned 64-bit integer payload.
const KindUint64 Kind = 7

// KindGroup tags a Value whose payload is a slice of AttrValue, used to model
// nested groups (slog.Group) without flattening into the parent record.
const KindGroup Kind = 8

// unknownKindLabel is the sentinel returned when Kind is outside the table —
// e.g. a future variant a stale handler does not yet recognise.
const unknownKindLabel string = "unknown"

// kindLabels is the lookup table indexed by Kind; lives here so String stays
// O(1) and below the cyclomatic-complexity ceiling.
var kindLabels = [...]string{
	KindAny:      "any",
	KindBool:     "bool",
	KindDuration: "duration",
	KindFloat64:  "float64",
	KindInt64:    "int64",
	KindString:   "string",
	KindTime:     "time",
	KindUint64:   "uint64",
	KindGroup:    "group",
}

// Kind discriminates the payload variant carried by a Value. Kind values are
// stable across the public API; new variants are appended at the end so old
// handlers default to KindAny rather than panic on an unknown discriminant.
type Kind int8

// String returns the lowercase textual label associated with the Kind
// receiver, suitable for diagnostic dumps and golden test fixtures.
//
// Returns:
//   - string: one of "any", "bool", "duration", "float64", "int64",
//     "string", "time", "uint64", "group" — or "unknown" for future variants.
func (k Kind) String() string {
	//: bounds-check before indexing so unknown variants degrade gracefully.
	if k < 0 || int(k) >= len(kindLabels) {
		//: stable label that signals "discriminant added after this build".
		return unknownKindLabel
	}
	//: O(1) lookup keeps the function under the cyclomatic ceiling.
	return kindLabels[k]
}
