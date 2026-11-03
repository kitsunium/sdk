package storetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/app"
	// The suite's cases declare stores, a topic and its subscription, and
	// workflows: it imports their subsystems, as a product does (ADR 0179
	// step 10) — the queues' and the workflows', which bring the stores'.
	_ "github.com/kitsunium/sdk/framework/app/mail"
	_ "github.com/kitsunium/sdk/framework/app/otlp"
	_ "github.com/kitsunium/sdk/framework/app/queue"
	_ "github.com/kitsunium/sdk/framework/app/workflow"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

const (
	// App is the name of the app every case runs: a database's URL is read
	// from STORETEST_<NAME>_URL.
	App string = "storetest"

	// putN is the N a Put gives the document an insert wrote.
	putN int = 3
	// writers is how many run at once.
	writers int = 32
	// pageUpdates is the N of a page's last update in the revisions case:
	// its versions are 2 to pageUpdates, the oldest pruned.
	pageUpdates int = 4
	// transitioned is the number of the version a guard's transition makes
	// after the revisions case's updates.
	transitioned uint64 = 5
	// rolledN is the N a rolled back write gives a page.
	rolledN int = 99
	// stopWait bounds a case's stop.
	stopWait time.Duration = 10 * time.Second
	// waitFor bounds what eventually waits for.
	waitFor time.Duration = 5 * time.Second
	// pollEvery is how often eventually looks.
	pollEvery time.Duration = 10 * time.Millisecond
	// trashDays is how long a store with revisions keeps what is deleted,
	// unless its setting says otherwise (ADR 0178 C4).
	trashDays time.Duration = 30 * 24 * time.Hour
	// rewrittenN is the N of a page written again after its purge.
	rewrittenN int = 7
	// askedN is the N a read-only query writes inside the caller's
	// transaction, which the query's own read-only option does not bind.
	askedN int = 3
)

var (
	// errRolled rolls a transaction back.
	errRolled = errs.New(CodeRolledBack, "ROLLED_BACK", "the case rolled its transaction back",
		"storetest: a case returned this error from a transaction's function to roll it back")

	// cases are the suite's, in order.
	cases = []struct {
		name string
		run  func(t *testing.T, s *suite)
		// clocked runs the case on a manual clock: what it checks waits
		// on the app's clock, never on the wall clock.
		clocked bool
	}{
		{"trash", trash, true},
		{"write-modes", writeModes, false},
		{"one-insert-wins", oneInsertWins, false},
		{"update-is-atomic", updateIsAtomic, false},
		{"unique-names-its-index", uniqueNamesItsIndex, false},
		{"find-in-key-order", findInKeyOrder, false},
		{"keys-are-bytes", keysAreBytes, false},
		{"document-as-written", documentAsWritten, false},
		{"transactions", transactions, false},
		{"transaction-options", transactionOptions, false},
		{"held-publish", heldPublish, false},
		{"hooks-after-the-commit", hooksAfterTheCommit, false},
		{"revisions", revisions, false},
		{"revisions-restore", revisionsRestore, false},
		{"revisions-rollback", revisionsRollback, false},
		{"queued-command", queuedCommand, true},
		{"topic-later", topicLater, true},
		{"query-read-only", queryReadOnly, false},
		{"records-keys", recordsKeys, false},
		{"outbox", outbox, false},
		{"watch-catch-up", watchCatchUp, false},
		{"workflow-journal", workflowJournal, true},
		{"data-copy", dataCopy, false},
		{"otlp-tables", otlpTables, true},
	}

	// declarations add, on a case's service, the nodes only that case reads:
	// the queues' cases a queued command and two more topics (#998).
	declarations = map[string]func(svc *app.Service, s *suite){
		"queued-command": declareQueues,
		"topic-later":    declareQueues,
		"query-read-only": func(svc *app.Service, s *suite) {
			s.reader = svc.Query("reader", func(ctx context.Context, d doc) (doc, error) {
				if d.N > 0 {
					return d, s.docs.Put(ctx, d)
				}
				return s.docs.Get(ctx, d.ID)
			}, app.ReadOnly())
		},
		"records-keys":     declareMemos,
		"outbox":           declareOutbox,
		"watch-catch-up":   declareMemoStore,
		"workflow-journal": declareTimers,
		"data-copy":        declareMemoStore,
	}

	// unorderedKeys are written out of order.
	unorderedKeys = []string{"c", "a", "b"}
	// orderedKeys is how Find answers unorderedKeys.
	orderedKeys = []string{"a", "b", "c"}

	// byteKeys are keys a collation would fold.
	byteKeys = []string{"a", "A", "a ", "é", "e"}
	// byteOrder is how List answers byteKeys, as Go compares them.
	byteOrder = []string{"A", "a", "a ", "e", "é"}

	// keptVersions are a page's versions after the revisions case's updates.
	keptVersions = []string{"4:4", "3:3", "2:2"}
	// transitionEdits are what changed between versions 3 and 5.
	transitionEdits = []string{"replace /n 3 4", "add /ready  true", "replace /state \"new\" \"done\""}
	// refusedRestore are a page's versions after a refused restore.
	refusedRestore = []string{"3:1", "2:2", "1:1"}

	// epoch is where the suite's manual clock starts.
	epoch = time.Date(2026, time.October, 1, 9, 0, 0, 0, time.UTC)
)

