package client

import (
	"net/http"
	"strconv"
	"strings"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// statusFieldKey names the non-2xx status a CallFailed carries.
const statusFieldKey string = "status"

// retryAfterFieldKey names the seconds a 429 or a 503 asked the caller to wait.
// It is the key resilience.RetryAfter reads, so RetryConfig.Delay honours the
// server's hint with no adapter; the OTLP sender writes the same one. The
// spelling is pinned against RetryAfter by this package's suite.
const retryAfterFieldKey string = "retry_after_seconds"

// statusError is the CallFailed a non-2xx response is returned with: its
// status, and — on a 429 or a 503 whose Retry-After holds delta-seconds — the
// delay the server asked for.
func statusError(raw *http.Response) error {
	//: only the two statuses RFC 9110 and RFC 6585 pair with Retry-After as
	//: "come back later"; a 3xx's Retry-After means something else.
	if raw.StatusCode == http.StatusTooManyRequests || raw.StatusCode == http.StatusServiceUnavailable {
		//: a hint the caller's retry can honour, when the server gave one.
		if seconds, ok := retryAfterSeconds(raw.Header); ok {
			//: the status and the delay, side by side.
			return errs.Wrap(corenet.CallFailed, errs.WrapParams{},
				errs.Int(statusFieldKey, raw.StatusCode), errs.Int(retryAfterFieldKey, seconds))
		}
	}
	//: the status alone.
	return errs.Wrap(corenet.CallFailed, errs.WrapParams{}, errs.Int(statusFieldKey, raw.StatusCode))
}

// retryAfterSeconds reads Retry-After's delta-seconds form. The HTTP-date form
// is not read: converting it would compare the server's clock to ours, which
// is how a retry storm hides for months. A value that is not a positive whole
// number is no hint.
func retryAfterSeconds(header http.Header) (int, bool) {
	seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After")))
	//: absent, a date, negative or zero: no hint.
	if err != nil || seconds <= 0 {
		//: the caller's own backoff decides.
		return 0, false
	}
	//: the server's own number.
	return seconds, true
}
