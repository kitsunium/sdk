//go:build integration

// The Valkey stream broker (third-party/db/redis/queue) against a real
// Valkey: the queue conformance rows on a ManualClock — the broker's leases
// are its own clock's, so a row drives every deadline without sleeping —,
// the race of two consumers over a thousand messages, and the group
// created on first use. Run from the e2e module:
//
//	cd e2e && GOWORK=off go test -tags integration -race ./integration/queue/redis/
//
// VALKEY_ADDR names a running server instead of a container.
package redis_test

import (
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"

	"github.com/kitsunium/sdk/e2e/integration/internal/servers"
	"github.com/kitsunium/sdk/e2e/integration/queue/brokerrows"
	rdsqueue "github.com/kitsunium/sdk/third-party/db/redis/queue"
)

// streamSeq names one stream per row.
var streamSeq atomic.Int64

func client(t *testing.T, addr string) goredis.UniversalClient {
	t.Helper()
	c := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestIntegration_ValkeyStreamBroker_conformance(t *testing.T) {
	addr := servers.Valkey(t)
	rdb := client(t, addr)
	brokerrows.Run(t, func(t *testing.T, policy corequeue.PolicyValue) brokerrows.Env {
		t.Helper()
		clk := clock.NewManualClock(time.Now())
		stream := "jobs-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatInt(streamSeq.Add(1), 10)
		build := func() corequeue.Broker {
			broker, err := rdsqueue.NewStream(rdsqueue.StreamConfig{
				Client: rdb, Stream: stream, Group: "workers", Policy: policy, Clock: clk, CreateGroup: true,
			})
			if err != nil {
				t.Fatalf("NewStream = %v", err)
			}
			return broker
		}
		return brokerrows.Env{Broker: build(), Advance: clk.Advance, Sibling: build}
	})
}

func TestIntegration_ValkeyStreamBroker_groupMissing(t *testing.T) {
	rdb := client(t, servers.Valkey(t))
	broker, err := rdsqueue.NewStream(rdsqueue.StreamConfig{
		Client: rdb, Stream: "nogroup-" + strconv.FormatInt(time.Now().UnixNano(), 36), Group: "workers",
		Policy: brokerrows.Policy(),
	})
	if err != nil {
		t.Fatalf("NewStream = %v", err)
	}
	if _, err := broker.Receive(t.Context(), 1); !errs.HasCode(err, rdsqueue.CodeRedisQueueGroupMissing) {
		t.Fatalf("Receive without the group = %v, want GROUP_MISSING", err)
	}
}

func TestIntegration_ValkeyStreamBroker_wakesOnItsOwnPublication(t *testing.T) {
	rdb := client(t, servers.Valkey(t))
	broker, err := rdsqueue.NewStream(rdsqueue.StreamConfig{
		Client: rdb, Stream: "wake-" + strconv.FormatInt(time.Now().UnixNano(), 36), Group: "workers",
		Policy: brokerrows.Policy(), CreateGroup: true,
	})
	if err != nil {
		t.Fatalf("NewStream = %v", err)
	}
	wake := broker.(corequeue.Waker).Wake()
	if _, err := broker.Publish(t.Context(), []byte("x")); err != nil {
		t.Fatalf("Publish = %v", err)
	}
	select {
	case <-wake.Signal:
	case <-time.After(time.Second):
		t.Fatal("a publication did not close the signal")
	}
}
