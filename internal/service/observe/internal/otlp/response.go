package otlp

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// jsonNull is the literal a JSON decoder is handed for an explicitly null
// field; it leaves a LenientInt64 at its zero, which means "fully accepted".
const jsonNull string = "null"

// RejectedCounter is a signal's partialSuccess message — ExportMetricsPartialSuccess,
// ExportTracePartialSuccess — reduced to the one number the sender reads.
//
// The messages differ in exactly one member name (rejectedDataPoints,
// rejectedSpans), so each signal declares its own small struct with its own
// json tag and this method, and DecodeRejected decodes into it: the tags stay
// static, which keeps encoding/json's matching rules what they were when each
// signal decoded its own response type.
type RejectedCounter interface {
	RejectedCount() int64
}

// LenientInt64 decodes a 64-bit integer written EITHER as a JSON number OR as a
// decimal string.
//
// This is not tolerance for its own sake, it is the specification: 64-bit
// integers are encoded as decimal strings, "and either numbers or strings are
// accepted when decoding". Collectors differ — the reference collector emits
// the string form, some proxies re-serialise it as a number — and a decoder
// that accepted only one of them would silently read every partial success as
// a full one whenever it met the other.
type LenientInt64 int64

// UnmarshalJSON accepts 42 and "42" alike.
func (v *LenientInt64) UnmarshalJSON(data []byte) error {
	//: JSON null leaves the zero value, which means "fully accepted".
	text := string(bytes.TrimSpace(data))
	//: an explicit null is not a rejection count.
	if text == jsonNull {
		//: leave the zero.
		return nil
	}
	//: the string form is a JSON string, escapes included ("42" is a
	//: valid spelling of "42"), so it is decoded as one rather than trimmed;
	//: a bare number is left as it is.
	if strings.HasPrefix(text, `"`) {
		//: a malformed string is as opaque as a malformed number.
		if err := json.Unmarshal(data, &text); err != nil {
			//: surface it so the caller falls back to the 200.
			return err
		}
	}
	parsed, err := strconv.ParseInt(text, decimalBase, floatBitSize)
	//: an unparseable count is the caller's cue to treat the body as opaque.
	if err != nil {
		//: surface it so partialSuccessOf can fall back to the 200.
		return err
	}
	//: store the decoded count.
	*v = LenientInt64(parsed)
	//: decoded.
	return nil
}

// exportResponse is a signal's ExportXServiceResponse: the body of every
// accepted OTLP/HTTP request, reduced to its partialSuccess. The field holds
// the signal's own message BY POINTER, and encoding/json decodes through an
// interface holding a non-nil pointer into the value it points to — so the
// decode lands in the caller's struct, with that struct's static tags.
type exportResponse struct {
	PartialSuccess RejectedCounter `json:"partialSuccess"`
}

// DecodeRejected reports how many items a 2xx body says were rejected,
// decoding the body's partialSuccess into into — a pointer to the signal's own
// message, which this function reads back and nothing else keeps.
//
// An unparseable or empty body is NOT a failure. The request was accepted —
// that is what the 200 said — and the specification's own posture on this
// exchange is that a receiver ignores what it does not recognise; turning a
// collector's malformed response into a lost-data verdict would report a
// failure that did not happen, and would do it on every export.
func DecodeRejected(payload []byte, into RejectedCounter) int64 {
	//: an empty body is a full success and the common answer.
	if len(payload) == 0 {
		//: nothing rejected.
		return 0
	}
	//: decode leniently; unknown fields are ignored by encoding/json already,
	//: which is what the specification requires of a receiver.
	//: a body that is not the expected message tells us nothing new.
	if err := json.Unmarshal(payload, new(exportResponse{PartialSuccess: into})); err != nil {
		//: the 200 stands.
		return 0
	}
	//: read the caller's message, not the field: an explicit null clears the
	//: field and leaves the message at its zero, "fully accepted".
	return into.RejectedCount()
}
