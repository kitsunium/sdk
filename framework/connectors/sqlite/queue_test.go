package sqlite_test

import (
	"context"
	stdsql "database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
	"github.com/kitsunium/sdk/framework/connectors/sqlite"
)

// Kit's queues on the database that keeps their consumer (ADR 0178 C1), on
// a real SQLite file: a topic's publication, a queued command's dispatch and
// a watch's notice made in app.Transact are written in the transaction, just
// before its commit — present and consumed once it commits, absent when it
// rolls back, and a queue that refuses one fails the transaction. A
// transaction on another database, or an app without one, keeps the release
// after the commit.

// memo is what the bureau keeps and sends: its text is moderated, which a
// watch hears.
type memo struct {
	ID   string `json:"id"`
	Text string `json:"text" app:"moderated"`
}

// bureau is a product whose one service keeps memos, publishes them, files
// them through a queued command and watches its own store, and what each
// consumer heard.
type bureau struct {
	app   *app.App
	memos *app.Store[memo]
	sent  *app.Topic[memo]
	file  *app.Command[memo, app.Empty]

	mu                    sync.Mutex
	heard, filed, noticed []string
}

// newBureau declares the bureau afresh — a service runs in one app at a time —,
// its data in dir, on a SQLite database unless the options say otherwise.
func newBureau(t *testing.T, dir string, opts ...app.AppOption) *bureau {
	t.Helper()
	d, svc := declareBureau(t)
	d.app = app.NewApp("bureau", svc).With(append(bureauOptions(dir), opts...)...)
	return d
}

// declareBureau declares the bureau's service and its nodes.
func declareBureau(t *testing.T) (*bureau, *app.Service) {
	t.Helper()
	t.Setenv("BUREAU_SECRETS", "memory")
	d := &bureau{}
	svc := app.NewService("bureau", "Memos, sent, filed and screened.")
	d.memos = svc.Store("memos", func(m memo) string { return m.ID })
	d.sent = svc.Topic[memo]("sent")
	svc.Subscribe("heard", d.sent, func(_ context.Context, m memo) error { return d.record(&d.heard, m.ID) })
	d.file = svc.Command("file", func(_ context.Context, m memo) (app.Empty, error) {
		return app.Empty{}, d.record(&d.filed, m.ID)
	}, app.Queued())
	svc.Watch("screen", app.Moderated, func(_ context.Context, w app.Written) error { return d.record(&d.noticed, w.Key) }, app.OwnStores())
	return d, svc
}

// bureauOptions run the bureau in production, its data in dir.
func bureauOptions(dir string) []app.AppOption {
	return []app.AppOption{app.Listen("127.0.0.1:0"), app.Env(app.EnvProduction), app.DataDir(dir), app.Logs(io.Discard)}
}

// onSQLite is the bureau's one default database, a SQLite file beside its
// data.
func onSQLite() app.AppOption { return app.Database("main", sqlite.Engine()) }

// record notes that a consumer heard id.
func (d *bureau) record(into *[]string, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	*into = append(*into, id)
	return nil
}

// got is what each consumer heard so far: the subscription, the queued
// command, the watch.
func (d *bureau) got() (heard, filed, noticed []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.heard), slices.Clone(d.filed), slices.Clone(d.noticed)
}

// write keeps m, publishes it and dispatches it, in the transaction ctx
// carries: one write, three effects.
func (d *bureau) write(ctx context.Context, m memo) error {
	if err := d.memos.Insert(ctx, m); err != nil {
		return err
	}
	if err := d.sent.Publish(ctx, m); err != nil {
		return err
	}
	_, err := d.file.Dispatch(ctx, m)
	return err
}

// waitFor waits until every consumer heard id.
func (d *bureau) waitFor(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		heard, filed, noticed := d.got()
		if slices.Contains(heard, id) && slices.Contains(filed, id) && slices.Contains(noticed, id) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	heard, filed, noticed := d.got()
	t.Fatalf("%s not heard by every consumer: heard %v, filed %v, noticed %v", id, heard, filed, noticed)
}

// queueTables are the three queues' tables, as kit names them.
var queueTables = []string{"app_queue__bureau__heard", "app_queue__bureau__file__command", "app_queue__bureau__screen"}

