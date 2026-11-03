package xml

import (
	stdxml "encoding/xml"
	"errors"
	"math"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// position returns the line encoding/xml's SyntaxError gives err, as the
// family's field codec.PositionOf reads (#1125) — the one coordinate
// encoding/xml reports. Any other failure is placed nowhere.
func position(err error) []errs.FieldValue {
	se, ok := errors.AsType[*stdxml.SyntaxError](err)
	//: not a syntax error, or one with no line.
	if !ok || se.Line <= 0 {
		return nil
	}
	//: the 1-based line.
	return codec.PositionFields(codec.PositionValue{Line: se.Line})
}

// offsetField is the family's offset field for a stream offset, counted in
// int64: placed nowhere past what an int holds — a 32-bit platform's long
// stream — rather than wrongly.
func offsetField(off int64) []errs.FieldValue {
	//: past an int: unknown, not wrapped around.
	if off > math.MaxInt {
		return nil
	}
	//: the 0-based byte.
	return codec.PositionFields(codec.PositionValue{Offset: int(off)})
}
