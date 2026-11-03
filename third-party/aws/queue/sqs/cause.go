package sqs

import (
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// reasonUnreported is the Reason recorded when a consumer nacked with a nil
// cause, as every broker of the SDK records it.
const reasonUnreported string = "UNREPORTED"

// reasonLeaseExpired is the Reason of a message that ran out of deliveries
// with no consumer ever reporting a cause: its leases lapsed.
const reasonLeaseExpired string = "LEASE_EXPIRED"

// maxTrail is the most trail codes a dead letter keeps, the kernel's own
// bound on an error's trail.
const maxTrail int = 16

// causeValue is what a dead letter keeps of a failure: the Reason, the
// wire-safe Public half, the dotted-quad code and the trail — never the
// Private half, which stays in the failing process's log (rule 4).
type causeValue struct {
	reason string
	public string
	trail  []uint32
	code   uint32
}

// describeCause reduces an error to what a dead letter may keep of it.
func describeCause(cause error) causeValue {
	//: a consumer that knows only "this did not work" may say so.
	if cause == nil {
		//: named, so the absence reads as a decision.
		return causeValue{reason: reasonUnreported}
	}
	described := causeValue{public: errs.PublicOf(cause)}
	//: a typed SDK error names its reason.
	if reason, named := errs.ReasonOf(cause); named {
		described.reason = reason
	}
	//: 0 means the cause carried no code.
	if code, coded := errs.CodeOf(cause); coded {
		described.code = uint32(code)
	}
	trail := errs.TrailOf(cause)
	trail = trail[max(len(trail)-maxTrail, 0):]
	//: oldest first, the newest kept.
	for _, code := range trail {
		described.trail = append(described.trail, uint32(code))
	}
	//: what the cause was willing to say in public.
	return described
}
