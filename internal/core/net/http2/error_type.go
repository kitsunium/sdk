package http2

const (
	// ErrorTypeProtocol counts an error answered PROTOCOL_ERROR, an unspecific protocol violation.
	ErrorTypeProtocol ErrorType = iota + 1
	// ErrorTypeInternal counts an error answered INTERNAL_ERROR, the endpoint's own failure.
	ErrorTypeInternal
	// ErrorTypeFlowControl counts an error answered FLOW_CONTROL_ERROR, a window overrun or overflowed.
	ErrorTypeFlowControl
	// ErrorTypeSettingsTimeout counts an error answered SETTINGS_TIMEOUT, a SETTINGS frame unanswered in time.
	ErrorTypeSettingsTimeout
	// ErrorTypeStreamClosed counts an error answered STREAM_CLOSED, a frame on a closed stream.
	ErrorTypeStreamClosed
	// ErrorTypeFrameSize counts an error answered FRAME_SIZE_ERROR, a frame of the wrong length.
	ErrorTypeFrameSize
	// ErrorTypeRefusedStream counts an error answered REFUSED_STREAM, a stream past the open-stream bound.
	ErrorTypeRefusedStream
	// ErrorTypeCancel counts an error answered CANCEL, a stream the endpoint no longer wanted.
	ErrorTypeCancel
	// ErrorTypeCompression counts an error answered COMPRESSION_ERROR, a field block HPACK refused.
	ErrorTypeCompression
	// ErrorTypeConnect counts an error answered CONNECT_ERROR, a CONNECT request's failure.
	ErrorTypeConnect
	// ErrorTypeEnhanceYourCalm counts an error answered ENHANCE_YOUR_CALM, a flood the codec cut short.
	ErrorTypeEnhanceYourCalm
	// ErrorTypeInadequateSecurity counts an error answered INADEQUATE_SECURITY, a transport below the minimum.
	ErrorTypeInadequateSecurity
	// ErrorTypeHTTP11Required counts an error answered HTTP_1_1_REQUIRED, a request HTTP/1.1 must carry.
	ErrorTypeHTTP11Required
	// ErrorTypeHeaderList counts a header list past the group's bound: answered 431
	// or refused, it is no RFC 9113 error code.
	ErrorTypeHeaderList
	// ErrorTypePingTimeout counts a connection closed because a PING sent on its
	// silence went unanswered (ADR 0195 §D3).
	ErrorTypePingTimeout
	// ErrorTypeOther counts an error the engine named outside this vocabulary.
	ErrorTypeOther
)

// ErrorTypes bounds the ErrorType values, the length of a table indexed by one.
const ErrorTypes int = 17

// errorTypeNames are the types' names, by value; the zero value names none.
var errorTypeNames = [ErrorTypes]string{
	"other", "protocol", "internal", "flow_control", "settings_timeout", "stream_closed",
	"frame_size", "refused_stream", "cancel", "compression", "connect",
	"enhance_your_calm", "inadequate_security", "http_1_1_required",
	"header_list", "ping_timeout", "other",
}

// ErrorType names an HTTP/2 error a group counts (#966): one per RFC 9113 code,
// numbered as the code is, and three the codes do not spell — a header list
// past its bound, a PING unanswered, and anything else. A fixed vocabulary, so
// a peer cannot grow the table.
type ErrorType uint8

// String names the type in lower snake case — "frame_size", "header_list",
// "ping_timeout" —, "other" for a value outside the vocabulary.
func (t ErrorType) String() string {
	//: past the vocabulary.
	if int(t) >= ErrorTypes {
		//: as the table's last row.
		return errorTypeNames[ErrorTypeOther]
	}
	//: by value.
	return errorTypeNames[t]
}

// MarshalText implements encoding.TextMarshaler: the name String gives.
func (t ErrorType) MarshalText() ([]byte, error) {
	//: never refused: every value has a name.
	return []byte(t.String()), nil
}

// ErrorTypeOf is the ErrorType counting an error answered with code: the
// type of the same number for an RFC 9113 code, ErrorTypeOther for
// NO_ERROR and any code outside the RFC.
func ErrorTypeOf(code ErrCode) ErrorType {
	//: an RFC code past NO_ERROR: the type of its number.
	if code >= ErrCodeProtocol && code <= ErrCodeHTTP11Required {
		//: numbered as the code.
		return ErrorType(code)
	}
	//: anything else.
	return ErrorTypeOther
}

// ErrorCountValue is one row of a listener's HTTP/2 error table: an error type
// and how many times the group's engine counted it since Start.
type ErrorCountValue struct {
	// Type is the error counted.
	Type ErrorType `json:"type"`
	// Count is how many times it was.
	Count uint64 `json:"count"`
}
