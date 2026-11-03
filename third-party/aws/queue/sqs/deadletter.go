package sqs

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// trailSep joins a trail's codes in attrTrail.
const trailSep string = ","

// deadLetter sends lease's message to the dead-letter queue with cause, then
// deletes it from the source queue. The two requests are not atomic: a crash
// between them leaves the message in both, and the source's copy is
// dead-lettered again once its lease lapses — a duplicate dead letter, never
// a lost message.
func (b *sqsBroker) deadLetter(ctx context.Context, lease *leaseValue, cause causeValue) error {
	_, serr := b.api.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl: aws.String(b.deadURL), MessageBody: aws.String(encodeBody(lease.message.Payload)),
		MessageAttributes: deadAttrs(lease, cause, b.clock.Now()),
	})
	//: nothing moved: the message stays in the source queue.
	if serr != nil {
		//: SEND_FAILED.
		return requestFailed(serr, SendFailed.Params(), "SendMessage(dead-letter)")
	}
	_, derr := b.api.DeleteMessage(ctx, &awssqs.DeleteMessageInput{QueueUrl: aws.String(b.queueURL), ReceiptHandle: aws.String(lease.handle)})
	//: dead-lettered, and still in the source: the duplicate stated above.
	if derr != nil {
		//: ACK_FAILED.
		return requestFailed(derr, AckFailed.Params(), "DeleteMessage")
	}
	//: moved.
	return nil
}

// deadAttrs are the attributes a dead letter carries: what DeadLetterValue
// holds besides the payload. SQS refuses an empty String, so an empty
// Reason, Cause or Trail is left out, and read back as empty.
func deadAttrs(lease *leaseValue, cause causeValue, failed time.Time) map[string]types.MessageAttributeValue {
	attrs := map[string]types.MessageAttributeValue{
		attrEncoding:   stringAttr(encodingBase64),
		attrEnqueued:   numberAttr(lease.message.EnqueuedAt.UnixNano()),
		attrID:         stringAttr(lease.message.ID),
		attrFailed:     numberAttr(failed.UnixNano()),
		attrCode:       numberAttr(int64(cause.code)),
		attrDeliveries: numberAttr(int64(lease.deliveries)),
	}
	//: the optional texts, each only when there is one.
	for name, value := range map[string]string{attrReason: cause.reason, attrCause: cause.public, attrTrail: encodeTrail(cause.trail)} {
		//: SQS refuses an empty value.
		if value != "" {
			attrs[name] = stringAttr(value)
		}
	}
	//: nine attributes at most: SQS allows ten.
	return attrs
}

// DeadLetters returns up to max dead letters, oldest first, without removing
// any: ReceiveMessage on the dead-letter queue with a zero visibility
// timeout, which hides nothing. SQS samples a standard queue, so a listing
// larger than a page may miss a dead letter another page would have shown.
func (b *sqsBroker) DeadLetters(ctx context.Context, max int) ([]corequeue.DeadLetterValue, error) {
	//: a batch is positive.
	if max <= 0 {
		//: INVALID_BATCH_SIZE.
		return nil, errs.Wrap(corequeue.InvalidBatchSize, errs.WrapParams{}, errs.Int("max", max))
	}
	seen := map[string]bool{}
	dead := make([]corequeue.DeadLetterValue, 0, min(max, MaxReceiveBatch))
	//: page by page until max, or a page that shows nothing new.
	for len(dead) < max {
		out, rerr := b.api.ReceiveMessage(ctx, b.receiveInput(b.deadURL, min(max-len(dead), MaxReceiveBatch), 0, 0))
		//: nothing read.
		if rerr != nil {
			//: RECEIVE_FAILED.
			return nil, requestFailed(rerr, ReceiveFailed.Params(), "ReceiveMessage(dead-letter)")
		}
		fresh := 0
		//: each dead letter once.
		for _, m := range out.Messages {
			//: a sample may repeat what an earlier page showed.
			if id := aws.ToString(m.MessageId); !seen[id] && len(dead) < max {
				seen[id] = true
				dead = append(dead, decodeDeadLetter(m))
				fresh++
			}
		}
		//: the queue showed nothing new.
		if fresh == 0 {
			break
		}
	}
	slices.SortStableFunc(dead, func(a, b corequeue.DeadLetterValue) int { return a.FailedAt.Compare(b.FailedAt) })
	//: oldest first.
	return dead, nil
}

// decodeDeadLetter reads back what deadAttrs wrote.
func decodeDeadLetter(m types.Message) corequeue.DeadLetterValue {
	message := decodeMessage(m)
	//: the source's identifier, not the dead-letter queue's.
	if id := attrString(m, attrID); id != "" {
		message.ID = id
	}
	//: every field of the record.
	return corequeue.DeadLetterValue{
		FailedAt: time.Unix(0, attrInt(m, attrFailed, 0)), Reason: attrString(m, attrReason),
		Cause: attrString(m, attrCause), Message: message, Trail: decodeTrail(attrString(m, attrTrail)),
		Code: uint32(attrInt(m, attrCode, 0)), Deliveries: int(attrInt(m, attrDeliveries, 0)),
	}
}

// encodeTrail writes a trail's codes in decimal, oldest first.
func encodeTrail(trail []uint32) string {
	parts := make([]string, len(trail))
	//: each code as Number attributes are written.
	for i, code := range trail {
		parts[i] = strconv.FormatUint(uint64(code), decimal)
	}
	//: "" for none.
	return strings.Join(parts, trailSep)
}

// decodeTrail reads back encodeTrail; a trail it cannot read whole is none.
func decodeTrail(text string) []uint32 {
	//: none written.
	if text == "" {
		return nil
	}
	parts := strings.Split(text, trailSep)
	trail := make([]uint32, 0, len(parts))
	//: each code a uint32, or the whole trail is dropped.
	for _, part := range parts {
		code, perr := strconv.ParseUint(part, decimal, 32)
		//: a corrupt field costs its own value only.
		if perr != nil {
			return nil
		}
		trail = append(trail, uint32(code))
	}
	//: oldest first.
	return trail
}
