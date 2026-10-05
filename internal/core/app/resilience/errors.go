package resilience

// exitUnavailable matches sysexits EX_TEMPFAIL (75) — a resilience rejection is a
// transient unavailability, not a generic internal fault.
const exitUnavailable int = 75

// exitConfig matches sysexits EX_CONFIG (78). PolicyMisconfigured is the one
// resilience outcome that is NOT transient: retrying a policy that was built
// with a configuration it cannot honour will never succeed.
const exitConfig int = 78

// The HTTP statuses the rejections carry, so errs.HTTPStatusOf answers a
// framework mapping errors to responses with the status that tells a client
// what to do — instead of a 500, which tells it the server is broken. The
// verdicts that are not rejections — RetryExhausted, FallbackFailed,
// PolicyMisconfigured — carry none: what failed there is the operation or the
// wiring, and neither is a status this domain can know.
const (
	// httpTooManyRequests is 429: slow down, and the Retry-After a framework
	// adds is the client's to honour.
	httpTooManyRequests int = 429
	// httpUnavailable is 503: this dependency cannot take the call right now;
	// the same call later may succeed.
	httpUnavailable int = 503
	// httpGatewayTimeout is 504: the call was made and its answer did not
	// arrive in time.
	httpGatewayTimeout int = 504
)
