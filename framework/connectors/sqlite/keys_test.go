package sqlite_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/connectors/sqlite"
	"github.com/kitsunium/sdk/framework/kit"
)

// promptly is what a write that makes a data key may take: far below the
// file's busy timeout (5s), which a key waiting for its own write's writer
// sits out before it fails.
const promptly time.Duration = 2 * time.Second

// kit.Privacy on a SQLite file (ADR 0004, ADR 0006): its holds and its
// journal live there, beside the product's stores, and its data keys stay
// in the data directory. A SQLite file has one writer, and a data key is
// written apart from every transaction: on the file, it would wait for the
// writer the very write that needs it holds — the store's own transaction
// around an update, kit.Transact's on the file —, the busy timeout long,
// then fail.

// deskCase is a person's case, sealed where it rests.
type deskCase struct {
	ID    string `json:"id"`
	Email string `json:"email" kit:"subject"`
	Note  string `json:"note" kit:"personal"`
}

// tallyRow is a public count: nothing of it is sealed.
type tallyRow struct {
	ID string `json:"id"`
	N  int    `json:"n"`
}

// deskLetter is a message about a person: sealed under their data key in
// its subscription's queue.
type deskLetter struct {
	To   string `json:"to" kit:"subject"`
	Body string `json:"body" kit:"personal"`
}

// deskReminder is a queued command's input about a person: sealed under
// their data key in the command's queue.
type deskReminder struct {
	Email string `json:"email" kit:"subject"`
	Text  string `json:"text" kit:"personal"`
}

// desk is a product that keeps its tally on its SQLite file, kit.Privacy
// beside it — and its cases there too, or in the data directory —, and
// posts letters and reminders.
type desk struct {
	app     *kit.App
	cases   *kit.Store[deskCase]
	tally   *kit.Store[tallyRow]
	letters *kit.Topic[deskLetter]
	remind  *kit.Command[deskReminder, kit.Empty]
	// posted and reminded are the letters and reminders delivered, opened.
	posted   chan deskLetter
	reminded chan deskReminder
}

// startDesk runs a desk in production, its data in a directory of its own,
// its tally and kit.Privacy on the SQLite database main, and its cases
// there too when casesOnTheFile.
func startDesk(t *testing.T, casesOnTheFile bool) *desk {
	t.Helper()
	t.Setenv("KIT_SECRETS", "memory")
	t.Setenv("KIT_DATA_KEY", "a data key of exactly 32 bytes..")
	t.Setenv("KIT_INDEX_KEY", "an index key of more than 16 bytes")
	svc := kit.NewService("desk", "Cases, letters and reminders, sealed; kit.Privacy on SQLite.")
	d := &desk{posted: make(chan deskLetter, 8), reminded: make(chan deskReminder, 8)}
	d.cases = svc.Store("cases", func(c deskCase) string { return c.ID }, kit.Purpose("Handle the cases"))
	d.tally = svc.Store("tally", func(r tallyRow) string { return r.ID })
	d.letters = svc.Topic[deskLetter]("letters")
	svc.Subscribe("post", d.letters, func(_ context.Context, l deskLetter) error {
		d.posted <- l
		return nil
	})
	d.remind = svc.Command("remind", func(_ context.Context, r deskReminder) (kit.Empty, error) {
		d.reminded <- r
		return kit.Empty{}, nil
	}, kit.Queued())
	keeps := []kit.Keepable{kit.Privacy, d.tally}
	if casesOnTheFile {
		keeps = append(keeps, d.cases)
	}
	d.app = kit.NewApp("desk", svc).With(kit.Database("main", sqlite.Engine(), kit.Keeps(keeps...)),
		kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvProduction), kit.DataDir(t.TempDir()), kit.Logs(io.Discard))
	run(t, d.app)
	return d
}

// timed runs what, and fails the test when it errs or takes more than
// promptly.
func timed(t *testing.T, what string, fn func() error) {
	t.Helper()
	began := time.Now()
	err := fn()
	took := time.Since(began)
	t.Logf("%s: %v, %v", what, took.Round(time.Millisecond), err)
	if err != nil || took > promptly {
		t.Errorf("%s took %v: %v", what, took.Round(time.Millisecond), err)
	}
}

// received is the next value of ch, within a few seconds.
func received[T any](t *testing.T, what string, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(10 * time.Second):
		t.Fatalf("%s was not delivered", what)
	}
	var zero T
	return zero
}