// BackendConfig is where Run runs the framework's stores: a name for the subtests and
// the options that place the stores there.
type BackendConfig struct {
	// Name names the backend in the subtests.
	Name string
	// Options place the stores of the app named app on the backend —
	// app.InMemory(), app.DataDir, an app.Database whose URL the environment
	// holds —, once per case. A database is named "database": the suite
	// gives its calls a minute, as 32 writers of one entity queue on a
	// server's lock.
	Options func(t *testing.T, app string) []app.AppOption
}

// doc is what the suite stores.
type doc struct {
	ID    string          `json:"id"`
	Email string          `json:"email,omitempty"`
	Teams []string        `json:"teams,omitempty"`
	N     int             `json:"n"`
	Ready bool            `json:"ready,omitempty"`
	State string          `json:"state,omitempty"`
	Raw   json.RawMessage `json:"raw,omitempty"`
}

// suite is one case's product, running.
type suite struct {
	docs      *app.Store[doc]
	moved     *app.Topic[doc]
	lifecycle *app.Workflow[doc, string]
	heard     *heard
	// pages keep their two previous versions (ADR 0007 §3), a workflow
	// over them.
	pages    *app.Store[doc]
	pageFlow *app.Workflow[doc, string]
	// clock is the app's, a manual one at epoch, for a case that waits on
	// it; nil for the others, which run on the wall clock.
	clock *clock.ManualClock
	// file is a queued command keyed by the document's ID, and filed what
	// it handled; delayed is a topic, onced one that publishes a key once
	// an hour, and later what their subscriptions heard — the queues'
	// cases' (declareQueues).
	file *app.Command[doc, app.Empty]
	// reader is a query declared read-only (#1140): it reads a document,
	// or, given an N, tries to write it.
	reader         *app.Query[doc, doc]
	delayed, onced *app.Topic[doc]
	filed, later   *heard
	// memos is a store whose text a watch hears, timers one a workflow with
	// a timer runs over, and mailer a mailer — the cases of what ADR 0178
	// placed beside the stores (#998, durable.go, subsystems.go).
	memos  *app.Store[memo]
	timers *app.Store[doc]
	expiry *app.Workflow[doc, string]
	mailer *app.Mailer
	// listed is what the memos' watch heard and listed.
	listed *listing
	// running is the case's product; name names its service, place is the
	// backend's options it runs on, and stopped says it was stopped.
	running *app.App
	name    string
	place   []app.AppOption
	stopped bool
}

// heard are the messages a subscription was delivered.
type heard struct {
	sync.Mutex
	ids []string
}

// key is the document's key.
func (d doc) key() string { return d.ID }

// add records that id was delivered.
func (h *heard) add(id string) {
	h.Lock()
	defer h.Unlock()
	h.ids = append(h.ids, id)
}

// count is how many times id was delivered.
func (h *heard) count(id string) int {
	h.Lock()
	defer h.Unlock()
	n := 0
	for _, got := range h.ids {
		if got == id {
			n++
		}
	}
	return n
}

// has reports whether id was delivered.
func (h *heard) has(id string) bool {
	h.Lock()
	defer h.Unlock()
	return slices.Contains(h.ids, id)
}

// Run runs every case of the suite on b.
func Run(t *testing.T, b BackendConfig) {
	for _, c := range cases {
		t.Run(b.Name+"/"+c.name, func(t *testing.T) { c.run(t, open(t, b, c.name, c.clocked)) })
	}
}

// open declares the case's service — its store, a topic and its
// subscription, a workflow — and runs it on b until the test ends.
func open(t *testing.T, b BackendConfig, name string, clocked bool) *suite {
	t.Helper()
	t.Setenv("STORETEST_SMTP_URL", "")
	t.Setenv("STORETEST_SECRETS", "memory")
	t.Setenv("STORETEST_DATABASE_TIMEOUT", "1m")
	var clk *clock.ManualClock
	if clocked {
		clk = clock.NewManualClock(epoch)
	}
	s := build(name, b.Options(t, App), clk, declarations[name])
	s.start(t)
	return s
}

// build declares the case's service on a product placed by place — on clk
// when it is not nil —, without starting it.
func build(name string, place []app.AppOption, clk *clock.ManualClock, declare func(*app.Service, *suite)) *suite {
	svc := app.NewService("st-"+name, "A case of the framework's store conformance suite.")
	docs := svc.Store("docs", doc.key,
		app.Unique("email", func(d doc) string { return d.Email }),
		app.Index("team", func(d doc) []string { return d.Teams }))
	pages := svc.Store("pages", doc.key, app.Revisions(2),
		app.Unique("email", func(d doc) string { return d.Email }))
	s := &suite{
		heard:     &heard{},
		docs:      docs,
		moved:     svc.Topic[doc]("moved"),
		lifecycle: svc.Workflow("lifecycle", docs, stateOf).Initial("new").When("ready", "new", "done", isReady),
		pages:     pages,
		pageFlow:  svc.Workflow("publishing", pages, stateOf).Initial("new").When("ready", "new", "done", isReady).On("hold", "new", "held"),
		name:      name,
		place:     place,
	}
	svc.Subscribe("heard", s.moved, func(_ context.Context, d doc) error { s.heard.add(d.ID); return nil })
	if declare != nil {
		declare(svc, s)
	}
	s.running = app.NewApp(App, svc).With(append([]app.AppOption{
		app.Listen("127.0.0.1:0"), app.Env(app.EnvDev), app.Analyze(false), app.Logs(io.Discard),
	}, place...)...)
	if clk != nil {
		s.clock = clk
		s.running = s.running.With(app.Clock(clk))
	}
	return s
}

