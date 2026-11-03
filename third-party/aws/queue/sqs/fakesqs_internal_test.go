package sqs

import (
	"context"
	"maps"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// fakeMessage is one message of the in-memory queue.
type fakeMessage struct {
	sent      time.Time
	visibleAt time.Time
	attrs     map[string]types.MessageAttributeValue
	id        string
	body      string
	handle    string
	receives  int
}

// fakeSQS answers the four requests the broker sends as SQS answers them,
// on a ManualClock: a message is invisible until its visibility deadline,
// every receive mints a new receipt handle and counts one more receive, and
// only the current handle deletes or changes a message in flight.
type fakeSQS struct {
	clock    clock.Clock
	queues   map[string][]*fakeMessage
	failWith error
	mu       sync.Mutex
	seq      int
}

// newFakeSQS returns an SQS with no message, on clk.
func newFakeSQS(clk clock.Clock) *fakeSQS {
	return &fakeSQS{clock: clk, queues: map[string][]*fakeMessage{}}
}

func (f *fakeSQS) SendMessage(_ context.Context, in *awssqs.SendMessageInput, _ ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	f.seq++
	now := f.clock.Now()
	m := &fakeMessage{
		id: "msg-" + strconv.Itoa(f.seq), body: aws.ToString(in.MessageBody), attrs: maps.Clone(in.MessageAttributes),
		sent: now, visibleAt: now.Add(time.Duration(in.DelaySeconds) * time.Second),
	}
	url := aws.ToString(in.QueueUrl)
	f.queues[url] = append(f.queues[url], m)
	return &awssqs.SendMessageOutput{MessageId: aws.String(m.id)}, nil
}

func (f *fakeSQS) ReceiveMessage(_ context.Context, in *awssqs.ReceiveMessageInput, _ ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	now := f.clock.Now()
	out := &awssqs.ReceiveMessageOutput{}
	for _, m := range f.queues[aws.ToString(in.QueueUrl)] {
		if len(out.Messages) == int(in.MaxNumberOfMessages) {
			break
		}
		if m.visibleAt.After(now) {
			continue
		}
		f.seq++
		m.receives++
		m.handle = "handle-" + strconv.Itoa(f.seq)
		m.visibleAt = now.Add(time.Duration(in.VisibilityTimeout) * time.Second)
		out.Messages = append(out.Messages, types.Message{
			MessageId: aws.String(m.id), ReceiptHandle: aws.String(m.handle), Body: aws.String(m.body),
			MessageAttributes: maps.Clone(m.attrs),
			Attributes: map[string]string{
				string(types.MessageSystemAttributeNameApproximateReceiveCount): strconv.Itoa(m.receives),
				string(types.MessageSystemAttributeNameSentTimestamp):           strconv.FormatInt(m.sent.UnixMilli(), 10),
			},
		})
	}
	return out, nil
}

func (f *fakeSQS) DeleteMessage(_ context.Context, in *awssqs.DeleteMessageInput, _ ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	url := aws.ToString(in.QueueUrl)
	for i, m := range f.queues[url] {
		if m.handle == aws.ToString(in.ReceiptHandle) {
			f.queues[url] = append(f.queues[url][:i], f.queues[url][i+1:]...)
			return &awssqs.DeleteMessageOutput{}, nil
		}
	}
	return nil, &types.ReceiptHandleIsInvalid{Message: aws.String("unknown handle")}
}

func (f *fakeSQS) ChangeMessageVisibility(_ context.Context, in *awssqs.ChangeMessageVisibilityInput, _ ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	now := f.clock.Now()
	for _, m := range f.queues[aws.ToString(in.QueueUrl)] {
		if m.handle == aws.ToString(in.ReceiptHandle) && m.visibleAt.After(now) {
			m.visibleAt = now.Add(time.Duration(in.VisibilityTimeout) * time.Second)
			return &awssqs.ChangeMessageVisibilityOutput{}, nil
		}
	}
	return nil, &types.MessageNotInflight{Message: aws.String("not in flight")}
}

// depth is how many messages url holds, visible or not.
func (f *fakeSQS) depth(url string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queues[url])
}

// fail makes every later request fail with err.
func (f *fakeSQS) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failWith = err
}