// Where kit.Privacy's stores live on a SQLite database: the holds and the
// journal on the file, the data keys in the data directory.
func TestTheDataKeysStayOffTheFile(t *testing.T) {
	needsFileStore(t)
	g := startDesk(t, true).app.Graph()
	for id, want := range map[string]string{
		"kit.privacy/store/keys": "file", "kit.privacy/store/holds": "sqlite",
		"kit.privacy/store/journal": "sqlite", "desk/store/cases": "sqlite",
	} {
		if n := g.Node(id); n == nil || n.Store.Backend != want {
			t.Errorf("%s: %+v, want the backend %s", id, n, want)
		}
	}
}

// A hold on a sealed record on the file moves it under a data key of its
// own, in the record's own transaction on the file, and a write of the held
// record seals under that key in another; a hold that replaces one rewrites
// it in the holds' own. Each makes its key without waiting.
func TestAHoldOnSQLiteMakesItsKeysWithoutWaiting(t *testing.T) {
	needsFileStore(t)
	d := startDesk(t, true)
	ctx := t.Context()
	timed(t, "the case of a person who has no key yet", func() error {
		return d.cases.Insert(ctx, deskCase{ID: "c1", Email: "ann@desk.test", Note: "a note about ann"})
	})
	timed(t, "the case held", func() error { return d.cases.Hold(ctx, "c1", "a court's request") })
	timed(t, "the case held again, its reason replaced", func() error { return d.cases.Hold(ctx, "c1", "the court's second request") })
	timed(t, "the held case written", func() error {
		return d.cases.Put(ctx, deskCase{ID: "c1", Email: "ann@desk.test", Note: "a second note about ann"})
	})
	if err := d.cases.Release(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	timed(t, "the case held after its release", func() error { return d.cases.Hold(ctx, "c1", "a new request") })
	if got, err := d.cases.Get(ctx, "c1"); err != nil || got.Note != "a second note about ann" {
		t.Errorf("the case held: %+v %v", got, err)
	}
}

// Inside kit.Transact on the file — its transaction holds the file's writer
// from its first call there —, what seals under a data key not made yet
// makes it without waiting: a case written and held, a hold of kit's own, a
// letter published, a reminder dispatched. What is delivered after the
// commit opens. The cases in the data directory are how a product kept
// kit.Privacy on SQLite before its keys left the file.
func TestATransactionOnSQLiteMakesDataKeysWithoutWaiting(t *testing.T) {
	needsFileStore(t)
	for _, onFile := range []bool{false, true} {
		name := "cases in the data directory"
		if onFile {
			name = "cases on the file"
		}
		t.Run(name, func(t *testing.T) {
			d := startDesk(t, onFile)
			ctx := t.Context()
			n := 0
			inTransaction := func(what string, then func(ctx context.Context) error) {
				t.Helper()
				n++
				timed(t, what, func() error {
					return kit.Transact(ctx, func(ctx context.Context) error {
						if err := d.tally.Put(ctx, tallyRow{ID: "t", N: n}); err != nil {
							return err
						}
						return then(ctx)
					})
				})
			}
			if onFile {
				inTransaction("the case of a person who has no key yet", func(ctx context.Context) error {
					return d.cases.Insert(ctx, deskCase{ID: "c2", Email: "dan@desk.test", Note: "a note about dan"})
				})
				inTransaction("the case held", func(ctx context.Context) error { return d.cases.Hold(ctx, "c2", "an audit") })
			}
			inTransaction("the tally held", func(ctx context.Context) error { return d.tally.Hold(ctx, "t", "an audit") })
			inTransaction("a letter to a person who has no key yet", func(ctx context.Context) error {
				return d.letters.Publish(ctx, deskLetter{To: "bob@desk.test", Body: "a letter to bob"})
			})
			inTransaction("a reminder about a person who has no key yet", func(ctx context.Context) error {
				_, err := d.remind.Dispatch(ctx, deskReminder{Email: "cy@desk.test", Text: "a reminder about cy"})
				return err
			})
			if got, err := d.tally.Get(ctx, "t"); err != nil || got.N != n {
				t.Errorf("the tally: %+v %v, want %d", got, err, n)
			}
			if got, err := d.cases.Get(ctx, "c2"); onFile && (err != nil || got.Note != "a note about dan") {
				t.Errorf("the case: %+v %v", got, err)
			}
			if got := received(t, "the letter", d.posted); got.Body != "a letter to bob" {
				t.Errorf("the letter delivered: %+v", got)
			}
			if got := received(t, "the reminder", d.reminded); got.Text != "a reminder about cy" {
				t.Errorf("the reminder delivered: %+v", got)
			}
		})
	}
}
