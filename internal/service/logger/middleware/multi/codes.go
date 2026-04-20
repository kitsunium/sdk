// Package multi: codes.go — range 3600-3699 reserved for the multi (fanout)
// Sink. Codes are declared at source as typed constants; the errs registry
// audit verifies uniqueness and range membership.
package multi

// range: 3600-3699

// CodeFanoutWriteFailed identifies a Write call where one or more of the
// fan-out targets returned a non-nil error. The error wraps an aggregated
// errors.Join of the per-sink failures so callers can drill in via errors.Is
// against the individual sink sentinels.
const CodeFanoutWriteFailed int = 3601
