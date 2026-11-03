//go:build localstack

// The SQS broker against LocalStack: the conformance rows the in-memory SQS
// runs in every lane, on the wall clock and the emulator's own visibility
// timeouts. Excluded from every default lane by its tag (rule 12); run it as
// the writers' suites:
//
//	docker run --rm -p 4566:4566 localstack/localstack
//	cd third-party/aws && GOWORK=off go test -tags localstack -race ./queue/sqs/...
//
// LOCALSTACK_ENDPOINT overrides the endpoint (default http://localhost:4566).
// ElasticMQ answers the same requests on its own port.
package sqs

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

const localRegion = "eu-west-3"

// localstackEndpoint is where the emulator listens.
func localstackEndpoint() string {
	if e := os.Getenv("LOCALSTACK_ENDPOINT"); e != "" {
		return e
	}
	return "http://localhost:4566"
}

// queueSeq names two fresh queues per test.
var queueSeq atomic.Int64

// createQueue creates name and returns its URL.
func createQueue(t *testing.T, raw *awssqs.Client, name string) string {
	t.Helper()
	out, err := raw.CreateQueue(t.Context(), &awssqs.CreateQueueInput{QueueName: aws.String(name)})
	if err != nil {
		t.Fatalf("CreateQueue(%s): %v (is LocalStack running at %s?)", name, err, localstackEndpoint())
	}
	return aws.ToString(out.QueueUrl)
}

// localstackEnv builds the broker over a fresh queue and dead-letter queue;
// time moves by sleeping, since the emulator keeps the visibility timeouts.
func localstackEnv(t *testing.T, policy corequeue.PolicyValue) queueEnv {
	t.Helper()
	raw := awssqs.NewFromConfig(aws.Config{Region: localRegion, Credentials: credAdapter{provider: staticCreds{}}},
		func(o *awssqs.Options) { o.BaseEndpoint = aws.String(localstackEndpoint()) })
	name := fmt.Sprintf("kitsunium-%d-%d", time.Now().UnixNano()%1_000_000, queueSeq.Add(1))
	broker, err := New(Config{
		QueueURL: createQueue(t, raw, name), DeadLetterQueueURL: createQueue(t, raw, name+"-dlq"),
		Region: localRegion, Endpoint: localstackEndpoint(), Credentials: staticCreds{}, Policy: policy, Clock: clock.System,
	})
	if err != nil {
		t.Fatalf("New = %v", err)
	}
	return queueEnv{broker: broker, advance: time.Sleep}
}

func TestLocalStackPassesTheQueueConformanceRows(t *testing.T) {
	runQueueRows(t, localstackEnv)
}
