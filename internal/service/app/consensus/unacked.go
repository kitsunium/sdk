package consensus

import "errors"

// kernelTimedOut reports whether err is the kernel aborting a connection
// whose bytes stayed unacknowledged: the bound boundUnacknowledged sets,
// or the platform's own retransmission limit where it sets none (#1116).
func kernelTimedOut(err error) bool {
	return err != nil && errors.Is(err, errTimedOut)
}
