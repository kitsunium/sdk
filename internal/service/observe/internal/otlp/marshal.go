package otlp

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Marshal renders request — a signal's whole payload tree — as ONE OTLP/JSON
// document: HTML escaping DISABLED and no trailing newline, exactly the bytes
// that go in the body of a POST.
//
// encoding/json escapes '<', '>' and '&' into their \u00xx forms by default, a
// defence for JSON embedded in a <script> element. An OTLP body never is, and
// OTel-conventional attributes carry URLs (url.full, http.route) whose query
// separator is exactly '&' — so the default turns a readable payload into an
// unreadable one for no gain, and makes this SDK's bytes differ from every
// other OTLP producer's for identical input. json.Encoder is the only way to
// turn it off, and it appends a newline a single-document body must not carry.
//
// failure is the CALLER'S: the wrap a rendering fault leaves under, with the
// signal's EXPORT_FAILED code and wording. The fault is near-impossible —
// every field of a payload tree is a Go primitive or one of the Marshalers in
// this package — and it is still typed rather than swallowed.
func Marshal[R any](request R, failure *errs.WrapParams) (doc []byte, err error) {
	//: encode into a local buffer so the escaping switch is available.
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	//: '&' in a URL attribute stays '&'.
	encoder.SetEscapeHTML(false)
	//: the only failure mode left is a type encoding/json cannot render.
	if encodeErr := encoder.Encode(request); encodeErr != nil {
		//: report it under the signal's own code rather than swallow it.
		return nil, errs.Wrap(encodeErr, *failure)
	}
	//: Encode appends a newline; the HTTP body is one document, so drop it.
	return bytes.TrimSuffix(buf.Bytes(), []byte{DocumentTerminator}), nil
}

// UnixNano converts a wall-clock instant to the fixed64 nanosecond timestamp
// OTLP carries, mapping an unset instant onto 0.
//
// The guard is not defensive noise. time.Time's own documentation says
// UnixNano's "result is undefined if the Unix time in nanoseconds cannot be
// represented by an int64", and the zero Time is exactly that case: it returns
// a large negative number which, cast to uint64, becomes a timestamp several
// centuries in the future. A snapshot built by hand rather than by a Meter, or
// an event stamped by a manual clock left at its zero, is the reachable path,
// and it would ship silently wrong rather than visibly empty. Zero is what the
// schema already means by an unknown timestamp.
func UnixNano(instant time.Time) Uint64 {
	//: the unset instant, and a pre-1970 one, have no unsigned spelling.
	if instant.IsZero() || instant.UnixNano() < 0 {
		//: the schema's own "unknown" value, rather than a wrapped one.
		return 0
	}
	//: in range and positive.
	return Uint64(instant.UnixNano())
}
