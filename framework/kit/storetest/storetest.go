//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/kit/storetest .

// Package storetest is the conformance suite of kit's stores (ADR 0004): what
// a store does on every backend — in memory, in the data directory, on each
// database engine —, run by kit's tests on memory, files and kit's fake
// database, and by each engine module's tests on its engine. It is what
// keeps the test double honest: a product tested in memory sees what its
// database does.
//
//	func TestConformance(t *testing.T) {
//		storetest.Run(t, storetest.BackendConfig{Name: "sqlite", Options: func(t *testing.T, app string) []kit.AppOption {
//			return []kit.AppOption{kit.DataDir(t.TempDir()), kit.Database("database", sqlite.Engine())}
//		}})
//	}
//
// Each case declares a service of its own, with a name of its own, so the
// cases of one run can share one database: their tables never meet.
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

	"github.com/kitsunium/sdk/framework/kit"
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
)

var (
	// errRolled rolls a transaction back.
	errRolled = errors.New("storetest: rolled back")

	// cases are the suite's, in order.
	cases = []struct {
		name string
		run  func(t *testing.T, s *suite)
	}{
		{"write-modes", writeModes},
		{"one-insert-wins", oneInsertWins},
		{"update-is-atomic", updateIsAtomic},
		{"unique-names-its-index", uniqueNamesItsIndex},
		{"find-in-key-order", findInKeyOrder},
		{"keys-are-bytes", keysAreBytes},
		{"document-as-written", documentAsWritten},
		{"transactions", transactions},
		{"held-publish", heldPublish},
		{"hooks-after-the-commit", hooksAfterTheCommit},
		{"revisions", revisions},
		{"revisions-restore", revisionsRestore},
		{"revisions-rollback", revisionsRollback},
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
)

// BackendConfig is where Run runs kit's stores: a name for the subtests and
// the options that place the stores there.
type BackendConfig struct {
	// Name names the backend in the subtests.
	Name string
	// Options place the stores of the app named app on the backend —
	// kit.InMemory(), kit.DataDir, a kit.Database whose URL the environment
	// holds —, once per case. A database is named "database": the suite
	// gives its calls a minute, as 32 writers of one entity queue on a
	// server's lock.
	Options func(t *testing.T, app string) []kit.AppOption
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
	docs      *kit.Store[doc]
	moved     *kit.Topic[doc]
	lifecycle *kit.Workflow[doc, string]
	heard     *heard
	// pages keep their two previous versions (ADR 0007 §3), a workflow
	// over them.
	pages    *kit.Store[doc]
	pageFlow *kit.Workflow[doc, string]
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

// has reports whether id was delivered.
func (h *heard) has(id string) bool {
	h.Lock()
	defer h.Unlock()
	return slices.Contains(h.ids, id)
}

// Run runs every case of the suite on b.
func Run(t *testing.T, b BackendConfig) {
	for _, c := range cases {
		t.Run(b.Name+"/"+c.name, func(t *testing.T) { c.run(t, open(t, b, c.name)) })
	}
}

// open declares the case's service — its store, a topic and its
// subscription, a workflow — and runs it on b until the test ends.
func open(t *testing.T, b BackendConfig, name string) *suite {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	t.Setenv("KIT_SECRETS", "memory")
	t.Setenv("STORETEST_DATABASE_TIMEOUT", "1m")
	svc := kit.NewService("st-"+name, "A case of kit's store conformance suite.")
	docs := svc.Store("docs", doc.key,
		kit.Unique("email", func(d doc) string { return d.Email }),
		kit.Index("team", func(d doc) []string { return d.Teams }))
	pages := svc.Store("pages", doc.key, kit.Revisions(2),
		kit.Unique("email", func(d doc) string { return d.Email }))
	s := &suite{
		heard:     &heard{},
		docs:      docs,
		moved:     svc.Topic[doc]("moved"),
		lifecycle: svc.Workflow("lifecycle", docs, stateOf).Initial("new").When("ready", "new", "done", isReady),
		pages:     pages,
		pageFlow:  svc.Workflow("publishing", pages, stateOf).Initial("new").When("ready", "new", "done", isReady).On("hold", "new", "held"),
	}
	svc.Subscribe("heard", s.moved, func(_ context.Context, d doc) error { s.heard.add(d.ID); return nil })
	app := kit.NewApp(App, svc).With(append([]kit.AppOption{
		kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard),
	}, b.Options(t, App)...)...)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), stopWait)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return s
}

// stateOf is where a document keeps its workflow's state.
func stateOf(d *doc) *string { return &d.State }

// isReady is the workflows' guard.
func isReady(d doc) bool { return d.Ready }

// code is the kit error code of err, "" for none.
func code(err error) string {
	if ke, ok := errors.AsType[*kit.Error](err); ok {
		return ke.Code
	}
	return ""
}

