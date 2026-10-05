package redis

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitIOErr matches sysexits EX_IOERR — a Redis failure is an I/O problem.
const exitIOErr int = 74

// wrapClientInit wraps cause under the client-init sentinel. The socket path /
// credentials are never attached.
func wrapClientInit(cause error) error {
	//: single wrap point so every init failure carries the client-init code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeRedisClientInitFailed,
		Reason:  "CLIENT_INIT_FAILED",
		Public:  "Redis writer could not initialise its client",
		Private: "third-party/db/writer/redis: client initialisation failed",
	})
}

// wrapAdd wraps cause under the add sentinel with only the entry count.
func wrapAdd(cause error, entries int) error {
	//: single wrap point so every XADD failure carries the add code.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeRedisXAddFailed,
		Reason:  "ADD_FAILED",
		Public:  "Redis writer failed to append a log batch",
		Private: "third-party/db/writer/redis: the pipelined XADD returned an error",
	}, errs.Int("entries", entries))
}
