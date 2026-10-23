package app_test

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// The OTLP export's queues on the product's database (issue #537): the
// setting otlp-database, a table per signal made by the framework's own
// migrations, and a backlog that outlives a restart with no data directory —
// on kit's fake database, so on every platform: nothing here touches a disk.

// otlpTraces and otlpMetrics are the export's tables on its database.
const (
	otlpTracesTable  = "app_otlp__traces"
	otlpMetricsTable = "app_otlp__metrics"
)

// otlpDatabaseApp is the ping service in production with no data
// directory — a container with no volume —, its secrets in memory, the
// fake database db declared as "database", and an export to send whose
// queues otlp-database places.
func otlpDatabaseApp(t *testing.T, db *app.FakeDB, clk *drivenClock, send func(context.Context, app.OTLPBatchValue) error, opts ...app.OpenTelemetryConfigurer) *app.App {
	t.Helper()
	t.Setenv("SHOP_SECRETS", "memory")
	t.Setenv("SHOP_SMTP_URL", "")
	t.Setenv("SHOP_DATABASE_URL", fakeURL("shop", "pw", "tls=verify-full"))
	return app.NewApp("shop", pingService()).With(
		app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.Analyze(false), app.Logs(io.Discard), app.Clock(clk),
		app.Database("database", db.Engine()),
		app.OTLP(append([]app.OpenTelemetryConfigurer{app.OTLPSender(send), app.OTLPSendTimeout(10 * time.Hour)}, opts...)...),
		app.Set("otlp-drain", time.Duration(0)), app.Set("otlp-interval", 100*time.Hour),
	)
}

// stopOnCleanup stops a at the end of the test.
func stopOnCleanup(t *testing.T, a *app.App) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
}

// With otlp-database, each signal's queue is a table of the database the
// framework's own migrations made: a batch the collector cannot take waits
// in it, and a second process on the same database — no data directory in
// either — sends it. The case without the setting is its control: the
// queues are in memory, the database holds no table of the export's, and
// the second process sends nothing of the first's.
func TestTheOTLPQueuesLieOnTheDatabase(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    []app.OpenTelemetryConfigurer
		durable bool
	}{{"on the database", []app.OpenTelemetryConfigurer{app.OTLPDatabase("database")}, true}, {"in memory", nil, false}} {
		t.Run(tc.name, func(t *testing.T) {
			db := app.NewFakeDB(sql.DialectPostgres)
			clk := newDrivenClock()
			var refused atomic.Int64
			down := func(context.Context, app.OTLPBatchValue) error {
				refused.Add(1)
				return app.NewError(http.StatusServiceUnavailable, app.WireUnavailable, "down")
			}
			first := otlpDatabaseApp(t, db, clk, down, tc.opts...)
			if err := first.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			expectAnswer(t, call(t, first, "GET /orders/ping", noBody), http.StatusNoContent, "")
			clk.drive(t, "the span's batch refused by a collector that is down", func() bool { return refused.Load() > 0 })
			// The runtime state says where the queues lie (issue #535).
			wantStore := "memory"
			if tc.durable {
				wantStore = "database:database"
			}
			if ex := first.Graph().Runtime.Export; ex == nil || ex.State != model.ExportRunning || len(ex.Signals) != 2 ||
				ex.Signals[0].Store != wantStore || ex.Signals[0].Durable != tc.durable {
				t.Errorf("the runtime state of the export: %+v", ex)
			}
			tables := db.QueueTables()
			if has := slices.Contains(tables, otlpTracesTable) && slices.Contains(tables, otlpMetricsTable); has != tc.durable {
				t.Fatalf("the export's tables on the database: %v, want %v", tables, tc.durable)
			}
			// The framework's registry records them: its own migrations
			// made them, and give them their versions at the next start.
			registry := db.Documents("app_tables")
			if _, has := registry[otlpTracesTable]; has != tc.durable {
				t.Errorf("the framework's registry of its tables: %v", registry)
			}
			if tc.durable && !slices.ContainsFunc(db.Messages(otlpTracesTable), func(m string) bool { return strings.Contains(m, "ping") }) {
				t.Errorf("the span's batch is not waiting in %s: %q", otlpTracesTable, db.Messages(otlpTracesTable))
			}
			stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := first.Stop(stopCtx); err != nil {
				t.Fatal(err)
			}
			send := newSent()
			second := otlpDatabaseApp(t, db, clk, send.send, tc.opts...)
			if err := second.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			stopOnCleanup(t, second)
			if !tc.durable {
				// Nothing to wait for: the first process's backlog was in
				// memory. A span of the second's own is sent, its first
				// batch, and it is not the first's.
				expectAnswer(t, call(t, second, "GET /orders/ping", noBody), http.StatusNoContent, "")
				clk.drive(t, "the second process's own span sent", func() bool { all, _ := send.count("traces"); return all > 0 })
				if all, _ := send.count("traces"); all != 1 {
					t.Errorf("in memory, the second process sent %d batches of traces, want its own only", all)
				}
				return
			}
			clk.drive(t, "the first process's backlog sent by the second", func() bool {
				_, holding := send.count("traces", "ping")
				return holding > 0
			})
		})
	}
}