// message is the wire-safe message of err, "" for none.
func message(err error) string {
	if ke, ok := errors.AsType[*kit.Error](err); ok {
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
	expect(t, "an insert over a taken key", s.docs.Insert(ctx, doc{ID: "a", N: 2}), kit.WireConflict)
	must(t, s.docs.Put(ctx, doc{ID: "a", N: putN}))
	got, err := s.docs.Update(ctx, "a", func(d *doc) error { d.N++; return nil })
	if err != nil || got.N != putN+1 {
		t.Errorf("an update: %+v %v", got, err)
	}
	_, err = s.docs.Update(ctx, "none", func(*doc) error { return nil })
	expect(t, "an update of nothing", err, kit.WireNotFound)
	_, err = s.docs.Update(ctx, "a", func(d *doc) error { d.ID = "b"; return nil })
	expect(t, "an update that renames", err, kit.WireInvalid)
	if n, err := s.docs.Count(ctx); err != nil || n != 1 {
		t.Errorf("count: %d %v", n, err)
	}
	must(t, s.docs.Delete(ctx, "a"))
	expect(t, "a delete of nothing", s.docs.Delete(ctx, "a"), kit.WireNotFound)
	_, err = s.docs.Get(ctx, "a")
	expect(t, "a get of nothing", err, kit.WireNotFound)
}

// oneInsertWins runs 32 inserts of one key at once: one wins, the others
// are refused.
func oneInsertWins(t *testing.T, s *suite) {
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() { errs[i] = s.docs.Insert(context.Background(), doc{ID: "one", N: i}) })
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch code(err) {
		case "":
			won++
		case kit.WireConflict:
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
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			_, errs[i] = s.docs.Update(context.Background(), "counter", func(d *doc) error { d.N++; return nil })
		})
	}
	wg.Wait()
	must(t, errors.Join(errs...))
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
	if code(err) != kit.WireConflict || !strings.Contains(said, `"email"`) || strings.Contains(said, "ada@example.com") {
		t.Errorf("a second holder of a unique key: %v", err)
	}
	if got, err := s.docs.Lookup(t.Context(), "email", "ada@example.com"); err != nil || got.ID != "ada" {
		t.Errorf("lookup: %+v %v", got, err)
	}
	_, err = s.docs.Lookup(t.Context(), "email", "eve@example.com")
	if code(err) != kit.WireNotFound || strings.Contains(message(err), "eve@example.com") {
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
	err := kit.Transact(ctx, func(ctx context.Context) error {
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
	expect(t, "a rolled back insert", err, kit.WireNotFound)
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
	err := kit.Transact(ctx, func(ctx context.Context) error {
		if err := s.docs.Insert(ctx, doc{ID: "outer"}); err != nil {
			return err
		}
		inner := kit.Transact(ctx, func(ctx context.Context) error {
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
	for id, want := range map[string]string{"outer": "", "after": "", "inner": kit.WireNotFound} {
		_, err := s.docs.Get(ctx, id)
		expect(t, id+" after the commit", err, want)
	}
}

// heldPublish checks that a publish waits for its transaction's commit, and
// that a rollback drops it.
func heldPublish(t *testing.T, s *suite) {
	must(t, kit.Transact(t.Context(), func(ctx context.Context) error {
		return s.moved.Publish(ctx, doc{ID: "committed"})
	}))
	eventually(t, "the committed message", func() bool { return s.heard.has("committed") })
	err := kit.Transact(t.Context(), func(ctx context.Context) error {
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
	err := kit.Transact(ctx, func(ctx context.Context) error {
		_, err := s.docs.Update(ctx, "rolled", ready)
		return errors.Join(err, errRolled)
	})
	if !errors.Is(err, errRolled) {
		t.Fatal(err)
	}
	must(t, kit.Transact(ctx, func(ctx context.Context) error {
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
	ctx := kit.WithUser(t.Context(), "u1", struct{}{})
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
	expect(t, "a pruned version", err, kit.WireNotFound)
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
	expect(t, "a restore a unique index refuses", err, kit.WireConflict)
	expectVersions(t, s, "after a refused restore", refusedRestore)
	must(t, s.pages.Delete(ctx, "q"))
	got, err = s.pages.Restore(ctx, "p", 1)
	if err != nil || got.Email != "a@example.com" || got.State != "done" {
		t.Errorf("the whole version restored: %+v %v", got, err)
	}
}

// revisionsRollback checks that a write kit.Transact rolls back leaves no
// version behind — nor does a transition of the state alone, which writes in
// place —: the record reads as before, and no version holds what never
// committed.
func revisionsRollback(t *testing.T, s *suite) {
	ctx := t.Context()
	_, err := s.pageFlow.Start(ctx, doc{ID: "p", N: 1})
	must(t, err)
	_, err = s.pages.Update(ctx, "p", setN(2))
	must(t, err)
	err = kit.Transact(ctx, func(ctx context.Context) error {
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
	err := kit.Transact(ctx, func(ctx context.Context) error {
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