// start starts the case's product, and stops it when the test ends unless
// the case stopped it first.
func (s *suite) start(t *testing.T) {
	t.Helper()
	if err := s.running.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { s.stop(t) })
}

// stop stops the case's product, once.
func (s *suite) stop(t *testing.T) {
	t.Helper()
	if s.stopped {
		return
	}
	s.stopped = true
	ctx, cancel := context.WithTimeout(context.Background(), stopWait)
	defer cancel()
	if err := s.running.Stop(ctx); err != nil {
		t.Errorf("stop: %v", err)
	}
}

// stateOf is where a document keeps its workflow's state.
func stateOf(d *doc) *string { return &d.State }

// isReady is the workflows' guard.
func isReady(d doc) bool { return d.Ready }

// code is the kit error code of err, "" for none.
func code(err error) string {
	if ke, ok := errors.AsType[*app.Error](err); ok {
		return ke.Code
	}
	return ""
}

// message is the wire-safe message of err, "" for none.
func message(err error) string {
	if ke, ok := errors.AsType[*app.Error](err); ok {
		return ke.Message
	}
	return ""
}

// expect fails the test when err's code is not want.
func expect(t *testing.T, what string, err error, want string) {
	t.Helper()
	if code(err) != want {
		t.Errorf("%s: %v", what, err)
	}
}

// must stops the test on err.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// eventually waits up to five seconds for cond.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(waitFor); time.Now().Before(deadline); time.Sleep(pollEvery) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// idsOf are the documents' keys, in their order.
func idsOf(docs []doc) []string {
	ids := make([]string, 0, len(docs))
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	return ids
}

// setN is an update that sets a document's N.
func setN(n int) func(d *doc) error {
	return func(d *doc) error { d.N = n; return nil }
}

// writeModes runs the three write modes, an update, a delete, and what each
// refuses.
func writeModes(t *testing.T, s *suite) {
	ctx := t.Context()
	must(t, s.docs.Insert(ctx, doc{ID: "a", N: 1}))
	expect(t, "an insert over a taken key", s.docs.Insert(ctx, doc{ID: "a", N: 2}), app.WireConflict)
	must(t, s.docs.Put(ctx, doc{ID: "a", N: putN}))
	got, err := s.docs.Update(ctx, "a", func(d *doc) error { d.N++; return nil })
	if err != nil || got.N != putN+1 {
		t.Errorf("an update: %+v %v", got, err)
	}
	_, err = s.docs.Update(ctx, "none", func(*doc) error { return nil })
	expect(t, "an update of nothing", err, app.WireNotFound)
	_, err = s.docs.Update(ctx, "a", func(d *doc) error { d.ID = "b"; return nil })
	expect(t, "an update that renames", err, app.WireInvalid)
	if n, err := s.docs.Count(ctx); err != nil || n != 1 {
		t.Errorf("count: %d %v", n, err)
	}
	must(t, s.docs.Delete(ctx, "a"))
	expect(t, "a delete of nothing", s.docs.Delete(ctx, "a"), app.WireNotFound)
	_, err = s.docs.Get(ctx, "a")
	expect(t, "a get of nothing", err, app.WireNotFound)
}

// oneInsertWins runs 32 inserts of one key at once: one wins, the others
// are refused.
func oneInsertWins(t *testing.T, s *suite) {
	results := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() { results[i] = s.docs.Insert(context.Background(), doc{ID: "one", N: i}) })
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		switch code(err) {
		case "":
			won++
		case app.WireConflict:
		default:
			t.Errorf("an insert failed: %v", err)
		}
	}
	if won != 1 {
		t.Errorf("%d inserts won", won)
	}
}

// updateIsAtomic runs 32 updates of one entity at once: each sees the one
// before.
func updateIsAtomic(t *testing.T, s *suite) {
	must(t, s.docs.Put(t.Context(), doc{ID: "counter"}))
	results := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			_, results[i] = s.docs.Update(context.Background(), "counter", func(d *doc) error { d.N++; return nil })
		})
	}
	wg.Wait()
	must(t, errors.Join(results...))
	if got, err := s.docs.Get(t.Context(), "counter"); err != nil || got.N != writers {
		t.Errorf("after %d updates: %+v %v", writers, got, err)
	}
}

// uniqueNamesItsIndex checks that a unique index refuses a second holder of
// a key, naming the index and never the key.
func uniqueNamesItsIndex(t *testing.T, s *suite) {
	must(t, s.docs.Put(t.Context(), doc{ID: "ada", Email: "ada@example.com"}))
	err := s.docs.Put(t.Context(), doc{ID: "eve", Email: "ada@example.com"})
	said := message(err)
	if code(err) != app.WireConflict || !strings.Contains(said, `"email"`) || strings.Contains(said, "ada@example.com") {
		t.Errorf("a second holder of a unique key: %v", err)
	}
	if got, err := s.docs.Lookup(t.Context(), "email", "ada@example.com"); err != nil || got.ID != "ada" {
		t.Errorf("lookup: %+v %v", got, err)
	}
	_, err = s.docs.Lookup(t.Context(), "email", "eve@example.com")
	if code(err) != app.WireNotFound || strings.Contains(message(err), "eve@example.com") {
		t.Errorf("a lookup of nobody: %v", err)
	}
}

