package mysql_test

import (
	"context"
	stdsql "database/sql"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/connectors/mysql"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// The queue command (issue #1138) on a consumer whose queue MySQL keeps: a
// subscription that fails at once dead-letters what it is sent, in its
// table; `queue replay` puts one back into the live queue, its attempts
// counted afresh, `queue drop` deletes the other, a second drop is refused,
// and a consumer the product does not mount is refused by name. Skipped
// without a test server, as every test here.

// The statements the test reads the subscription's table with.
const (
	queueDeadIDs   = "SELECT id FROM app_queue__books__filed WHERE dead = 1 ORDER BY id"
	queueRowOf     = "SELECT dead, deliveries FROM app_queue__books__filed WHERE id = ?"
	queueCommandID = "books/subscription/filed"
)

// filedLetter is what the subscription is sent.
type filedLetter struct {
	ID string `json:"id"`
}

// queueLedger is the product with a topic and a subscription whose handler
// fails at once, its queue on the database, its data in dir.
func queueLedger(t *testing.T, dir string) (*app.App, *app.Topic[filedLetter]) {
	t.Helper()
	books := app.NewService("books", "The books, and what is filed.")
	books.Store("entries", func(e entry) string { return e.ID })
	filed := books.Topic[filedLetter]("filed-letters")
	books.Subscribe("filed", filed, func(context.Context, filedLetter) error {
		return errors.New("the handler refuses")
	}, app.MaxDeliveries(1))
	a := app.NewApp("ledger", books).With(
		app.Database("database", mysql.Engine()), app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction),
		app.DataDir(dir), app.Logs(io.Discard),
	)
	return a, filed
}

func TestTheQueueCommandActsOnASubscriptionsTableOnMySQL(t *testing.T) {
	t.Setenv("LEDGER_DATABASE_URL", fresh(t))
	t.Setenv("LEDGER_SECRETS", "memory")
	dir := t.TempDir()
	first, filed := queueLedger(t, dir)
	if err := first.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	for _, id := range []string{"l1", "l2"} {
		if err := filed.Publish(t.Context(), filedLetter{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	pool := kitTestPool(t)
	var ids []string
	deadline := time.Now().Add(20 * time.Second)
	for len(ids) < 2 && time.Now().Before(deadline) {
		ids = deadIDs(t, pool)
		time.Sleep(50 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := first.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("dead letters = %v, want the two letters the handler refused", ids)
	}
	for _, c := range []struct {
		args []string
		want int
	}{
		{[]string{"queue", "dead-letters"}, 0},
		{[]string{"queue", "dead-letters", "books/subscription/nothing"}, 2},
		{[]string{"queue", "replay", queueCommandID, ids[0]}, 0},
		{[]string{"queue", "replay", queueCommandID, ids[0]}, 1},
		{[]string{"queue", "drop", queueCommandID, ids[1]}, 0},
		{[]string{"queue", "drop", queueCommandID, ids[1]}, 1},
	} {
		product, _ := queueLedger(t, dir)
		if got := product.Main(t.Context(), c.args); got != c.want {
			t.Fatalf("%q = %d, want %d", c.args, got, c.want)
		}
	}
	var dead, deliveries int64
	if err := pool.QueryRowContext(t.Context(), queueRowOf, ids[0]).Scan(&dead, &deliveries); err != nil || dead != 0 || deliveries != 0 {
		t.Fatalf("the replayed row = dead %d, %d deliveries, %v; want live, its attempts afresh", dead, deliveries, err)
	}
	if err := pool.QueryRowContext(t.Context(), queueRowOf, ids[1]).Scan(&dead, &deliveries); !errors.Is(err, stdsql.ErrNoRows) {
		t.Fatalf("the dropped row = %v; want none", err)
	}
}

// deadIDs are the identifiers of the subscription's dead letters, none while
// its table is not there yet.
func deadIDs(t *testing.T, pool *stdsql.DB) []string {
	t.Helper()
	rows, err := pool.QueryContext(t.Context(), queueDeadIDs)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// kitTestPool is a pool on kit_test as the product reaches it, opened by the
// engine itself.
func kitTestPool(t *testing.T) *stdsql.DB {
	t.Helper()
	v := secret.FromString(testURL(t, nil))
	pool, err := mysql.Engine().Open(v, func(context.Context) (secret.Value, error) { return v, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Logf("closing the pool: %v", err)
		}
	})
	return pool
}
