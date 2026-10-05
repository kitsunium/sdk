package trace

// The three codes, in the schema's own declaration order: iota reproduces
// STATUS_CODE_UNSET = 0 through STATUS_CODE_ERROR = 2 exactly, which is what the
// OTLP encoder emits as the enum's integer.
const (
	// StatusUnset is STATUS_CODE_UNSET (0), the default and the overwhelmingly
	// common value. It means "no explicit judgement was recorded" — NOT "it
	// worked". A backend applies its own heuristics (an HTTP 5xx, a timeout);
	// an SDK claiming OK on the caller's behalf would suppress exactly those.
	StatusUnset StatusCode = iota
	// StatusOK is STATUS_CODE_OK (1): the developer explicitly asserted
	// success, which the specification says should override any backend
	// heuristic. It is therefore a claim, never set by this SDK on its own.
	StatusOK
	// StatusError is STATUS_CODE_ERROR (2): the operation failed.
	StatusError
)

// Resolved returns the status a span actually carries: an undeclared code
// clamps to StatusUnset, and a Message survives only on StatusError.
func (s StatusValue) Resolved() StatusValue {
	//: an error keeps its message — the only pairing the schema endorses.
	if s.Code == StatusError {
		//: as recorded.
		return s
	}
	//: OK is a deliberate assertion and is preserved, minus any message.
	if s.Code == StatusOK {
		//: the message is dropped rather than travelling on a success.
		return StatusValue{Code: StatusOK}
	}
	//: StatusUnset, or an integer cast into the type.
	return StatusValue{Code: StatusUnset}
}

// IsUnset reports whether the status carries no judgement at all. It is what the
// OTLP encoder asks before deciding whether the `status` field is worth a line.
func (s StatusValue) IsUnset() bool {
	//: unset AND unmessaged; a message with no code is not a status.
	return s.Code == StatusUnset && s.Message == ""
}

// String renders the code as its OpenTelemetry name, for text output and
// readable test failures. The wire carries the integer.
func (c StatusCode) String() string {
	//: one name per declared value.
	switch c {
	//: an explicit success claim.
	case StatusOK:
		//: asserted by the developer.
		return "OK"
	//: a failure.
	case StatusError:
		//: the operation did not succeed.
		return "ERROR"
	//: the zero value and every cast.
	default:
		//: no judgement recorded.
		return "UNSET"
	}
}