// findInKeyOrder checks that Find answers in store-key order, whatever the
// order of the writes.
func findInKeyOrder(t *testing.T, s *suite) {
	for _, id := range unorderedKeys {
		must(t, s.docs.Put(t.Context(), doc{ID: id, Teams: []string{"red"}}))
	}
	got, err := s.docs.Find(t.Context(), "team", "red")
	if ids := idsOf(got); err != nil || !slices.Equal(ids, orderedKeys) {
		t.Errorf("find: %v %v", ids, err)
	}
	if none, err := s.docs.Find(t.Context(), "team", "blue"); err != nil || none == nil || len(none) != 0 {
		t.Errorf("a find of nothing: %v %v", none, err)
	}
}

// keysAreBytes checks that keys compare as bytes, as Go compares them: a
// and A, a and "a ", é and e are distinct keys.
func keysAreBytes(t *testing.T, s *suite) {
	for i, k := range byteKeys {
		if err := s.docs.Insert(t.Context(), doc{ID: k, N: i}); err != nil {
			t.Fatalf("insert %q: %v", k, err)
		}
	}
	for i, k := range byteKeys {
		if got, err := s.docs.Get(t.Context(), k); err != nil || got.N != i {
			t.Errorf("get %q: %+v %v", k, got, err)
		}
	}
	all, err := s.docs.List(t.Context())
	if ids := idsOf(all); err != nil || !slices.Equal(ids, byteOrder) {
		t.Errorf("list in byte order: %q %v", ids, err)
	}
}

// documentAsWritten checks that a document reads back as kit encoded it:
// its members' order, a number's spelling, a duplicate member.
func documentAsWritten(t *testing.T, s *suite) {
	raw := json.RawMessage(`{"b":1,"a":1.230e-5,"a":2}`)
	must(t, s.docs.Put(t.Context(), doc{ID: "raw", Raw: raw}))
	if got, err := s.docs.Get(t.Context(), "raw"); err != nil || !bytes.Equal(got.Raw, raw) {
		t.Errorf("read back: %s %v", got.Raw, err)
	}
}

// transactions checks that a transaction commits its writes together; one
// that fails undoes every one — an insert, a replacement, a deletion —; one
// nested in it is a savepoint.
func transactions(t *testing.T, s *suite) {
	for _, id := range []string{"kept", "gone"} {
		must(t, s.docs.Put(t.Context(), doc{ID: id, N: 1}))
	}
	rolledBack(t, s)
	savepoint(t, s)
}

// rolledBack runs a transaction that fails, and checks that it undid every
// write.
func rolledBack(t *testing.T, s *suite) {
	ctx := t.Context()
	err := app.Transact(ctx, func(ctx context.Context) error {
		if err := errors.Join(s.docs.Insert(ctx, doc{ID: "new"}), s.docs.Delete(ctx, "gone")); err != nil {
			return err
		}
		if _, err := s.docs.Update(ctx, "kept", setN(2)); err != nil {
			return err
		}
		if got, err := s.docs.Get(ctx, "kept"); err != nil || got.N != 2 {
			t.Errorf("inside the transaction: %+v %v", got, err)
		}
		return errRolled
	})
	if !errors.Is(err, errRolled) {
		t.Fatalf("the rolled back transaction: %v", err)
	}
	_, err = s.docs.Get(ctx, "new")
	expect(t, "a rolled back insert", err, app.WireNotFound)
	if got, err := s.docs.Get(ctx, "kept"); err != nil || got.N != 1 {
		t.Errorf("a rolled back update: %+v %v", got, err)
	}
	_, err = s.docs.Get(ctx, "gone")
	expect(t, "a rolled back delete", err, "")
}

// savepoint runs a transaction with a nested one that fails: the outer one
// commits without the inner one's writes.
func savepoint(t *testing.T, s *suite) {
	ctx := t.Context()
	err := app.Transact(ctx, func(ctx context.Context) error {
		if err := s.docs.Insert(ctx, doc{ID: "outer"}); err != nil {
			return err
		}
		inner := app.Transact(ctx, func(ctx context.Context) error {
			return errors.Join(s.docs.Insert(ctx, doc{ID: "inner"}), errRolled)
		})
		if !errors.Is(inner, errRolled) {
			t.Errorf("the savepoint: %v", inner)
		}
		return s.docs.Insert(ctx, doc{ID: "after"})
	})
	if err != nil {
		t.Fatalf("the committed transaction: %v", err)
	}
	for id, want := range map[string]string{"outer": "", "after": "", "inner": app.WireNotFound} {
		_, err := s.docs.Get(ctx, id)
		expect(t, id+" after the commit", err, want)
	}
}

// transactionOptions checks app.Transact's options (#1140) on the backend:
// a SERIALIZABLE and a REPEATABLE READ transaction commit what they wrote; a
// read-only one reads and refuses a store's write with
// CodeTransactionReadOnly, writing nothing; and a nested Transact given an
// option is refused with CodeTransactionNested, the outer one going on.
func transactionOptions(t *testing.T, s *suite) {
	isolatedTransactions(t, s)
	readOnlyTransaction(t, s)
	nestedTransactionOptions(t, s)
}

// isolatedTransactions checks that a SERIALIZABLE and a REPEATABLE READ
// transaction commit what they wrote.
func isolatedTransactions(t *testing.T, s *suite) {
	ctx := t.Context()
	for id, opt := range map[string]app.TransactOption{"serializable": app.Serializable(), "repeatable": app.RepeatableRead()} {
		must(t, app.Transact(ctx, func(ctx context.Context) error {
			return s.docs.Insert(ctx, doc{ID: id, N: 1})
		}, opt))
		if got, err := s.docs.Get(ctx, id); err != nil || got.N != 1 {
			t.Errorf("a %s transaction's write: %+v %v", id, got, err)
		}
	}
}