// withOTLPDatabase are opts with the database a case names, on a fake
// database: the app keeps its data in memory, so it never opens.
func withOTLPDatabase(name string, opts ...app.AppConfigurer) []app.AppConfigurer {
	return append(opts, app.Database(name, app.NewFakeDB(sql.DialectPostgres).Engine()))
}

// otlp-database is a setting of the framework's: empty by default, the
// code's value its default, overridden by a configuration file and by the
// variable; a key misspelled is refused as any key no service declares.
func TestTheOTLPDatabaseSetting(t *testing.T) {
	t.Run("by default", func(t *testing.T) {
		a, err := startPolicies(t, []*app.Service{pingService()}, app.OTLP())
		if err != nil {
			t.Fatal(err)
		}
		if s := settingOf(t, a, "SHOP_OTLP_DATABASE"); s.Key != "otlp-database" || s.Type != "text" || s.From != model.SettingDefault || s.Value != "" {
			t.Errorf("%+v, want an empty text by default", s)
		}
	})
	t.Run("from the code", func(t *testing.T) {
		a, err := startPolicies(t, []*app.Service{pingService()}, withOTLPDatabase("events", app.OTLP(app.OTLPDatabase("events")))...)
		if err != nil {
			t.Fatal(err)
		}
		if s := settingOf(t, a, "SHOP_OTLP_DATABASE"); s.From != model.SettingDefault || s.Value != "events" {
			t.Errorf("%+v, want the code's events", s)
		}
	})
	t.Run("from a file", func(t *testing.T) {
		a, err := startPolicies(t, []*app.Service{pingService()}, withOTLPDatabase("audit", app.OTLP(app.OTLPDatabase("events")),
			app.ConfigFiles(fstest.MapFS{"config/dev.json": {Data: []byte(`{"otlp-database": "audit"}`)}}))...)
		if err != nil {
			t.Fatal(err)
		}
		if s := settingOf(t, a, "SHOP_OTLP_DATABASE"); s.From != model.SettingFile || s.Detail != "config/dev.json" || s.Value != "audit" {
			t.Errorf("%+v, want the file's audit", s)
		}
	})
	t.Run("from the variable", func(t *testing.T) {
		t.Setenv("SHOP_OTLP_DATABASE", "ledger")
		a, err := startPolicies(t, []*app.Service{pingService()}, withOTLPDatabase("ledger", app.OTLP(app.OTLPDatabase("events")))...)
		if err != nil {
			t.Fatal(err)
		}
		if s := settingOf(t, a, "SHOP_OTLP_DATABASE"); s.From != model.SettingEnv || s.Value != "ledger" {
			t.Errorf("%+v, want the variable's ledger", s)
		}
	})
	t.Run("misspelled", func(t *testing.T) {
		_, err := startPolicies(t, []*app.Service{pingService()}, app.OTLP(),
			app.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"otlp-databases": "events"}`)}}))
		if err == nil || !strings.Contains(err.Error(), `config/config.json sets "otlp-databases", which no service declares`) {
			t.Errorf("a misspelled key: %v", err)
		}
	})
}

// An export that runs refuses a database the app does not declare — from
// the code or the variable alike —, and names its import when the program
// does not import framework/app/queue (otlp_internal_test.go).
func TestAnOTLPDatabaseTheAppDoesNotDeclareStopsTheStart(t *testing.T) {
	send := newSent()
	for _, tc := range []struct {
		name string
		opts []app.AppConfigurer
	}{
		{"in the code", []app.AppConfigurer{app.OTLP(app.OTLPSender(send.send), app.OTLPDatabase("nowhere"))}},
		{"in a file", []app.AppConfigurer{
			app.OTLP(app.OTLPSender(send.send)),
			app.ConfigFiles(fstest.MapFS{"config/config.json": {Data: []byte(`{"otlp-database": "nowhere"}`)}}),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := startPolicies(t, []*app.Service{pingService()}, tc.opts...)
			if err == nil || !strings.Contains(err.Error(), "the OTLP export's setting otlp-database names nowhere, a database the app does not declare") {
				t.Errorf("an export on a database the app does not declare: %v", err)
			}
		})
	}
}
