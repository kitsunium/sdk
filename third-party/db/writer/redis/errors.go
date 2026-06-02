// Package redis — declares the sentinels returned by the Redis writer's
// constructor and XADD path. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form (short names per the AWS-writer convention; the package
// qualifier gives context). No socket path, credential, or record value is ever
// echoed into these errors (secret gate).
package redis

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitIOErr matches sysexits EX_IOERR — a Redis failure is an I/O problem.
const exitIOErr int = 74

var (
	// ClientInitFailed wraps a failure to build the client: a missing
	// socket/stream or an unresolvable credential provider.
	ClientInitFailed = errs.Define(CodeRedisClientInitFailed, "CLIENT_INIT_FAILED",
		"Redis writer could not initialise its client",
		"third-party/db/writer/redis: socket/stream missing or credentials unresolvable",
		errs.WithExitCode(exitIOErr))

	// AddFailed wraps a failed pipelined XADD batch.
	AddFailed = errs.Define(CodeRedisXAddFailed, "ADD_FAILED",
		"Redis writer failed to append a log batch",
		"third-party/db/writer/redis: the pipelined XADD returned an error",
		errs.WithExitCode(exitIOErr))
)

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
