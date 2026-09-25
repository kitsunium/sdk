// Package secret — the Value: a secret that every rendering writes as a
// placeholder, and that only an explicit Reveal hands back.
package secret

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// Redacted is the placeholder every rendering of a [Value] writes, whatever
// the secret holds — including nothing. One constant for every value is the
// point: a rendering that varied with the secret, even only in its length,
// would be a channel the secret leaks through.
const Redacted string = "<redacted>"

// jsonNull is the token encoding/json hands an Unmarshaler for an explicit
// null, which by the package's own convention leaves the target untouched.
const jsonNull string = "null"

// jsonQuote opens and closes a JSON string token.
const jsonQuote byte = '"'

// Value is an immutable secret: a password, a token, a connection string, key
// material. It exists so that a secret can travel through a program — a
// configuration struct, a log line, an error, a JSON dump — without being
// written down on the way.
//
// Immutable means the BYTES: nothing reachable from a Value changes them, so
// copies share them safely. The two decoders assign a new Value to the
// variable they are given — exactly as time.Time's UnmarshalJSON assigns a new
// Time — which replaces what the variable holds and changes no bytes any other
// copy holds; that is what lets a configuration loader fill a Value field.
//
//   - EVERY rendering writes [Redacted]: String, GoString, Format (so %v, %+v,
//     %#v, %s and %q alike), MarshalJSON, MarshalText. A configuration dumped
//     for a --show-config flag, a struct printed while debugging and a value
//     logged by a handler that did not know what it held all print the same
//     ten characters.
//   - [Value.Reveal] and [Value.RevealString] are the only ways out, and they
//     are spelled to read like a decision at the call site that makes it.
//   - [Value.Equal] compares in constant time, and == does not compile: the
//     struct carries a zero-size field of a non-comparable type, so neither
//     the byte comparison that leaks a shared prefix's length nor a pointer
//     comparison that answers "same allocation" can be written by accident.
//     A Value cannot be a map key for the same reason.
//   - It DECODES from a JSON string and from text, so a configuration loader
//     fills a Value field from a file or from the environment exactly as it
//     fills a string field. It refuses anything that is not a string, and the
//     placeholder itself — see [ValueRefused].
//
// The bytes sit behind a pointer rather than in the struct, and that is a
// defence, not a detail. fmt cannot call Format on a Value held in an
// UNEXPORTED field of another struct — reflection will not hand it the method
// — so it prints that field's own fields instead. Holding a slice there would
// print the secret as a list of byte values; holding a pointer prints an
// address. A Value in an exported field, a slice, a map or a variable is
// formatted through its methods and prints the placeholder.
//
// The zero Value is the empty secret: [Value.IsZero] reports it, it reveals
// nil, and a store refuses to Put it.
type Value struct {
	// _ makes the struct non-comparable at zero cost, so == and map keys are
	// compile errors rather than an identity comparison that looks like an
	// equality one.
	_ [0]func()
	// held points at the secret's bytes; nil for the zero Value. It is never
	// mutated after construction, so copies of a Value share it safely.
	held *heldBytes
}

// heldBytes is the one allocation a Value owns. It is a separate type only so
// that the field a Value carries is a pointer — see the Value doc comment.
type heldBytes struct {
	// raw is the secret, never empty: the empty secret is a nil held pointer.
	raw []byte
}

// NewValue returns a Value holding a copy of raw. The caller's slice is not
// the Value's storage, so clearing it afterwards — as anyone handling key
// material should — does not change the Value. An empty raw yields the zero
// Value.
func NewValue(raw []byte) Value {
	//: an empty secret is the zero Value, never a pointer to nothing.
	if len(raw) == 0 {
		//: IsZero reports it.
		return Value{}
	}
	//: a defensive copy, so the caller's buffer is not the secret's storage.
	return Value{held: &heldBytes{raw: bytes.Clone(raw)}}
}

// FromString returns a Value holding text. An empty text yields the zero
// Value.
func FromString(text string) Value {
	//: the conversion copies; a string is immutable anyway.
	return NewValue([]byte(text))
}

// Reveal returns a fresh copy of the secret's bytes — the only way out besides
// [Value.RevealString]. The copy is the caller's: mutating it cannot reach the
// Value. The zero Value reveals nil.
//
// Hand the result to the API that consumes the secret; never to a logger, a
// metric label, a URL or an error message.
func (v Value) Reveal() []byte {
	//: the zero Value has nothing to reveal.
	if v.held == nil {
		//: nil, not an empty slice, so a caller can tell "unset" apart.
		return nil
	}
	//: a copy, so the secret stays immutable whatever the caller does.
	return bytes.Clone(v.held.raw)
}

// RevealString returns the secret as a string. The same warning as
// [Value.Reveal] applies, with one more: a Go string cannot be cleared, so a
// revealed string lives until the collector reclaims it.
func (v Value) RevealString() string {
	//: the zero Value reveals the empty string.
	if v.held == nil {
		//: nothing held.
		return ""
	}
	//: the conversion copies the bytes.
	return string(v.held.raw)
}

// IsZero reports whether the Value is the empty secret.
func (v Value) IsZero() bool {
	//: only the zero Value has no held bytes; NewValue never stores an empty one.
	return v.held == nil
}

