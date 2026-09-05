// Package logger — re-exports the Value payload discriminant so consumers can
// name what Value.Kind() returns.
//
// Without these aliases MemorySink is only half usable: it hands back
// RecordSnapshot values whose Attrs carry a Kind, but the Kind type itself
// lived behind the internal/ firewall, so a consumer test could read the
// discriminant and still not write it down. The core doc already calls these
// values "stable across the public API"; this file makes that true.
package logger

import corelogger "github.com/kitsunium/sdk/internal/core/logger"

// Kind is the stable alias for the internal payload discriminant carried by a
// Value. Switch on it to select the matching typed accessor (String, Int64,
// Float64, …) instead of paying for an `any` assertion at render time.
type Kind = corelogger.Kind

// KindAny tags a Value whose payload is an unconstrained interface. It is also
// the zero value of Kind, so an unrecognised future variant degrades here
// rather than panicking.
const KindAny Kind = corelogger.KindAny

// KindBool tags a Value carrying a boolean payload.
const KindBool Kind = corelogger.KindBool

// KindDuration tags a Value carrying a time.Duration payload.
const KindDuration Kind = corelogger.KindDuration

// KindFloat64 tags a Value carrying a float64 payload.
const KindFloat64 Kind = corelogger.KindFloat64

// KindInt64 tags a Value carrying an int64 payload (also covers int / int32,
// which IntValue widens on the way in).
const KindInt64 Kind = corelogger.KindInt64

// KindString tags a Value carrying a string payload.
const KindString Kind = corelogger.KindString

// KindTime tags a Value carrying a time.Time payload.
const KindTime Kind = corelogger.KindTime

// KindUint64 tags a Value carrying an unsigned 64-bit integer payload.
const KindUint64 Kind = corelogger.KindUint64

// KindGroup tags a Value whose payload is a nested []Attr. Producers that
// target the bundled text and JSON encoders flatten groups into dotted keys
// instead, since neither encoder renders a group payload directly.
const KindGroup Kind = corelogger.KindGroup
