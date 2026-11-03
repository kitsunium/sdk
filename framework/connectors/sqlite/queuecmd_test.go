package sqlite_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	sdksql "github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// The queue command (issue #1138) on a consumer whose queue a real SQLite
// database keeps: a dead letter of the subscription's table replayed into
// its live queue, another dropped, a second drop refused, and a consumer the
// product does not mount refused by name — the statements run by SQLite.
func TestTheQueueCommandActsOnASubscriptionsTableOnSQLite(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	first := newRegistry(t, dir, io.Discard, true)
	if err := first.app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := first.app.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}

	db := openMain(t, dir)
	tm, err := sdksql.NewTransactor(sdksql.Config{DB: db, Dialect: sdksql.DialectSQLite, Pool: sdksql.PoolConfig{MaxOpen: 1}})
	if err != nil {
		t.Fatal(err)
	}
	table, err := queue.NewSQL(queue.SQLConfig{
		Transactor: tm, Dialect: sdksql.DialectSQLite, Table: "app_queue__registry__filed",
		Policy: queue.Policy{VisibilityTimeout: time.Hour, MaxDeliveries: 5}, Clock: clock.System, Trail: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, payload := range []string{"replayed", "dropped"} {
		id, err := table.(queue.DeadLetterWriter).PutDeadLetter(t.Context(), queue.DeadLetter{
			Message: queue.Message{Payload: []byte(payload), EnqueuedAt: time.Now()}, FailedAt: time.Now(), Deliveries: 5, Reason: "HANDLER_FAILED",
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	_ = db.Close()

	run := func(args ...string) int {
		t.Helper()
		return newRegistry(t, dir, io.Discard, true).app.Main(t.Context(), args)
	}
	node := "registry/subscription/filed"
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"queue", "dead-letters"}, 0},
		{[]string{"queue", "dead-letters", node}, 0},
		{[]string{"queue", "dead-letters", "registry/subscription/nothing"}, 2},
		{[]string{"queue", "replay", node, ids[0]}, 0},
		{[]string{"queue", "replay", node, ids[0]}, 1},
		{[]string{"queue", "drop", node, ids[1]}, 0},
		{[]string{"queue", "drop", node, ids[1]}, 1},
	} {
		if got := run(c.args...); got != c.want {
			t.Fatalf("%q = %d, want %d", c.args, got, c.want)
		}
	}

	db = openMain(t, dir)
	tm, err = sdksql.NewTransactor(sdksql.Config{DB: db, Dialect: sdksql.DialectSQLite, Pool: sdksql.PoolConfig{MaxOpen: 1}})
	if err != nil {
		t.Fatal(err)
	}
	table, err = queue.NewSQL(queue.SQLConfig{
		Transactor: tm, Dialect: sdksql.DialectSQLite, Table: "app_queue__registry__filed",
		Policy: queue.Policy{VisibilityTimeout: time.Hour, MaxDeliveries: 5}, Clock: clock.System, Trail: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	live, err := table.(queue.MessageReader).Messages(t.Context(), "", 10)
	if err != nil || len(live) != 1 || string(live[0].Message.Payload) != "replayed" || live[0].Deliveries != 0 {
		t.Fatalf("the subscription's live queue = %+v, %v; want the replayed message, its attempts afresh", live, err)
	}
	if dead, err := table.(queue.DeadLetterReader).DeadLetters(t.Context(), 10); err != nil || len(dead) != 0 {
		t.Fatalf("the subscription's dead letters = %d, %v; want none", len(dead), err)
	}
}