// Len reports the secret's length in bytes. A length is metadata, not the
// secret, and a caller validating key material needs it; it is exposed here
// so that nobody reveals a secret only to measure it.
func (v Value) Len() int {
	//: the zero Value is zero bytes long.
	if v.held == nil {
		//: nothing held.
		return 0
	}
	//: the held secret's length.
	return len(v.held.raw)
}

// Equal reports whether v and other hold the same bytes, in time that does not
// depend on how many leading bytes they share. Two zero Values are equal.
//
// What IS observable is whether the two lengths match: crypto/subtle returns
// at once on a length mismatch, as every constant-time comparison in the
// standard library does. Compare against a value of the length you expect, or
// compare digests, when the length itself must stay private.
func (v Value) Equal(other Value) bool {
	//: ConstantTimeCompare over the held bytes; a nil slice is zero-length.
	return subtle.ConstantTimeCompare(v.bytes(), other.bytes()) == 1
}

// String implements fmt.Stringer and always writes [Redacted].
func (v Value) String() string {
	//: one constant, whatever is held — a length would leak too.
	return Redacted
}

// GoString implements fmt.GoStringer so %#v stays redacted: fmt bypasses
// String for Go-syntax formatting and would otherwise print the struct.
func (v Value) GoString() string {
	//: the same constant as String.
	return Redacted
}

// Format implements fmt.Formatter, so EVERY verb writes the placeholder — %v,
// %+v, %#v, %s, %x and the rest. %q writes it quoted, because a caller who
// asked for quotes is usually building a larger quoted rendering.
func (v Value) Format(state fmt.State, verb rune) {
	//: %q quotes, so a surrounding rendering stays well formed.
	if verb == 'q' {
		//: the placeholder, quoted.
		writeRendering(state, strconv.Quote(Redacted))
		//: nothing else to write.
		return
	}
	//: every other verb writes the bare placeholder.
	writeRendering(state, Redacted)
}

// MarshalJSON implements json.Marshaler and writes [Redacted] as a JSON string,
// so a configuration dumped as JSON never carries the secret. encoding/json
// escapes the angle brackets, which changes the spelling on the wire and not
// the string it decodes to.
func (v Value) MarshalJSON() (encoded []byte, err error) {
	//: a fresh slice every time, so a caller that edits it cannot poison the
	//: next rendering.
	return []byte(`"` + Redacted + `"`), nil
}

// MarshalText implements encoding.TextMarshaler and writes [Redacted], which is
// what TOML, YAML, XML and log/slog's text handler reach for.
func (v Value) MarshalText() (text []byte, err error) {
	//: a fresh slice, for the same reason as MarshalJSON.
	return []byte(Redacted), nil
}

// UnmarshalJSON implements json.Unmarshaler. It accepts a JSON STRING and
// nothing else: a number has already been re-spelled before a decoder sees it
// (1e3 arrives as 1000, a twenty-digit token loses its tail to float64), so
// storing one would store a secret nobody wrote. A null leaves the Value as it
// was, which is encoding/json's own convention for a field that decodes
// itself. Every refusal is [ValueRefused], and none of them quotes the input.
func (v *Value) UnmarshalJSON(data []byte) error {
	//: an explicit null is "not supplied", as it is for every other field.
	if string(data) == jsonNull {
		//: the Value keeps whatever it held.
		return nil
	}
	//: only a string token is a secret spelled exactly as the operator wrote it.
	if len(data) == 0 || data[0] != jsonQuote {
		//: a number, a boolean, an object — refused, never re-spelled.
		return ValueRefused
	}
	//: the token's text, escapes resolved.
	var text string
	//: a malformed string token is refused like any other.
	if err := json.Unmarshal(data, &text); err != nil {
		//: the decoder's own message is dropped: it can quote the input.
		return ValueRefused
	}
	//: the placeholder check and the copy are the text path's.
	return v.UnmarshalText([]byte(text))
}

// UnmarshalText implements encoding.TextUnmarshaler. The text becomes the
// secret, copied — except [Redacted] itself, which is refused with
// [ValueRefused]: every rendering of a Value writes it, so finding it on the
// way in means a rendered configuration was loaded as a real one, and every
// secret in it would otherwise silently become the placeholder.
func (v *Value) UnmarshalText(text []byte) error {
	//: a rendering fed back is refused, never stored.
	if string(text) == Redacted {
		//: ValueRefused names nothing: the input is known and is not a secret.
		return ValueRefused
	}
	//: replace the whole Value; the bytes a previous Value held are untouched,
	//: so copies of it stay what they were.
	*v = NewValue(text)
	//: decoded.
	return nil
}

// bytes returns the held secret without copying, for the comparisons inside
// this package. It never leaves the package.
func (v Value) bytes() []byte {
	//: the zero Value holds no bytes.
	if v.held == nil {
		//: a nil slice is zero-length to crypto/subtle.
		return nil
	}
	//: the held bytes, shared and never mutated.
	return v.held.raw
}

// writeRendering writes a rendering into the state fmt handed to Format.
//
// fmt.Formatter has no error return, and fmt itself discards what a write into
// its own buffer reports, so there is nowhere for the error to go. It is read
// here rather than assigned to a blank, so the discard is visible as a
// decision.
func writeRendering(state fmt.State, text string) {
	//: fmt's buffer is the only destination, and it does not fail.
	if _, err := io.WriteString(state, text); err != nil {
		//: nothing to report to — fmt drops the same error.
		return
	}
}
