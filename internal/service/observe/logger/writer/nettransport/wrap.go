package nettransport

import (
	corenettransport "github.com/kitsunium/sdk/internal/core/observe/logger/writer/nettransport"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// wrapDial wraps cause under the dial sentinel with a private diagnostic. The
// address is NOT attached — only the network identifier — so a consumer-supplied
// endpoint never leaks into the error (SSRF/secret gate).
func wrapDial(cause error, network string) error {
	//: single wrap point so every construction failure carries the dial code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corenettransport.CodeNetTransportDialFailed,
		Reason:  "NET_TRANSPORT_DIAL_FAILED",
		Public:  "Network transport could not establish its destination",
		Private: "service/observe/logger/writer/nettransport: establishing the transport failed",
	}, errs.String("network", network))
}

// wrapWrite wraps cause under the write sentinel with a private diagnostic. Only
// the network and byte count are attached — never the payload or address.
func wrapWrite(cause error, network string, n int) error {
	//: single wrap point so every send failure carries the write code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    corenettransport.CodeNetTransportWriteFailed,
		Reason:  "NET_TRANSPORT_WRITE_FAILED",
		Public:  "Network transport write failed",
		Private: "service/observe/logger/writer/nettransport: the underlying transport returned an error",
	}, errs.String("network", network), errs.Int("bytes", n))
}
