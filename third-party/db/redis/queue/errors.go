package queue

import (
	"context"
	"errors"
	"io"
	"net"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// commandError types a failed command: STREAM_UNREACHABLE for a connection
// that failed, COMMAND_REFUSED for an error the server answered, each with
// QUEUE_BACKEND_FAILED in its trail and go-redis's error in the chain.
func commandError(cause error, op string) error {
	sentinel := CommandRefused
	//: no answer at all: a dial, a read, a timeout, a closed pool.
	if isUnreachable(cause) {
		sentinel = StreamUnreachable
	}
	coded := errs.Wrap(cause, sentinel.Params(), errs.String("op", op))
	//: origin wins: the package's code stays, the domain's joins the trail.
	return errs.Wrap(coded, corequeue.QueueBackendFailed.Params())
}

// isUnreachable reports whether cause is a connection failure rather than a
// server's answer.
func isUnreachable(cause error) bool {
	//: a server's answer carries its own prefix.
	if answer, answered := errors.AsType[goredis.Error](cause); answered && answer != nil {
		return false
	}
	netErr, transport := errors.AsType[net.Error](cause)
	//: a transport failure, a connection cut, the pool closed under the call,
	//: or no answer before the deadline.
	return (transport && netErr != nil) || errors.Is(cause, io.EOF) || errors.Is(cause, io.ErrUnexpectedEOF) ||
		errors.Is(cause, goredis.ErrClosed) || errors.Is(cause, context.DeadlineExceeded)
}

// leaseLost is the error a receipt whose lease no longer holds is.
func leaseLost(status string) error {
	//: lapsed, settled, dead-lettered or replaced: one answer for all.
	return errs.Wrap(corequeue.LeaseExpired, errs.WrapParams{}, errs.String("lease", status))
}

// unknownReceipt is the error a receipt this package never minted is.
func unknownReceipt() error {
	//: origin wins.
	return errs.Wrap(corequeue.UnknownReceipt, errs.WrapParams{}, errs.String("broker", "redis-stream"))
}
