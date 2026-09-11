// Package metrics — the collector's answer to an OTLP/HTTP export, and the
// lenient 64-bit decoder the specification asks for.
package metrics

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// jsonNull is the literal a JSON decoder is handed for an explicitly null
// field; it leaves otlpLenientInt64 at its zero, which means "fully accepted".
const jsonNull string = "null"

// otlpExportResponse is ExportMetricsServiceResponse, the body of every
// accepted OTLP/HTTP request.
type otlpExportResponse struct {
	PartialSuccess otlpPartialSuccess `json:"partialSuccess"`
}

// otlpPartialSuccess is ExportMetricsPartialSuccess. errorMessage is declared
// so the shape matches the schema and encoding/json has somewhere to put it;
// its value is never read into an error, because it is unbounded
// remote-controlled text and an errs Field goes straight into structured logs.
type otlpPartialSuccess struct {
	RejectedDataPoints otlpLenientInt64 `json:"rejectedDataPoints"`
	ErrorMessage       string           `json:"errorMessage"`
}

// otlpLenientInt64 decodes a 64-bit integer written EITHER as a JSON number OR
// as a decimal string.
//
// This is not tolerance for its own sake, it is the specification: 64-bit
// integers are encoded as decimal strings, "and either numbers or strings are
// accepted when decoding". Collectors differ — the reference collector emits
// the string form, some proxies re-serialise it as a number — and a decoder
// that accepted only one of them would silently read every partial success as a
// full one whenever it met the other.
type otlpLenientInt64 int64

// UnmarshalJSON accepts 42 and "42" alike.
func (v *otlpLenientInt64) UnmarshalJSON(data []byte) error {
	//: JSON null leaves the zero value, which means "fully accepted".
	text := string(bytes.TrimSpace(data))
	//: an explicit null is not a rejection count.
	if text == jsonNull {
		//: leave the zero.
		return nil
	}
	//: the string form is a JSON string, escapes included ("\u0034\u0032" is a
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
		//: surface it so otlpPartialSuccessOf can fall back to the 200.
		return err
	}
	//: store the decoded count.
	*v = otlpLenientInt64(parsed)
	//: decoded.
	return nil
}

// decodeOTLPRejected reports how many data points a 2xx body says were
// rejected, and whether the body could be read as an
// ExportMetricsServiceResponse at all.
//
// An unparseable or empty body is NOT a failure. The request was accepted —
// that is what the 200 said — and the specification's own posture on this
// exchange is that a receiver ignores what it does not recognise; turning a
// collector's malformed response into a lost-data verdict would report a
// failure that did not happen, and would do it for every scrape.
func decodeOTLPRejected(payload []byte) int64 {
	//: an empty body is a full success and the common answer.
	if len(payload) == 0 {
		//: nothing rejected.
		return 0
	}
	//: decode leniently; unknown fields are ignored by encoding/json already,
	//: which is what the specification requires of a receiver.
	var decoded otlpExportResponse
	//: a body that is not the expected message tells us nothing new.
	if err := json.Unmarshal(payload, &decoded); err != nil {
		//: the 200 stands.
		return 0
	}
	//: zero is documented as "the request was fully accepted".
	return int64(decoded.PartialSuccess.RejectedDataPoints)
}
