package kit_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// `data copy --to NAME` (ADR 0178 C7), on kit's fake database: a store kept
// in the data directory — its versions, its records' former values, a record
// the product deleted, in the trash — and a
// subscription's file queue — waiting, retried, leased and dead messages —
// are copied into the database the product now declares, verified by
// fingerprints; the file queue is moved aside, so the start no longer
// reports it; and a second copy is refused, the database's tables holding
// rows.

// note is what the archive keeps: a title under a unique index, versions,
// and a nickname that remembers its last two values.
type note struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Nick  string `json:"nick" kit:"history=2"`
	State string `json:"state"`
}

// archive is a product whose one service keeps notes and reads what is
// posted, declared afresh for each run: a service runs in one app at a time.
type archive struct {
	svc   *kit.Service
	notes *kit.StoreService[note]
	flow  *kit.WorkflowService[note, string]
}

func newArchive() archive {
	svc := kit.NewService("archive", "Notes, copied into a database.")
	a := archive{svc: svc}
	a.notes = svc.Store("notes", func(n note) string { return n.ID }, kit.Revisions(2), kit.Unique("title", func(n note) string { return n.Title }))
	a.flow = svc.Workflow("flow", a.notes, func(n *note) *string { return &n.State }).Initial("new").On("close", "new", "closed")
	posted := svc.Topic[note]("posted")
	svc.Subscribe("read", posted, func(context.Context, note) error { return nil })
	return a
}

// archiveApp is the archive in production, its data in dir, on db when it
// is set.
func archiveApp(a archive, dir string, logs *syncBuffer, db *kit.FakeDB) *kit.App {
	opts := []kit.AppConfigurer{kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvProduction), kit.DataDir(dir), kit.Logs(logs)}
	if db != nil {
		opts = append(opts, kit.Database("main", db.Engine()))
	}
	return kit.NewApp("archive", a.svc).With(opts...)
}

// readQueue is the subscription's file queue under dir.
func readQueue(dir string) string { return filepath.Join(dir, "archive", "queues", "read") }

