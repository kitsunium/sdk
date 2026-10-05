package logger

import (
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/observe/logger/encoder"
)

// NewTextEncoder returns the default human-readable encoder, rendering each
// record as "TIME LEVEL msg key=val …\n" with RFC3339-millisecond timestamps.
// It is a named peer of TextEncoder bound to the real system clock.
func NewTextEncoder() Encoder {
	//: bind to the system clock so zero-Time records get a real timestamp.
	return encoder.NewText(clock.System)
}

// NewJSONEncoder returns a structured single-line JSON encoder, rendering each
// record as one encoding/json-compatible object per line:
// {"ts":…,"level":…,"msg":…,<flat attrs>}. Grouped attributes flatten to
// dotted keys ("g1.g2.key") to match the text encoder's convention. Pass it to
// NewWithSink via SinkConfig.Encoder for machine-readable output.
func NewJSONEncoder() Encoder {
	//: bind to the system clock so zero-Time records get a real timestamp.
	return encoder.NewJSON(clock.System)
}