// waiting counts the messages the three queues hold.
func waiting(t *testing.T, db *stdsql.DB) int {
	t.Helper()
	total := 0
	for _, table := range queueTables {
		var n int
		if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM "`+table+`"`).Scan(&n); err != nil {
			t.Fatalf("the queue table %s: %v", table, err)
		}
		total += n
	}
	return total
}

// errUndo rolls a transaction back.
var errUndo = errors.New("the memo is taken back")

// A transaction on the database that keeps the consumers writes its
// messages with its writes: committed, they are there and consumed; rolled
// back, none of them ever was — the queues' tables hold nothing, and no
// consumer hears them, while a later committed memo goes through.
func TestEffectsJoinTheTransactionOnTheirDatabase(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	d := newBureau(t, dir, onSQLite())
	run(t, d.app)
	ctx := t.Context()
	if g := d.app.Graph(); g.Node("bureau/command/file").Command.Queue != "sql" || g.Node("bureau/topic/sent").Topic.Broker != "sql" {
		t.Fatalf("the queues are not on the database: %+v %+v", g.Node("bureau/command/file").Command, g.Node("bureau/topic/sent").Topic)
	}
	db := open(t, filepath.Join(dir, "main.sqlite"))
	if err := app.Transact(ctx, func(ctx context.Context) error {
		return errors.Join(d.write(ctx, memo{ID: "gone", Text: "draft"}), errUndo)
	}); !errors.Is(err, errUndo) {
		t.Fatalf("the rolled back transaction: %v", err)
	}
	if n := waiting(t, db); n != 0 {
		t.Fatalf("a rolled back transaction left %d messages in the queues", n)
	}
	if err := app.Transact(ctx, func(ctx context.Context) error { return d.write(ctx, memo{ID: "kept", Text: "final"}) }); err != nil {
		t.Fatalf("the committed transaction: %v", err)
	}
	d.waitFor(t, "kept")
	heard, filed, noticed := d.got()
	for _, got := range [][]string{heard, filed, noticed} {
		if slices.Contains(got, "gone") {
			t.Errorf("a rolled back memo was delivered: %v %v %v", heard, filed, noticed)
		}
	}
	if _, err := d.memos.Get(ctx, "gone"); err == nil {
		t.Error("the rolled back memo was kept")
	}
}

// A publication whose queue refuses it in the transaction — a message over
// the queue's size bound — fails the transaction: Transact returns the
// refusal, and the memo written before it is not kept.
func TestARefusedPublicationFailsTheTransaction(t *testing.T) {
	needsFileStore(t)
	d := newBureau(t, t.TempDir(), onSQLite())
	run(t, d.app)
	ctx := t.Context()
	huge := memo{ID: "huge", Text: strings.Repeat("x", 2<<20)}
	err := app.Transact(ctx, func(ctx context.Context) error {
		if err := d.memos.Insert(ctx, memo{ID: "before", Text: "small"}); err != nil {
			return err
		}
		return d.sent.Publish(ctx, huge)
	})
	if err == nil {
		t.Fatal("a transaction whose queue refused its message committed")
	}
	if _, err := d.memos.Get(ctx, "before"); err == nil {
		t.Error("the write before the refused publication was kept")
	}
}

// Where no queue lives on the transaction's database — the consumers on
// another database, or no database at all — the effects leave after the
// commit as before: a refusal is reported, and the commit stands.
func TestARefusalAfterTheCommitLeavesTheCommitStanding(t *testing.T) {
	needsFileStore(t)
	for name, build := range map[string]func(t *testing.T, dir string) *bureau{
		// The memos on the archive, the consumers on main: the transaction
		// belongs to the archive.
		"another database": newBureauApart,
		"no database":      func(t *testing.T, dir string) *bureau { return newBureau(t, dir) },
	} {
		t.Run(name, func(t *testing.T) {
			d := build(t, t.TempDir())
			run(t, d.app)
			ctx := t.Context()
			huge := memo{ID: "huge", Text: strings.Repeat("x", 2<<20)}
			err := app.Transact(ctx, func(ctx context.Context) error {
				if err := d.memos.Insert(ctx, memo{ID: "before", Text: "small"}); err != nil {
					return err
				}
				return d.sent.Publish(ctx, huge)
			})
			if err != nil {
				t.Fatalf("the transaction: %v", err)
			}
			if _, err := d.memos.Get(ctx, "before"); err != nil {
				t.Errorf("the commit did not stand: %v", err)
			}
			if err := app.Transact(ctx, func(ctx context.Context) error { return d.write(ctx, memo{ID: "kept", Text: "final"}) }); err != nil {
				t.Fatalf("the committed transaction: %v", err)
			}
			d.waitFor(t, "kept")
		})
	}
}

// A consumer whose queue moved from the data directory to its database, and
// left messages in its file queue, says so at its start: nothing consumes
// them now.
func TestAFileQueueLeftBehindIsSaid(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	// The file broker's state directories, kept once empty: only the message
	// waiting in ready/ is something left behind.
	left := filepath.Join(dir, "bureau", "queues", "heard")
	for _, state := range []string{"ready", "inflight", "dead"} {
		if err := os.MkdirAll(filepath.Join(left, state), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(left, "ready", "waiting.msg"), []byte("a message"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A file queue that holds nothing, its directories kept: nothing to say.
	for _, state := range []string{"ready", "inflight", "dead"} {
		if err := os.MkdirAll(filepath.Join(dir, "bureau", "commands", "file", state), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	d := newBureau(t, dir, onSQLite())
	run(t, d.app)
	var said, empty []string
	for _, p := range d.app.Graph().Diagnostics {
		switch {
		case p.Node == "bureau/subscription/heard" && strings.Contains(p.Message, `database "main"`):
			said = append(said, p.Message)
		case p.Node == "bureau/command/file" && strings.Contains(p.Message, `database "main"`):
			empty = append(empty, p.Message)
		}
	}
	if len(said) != 1 {
		t.Errorf("the file queue left behind: %v", said)
	}
	if len(empty) != 0 {
		t.Errorf("an empty file queue was said to be left behind: %v", empty)
	}
}

// newBureauApart is the bureau whose memos a database "archive" keeps, apart
// from its consumers, which the default database "main" keeps.
func newBureauApart(t *testing.T, dir string) *bureau {
	t.Helper()
	d, svc := declareBureau(t)
	d.app = app.NewApp("bureau", svc).With(append(bureauOptions(dir),
		app.Database("main", sqlite.Engine()), app.Database("archive", sqlite.Engine(), app.Keeps(d.memos)))...)
	return d
}