// fillQueue leaves in the file queue at dir a message waiting, one retried
// twice, one leased by a consumer that stopped, and a dead letter.
func fillQueue(t *testing.T, dir string) {
	t.Helper()
	b, err := queue.NewFile(queue.FileConfig{Dir: dir, Policy: queue.Policy{VisibilityTimeout: time.Hour, MaxDeliveries: 5}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if _, err := b.Publish(ctx, []byte(`"leased"`)); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Receive(ctx, 1); err != nil || len(got) != 1 {
		t.Fatalf("Receive() = %v, %v", got, err)
	}
	now := time.Now()
	if _, err := b.Publish(ctx, []byte(`"waiting"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.(queue.MessageWriter).PutMessage(ctx, queue.LiveMessage{
		Message: queue.Message{Payload: []byte(`"retried"`), EnqueuedAt: now}, VisibleAt: now, Deliveries: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.(queue.DeadLetterWriter).PutDeadLetter(ctx, queue.DeadLetter{
		Message: queue.Message{Payload: []byte(`"dead"`), EnqueuedAt: now}, FailedAt: now, Deliveries: 5, Reason: "LEASE_EXPIRED",
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.(interface{ Close() error }).Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDataCopyMovesTheDataDirectoryIntoTheDatabase(t *testing.T) {
	needsFileStore(t)
	t.Setenv("KIT_SECRETS", "memory")
	t.Setenv("KIT_SMTP_URL", "")
	dir := t.TempDir()
	ctx := t.Context()

	// The product as it was: its data in the directory.
	before := newArchive()
	app := archiveApp(before, dir, &syncBuffer{}, nil)
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := before.notes.Insert(ctx, note{ID: "n1", Title: "first", Nick: "a", State: "new"}); err != nil {
		t.Fatal(err)
	}
	for _, nick := range []string{"b", "c"} {
		if _, err := before.notes.Update(ctx, "n1", func(n *note) error { n.Nick = nick; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := before.notes.Insert(ctx, note{ID: "n2", Title: "second", Nick: "z", State: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := before.flow.Fire(ctx, "n2", "close"); err != nil {
		t.Fatal(err)
	}
	if err := before.notes.Insert(ctx, note{ID: "n3", Title: "third", Nick: "y", State: "new"}); err != nil {
		t.Fatal(err)
	}
	if err := before.notes.Delete(ctx, "n3"); err != nil {
		t.Fatal(err)
	}
	trashed, err := before.notes.Trashed(ctx)
	if err != nil || len(trashed) != 1 {
		t.Fatalf("the trash before the copy: %+v %v, want n3", trashed, err)
	}
	stop(t, app)
	fillQueue(t, readQueue(dir))

	// The copy, into the database the product now declares.
	db := kit.NewFakeDB(sql.DialectPostgres)
	t.Setenv("ARCHIVE_MAIN_URL", verifiedURL())
	if code := archiveApp(newArchive(), dir, &syncBuffer{}, db).Main(ctx, []string{"data", "copy", "--to", "main"}); code != 0 {
		ex := db.Executed()
		t.Fatalf("data copy exited %d, want 0; last statements %q", code, ex[max(0, len(ex)-3):])
	}
	if docs := db.Documents("archive__notes"); len(docs) != 2 {
		t.Fatalf("the notes' table holds %d documents, want 2", len(docs))
	}
	if hist := db.Documents("archive__notes__history"); len(hist) != 1 || !strings.Contains(hist["n1"], `"b"`) {
		t.Fatalf("the notes' former values on the database = %v, want n1's", hist)
	}
	if journal := db.Documents("archive__flow__workflow"); !strings.Contains(journal["n2"], `"closed"`) {
		t.Fatalf("the workflow's journal on the database = %v, want n2 closed", journal)
	}
	if msgs := db.Messages("kit_queue__archive__read"); len(msgs) != 4 {
		t.Fatalf("the queue's table holds %d messages, want 4: %q", len(msgs), msgs)
	}
	if _, err := os.Stat(readQueue(dir)); !os.IsNotExist(err) {
		t.Fatalf("the file queue is still where it was: %v", err)
	}
	if moved, _ := filepath.Glob(filepath.Join(dir, ".data-copy", "*", "archive", "queues", "read")); len(moved) != 1 {
		t.Fatalf("the file queue was not moved aside: %v", moved)
	}

	// The product on its database: the notes, their versions and former
	// values there, and no file queue left to report.
	after := newArchive()
	logs := &syncBuffer{}
	app = archiveApp(after, dir, logs, db)
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	revs, err := after.notes.Revisions(ctx, "n1")
	if err != nil {
		t.Fatal(err)
	}
	var numbers []uint64
	for _, r := range revs {
		numbers = append(numbers, r.Number)
	}
	if !slices.Equal(numbers, []uint64{3, 2, 1}) {
		t.Errorf("n1's versions on the database are %v, want [3 2 1]", numbers)
	}
	if n, err := after.notes.Get(ctx, "n2"); err != nil || n.Title != "second" {
		t.Errorf("n2 on the database = %+v, %v", n, err)
	}
	if stones, err := after.notes.Trashed(ctx); err != nil || len(stones) != 1 || stones[0].Key != "n3" || !stones[0].At.Equal(trashed[0].At) {
		t.Errorf("the trash on the database = %+v, %v; want n3 trashed at %v", stones, err, trashed[0].At)
	}
	stop(t, app)
	if strings.Contains(logs.String(), "now lives on database") {
		t.Errorf("the start still reports a file queue left behind:\n%s", logs.String())
	}

	// A second copy finds the tables full, and refuses.
	if code := archiveApp(newArchive(), dir, &syncBuffer{}, db).Main(ctx, []string{"data", "copy", "--to", "main"}); code != 1 {
		t.Fatalf("a second data copy exited %d, want 1", code)
	}
	if code := archiveApp(newArchive(), dir, &syncBuffer{}, db).Main(ctx, []string{"data", "copy", "--to", "elsewhere"}); code != 1 {
		t.Fatalf("a copy to an undeclared database exited %d, want 1", code)
	}
	if code := archiveApp(newArchive(), dir, &syncBuffer{}, db).Main(ctx, []string{"data", "copy"}); code != 2 {
		t.Fatalf("a copy with no target exited %d, want 2", code)
	}
}

// stop stops app within ten seconds.
func stop(t *testing.T, app *kit.App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
