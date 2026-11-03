package queue

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// requestError types a failed request: the package's sentinel it is,
// QUEUE_BACKEND_FAILED in its trail, the vendor's error in the chain.
func requestError(cause error, op string) error {
	coded := errs.Wrap(cause, classify(cause).Params(), errs.String("op", op))
	//: origin wins: the package's code stays, the domain's joins the trail.
	return errs.Wrap(coded, corequeue.QueueBackendFailed.Params())
}

// publishError types a failed publication: a request that got no PubAck may
// have been stored, and says so; anything else is requestError's.
func publishError(cause error) error {
	//: no answer: stored or not, nobody knows.
	if isUnreachable(cause) {
		coded := errs.Wrap(cause, PublishUnacked.Params(), errs.String("op", "publish"))
		//: PUBLISH_UNACKED.
		return errs.Wrap(coded, corequeue.QueueBackendFailed.Params())
	}
	//: the server refused it.
	return requestError(cause, "publish")
}

// classify picks the sentinel a failed request is.
func classify(cause error) *errs.Error {
	//: the server's own answers first.
	switch {
	case errors.Is(cause, jetstream.ErrStreamNotFound):
		//: STREAM_MISSING.
		return StreamMissing
	case errors.Is(cause, jetstream.ErrConsumerNotFound):
		//: CONSUMER_MISSING.
		return ConsumerMissing
	case isUnreachable(cause):
		//: SERVER_UNREACHABLE.
		return ServerUnreachable
	default:
		//: REQUEST_FAILED.
		return RequestFailed
	}
}

// isUnreachable reports whether cause is no answer at all.
func isUnreachable(cause error) bool {
	//: a closed connection, nobody listening, or no answer in time.
	return errors.Is(cause, nats.ErrConnectionClosed) || errors.Is(cause, nats.ErrTimeout) ||
		errors.Is(cause, nats.ErrNoResponders) || errors.Is(cause, jetstream.ErrNoStreamResponse) ||
		errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, nats.ErrDisconnected)
}

// leaseLost is the error a receipt whose lease no longer holds is.
func leaseLost(why string) error {
	//: lapsed, settled or replaced: one answer for all.
	return errs.Wrap(corequeue.LeaseExpired, errs.WrapParams{}, errs.String("lease", why))
}
