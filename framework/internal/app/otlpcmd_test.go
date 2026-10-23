package app_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// The OTLP export's dead letters from the product's CLI (issue #536): a
// batch the collector refuses for good is listed — never its payload —,
// replayed into the live queue, which the next export sends, or dropped;
// on kit's fake database, so on every platform, with the data directory's
// queues beside them where the SDK's file store exists.

// otlpRun runs one command of `otlp` on a, and returns its status and
// streams.
func otlpRun(t *testing.T, a *app.App, args ...string) (status int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	status = a.OTLPCommand(t.Context(), args, &out, &errb)
	return status, out.String(), errb.String()
}

// refusing is a sender whose collector refuses every batch for good — a
// 400 —, and counts the traces it refused.
func refusing(traces *atomic.Int64) func(context.Context, app.OTLPBatchValue) error {
	return func(_ context.Context, b app.OTLPBatchValue) error {
		if b.Signal == app.OTLPTraces {
			traces.Add(1)
		}
		return app.NewError(http.StatusBadRequest, app.WireInvalid, "refused for good")
	}
}

// deadLetterTrace runs a process of the ping service on db whose collector
// refuses a ping's span for good, and stops it once the batch is
// dead-lettered.
func deadLetterTrace(t *testing.T, db *app.FakeDB, clk *drivenClock) {
	t.Helper()
	var refused atomic.Int64
	a := otlpDatabaseApp(t, db, clk, refusing(&refused), app.OTLPDatabase("database"))
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, a, "GET /orders/ping", noBody), http.StatusNoContent, "")
	// The runtime state counts it (issue #535) once its queue buried it.
	clk.drive(t, "the span's batch dead-lettered", func() bool {
		ex := a.Graph().Runtime.Export
		return refused.Load() > 0 && ex != nil && len(ex.Signals) > 0 && ex.Signals[0].DeadLettered == 1
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

// deadLetterIDs are the IDs of the traces' dead letters a listing shows.
func deadLetterIDs(listing string) []string {
	var out []string
	for line := range strings.Lines(listing) {
		if fields := strings.Split(strings.TrimSpace(line), "\t"); len(fields) > 2 && fields[0] == app.OTLPTraces {
			out = append(out, fields[1])
		}
	}
	return out
}

// A batch the collector refused for good is listed by `otlp dead-letters`
// — its signal, ID, instant, attempts and size, the failure's public text,
// never its payload —, put back by `otlp replay traces ID` into the live
// queue, which the next process's export sends, and gone from the listing.
func TestAnOTLPDeadLetterIsListedThenReplayed(t *testing.T) {
	db := app.NewFakeDB(sql.DialectPostgres)
	clk := newDrivenClock()
	deadLetterTrace(t, db, clk)
	cli := otlpDatabaseApp(t, db, clk, newSent().send, app.OTLPDatabase("database"))
	status, out, errOut := otlpRun(t, cli, "dead-letters")
	ids := deadLetterIDs(out)
	if status != 0 || len(ids) != 1 || !strings.Contains(out, "attempts") || !strings.Contains(out, " bytes") || !strings.Contains(out, "0.4.2.90\tOTLP_REFUSED") || !strings.Contains(out, "the product's OTLP sender refused the batch for good") || !strings.Contains(out, "\ttrail ") {
		t.Fatalf("dead-letters: %d\n%s%s", status, out, errOut)
	}
	if strings.Contains(out, "orders/endpoint/ping") || strings.Contains(out, "resourceSpans") {
		t.Errorf("the listing shows the payload:\n%s", out)
	}
	if status, out, errOut := otlpRun(t, cli, "replay", "traces", ids[0]); status != 0 || !strings.Contains(out, "replayed 1 dead letter(s) of traces") {
		t.Fatalf("replay: %d\n%s%s", status, out, errOut)
	}
	if status, out, _ := otlpRun(t, cli, "dead-letters", "traces"); status != 0 || !strings.Contains(out, "no dead letter in the OTLP export's queues: traces") {
		t.Errorf("after the replay, dead-letters says %d\n%s", status, out)
	}
	send := newSent()
	next := otlpDatabaseApp(t, db, clk, send.send, app.OTLPDatabase("database"))
	if err := next.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	stopOnCleanup(t, next)
	clk.drive(t, "the replayed batch sent", func() bool {
		_, holding := send.count("traces", "ping")
		return holding > 0
	})
}

// `otlp replay --drop` deletes every dead letter for good: none is listed
// after it, and none is sent by the next process.
func TestAnOTLPDeadLetterIsDropped(t *testing.T) {
	db := app.NewFakeDB(sql.DialectPostgres)
	clk := newDrivenClock()
	deadLetterTrace(t, db, clk)
	cli := otlpDatabaseApp(t, db, clk, newSent().send, app.OTLPDatabase("database"))
	if status, out, errOut := otlpRun(t, cli, "replay", "--drop"); status != 0 ||
		!strings.Contains(out, "dropped 1 dead letter(s) of traces") || !strings.Contains(out, "dropped 0 dead letter(s) of metrics") {
		t.Fatalf("replay --drop: %d\n%s%s", status, out, errOut)
	}
	if status, out, _ := otlpRun(t, cli, "dead-letters"); status != 0 || len(deadLetterIDs(out)) != 0 {
		t.Errorf("after the drop, dead-letters says %d\n%s", status, out)
	}
	for _, m := range db.Messages("app_otlp__traces") {
		if strings.Contains(m, "ping") {
			t.Errorf("the dropped batch is still in its table: %q", m)
		}
	}
}

// A dead letter the queues do not hold is said on standard error and fails
// the command; a usage error is status 2; an export that keeps its queues
// in memory has none to read; a database the app does not declare stops the
// command as it stops the start.
func TestTheOTLPCommandRefuses(t *testing.T) {
	db := app.NewFakeDB(sql.DialectPostgres)
	clk := newDrivenClock()
	onDatabase := otlpDatabaseApp(t, db, clk, newSent().send, app.OTLPDatabase("database"))
	if status, _, errOut := otlpRun(t, onDatabase, "replay", "traces", "nope"); status != 1 || !strings.Contains(errOut, "no dead letter nope in the OTLP export's queues") {
		t.Errorf("an unknown dead letter: %d %s", status, errOut)
	}
	for _, args := range [][]string{nil, {"frob"}, {"dead-letters", "logs"}, {"dead-letters", "--drop"}, {"replay", "--force"}} {
		if status, _, errOut := otlpRun(t, onDatabase, args...); status != 2 || !strings.Contains(errOut, "usage:") {
			t.Errorf("otlp %q: %d %s", args, status, errOut)
		}
	}
	inMemory := otlpDatabaseApp(t, db, clk, newSent().send)
	if status, _, errOut := otlpRun(t, inMemory, "dead-letters"); status != 1 || !strings.Contains(errOut, "keeps its queues in memory") {
		t.Errorf("an export in memory: %d %s", status, errOut)
	}
	nowhere := otlpDatabaseApp(t, db, clk, newSent().send, app.OTLPDatabase("nowhere"))
	if status, _, errOut := otlpRun(t, nowhere, "dead-letters"); status != 78 || !strings.Contains(errOut, "names nowhere, a database the app does not declare") {
		t.Errorf("a database the app does not declare: %d %s", status, errOut)
	}
}

// With a data directory and no otlp-database, the command reads the queues
// under <data>/.otlp, as the export keeps them. A platform whose file store
// the SDK refuses refuses the data directory first: needsFileStore asserts
// that refusal, then skips.
func TestTheOTLPCommandReadsTheDataDirectory(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	clk := newDrivenClock()
	var refused atomic.Int64
	product := func(send func(context.Context, app.OTLPBatchValue) error) *app.App {
		t.Setenv("SHOP_SMTP_URL", "")
		return app.NewApp("shop", pingService()).With(app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false),
			app.Logs(io.Discard), app.Clock(clk), app.DataDir(dir),
			app.OTLP(app.OTLPSender(send), app.OTLPSendTimeout(10*time.Hour)),
			app.Set("otlp-drain", time.Duration(0)), app.Set("otlp-interval", 100*time.Hour))
	}
	a := product(refusing(&refused))
	if err := a.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	expectAnswer(t, call(t, a, "GET /orders/ping", noBody), http.StatusNoContent, "")
	clk.drive(t, "the span's batch refused for good", func() bool { return refused.Load() > 0 })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if status, out, errOut := otlpRun(t, product(newSent().send), "dead-letters", "traces"); status != 0 || len(deadLetterIDs(out)) != 1 {
		t.Errorf("dead-letters under the data directory: %d\n%s%s", status, out, errOut)
	}
}
