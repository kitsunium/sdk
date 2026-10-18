package app

import (
	"testing"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/internal/app/queueengine"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
)

// sdkQueue is the SDK's broker behind b, a queue the queue subsystem opened,
// as it hands it on to a mailer's spool (queueengine.BrokerOf): what a test
// leases, nacks and reads the dead letters of as the SDK's queue does.
func sdkQueue(t *testing.T, b plug.Broker) queue.Broker {
	t.Helper()
	q, ok := queueengine.BrokerOf(b)
	if !ok {
		t.Fatalf("the queue subsystem hands on no SDK broker behind %T", b)
	}
	return q
}
