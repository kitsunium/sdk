package http2

import (
	"strconv"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// FrameHeaderLen is the length of an HTTP/2 frame header, RFC 9113 §4.1.
const FrameHeaderLen int = 9

const (
	// DefaultMaxStreams is how many streams a connection may hold open: a zero
	// LimitsValue.MaxStreams selects it.
	DefaultMaxStreams int = 100
	// DefaultMaxFrame is the largest frame the codec accepts by default, and the
	// protocol's minimum: a zero LimitsValue.MaxFrame selects it.
	DefaultMaxFrame int = 16384
	// MaxFrameLimit is the largest frame the protocol can spell, RFC 9113 §4.2.
	MaxFrameLimit int = 1<<24 - 1
	// DefaultHeaderTable is the HPACK dynamic table the server advertises by
	// default, RFC 7541 §4.2's own size.
	DefaultHeaderTable int = 4096
	// MaxHeaderTable is the largest HEADER_TABLE_SIZE a configuration may ask for:
	// the table's entries are measured with 16 bits.
	MaxHeaderTable int = 1<<16 - 1
	// NoDynamicTable, as LimitsValue.HeaderTable, advertises a table of size zero:
	// a zero field is never inert (ADR 0031), so -1 says it.
	NoDynamicTable int = -1
	// DefaultReceivePerStream is the window one stream's body may hold unread.
	DefaultReceivePerStream int = 256 << 10
	// DefaultMaxHeaderList is the header list a request may carry, RFC 9113 §6.5.2's
	// measure: the bound of http1's head (ADR 0186).
	DefaultMaxHeaderList int = 8192
	// DefaultWindow is the protocol's initial flow-control window, RFC 9113 §6.9.2.
	DefaultWindow int = 65535
	// MaxWindow is the largest flow-control window, RFC 9113 §6.9.1.
	MaxWindow int = 1<<31 - 1
	// perConnFactor is ReceivePerConn's default, in multiples of ReceivePerStream.
	perConnFactor int = 4
)

const (
	// FrameData carries a stream's content, RFC 9113 §6.1.
	FrameData FrameType = iota
	// FrameHeaders opens a stream and carries its field block, §6.2.
	FrameHeaders
	// FramePriority is the deprecated priority signal, §6.3; ignored.
	FramePriority
	// FrameRSTStream ends a stream at once, §6.4.
	FrameRSTStream
	// FrameSettings carries or acknowledges a connection's settings, §6.5.
	FrameSettings
	// FramePushPromise reserves a server-initiated stream, §6.6.
	FramePushPromise
	// FramePing measures the round trip and checks liveness, §6.7.
	FramePing
	// FrameGoAway ends a connection gracefully or with an error, §6.8.
	FrameGoAway
	// FrameWindowUpdate grants flow-control credit, §6.9.
	FrameWindowUpdate
	// FrameContinuation extends a field block past one frame, §6.10.
	FrameContinuation
)

const (
	// FlagAck acknowledges a SETTINGS or a PING frame.
	FlagAck FrameFlags = 0x1
	// FlagEndStream marks the last frame of a stream on DATA and HEADERS.
	FlagEndStream FrameFlags = 0x1
	// FlagEndHeaders marks the last frame of a field block.
	FlagEndHeaders FrameFlags = 0x4
	// FlagPadded marks a frame carrying a pad length and padding.
	FlagPadded FrameFlags = 0x8
	// FlagPriority marks a HEADERS frame carrying a priority field.
	FlagPriority FrameFlags = 0x20
)

const (
	// ErrCodeNoError is no error, a graceful shutdown or a discarded stream.
	ErrCodeNoError ErrCode = iota
	// ErrCodeProtocol is an unspecific protocol violation.
	ErrCodeProtocol
	// ErrCodeInternal is an unexpected internal failure of the endpoint.
	ErrCodeInternal
	// ErrCodeFlowControl is a flow-control violation.
	ErrCodeFlowControl
	// ErrCodeSettingsTimeout is a SETTINGS frame unanswered in time.
	ErrCodeSettingsTimeout
	// ErrCodeStreamClosed is a frame received on a closed stream.
	ErrCodeStreamClosed
	// ErrCodeFrameSize is a frame of the wrong size.
	ErrCodeFrameSize
	// ErrCodeRefusedStream is a stream refused before any processing.
	ErrCodeRefusedStream
	// ErrCodeCancel is a stream no longer wanted.
	ErrCodeCancel
	// ErrCodeCompression is a field block the connection's compression state
	// cannot survive.
	ErrCodeCompression
	// ErrCodeConnect is a CONNECT tunnel's failure.
	ErrCodeConnect
	// ErrCodeEnhanceYourCalm is a peer exhibiting excessive load.
	ErrCodeEnhanceYourCalm
	// ErrCodeInadequateSecurity is a transport below the minimum.
	ErrCodeInadequateSecurity
	// ErrCodeHTTP11Required asks the client to retry over HTTP/1.1.
	ErrCodeHTTP11Required
)

const (
	// SettingHeaderTableSize bounds the peer's HPACK dynamic table.
	SettingHeaderTableSize SettingID = 0x1
	// SettingEnablePush says whether the peer may push.
	SettingEnablePush SettingID = 0x2
	// SettingMaxConcurrentStreams bounds the peer's open streams.
	SettingMaxConcurrentStreams SettingID = 0x3
	// SettingInitialWindowSize is the peer's initial stream window.
	SettingInitialWindowSize SettingID = 0x4
	// SettingMaxFrameSize is the largest frame the peer accepts.
	SettingMaxFrameSize SettingID = 0x5
	// SettingMaxHeaderListSize bounds the header list the peer accepts.
	SettingMaxHeaderListSize SettingID = 0x6
	// SettingEnableConnectProtocol enables RFC 8441's extended CONNECT.
	SettingEnableConnectProtocol SettingID = 0x8
)

// errCodeNames names each code as the RFC does, by value.
var errCodeNames = [...]string{
	"NO_ERROR", "PROTOCOL_ERROR", "INTERNAL_ERROR", "FLOW_CONTROL_ERROR",
	"SETTINGS_TIMEOUT", "STREAM_CLOSED", "FRAME_SIZE_ERROR", "REFUSED_STREAM",
	"CANCEL", "COMPRESSION_ERROR", "CONNECT_ERROR", "ENHANCE_YOUR_CALM",
	"INADEQUATE_SECURITY", "HTTP_1_1_REQUIRED",
}

// FrameType is an HTTP/2 frame's type, RFC 9113 §6. A type this package names
// no constant for is an extension frame, which an endpoint ignores (§4.1).
type FrameType uint8

// FrameFlags is a frame header's flags byte, RFC 9113 §4.1.
type FrameFlags uint8

// ErrCode is an HTTP/2 error code, RFC 9113 §7: what RST_STREAM and GOAWAY
// carry. It is the wire's vocabulary, not an error of the SDK's: a refusal the
// codec answers with on the wire is an ErrCode, one it returns to a program is
// a sentinel of this package.
type ErrCode uint32

// String names the code as RFC 9113 §7 does; a code outside the RFC reads as
// its number, as an unknown code must be treated (§7).
func (c ErrCode) String() string {
	//: an RFC code is named by the table; any other is its number.
	if int(c) < len(errCodeNames) {
		return errCodeNames[c]
	}
	return "ERR_" + strconv.FormatUint(uint64(c), 16)
}

// SettingID is a SETTINGS parameter's identifier, RFC 9113 §6.5.2.
type SettingID uint16

// SettingValue is one SETTINGS parameter as RFC 9113 §6.5.1 lays it out: a
// 16-bit identifier and a 32-bit value.
type SettingValue struct {
	// ID identifies the parameter.
	ID SettingID
	// Val is the parameter's value.
	Val uint32
}

// FrameValue is a frame's nine-byte header, read by the frame layer of
// internal/service/net/http2. The payload that follows is Length bytes.
type FrameValue struct {
	// Type is the frame's type.
	Type FrameType
	// Flags is the frame's flags byte.
	Flags FrameFlags
	// StreamID is the stream the frame belongs to, the reserved top bit
	// cleared; zero is the connection itself.
	StreamID uint32
	// Length is the frame's payload length, in bytes.
	Length int
}

// LimitsValue bounds what one HTTP/2 connection may do, the numbers ADR 0195
// §D3 chose. A zero field selects its default, never unbounded (ADR 0031); a
// value it cannot honour is refused by Resolve, never narrowed in silence.
type LimitsValue struct {
	// MaxStreams is how many streams a connection may hold open at once: the
	// next one is refused with REFUSED_STREAM. Zero selects DefaultMaxStreams.
	MaxStreams int `json:"max_streams"`
	// ReceivePerStream is the window one stream's body may hold unread. Zero
	// selects DefaultReceivePerStream.
	ReceivePerStream int `json:"receive_per_stream"`
	// ReceivePerConn is the window all of a connection's streams share. Zero
	// selects four ReceivePerStream, so a handler that never reads its body
	// does not stop the others.
	ReceivePerConn int `json:"receive_per_conn"`
	// MaxFrame is the largest frame the codec accepts, between
	// DefaultMaxFrame and MaxFrameLimit. Zero selects DefaultMaxFrame.
	MaxFrame int `json:"max_frame"`
	// HeaderTable is the HPACK dynamic table the server advertises, at most
	// MaxHeaderTable. Zero selects DefaultHeaderTable; NoDynamicTable
	// advertises none.
	HeaderTable int `json:"header_table"`
	// MaxHeaderList bounds a request's header list as RFC 9113 §6.5.2 counts
	// it, the name and value of each field plus 32. Zero selects
	// DefaultMaxHeaderList.
	MaxHeaderList int `json:"max_header_list"`
}

// Resolve returns v with each zero field replaced by its default. A negative
// field, a MaxFrame outside the protocol's range or a ReceivePerConn below
// ReceivePerStream is refused with LimitsRefused, naming the field in reason.
func (v LimitsValue) Resolve() (LimitsValue, error) {
	r := v.withDefaults()
	//: each field keeps its own bound and its own name in the refusal.
	for _, c := range limitChecks(&r) {
		//: the first field out of its bound is named.
		if c.value < c.low || c.value > c.high {
			return LimitsValue{}, errs.Wrap(LimitsRefused, errs.WrapParams{}, errs.String("reason", c.name))
		}
	}
	//: the connection window must hold at least one stream's.
	if r.ReceivePerConn < r.ReceivePerStream {
		return LimitsValue{}, errs.Wrap(LimitsRefused, errs.WrapParams{}, errs.String("reason", "receive_per_conn"))
	}
	return r, nil
}

// limitCheck is one field's bound for Resolve.
type limitCheck struct {
	// name is the field, as a refusal names it.
	name string
	// value is the field's resolved value.
	value int
	// low and high are its inclusive bounds.
	low, high int
}

// limitChecks lists r's fields with their bounds.
func limitChecks(r *LimitsValue) [5]limitCheck {
	return [5]limitCheck{
		{name: "max_streams", value: r.MaxStreams, low: 1, high: MaxWindow},
		{name: "receive_per_stream", value: r.ReceivePerStream, low: 1, high: MaxWindow},
		{name: "receive_per_conn", value: r.ReceivePerConn, low: 1, high: MaxWindow},
		{name: "max_frame", value: r.MaxFrame, low: DefaultMaxFrame, high: MaxFrameLimit},
		{name: "header_table", value: r.HeaderTable, low: 0, high: MaxHeaderTable},
	}
}

// withDefaults replaces each zero field of v with its default, and the
// NoDynamicTable sentinel with the size it advertises: zero.
func (v LimitsValue) withDefaults() LimitsValue {
	r := LimitsValue{
		MaxStreams:       cmpOr(v.MaxStreams, DefaultMaxStreams),
		ReceivePerStream: cmpOr(v.ReceivePerStream, DefaultReceivePerStream),
		MaxFrame:         cmpOr(v.MaxFrame, DefaultMaxFrame),
		HeaderTable:      cmpOr(v.HeaderTable, DefaultHeaderTable),
		MaxHeaderList:    cmpOr(v.MaxHeaderList, DefaultMaxHeaderList),
	}
	r.ReceivePerConn = cmpOr(v.ReceivePerConn, perConnFactor*r.ReceivePerStream)
	//: -1 means no dynamic table; zero is the default, which Resolve has already placed.
	if v.HeaderTable == NoDynamicTable {
		r.HeaderTable = 0
	}
	return r
}

// cmpOr returns v unless it is zero, then def.
func cmpOr(v, def int) int {
	//: zero is "the default".
	if v == 0 {
		return def
	}
	return v
}