// readOnlyTransaction checks that a read-only transaction reads and refuses
// a store's write with CodeTransactionReadOnly, writing nothing.
func readOnlyTransaction(t *testing.T, s *suite) {
	ctx := t.Context()
	err := app.Transact(ctx, func(ctx context.Context) error {
		if _, err := s.docs.Get(ctx, "serializable"); err != nil {
			return err
		}
		return s.docs.Insert(ctx, doc{ID: "read-only"})
	}, app.ReadOnly())
	if !errs.HasCode(err, app.CodeTransactionReadOnly) {
		t.Errorf("a write in a read-only transaction: %v, want CodeTransactionReadOnly", err)
	}
	_, err = s.docs.Get(ctx, "read-only")
	expect(t, "the refused write", err, app.WireNotFound)
}

// nestedTransactionOptions checks that a nested Transact given an option is
// refused with CodeTransactionNested, the outer one going on.
func nestedTransactionOptions(t *testing.T, s *suite) {
	ctx := t.Context()
	err := app.Transact(ctx, func(ctx context.Context) error {
		if err := s.docs.Insert(ctx, doc{ID: "outer-options"}); err != nil {
			return err
		}
		nested := app.Transact(ctx, func(ctx context.Context) error {
			return s.docs.Insert(ctx, doc{ID: "nested-options"})
		}, app.Serializable())
		if !errs.HasCode(nested, app.CodeTransactionNested) {
			t.Errorf("a nested Transact given an option: %v, want CodeTransactionNested", nested)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("the outer transaction: %v", err)
	}
	for id, want := range map[string]string{"outer-options": "", "nested-options": app.WireNotFound} {
		_, err := s.docs.Get(ctx, id)
		expect(t, id+" after the commit", err, want)
	}
}

// heldPublish checks that a publish waits for its transaction's commit, and
// that a rollback drops it.
func heldPublish(t *testing.T, s *suite) {
	must(t, app.Transact(t.Context(), func(ctx context.Context) error {
		return s.moved.Publish(ctx, doc{ID: "committed"})
	}))
	eventually(t, "the committed message", func() bool { return s.heard.has("committed") })
	err := app.Transact(t.Context(), func(ctx context.Context) error {
		return errors.Join(s.moved.Publish(ctx, doc{ID: "dropped"}), errRolled)
	})
	if !errors.Is(err, errRolled) {
		t.Fatal(err)
	}
	must(t, s.moved.Publish(t.Context(), doc{ID: "after"}))
	eventually(t, "the message after", func() bool { return s.heard.has("after") })
	if s.heard.has("dropped") {
		t.Error("a rolled back publish was delivered")
	}
}

// ready is an update that makes a document ready.
func ready(d *doc) error { d.Ready = true; return nil }

// hooksAfterTheCommit checks that a write's hooks — what wakes a workflow —
// run once its transaction committed, and never for one rolled back.
func hooksAfterTheCommit(t *testing.T, s *suite) {
	ctx := t.Context()
	for _, id := range []string{"rolled", "committed"} {
		_, err := s.lifecycle.Start(ctx, doc{ID: id})
		must(t, err)
	}
	err := app.Transact(ctx, func(ctx context.Context) error {
		_, err := s.docs.Update(ctx, "rolled", ready)
		return errors.Join(err, errRolled)
	})
	if !errors.Is(err, errRolled) {
		t.Fatal(err)
	}
	must(t, app.Transact(ctx, func(ctx context.Context) error {
		_, err := s.docs.Update(ctx, "committed", ready)
		return err
	}))
	eventually(t, "the committed write's transition", func() bool {
		d, err := s.docs.Get(ctx, "committed")
		return err == nil && d.State == "done"
	})
	if d, err := s.docs.Get(ctx, "rolled"); err != nil || d.State != "new" || d.Ready {
		t.Errorf("a rolled back write moved the workflow: %+v %v", d, err)
	}
}

// ns are the numbers and the N of a page's versions, newest first.
func ns(t *testing.T, s *suite, key string) []string {
	t.Helper()
	revs, err := s.pages.Revisions(t.Context(), key)
	if err != nil {
		t.Fatalf("the versions of %s: %v", key, err)
	}
	out := make([]string, 0, len(revs))
	for _, r := range revs {
		out = append(out, strconv.FormatUint(r.Number, 10)+":"+strconv.Itoa(r.Value.N))
	}
	return out
}

// expectVersions fails the test when a page's versions are not want.
func expectVersions(t *testing.T, s *suite, what string, want []string) {
	t.Helper()
	if got := ns(t, s, "p"); !slices.Equal(got, want) {
		t.Errorf("versions %q %s, want %q", got, what, want)
	}
}

// revisions checks that a record keeps its two previous versions, numbered,
// stamped with the user, the oldest pruned in the write that makes a newer
// one; the same JSON makes none, nor a transition that changes only the
// state; Diff says what changed (ADR 0007 §3).
func revisions(t *testing.T, s *suite) {
	ctx := app.WithUser(t.Context(), "u1", struct{}{})
	_, err := s.pageFlow.Start(ctx, doc{ID: "p", N: 1})
	must(t, err)
	for n := 2; n <= pageUpdates; n++ {
		_, err := s.pages.Update(ctx, "p", setN(n))
		must(t, err)
	}
	expectVersions(t, s, "after the updates", keptVersions)
	cur, err := s.pages.Get(ctx, "p")
	must(t, err)
	must(t, s.pages.Put(ctx, cur))
	expectVersions(t, s, "after a write of the same record", keptVersions)
	_, err = s.pages.Update(ctx, "p", ready)
	must(t, err)
	eventually(t, "the guard's transition", func() bool {
		d, err := s.pages.Get(ctx, "p")
		return err == nil && d.State == "done"
	})
	transitionVersions(t, s)
	_, err = s.pages.Revision(ctx, "p", 1)
	expect(t, "a pruned version", err, app.WireNotFound)
}

// transitionVersions checks a page's versions after a write and a
// transition of the state, and what changed between them.
func transitionVersions(t *testing.T, s *suite) {
	t.Helper()
	ctx := t.Context()
	revs, err := s.pages.Revisions(ctx, "p")
	must(t, err)
	if len(revs) != len(keptVersions) || revs[0].Number != transitioned || revs[0].Value.State != "done" || revs[0].By != "u1" || revs[1].Number != transitioned-1 {
		t.Errorf("after a write and a transition of the state: %+v", revs)
	}
	edits, err := s.pages.Diff(ctx, "p", transitioned-2, transitioned)
	must(t, err)
	said := make([]string, 0, len(edits))
	for _, e := range edits {
		said = append(said, strings.Join([]string{e.Op, e.Path, rawText(e.From), rawText(e.To)}, " "))
	}
	if !slices.Equal(said, transitionEdits) {
		t.Errorf("diff %q, want %q", said, transitionEdits)
	}
}

// rawText is a JSON value as its bytes read, "" for none.
func rawText(raw json.RawMessage) string { return string(raw) }

// revisionsRestore checks that a restore writes a version back as a new
// one — whole, or the fields named —, keeping the workflow's state; a unique
// index refuses one and nothing changes.
func revisionsRestore(t *testing.T, s *suite) {
	ctx := t.Context()
	must(t, s.pages.Insert(ctx, doc{ID: "p", Email: "a@example.com", N: 1, State: "new"}))
	_, err := s.pages.Update(ctx, "p", func(d *doc) error { d.Email, d.N, d.State = "b@example.com", 2, "done"; return nil })
	must(t, err)
	got, err := s.pages.Restore(ctx, "p", 1, "/n")
	if err != nil || got.N != 1 || got.Email != "b@example.com" || got.State != "done" {
		t.Errorf("a field restored: %+v %v", got, err)
	}
	must(t, s.pages.Insert(ctx, doc{ID: "q", Email: "a@example.com"}))
	_, err = s.pages.Restore(ctx, "p", 1)
	expect(t, "a restore a unique index refuses", err, app.WireConflict)
	expectVersions(t, s, "after a refused restore", refusedRestore)
	must(t, s.pages.Delete(ctx, "q"))
	got, err = s.pages.Restore(ctx, "p", 1)
	if err != nil || got.Email != "a@example.com" || got.State != "done" {
		t.Errorf("the whole version restored: %+v %v", got, err)
	}
}

// revisionsRollback checks that a write app.Transact rolls back leaves no
// version behind — nor does a transition of the state alone, which writes in
// place —: the record reads as before, and no version holds what never
// committed.
func revisionsRollback(t *testing.T, s *suite) {
	ctx := t.Context()
	_, err := s.pageFlow.Start(ctx, doc{ID: "p", N: 1})
	must(t, err)
	_, err = s.pages.Update(ctx, "p", setN(2))
	must(t, err)
	err = app.Transact(ctx, func(ctx context.Context) error {
		_, err := s.pages.Update(ctx, "p", setN(rolledN))
		return errors.Join(err, errRolled)
	})
	if !errors.Is(err, errRolled) {
		t.Fatal(err)
	}
	if got, err := s.pages.Get(ctx, "p"); err != nil || got.N != 2 {
		t.Errorf("the record after the rollback: %+v %v", got, err)
	}
	versions := ns(t, s, "p")
	if slices.ContainsFunc(versions, func(v string) bool { return strings.HasSuffix(v, ":"+strconv.Itoa(rolledN)) }) {
		t.Errorf("a rolled back write shows in the versions: %q", versions)
	}
	if versions[len(versions)-1] != "1:1" {
		t.Errorf("the rollback pruned a version: %q", versions)
	}
	rolledTransition(t, s)
}

// rolledTransition checks that a transition a rollback undid leaves no
// version holding its state.
func rolledTransition(t *testing.T, s *suite) {
	t.Helper()
	ctx := t.Context()
	err := app.Transact(ctx, func(ctx context.Context) error {
		_, err := s.pageFlow.Fire(ctx, "p", "hold")
		return errors.Join(err, errRolled)
	})
	if !errors.Is(err, errRolled) {
		t.Fatal(err)
	}
	revs, err := s.pages.Revisions(ctx, "p")
	must(t, err)
	for _, r := range revs {
		if r.Value.State != "new" {
			t.Errorf("version %d holds the state a rollback undid: %q", r.Number, r.Value.State)
		}
	}
}

// trash checks the trash of a store with revisions (ADR 0178 C4): a deletion
// leaves every read and frees its unique keys, Untrash brings the record
// back with its versions — refused while another record holds its unique
// key —, and kit purges it 30 days after on the app's clock, not a second
// before, after which the key starts again at version 1. A store without
// revisions deletes at once and keeps no trash.
func trash(t *testing.T, s *suite) {
	trashDeletes(t, s)
	trashUntrashes(t, s)
	trashPurges(t, s)
	trashNeedsRevisions(t, s)
}

// trashDeletes checks that a deletion leaves every read, into the trash.
func trashDeletes(t *testing.T, s *suite) {
	ctx := t.Context()
	must(t, s.pages.Insert(ctx, doc{ID: "p", Email: "a@example.com", N: 1}))
	_, err := s.pages.Update(ctx, "p", setN(2))
	must(t, err)
	must(t, s.pages.Delete(ctx, "p"))
	_, err = s.pages.Get(ctx, "p")
	expect(t, "a read of a trashed page", err, app.WireNotFound)
	if all, err := s.pages.List(ctx); err != nil || len(all) != 0 {
		t.Errorf("a list after the deletion: %v %v", idsOf(all), err)
	}
	expectTrash(t, s, "after the deletion", []app.Tombstone{{Key: "p", At: epoch, PurgeAt: epoch.Add(trashDays)}})
}

// trashUntrashes checks that Untrash brings a page back with its versions,
// once no other page holds its unique key.
func trashUntrashes(t *testing.T, s *suite) {
	ctx := t.Context()
	must(t, s.pages.Insert(ctx, doc{ID: "q", Email: "a@example.com"}))
	_, err := s.pages.Untrash(ctx, "p")
	expect(t, "an untrash a unique index refuses", err, app.WireConflict)
	must(t, s.pages.Delete(ctx, "q"))
	got, err := s.pages.Untrash(ctx, "p")
	if err != nil || got.N != 2 || got.Email != "a@example.com" {
		t.Errorf("the page untrashed: %+v %v", got, err)
	}
	expectVersions(t, s, "after the untrash", []string{"2:2", "1:1"})
	_, err = s.pages.Untrash(ctx, "p")
	expect(t, "an untrash of a page not in the trash", err, app.WireNotFound)
	expectTrash(t, s, "after the untrash", []app.Tombstone{{Key: "q", At: epoch, PurgeAt: epoch.Add(trashDays)}})
}

// trashPurges checks that kit purges each page at its instant on the app's
// clock, not a second before, and that its key starts again at version 1.
func trashPurges(t *testing.T, s *suite) {
	ctx := t.Context()
	s.clock.Advance(time.Hour)
	must(t, s.pages.Delete(ctx, "p"))
	s.clock.Advance(trashDays - time.Second)
	eventually(t, "q purged at its instant", func() bool {
		stones, err := s.pages.Trashed(ctx)
		return err == nil && len(stones) == 1 && stones[0].Key == "p"
	})
	expectTrash(t, s, "a second before p's instant", []app.Tombstone{{Key: "p", At: epoch.Add(time.Hour), PurgeAt: epoch.Add(time.Hour + trashDays)}})
	s.clock.Advance(time.Second)
	eventually(t, "p purged at its instant", func() bool {
		stones, err := s.pages.Trashed(ctx)
		return err == nil && len(stones) == 0
	})
	_, err := s.pages.Revisions(ctx, "p")
	expect(t, "the versions of a purged page", err, app.WireNotFound)
	must(t, s.pages.Insert(ctx, doc{ID: "p", N: rewrittenN}))
	if got := ns(t, s, "p"); !slices.Equal(got, []string{"1:" + strconv.Itoa(rewrittenN)}) {
		t.Errorf("a page written after its purge: %q, want version 1 again", got)
	}
}

// trashNeedsRevisions checks that a store without revisions deletes at once
// and keeps no trash.
func trashNeedsRevisions(t *testing.T, s *suite) {
	ctx := t.Context()
	must(t, s.docs.Insert(ctx, doc{ID: "d"}))
	must(t, s.docs.Delete(ctx, "d"))
	_, err := s.docs.Untrash(ctx, "d")
	expect(t, "an untrash on a store without revisions", err, app.WireInvalid)
	_, err = s.docs.Trashed(ctx)
	expect(t, "the trash of a store without revisions", err, app.WireInvalid)
	must(t, s.docs.Insert(ctx, doc{ID: "d"}))
}

// expectTrash checks what the pages' trash holds.
func expectTrash(t *testing.T, s *suite, what string, want []app.Tombstone) {
	t.Helper()
	got, err := s.pages.Trashed(t.Context())
	if err != nil {
		t.Fatalf("the trash %s: %v", what, err)
	}
	same := len(got) == len(want)
	for i := 0; same && i < len(got); i++ {
		same = got[i].Key == want[i].Key && got[i].At.Equal(want[i].At) && got[i].PurgeAt.Equal(want[i].PurgeAt)
	}
	if !same {
		t.Errorf("the trash %s: %+v, want %+v", what, got, want)
	}
}

// declareQueues declares what the queues' cases read: a queued command, keyed
// by the document's ID — a dispatch whose key waits is not queued again —,
// a topic published later, and one that publishes a key once an hour (ADR
// 0178 C1, #998). On a database each is a table of the SQL broker, leased
// with SKIP LOCKED where the engine has it.
func declareQueues(svc *app.Service, s *suite) {
	s.filed, s.later = &heard{}, &heard{}
	s.file = svc.Command("file", func(_ context.Context, d doc) (app.Empty, error) {
		s.filed.add(d.ID)
		return app.Empty{}, nil
	}, app.Queued()).Key(doc.key)
	s.delayed = svc.Topic[doc]("delayed")
	svc.Subscribe("delayed-heard", s.delayed, func(_ context.Context, d doc) error { s.later.add(d.ID); return nil })
	s.onced = svc.Topic[doc]("onced").Once(doc.key, time.Hour)
	svc.Subscribe("onced-heard", s.onced, func(_ context.Context, d doc) error { s.later.add(d.ID); return nil })
}

// ticking waits, as eventually does, for cond, moving the case's manual
// clock a second at each look: what a consumer waits on — its poll, a
// message's instant — is the app's clock.
func ticking(t *testing.T, s *suite, what string, cond func() bool) {
	t.Helper()
	eventually(t, what, func() bool {
		s.clock.Advance(time.Second)
		return cond()
	})
}

// queuedCommand checks a queued command on the backend: a dispatch inside a
// transaction exists if and only if it commits; one queued for later is not
// handled before its instant and is once after it; a second dispatch of a
// key that waits is not queued again.
func queuedCommand(t *testing.T, s *suite) {
	ctx := t.Context()
	must(t, app.Transact(ctx, func(ctx context.Context) error {
		_, err := s.file.Dispatch(ctx, doc{ID: "kept"})
		return err
	}))
	ticking(t, s, "the committed dispatch", func() bool { return s.filed.has("kept") })
	err := app.Transact(ctx, func(ctx context.Context) error {
		_, dispatchErr := s.file.Dispatch(ctx, doc{ID: "dropped"})
		return errors.Join(dispatchErr, errRolled)
	})
	if !errors.Is(err, errRolled) {
		t.Fatal(err)
	}
	at := s.clock.Now().Add(time.Hour)
	must(t, s.file.DispatchAt(ctx, doc{ID: "later"}, at))
	must(t, s.file.DispatchAt(ctx, doc{ID: "later", N: 1}, at.Add(time.Hour)))
	time.Sleep(10 * pollEvery)
	if s.filed.has("later") {
		t.Fatal("a dispatch for later was handled before its instant")
	}
	s.clock.Advance(time.Hour)
	ticking(t, s, "the dispatch for later", func() bool { return s.filed.has("later") })
	// One worker handles one dispatch at a time, in order (Parallelism 1,
	// BatchSize 1): a dispatch due now, handled, says the one for later was
	// acknowledged, so its lease cannot lapse under the jump below and bring
	// it back — a second handling the case would read as a key queued twice.
	_, err = s.file.Dispatch(ctx, doc{ID: "settled"})
	must(t, err)
	eventually(t, "the dispatch after the one for later", func() bool { return s.filed.has("settled") })
	s.clock.Advance(2 * time.Hour)
	_, err = s.file.Dispatch(ctx, doc{ID: "after"})
	must(t, err)
	ticking(t, s, "the dispatch after", func() bool { return s.filed.has("after") })
	if s.filed.has("dropped") || s.filed.count("later") != 1 {
		t.Errorf("a rolled back dispatch handled: %v; a key that waited handled %d times, want once",
			s.filed.has("dropped"), s.filed.count("later"))
	}
}

// topicLater checks a topic's publication for later and once on the
// backend: a message published for later is not delivered before its
// instant and is after it; a key published twice within its window is
// delivered once.
func topicLater(t *testing.T, s *suite) {
	ctx := t.Context()
	must(t, s.delayed.PublishAt(ctx, doc{ID: "soon"}, s.clock.Now().Add(time.Hour)))
	time.Sleep(10 * pollEvery)
	if s.later.has("soon") {
		t.Fatal("a message for later was delivered before its instant")
	}
	s.clock.Advance(time.Hour)
	ticking(t, s, "the message for later", func() bool { return s.later.has("soon") })
	must(t, s.onced.Publish(ctx, doc{ID: "x"}))
	must(t, s.onced.Publish(ctx, doc{ID: "x", N: 1}))
	must(t, s.onced.Publish(ctx, doc{ID: "y"}))
	ticking(t, s, "the messages once", func() bool { return s.later.has("x") && s.later.has("y") })
	time.Sleep(10 * pollEvery)
	if n := s.later.count("x"); n != 1 {
		t.Errorf("a key published twice within its window was delivered %d times", n)
	}
}

// queryReadOnly checks a query declared with app.ReadOnly (#1140): its Ask
// runs in a read-only transaction — a read answers, a store's write is
// refused before it reaches the backend, nothing written —, and an Ask
// inside the caller's transaction runs in that one, whose options stand.
func queryReadOnly(t *testing.T, s *suite) {
	ctx := t.Context()
	must(t, s.docs.Put(ctx, doc{ID: "a", N: 1}))
	if got, err := s.reader.Ask(ctx, doc{ID: "a"}); err != nil || got.N != 1 {
		t.Fatalf("a read-only query's read: %+v %v", got, err)
	}
	_, err := s.reader.Ask(ctx, doc{ID: "b", N: 2})
	expect(t, "a read-only query's write", err, app.WireInvalid)
	if _, err := s.docs.Get(ctx, "b"); code(err) != app.WireNotFound {
		t.Errorf("a refused write left a document: %v", err)
	}
	must(t, app.Transact(ctx, func(ctx context.Context) error {
		_, err := s.reader.Ask(ctx, doc{ID: "c", N: askedN})
		return err
	}))
	if got, err := s.docs.Get(ctx, "c"); err != nil || got.N != askedN {
		t.Errorf("an Ask inside the caller's transaction: %+v %v", got, err)
	}
}
