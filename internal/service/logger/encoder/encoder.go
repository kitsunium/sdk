// Package encoder declares the Encoder interface — the format-side boundary
// of the logger architecture. Encoders convert a corelogger.RecordEvent into
// formatted bytes; the Handler then hands those bytes to a Sink for delivery
// (file, console, syslog, S3, …).
//
// Concrete encoders live alongside in this package: Text for human-readable
// "TIME LEVEL msg key=val" output; future commits add NDJSON / JSON encoders
// reusing internal/core/codec.Appender for true zero-allocation paths.
package encoder

import corelogger "github.com/kitsunium/sdk/internal/core/logger"

// Encoder formats a corelogger.RecordEvent into a byte slice. Implementations
// MUST be safe for concurrent use by multiple goroutines and SHOULD avoid
// allocating beyond the caller-supplied buffer (typically borrowed from the
// kernel/buffer pool) so the hot path stays zero-allocation.
type Encoder interface {
	// Name returns the canonical encoder identifier ("text", "ndjson",
	// "json"). Useful for diagnostic dumps and metrics tagging.
	//
	// Returns:
	//   - string: the canonical identifier chosen at construction time.
	Name() (name string)
	// Append serialises r onto dst and returns the (possibly re-allocated)
	// buffer. groups carries the active group prefix stack — encoders are
	// responsible for rendering it as their format-specific prefix
	// ("g1.g2.key" for text, nested objects for JSON).
	//
	// Params:
	//   - dst: caller-supplied buffer; encoded bytes are appended onto it.
	//   - groups: active group prefix stack (outermost first); may be empty.
	//   - r: record to render.
	//
	// Returns:
	//   - []byte: the (possibly re-allocated) buffer with encoded bytes.
	Append(dst []byte, groups []string, r corelogger.RecordEvent) (out []byte)
}
