package app_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// The OTLP export switched from the data directory to the product's
// database (issue #580): the batches its file queues still hold are said at
// the start — logged and shown as a problem, in the product's language —,
// and `otlp migrate` moves them onto the database, where the export sends
// them. The file queues need the SDK's file store: needsFileStore asserts
// its refusal where the platform has none, then skips.

// otlpDiskApp is the ping service in production with its data directory
// dir, its secrets in memory, the fake database db declared as "database",
// and an export to send whose queues opts may place on the database.
func otlpDiskApp(t *testing.T, dir string, db *app.FakeDB, clk *drivenClock, logs io.Writer, send func(context.Context, app.OTLPBatchValue) error, opts ...app.OpenTelemetryConfigurer) *app.App {
	t.Helper()
	t.Setenv("SHOP_SECRETS", "memory")
	t.Setenv("SHOP_SMTP_URL", "")
	t.Setenv("SHOP_DATABASE_URL", fakeURL("shop", "pw", "tls=verify-full"))
	return app.NewApp("shop", pingService()).With(
		app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.Analyze(false), app.Logs(logs), app.Clock(clk), app.DataDir(dir),
		app.Database("database", db.Engine()),
		app.OTLP(append([]app.OpenTelemetryConfigurer{app.OTLPSender(send), app.OTLPSendTimeout(10 * time.Hour)}, opts...)...),
		app.Set("otlp-drain", time.Duration(0)), app.Set("otlp-interval", 100*time.Hour),
	)
}

// leaveOTLPBatchesOnDisk runs the product with its export's queues under
// dir: a first ping's batch refused for good — a dead letter —, a second's
// waiting for a collector that is down, and the stop's last snapshot of the
// metrics; it stops once both traces were tried.
func leaveOTLPBatchesOnDisk(t *testing.T, dir string, db *app.FakeDB, clk *drivenClock) {
	t.Helper()
	var traces atomic.Int64
	collector := func(_ context.Context, b app.OTLPBatchValue) error {
		if b.Signal == app.OTLPTraces && traces.Add(1) == 1 {
			return app.NewError(http.StatusBadRequest, app.WireInvalid, "refused for good")
		}
		return app.NewError(http.StatusServiceUnavailable, app.WireUnavailable, "down")
	}
	a := otlpDiskApp(t, dir, db, clk, io.Discard, collector)
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, a, "GET /orders/ping", noBody), http.StatusNoContent, "")
	clk.drive(t, "the first batch dead-lettered", func() bool {
		ex := a.Graph().Runtime.Export
		return ex != nil && len(ex.Signals) > 0 && ex.Signals[0].DeadLettered == 1
	})
	expectAnswer(t, call(t, a, "GET /orders/ping", noBody), http.StatusNoContent, "")
	clk.drive(t, "the second batch tried", func() bool { return traces.Load() > 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// A product that moves its export's queues onto its database with batches
// left under the data directory is told so at its start, signal by signal,
// with the command that moves them; `otlp migrate` moves the waiting batch
// and the dead letter onto the database and the file queues aside, after
// which the start says nothing, `otlp dead-letters` lists the dead letter
// from the database, and the export sends the waiting batch.
func TestAnOTLPSwitchToTheDatabaseWarnsThenMigrates(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	db := app.NewFakeDB(sql.DialectPostgres)
	clk := newDrivenClock()
	leaveOTLPBatchesOnDisk(t, dir, db, clk)

	logs := &syncBuffer{}
	switched := otlpDiskApp(t, dir, db, clk, logs, newSent().send, app.OTLPDatabase("database"))
	if err := switched.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, signal := range []string{app.OTLPTraces, app.OTLPMetrics} {
		want := "the OTLP export's " + signal + ` queue now lives on database \"database\"`
		if !strings.Contains(logs.String(), want) || !strings.Contains(logs.String(), "shop otlp migrate "+signal) {
			t.Errorf("the start does not say the %s left on disk:\n%s", signal, logs.String())
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := switched.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	cli := otlpDiskApp(t, dir, db, clk, io.Discard, newSent().send, app.OTLPDatabase("database"))
	status, out, errOut := otlpRun(t, cli, "migrate")
	if status != 0 || !strings.Contains(out, "traces: 2 batches moved onto app_otlp__traces, fingerprints found there") ||
		!strings.Contains(out, "batches moved onto app_otlp__metrics") {
		t.Fatalf("migrate: %d\n%s%s", status, out, errOut)
	}
	if moved, _ := filepath.Glob(filepath.Join(dir, ".data-copy", "*", ".otlp", app.OTLPTraces)); len(moved) != 1 {
		t.Errorf("the traces' file queue was not moved aside: %v", moved)
	}
	if status, out, _ := otlpRun(t, cli, "dead-letters", "traces"); status != 0 || len(deadLetterIDs(out)) != 1 {
		t.Errorf("the dead letter on the database: %d\n%s", status, out)
	}
	if status, out, _ := otlpRun(t, cli, "migrate", "traces"); status != 0 || !strings.Contains(out, "traces: no batch left under the data directory") {
		t.Errorf("a second migrate: %d\n%s", status, out)
	}

	logs = &syncBuffer{}
	send := newSent()
	next := otlpDiskApp(t, dir, db, clk, logs, send.send, app.OTLPDatabase("database"))
	if err := next.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	stopOnCleanup(t, next)
	clk.drive(t, "the batch moved sent", func() bool {
		_, holding := send.count("traces", "ping")
		return holding > 0
	})
	if strings.Contains(logs.String(), "now lives on database") {
		t.Errorf("the start still reports a file queue left behind:\n%s", logs.String())
	}
}

// A file queue `otlp migrate` cannot read is a failure, never an empty
// queue: a state directory that is a file — readable as no directory on
// every platform — fails the command instead of saying there is nothing to
// move.
func TestOTLPMigrateRefusesAnUnreadableFileQueue(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	traces := filepath.Join(dir, ".otlp", app.OTLPTraces)
	if err := os.MkdirAll(traces, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(traces, "ready"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	cli := otlpDiskApp(t, dir, app.NewFakeDB(sql.DialectPostgres), newDrivenClock(), io.Discard, newSent().send, app.OTLPDatabase("database"))
	if status, out, errOut := otlpRun(t, cli, "migrate", "traces"); status != 1 || !strings.Contains(errOut, "the OTLP export's file queue cannot be read") {
		t.Errorf("migrate over an unreadable file queue: %d\n%s%s", status, out, errOut)
	}
}

// `otlp migrate` needs otlp-database: without it, the export's queues are
// the data directory's and there is nothing to move them onto.
func TestOTLPMigrateNeedsTheDatabase(t *testing.T) {
	db := app.NewFakeDB(sql.DialectPostgres)
	clk := newDrivenClock()
	a := otlpDatabaseApp(t, db, clk, newSent().send)
	if status, _, errOut := otlpRun(t, a, "migrate"); status != 1 || !strings.Contains(errOut, "otlp-database names, and it names none") {
		t.Errorf("migrate without otlp-database: %d %s", status, errOut)
	}
	for _, args := range [][]string{{"migrate", "--drop"}, {"migrate", "traces", "id"}} {
		if status, _, errOut := otlpRun(t, a, args...); status != 2 || !strings.Contains(errOut, "usage:") {
			t.Errorf("otlp %q: %d %s", args, status, errOut)
		}
	}
}
