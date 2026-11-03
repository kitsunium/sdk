package sqs

import (
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// The message attributes the broker writes. An SQS body is text, so the
// payload travels base64-encoded and attrEncoding says so: a message another
// producer wrote, without it, is delivered as its body's bytes.
const (
	attrEncoding   string = "kitsunium-encoding"
	attrEnqueued   string = "kitsunium-enqueued"
	attrID         string = "kitsunium-id"
	attrFailed     string = "kitsunium-failed"
	attrReason     string = "kitsunium-reason"
	attrCause      string = "kitsunium-cause"
	attrCode       string = "kitsunium-code"
	attrTrail      string = "kitsunium-trail"
	attrDeliveries string = "kitsunium-deliveries"
)

// encodingBase64 is attrEncoding's one value.
const encodingBase64 string = "base64"

// The SQS data types of an attribute.
const (
	dataString string = "String"
	dataNumber string = "Number"
)

// decimal is the base every number attribute is written in.
const decimal int = 10

// attrAll asks ReceiveMessage for every message attribute.
const attrAll string = "All"

// encodeBody is the SQS body of payload.
func encodeBody(payload []byte) string {
	//: standard base64: an SQS body accepts it whole.
	return base64.StdEncoding.EncodeToString(payload)
}

// stringAttr is a String message attribute.
func stringAttr(value string) types.MessageAttributeValue {
	//: SQS refuses an empty value; callers omit those.
	return types.MessageAttributeValue{DataType: aws.String(dataString), StringValue: aws.String(value)}
}

// numberAttr is a Number message attribute.
func numberAttr(value int64) types.MessageAttributeValue {
	//: decimal, as SQS reads a Number.
	return types.MessageAttributeValue{DataType: aws.String(dataNumber), StringValue: aws.String(strconv.FormatInt(value, decimal))}
}

// publishAttrs are the attributes of a publication enqueued at enqueued.
func publishAttrs(enqueued time.Time) map[string]types.MessageAttributeValue {
	//: the encoding and the broker's enqueue instant.
	return map[string]types.MessageAttributeValue{
		attrEncoding: stringAttr(encodingBase64),
		attrEnqueued: numberAttr(enqueued.UnixNano()),
	}
}

// decodeMessage reads the message the broker published from what SQS
// delivered: the payload decoded, the enqueue instant the broker stamped, or
// SQS's SentTimestamp for a message another producer sent.
func decodeMessage(m types.Message) corequeue.MessageValue {
	body := aws.ToString(m.Body)
	payload := []byte(body)
	//: a body this broker wrote is base64.
	if attrString(m, attrEncoding) == encodingBase64 {
		//: a body that does not decode is delivered as it is.
		if decoded, derr := base64.StdEncoding.DecodeString(body); derr == nil {
			payload = decoded
		}
	}
	enqueued := time.Unix(0, attrInt(m, attrEnqueued, 0))
	//: another producer's message: SQS's own instant, in milliseconds.
	if attrInt(m, attrEnqueued, 0) == 0 {
		enqueued = sentAt(m)
	}
	//: the identifier SQS minted.
	return corequeue.MessageValue{ID: aws.ToString(m.MessageId), Payload: payload, EnqueuedAt: enqueued}
}

// receiveCount is SQS's ApproximateReceiveCount: this delivery's number,
// counting from 1.
func receiveCount(m types.Message) int {
	count, perr := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	//: a count SQS did not send is the first delivery.
	if perr != nil || count < 1 {
		//: from 1, as Deliveries counts.
		return 1
	}
	//: as SQS counted.
	return count
}

// attrString is the string value of a message attribute, empty when absent.
func attrString(m types.Message, name string) string {
	//: as written; an absent attribute holds no string, which reads empty.
	return aws.ToString(m.MessageAttributes[name].StringValue)
}

// attrInt is the integer value of a Number attribute, fallback when absent
// or unreadable.
func attrInt(m types.Message, name string, fallback int64) int64 {
	value, perr := strconv.ParseInt(attrString(m, name), decimal, 64)
	//: absent or not ours.
	if perr != nil {
		return fallback
	}
	//: as written.
	return value
}

// sentAt is SQS's own instant for m, SentTimestamp in milliseconds: absent,
// or not a number, is the epoch; one out of range is the bound it passed, as
// ParseInt reads it.
func sentAt(m types.Message) time.Time {
	sent, perr := strconv.ParseInt(m.Attributes[string(types.MessageSystemAttributeNameSentTimestamp)], decimal, 64)
	//: not a number at all.
	if errors.Is(perr, strconv.ErrSyntax) {
		//: the epoch.
		return time.UnixMilli(0)
	}
	//: as SQS stamped it.
	return time.UnixMilli(sent)
}
