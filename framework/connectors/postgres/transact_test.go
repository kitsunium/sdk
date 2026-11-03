package postgres_test

import (
	"context"
	stdsql "database/sql"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/connectors/postgres"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// A transaction's options and its retries on PostgreSQL, asserted on what
// the server is sent (#1140, #1000): the connector's pool behind a tracer
// that records every statement its connections send — BEGIN included, which
// pgx writes with the level and the access mode the transaction asked for.

// statementLog records the statements a pool's connections send.
type statementLog struct {
	mu   sync.Mutex
	sent []string
}

// TraceQueryStart records the statement.
func (l *statementLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sent = append(l.sent, strings.ToLower(d.SQL))
	return ctx
}

// TraceQueryEnd records nothing.
func (*statementLog) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// count is how many statements sent begin with prefix.
func (l *statementLog) count(prefix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.sent {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

// tracedEngine is the connector's engine with its pool behind log: Open
// parses the URL as the connector does and sets the tracer. Transient is
// the connector's own, so a retry is decided as it is in production.
type tracedEngine struct {
	app.Engine
	log *statementLog
}

// Open opens the pool the URL names, every statement recorded.
func (e tracedEngine) Open(u secret.Value, _ func(context.Context) (secret.Value, error)) (*stdsql.DB, error) {
	cfg, err := pgx.ParseConfig(u.RevealString())
	if err != nil {
		return nil, err
	}
	cfg.Tracer = e.log
	return stdlib.OpenDB(*cfg), nil
}

// Transient is the connector's reading of a failure.
func (e tracedEngine) Transient(err error) bool {
	c, ok := e.Engine.(app.Classifier)
	return ok && c.Transient(err)
}

// counter is what the traced ledger keeps: a count each transaction bumps.
type counter struct {
	ID string `json:"id"`
	N  int    `json:"n"`
}

// bump adds one to a counter.
func bump(c *counter) error { c.N++; return nil }

// tracedLedger runs a ledger of counters on a fresh database, its pool
// behind log, until the test ends.
func tracedLedger(t *testing.T, log *statementLog) *app.Store[counter] {
	t.Helper()
	t.Setenv("LEDGER_DATABASE_URL", fresh(t))
	t.Setenv("LEDGER_SECRETS", "memory")
	t.Setenv("LEDGER_DATABASE_TX_RETRY_DELAY", "10ms")
	books := app.NewService("books", "The counters.")
	counters := books.Store("counters", func(c counter) string { return c.ID })
	run(t, app.NewApp("ledger", books).With(
		app.Database("database", tracedEngine{Engine: postgres.Engine(), log: log}),
		app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.DataDir(t.TempDir()), app.Logs(io.Discard),
	))
	return counters
}

// TestATransactionsOptionsReachPostgres is #1140 on a server: Serializable
// opens the transaction SERIALIZABLE and ReadOnly opens it READ ONLY, as the
// statements sent say, and a write inside a read-only one is refused before
// it reaches the server.
func TestATransactionsOptionsReachPostgres(t *testing.T) {
	server(t)
	log := &statementLog{}
	counters := tracedLedger(t, log)
	ctx := t.Context()
	if err := app.Transact(ctx, func(ctx context.Context) error {
		return counters.Put(ctx, counter{ID: "a"})
	}, app.Serializable()); err != nil {
		t.Fatal(err)
	}
	if err := app.Transact(ctx, func(ctx context.Context) error {
		_, err := counters.Get(ctx, "a")
		return err
	}, app.ReadOnly()); err != nil {
		t.Fatal(err)
	}
	err := app.Transact(ctx, func(ctx context.Context) error {
		return counters.Put(ctx, counter{ID: "b"})
	}, app.ReadOnly())
	if !errs.HasCode(err, app.CodeTransactionReadOnly) {
		t.Fatalf("a write in a read-only transaction: %v", err)
	}
	if log.count("begin isolation level serializable") != 1 || log.count("begin read only") != 2 {
		t.Fatalf("the server was told %v; want one SERIALIZABLE BEGIN and two READ ONLY", log.sent)
	}
}

// TestADeadlockedTransactionIsRetried is #1000 on a server: two
// transactions updating two counters in opposite orders deadlock,
// PostgreSQL ends one with SQLSTATE 40P01, and the framework runs it again:
// both commit, each counter bumped twice, three BEGINs sent.
func TestADeadlockedTransactionIsRetried(t *testing.T) {
	server(t)
	log := &statementLog{}
	counters := tracedLedger(t, log)
	ctx := t.Context()
	for _, id := range []string{"a", "b"} {
		if err := counters.Put(ctx, counter{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	before := log.count("begin")
	var arrived atomic.Int32
	both := make(chan struct{})
	cross := func(first, second string) error {
		return app.Transact(ctx, func(ctx context.Context) error {
			if _, err := counters.Update(ctx, first, bump); err != nil {
				return err
			}
			if arrived.Add(1) == 2 {
				close(both)
			}
			select {
			case <-both:
			case <-time.After(5 * time.Second):
			}
			_, err := counters.Update(ctx, second, bump)
			return err
		})
	}
	var wg sync.WaitGroup
	results := make([]error, 2)
	wg.Go(func() { results[0] = cross("a", "b") })
	wg.Go(func() { results[1] = cross("b", "a") })
	wg.Wait()
	for i, err := range results {
		if err != nil {
			t.Fatalf("transaction %d: %v", i, err)
		}
	}
	for _, id := range []string{"a", "b"} {
		if c, err := counters.Get(ctx, id); err != nil || c.N != 2 {
			t.Fatalf("counter %s: %+v %v, want bumped twice", id, c, err)
		}
	}
	if begun := log.count("begin") - before; begun < 3 {
		t.Fatalf("%d BEGINs for two transactions, want a third for the retried one", begun)
	}
}
