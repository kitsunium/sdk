package logger

import (
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/observe/logger/encoder"
)

// newTextEncoder is NewTextEncoder's body: decl_gen.go writes NewTextEncoder, from the
// design, as one call of it.
func newTextEncoder() Encoder {
	//: bind to the system clock so zero-Time records get a real timestamp.
	return encoder.NewText(clock.System)
}

// newJSONEncoder is NewJSONEncoder's body: decl_gen.go writes NewJSONEncoder, from the
// design, as one call of it.
func newJSONEncoder() Encoder {
	//: bind to the system clock so zero-Time records get a real timestamp.
	return encoder.NewJSON(clock.System)
}
