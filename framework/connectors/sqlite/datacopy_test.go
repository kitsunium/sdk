package sqlite_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/connectors/sqlite"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
)

// `data copy --to NAME` (ADR 0178 C7) on a real SQLite file: a product that
// kept its data in its directory — a store whose records are sealed where
// they rest, with their versions; a subscription's file queue holding a
// waiting, a retried and a dead message; a mailer's outbox holding a mail in
// clear — declares a SQLite database and copies its data into it. The copy
// is verified by fingerprints; the records come back opened, with their
// versions, a record the product deleted still in the trash at its instant;
// the queue's messages come across with their counts; the mail
// rests sealed in its table; the file queues are moved aside, and the start
// no longer reports them. kit's data keys stay in the data directory: a
// SQLite file never keeps them.

// dossier is what the registry keeps: its holder's name sealed where it
// rests.
type dossier struct {
	ID     string `json:"id"`
	Holder string `json:"holder" app:"subject"`
	Name   string `json:"name" app:"personal"`
	Status string `json:"status"`
}

// The mail the registry's outbox holds, in clear in the file queue, never in
// clear in the table.
const outboxMail = `{"to":"holder@registry.test","subject":"Your dossier"}`

// registry is a product that keeps dossiers, posts them and mails about
// them, declared afresh for each run.
type registry struct {
	app      *app.App
	dossiers *app.Store[dossier]
}

// newRegistry declares the registry, its data in dir, on SQLite when lite.
func newRegistry(t *testing.T, dir string, logs io.Writer, lite bool) *registry {
	t.Helper()
	t.Setenv("REGISTRY_SECRETS", "memory")
	t.Setenv("REGISTRY_DATA_KEY", "a data key of exactly 32 bytes..")
	t.Setenv("REGISTRY_INDEX_KEY", "an index key of more than 16 bytes")
	svc := app.NewService("registry", "Dossiers, kept, posted and mailed.")
	r := &registry{}
	r.dossiers = svc.Store("dossiers", func(d dossier) string { return d.ID }, app.Revisions(2), app.Purpose("Keep the dossiers"))
	posted := svc.Topic[dossier]("posted")
	svc.Subscribe("filed", posted, func(context.Context, dossier) error { return nil })
	svc.Mailer("notify", app.From("Registry", "registry@example.com"), app.MailAttempts(1))
	opts := []app.AppOption{app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.DataDir(dir), app.Logs(logs)}
	if lite {
		opts = append(opts, app.Database("main", sqlite.Engine()))
	}
	r.app = app.NewApp("registry", svc).With(opts...)
	return r
}

// fileQueue opens the file queue under dir, as the SDK keeps it.
func fileQueue(t *testing.T, dir string) queue.Broker {
	t.Helper()
	b, err := queue.NewFile(queue.FileConfig{Dir: dir, Policy: queue.Policy{VisibilityTimeout: time.Hour, MaxDeliveries: 5}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.(io.Closer).Close() })
	return b
}

