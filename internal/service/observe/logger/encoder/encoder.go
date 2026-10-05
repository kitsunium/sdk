package encoder

import (
	"strings"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
)

// ReservedPrefix is what a TOP-LEVEL attribute whose key the SDK reserves is
// rendered under, so the two never collide on one line. The whole namespace
// under it is reserved with it — see [ReservesKey].
const ReservedPrefix string = "attr."

// Encoder is the format-side port now rooted in internal/core/observe/logger.
// Kept as an alias here so consumers that import service/observe/logger/encoder
// for the interface type keep compiling. New code should import the
// canonical type from core/observe/logger directly.
type Encoder = corelogger.Encoder

// reservesKey is ReservesKey's body: decl_gen.go writes ReservesKey, from the
// design, as one call of it.
func reservesKey(key string) bool {
	//: the two keys ADR 0062 writes, plus the escape namespace they are
	//: renamed into, which must be escaped in turn to stay one-to-one.
	return key == corelogger.TraceIDKey || key == corelogger.SpanIDKey ||
		strings.HasPrefix(key, ReservedPrefix)
}
