// Package level defines severity levels for log records. It is a core/logger
// sub-package (logger-domain vocabulary, so not kernel) and remains stdlib-only:
// no dependency beyond the Go standard library, and specifically never log/slog.
package level

// Level represents the importance of a log record; higher values are more severe.
type Level int8

// Debug represents detailed tracing information useful during development.
const Debug Level = -4

// Info represents routine operational events occurring during normal execution.
const Info Level = 0

// Warn represents abnormal but recoverable conditions worth human attention.
const Warn Level = 4

// Error represents failures that usually signal a broken invariant and require action.
const Error Level = 8

// String returns the uppercase textual label associated with the Level receiver.
func (l Level) String() string {
	//: classify the numeric level into one of four named severity windows.
	switch {
	//: strictly below Info → Debug window.
	case l < Info:
		//: emit Debug label.
		return "DEBUG"
	//: [Info, Warn) → Info window.
	case l < Warn:
		//: emit Info label.
		return "INFO"
	//: [Warn, Error) → Warn window.
	case l < Error:
		//: emit Warn label.
		return "WARN"
	//: at or above Error → Error window.
	default:
		//: emit Error label.
		return "ERROR"
	}
}
