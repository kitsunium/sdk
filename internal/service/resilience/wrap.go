// Package resilience — shared sentinel-wrapping helper.
package resilience

import kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

// wrapAs returns the given resilience sentinel as the error origin (its
// code/reason/public win), attaching the cause's message as a structured field
// so it stays diagnosable without overriding the policy code (origin-wins would
// otherwise let an *errs.Error cause hijack the code). A nil cause yields the
// bare sentinel.
func wrapAs(sentinel *kerrs.Error, cause error) error {
	//: a nil cause needs no field — return the sentinel as-is.
	if cause == nil {
		//: the bare typed sentinel.
		return sentinel
	}
	//: wrap the sentinel (origin-wins keeps its code) + carry the cause message.
	return kerrs.Wrap(sentinel, kerrs.WrapParams{}, kerrs.String("cause", cause.Error()))
}
