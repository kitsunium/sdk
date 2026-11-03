package codec

import "github.com/kitsunium/sdk/internal/kernel/errs"

// The reasons a bound refusal carries in every codec of the family (#894):
// the per-format code is the origin, the reason is shared, so one test answers
// "too large" or "too deep" across the formats.
const (
	// reasonSizeExceeded is the family's reason for input past a size bound.
	reasonSizeExceeded string = "SIZE_EXCEEDED"
	// reasonDepthExceeded is the family's reason for input past a depth bound.
	reasonDepthExceeded string = "DEPTH_EXCEEDED"
	// reasonLimitExceeded is multipart's older spelling of a size refusal.
	reasonLimitExceeded string = "LIMIT_EXCEEDED"
	// reasonDocumentTooLarge is strictjson's older spelling of one.
	reasonDocumentTooLarge string = "DOCUMENT_TOO_LARGE"
)

// IsTooLarge reports whether err, or an error it wraps, is a codec's refusal
// of input past a size bound — the family's SIZE_EXCEEDED reason, which every
// bounded codec gives its byte, node, pair and entry bounds (#894), or the two
// older spellings kept as they were: multipart's LIMIT_EXCEEDED and
// strictjson's DOCUMENT_TOO_LARGE. An HTTP server answers it with 413.
func IsTooLarge(err error) bool {
	//: the family's reason, or one of the two kept spellings.
	return errs.HasReason(err, reasonSizeExceeded) ||
		errs.HasReason(err, reasonLimitExceeded) ||
		errs.HasReason(err, reasonDocumentTooLarge)
}

// IsTooDeep reports whether err, or an error it wraps, is a codec's refusal of
// input nested past a depth bound: the family's DEPTH_EXCEEDED reason (#894).
func IsTooDeep(err error) bool {
	//: the family's reason.
	return errs.HasReason(err, reasonDepthExceeded)
}