func TestDataCopyIntoSQLite(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	ctx := app.WithUser(t.Context(), "clerk", struct{}{})

	// The product as it was: its data in the directory.
	before := newRegistry(t, dir, io.Discard, false)
	if err := before.app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := before.dossiers.Insert(ctx, dossier{ID: "d1", Holder: "ann@registry.test", Name: "Ann Quist", Status: "opened"}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"reviewed", "closed"} {
		if _, err := before.dossiers.Update(ctx, "d1", func(d *dossier) error { d.Status = status; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := before.dossiers.Insert(ctx, dossier{ID: "d2", Holder: "bob@registry.test", Name: "Bob Lowmoor", Status: "opened"}); err != nil {
		t.Fatal(err)
	}
	if err := before.dossiers.Insert(ctx, dossier{ID: "d3", Holder: "cy@registry.test", Name: "Cy Ormond", Status: "opened"}); err != nil {
		t.Fatal(err)
	}
	if err := before.dossiers.Delete(ctx, "d3"); err != nil {
		t.Fatal(err)
	}
	trashed, err := before.dossiers.Trashed(ctx)
	if err != nil || len(trashed) != 1 {
		t.Fatalf("the trash before the copy: %+v %v, want d3", trashed, err)
	}
	if err := before.app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	filed := fileQueue(t, filepath.Join(dir, "registry", "queues", "filed"))
	if _, err := filed.Publish(t.Context(), []byte(`"waiting"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := filed.(queue.MessageWriter).PutMessage(t.Context(), queue.LiveMessage{
		Message: queue.Message{Payload: []byte(`"retried"`), EnqueuedAt: now}, VisibleAt: now.Add(time.Hour), Deliveries: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := filed.(queue.DeadLetterWriter).PutDeadLetter(t.Context(), queue.DeadLetter{
		Message: queue.Message{Payload: []byte(`"dead"`), EnqueuedAt: now}, FailedAt: now, Deliveries: 5, Reason: "LEASE_EXPIRED",
	}); err != nil {
		t.Fatal(err)
	}
	outbox := fileQueue(t, filepath.Join(dir, "registry", "outbox", "notify"))
	if _, err := outbox.(queue.MessageWriter).PutMessage(t.Context(), queue.LiveMessage{
		Message: queue.Message{Payload: []byte(outboxMail), EnqueuedAt: now}, VisibleAt: now.Add(time.Hour), Deliveries: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// The copy, into the SQLite file the product now declares.
	if code := newRegistry(t, dir, io.Discard, true).app.Main(t.Context(), []string{"data", "copy", "--to", "main"}); code != 0 {
		t.Fatalf("data copy exited %d, want 0", code)
	}
	db := open(t, filepath.Join(dir, "main.sqlite"))
	count := func(table string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM "`+table+`"`).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		return n
	}
	if n := count("registry__dossiers"); n != 2 {
		t.Errorf("the dossiers' table holds %d rows, want 2", n)
	}
	if n := count("app_queue__registry__filed"); n != 3 {
		t.Errorf("the subscription's table holds %d messages, want 3", n)
	}
	var box []byte
	if err := db.QueryRowContext(t.Context(), `SELECT payload FROM "app_queue__registry__notify__outbox"`).Scan(&box); err != nil {
		t.Fatalf("the outbox's table: %v", err)
	}
	if bytes.Contains(box, []byte("holder@registry.test")) {
		t.Errorf("the mail rests in clear in the outbox's table: %s", box)
	}
	var deliveries int
	if err := db.QueryRowContext(t.Context(), `SELECT deliveries FROM "app_queue__registry__filed" WHERE payload = ?`, []byte(`"retried"`)).Scan(&deliveries); err != nil || deliveries != 3 {
		t.Errorf("the retried message's count on the table = %d, %v; want 3", deliveries, err)
	}
	for _, q := range []string{filepath.Join("registry", "queues", "filed"), filepath.Join("registry", "outbox", "notify")} {
		if _, err := os.Stat(filepath.Join(dir, q)); !os.IsNotExist(err) {
			t.Errorf("the file queue %s is still where it was: %v", q, err)
		}
	}

	// The product on its SQLite file: the dossiers opened, their versions
	// there, no file queue left to report.
	var logs bytes.Buffer
	after := newRegistry(t, dir, &syncWriter{w: &logs}, true)
	run(t, after.app)
	d, err := after.dossiers.Get(ctx, "d2")
	if err != nil || d.Name != "Bob Lowmoor" {
		t.Fatalf("d2 on SQLite = %+v, %v", d, err)
	}
	revs, err := after.dossiers.Revisions(ctx, "d1")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range revs {
		got = append(got, strconv.FormatUint(r.Number, 10)+":"+r.Value.Status)
	}
	if !slices.Equal(got, []string{"3:closed", "2:reviewed", "1:opened"}) {
		t.Errorf("d1's versions on SQLite are %q", got)
	}
	stones, err := after.dossiers.Trashed(ctx)
	if err != nil || len(stones) != 1 || stones[0].Key != "d3" || !stones[0].At.Equal(trashed[0].At) {
		t.Errorf("the trash on SQLite: %+v %v, want d3 trashed at %v", stones, err, trashed[0].At)
	}
	back, err := after.dossiers.Untrash(ctx, "d3")
	if err != nil || back.Name != "Cy Ormond" {
		t.Errorf("d3 untrashed on SQLite: %+v %v", back, err)
	}
	if strings.Contains(logs.String(), "now lives on database") {
		t.Errorf("the start still reports a file queue left behind")
	}
}

// syncWriter is a writer the app's logger and the test share.
type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
