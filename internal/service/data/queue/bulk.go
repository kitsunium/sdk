package queue

import (
	"context"
	"iter"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// allMessagesPage is how many live messages an AllMessages iterator reads per
// page of a broker's Messages: large enough that a queue of thousands costs a
// handful of reads, small enough that a page's payloads stay a bounded heap.
const allMessagesPage int = 256

// checkSizes refuses a batch any of whose payloads is above maxBytes, naming
// the first one by its index, before anything of the batch is written.
func checkSizes(payloads [][]byte, maxBytes int) error {
	//: every payload, in order, so the refusal names the first offender.
	for index, payload := range payloads {
		//: MessageTooLarge, with both numbers, its index and no bytes.
		if tooLarge := checkSize(len(payload), maxBytes); tooLarge != nil {
			return kerrs.Wrap(tooLarge, kerrs.WrapParams{}, kerrs.Int("index", index))
		}
	}
	//: every payload fits.
	return nil
}

// pagedMessages is MessageLister.AllMessages over a broker's Messages: it
// threads the last identifier of each page into the next call, and stops on
// a short page, a failure — yielded once beside a zero value — or a loop
// that breaks. A cancelled ctx is the failure Messages answers with.
func pagedMessages(
	ctx context.Context, after string,
	page func(ctx context.Context, after string, max int) ([]corequeue.LiveMessageValue, error),
) iter.Seq2[corequeue.LiveMessageValue, error] {
	return func(yield func(corequeue.LiveMessageValue, error) bool) {
		cursor := after
		//: one page per round, until the broker has nothing after the cursor.
		for {
			live, err := page(ctx, cursor, allMessagesPage)
			//: the broker refused, or ctx ended: said once, then stopped.
			if err != nil {
				yieldFailure(yield, err)
				return
			}
			//: every message of the page, in identifier order.
			for _, message := range live {
				//: the caller broke out of its loop.
				if !yield(message, nil) {
					return
				}
			}
			//: a short page is the last one.
			if len(live) < allMessagesPage {
				return
			}
			cursor = live[len(live)-1].Message.ID
		}
	}
}

// refusedReceipt reports whether err is Ack's refusal of the receipt itself —
// a lapsed lease, or a receipt the broker never issued — rather than a
// failure of the call.
func refusedReceipt(err error) bool {
	//: the two refusals an acknowledgement reports as an outcome.
	return kerrs.HasCode(err, corequeue.CodeLeaseExpired) || kerrs.HasCode(err, corequeue.CodeUnknownReceipt)
}

// yieldFailure yields err once beside a zero value: the last thing a walk
// says, whatever the caller answers.
func yieldFailure[V any](yield func(V, error) bool, err error) {
	var zero V
	//: the only yield; there is nothing after it either way.
	if !yield(zero, err) {
		//: the caller stopped.
		return
	}
}
