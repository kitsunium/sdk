package kit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/queue"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// Sealing at rest, from inside (ADR 0006 §4): the documents as they rest,
// where a box opens, a store keyed by what it seals, a deleted record's key,
// what a store kept in clear before, the password policy's hashes, a dead
// letter of a person erased, and where the keys are kept.

// A document is read as encoding/json wrote it — escaped names, nested
// values, whitespace —, and written again with only what changed replaced.
func TestADocumentIsReadAsWritten(t *testing.T) {
	doc := []byte(` { "a\"b" : [1, {"c": "}"}] , "d":null,"e":"x\\"}`)
	members, ok := objectMembers(doc)
	if !ok || len(members) != 3 || string(members[0].name) != `a"b` || string(members[0].value) != `[1, {"c": "}"}]` || string(members[2].value) != `"x\\"` {
		t.Fatalf("members of %s: %+v %t", doc, members, ok)
	}
	elems, ok := arrayElems(members[0].value)
	if !ok || len(elems) != 2 || string(elems[1]) != `{"c": "}"}` {
		t.Fatalf("elements: %q %t", elems, ok)
	}
	if got := string(setAt([]byte(`{"a":[{"b":1}]}`), "/a/0/c~1d", []byte(`"x"`))); got != `{"a":[{"b":1,"c/d":"x"}]}` {
		t.Errorf("setAt adds a member: %s", got)
	}
	if got := string(setAt([]byte(`{"a":[1]}`), "/a/3", []byte(`2`))); got != `{"a":[1]}` {
		t.Errorf("setAt past a list's end: %s", got)
	}
	for _, bad := range []string{`{"a":}`, `{"a" 1}`, `[1,,2]`, `{"a":"b"`, `"s"`} {
		if _, ok := objectMembers([]byte(bad)); ok {
			t.Errorf("%s read as an object", bad)
		}
	}
}

// A box does not tell a short value from another: every value is padded to
// a multiple of 16 bytes before it is sealed, and read back without it.
func TestABoxHidesAValuesLength(t *testing.T) {
	if len(padded([]byte("true"))) != len(padded([]byte("false"))) || len(padded([]byte(`""`))) != len(padded([]byte(`"ann"`))) {
		t.Error("two short values pad to different lengths")
	}
	if got := unpadded(padded([]byte(`"a b "`))); string(got) != `"a b "` {
		t.Errorf("a padded value reads back as %q", got)
	}
}

// card is a record the internal sealing tests keep.
type card struct {
	ID     string `json:"id"`
	Owner  string `json:"owner,omitempty" kit:"subject"`
	Secret string `json:"secret,omitempty" kit:"secret"`
	Note   string `json:"note,omitempty" kit:"personal,history=2"`
}

// pinDataKeyWithoutFileStore pins data-key where the SDK keeps no protected
// file store — Windows, Plan 9 —: the secrets live in memory there, and kit
// refuses a store sealed on disk whose data-key would be made again.
func pinDataKeyWithoutFileStore(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Setenv("KIT_DATA_KEY", "a data key of exactly 32 bytes..")
	}
}

// sealedCards runs a store of cards on the data in dir, alone, in dev.
func sealedCards(t *testing.T, name, dir string) (*App, *StoreService[card]) {
	t.Helper()
	pinDataKeyWithoutFileStore(t)
	svc := NewService(name, "Cards sealed at rest, for the sealing tests.")
	cards := svc.Store("cards", func(c card) string { return c.ID })
	app := NewApp(name, svc).With(DataDir(dir), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	must(t, app.Start(t.Context()))
	stopAtCleanup(t, app)
	return app, cards
}

// restingAt is the record under key as its store keeps it at rest.
func restingAt(t *testing.T, s *StoreService[card], key string) []byte {
	t.Helper()
	d, err := s.sealing().inner.Get(t.Context(), key)
	must(t, err)
	return d.raw
}

// A box opens only where it was sealed: moved into another record, or into
// another field of its own, it is refused, never read as the other's value.
func TestABoxOpensOnlyWhereItLies(t *testing.T) {
	_, cards := sealedCards(t, "binding", t.TempDir())
	ctx := t.Context()
	must(t, cards.Insert(ctx, card{ID: "c1", Owner: "ann", Secret: "s1", Note: "n1"}))
	must(t, cards.Insert(ctx, card{ID: "c2", Owner: "ann", Secret: "s2", Note: "n2"}))
	c1 := restingAt(t, cards, "c1")
	members, _ := objectMembers(c1)
	note := members[slices.IndexFunc(members, func(m jsonMember) bool { return string(m.name) == "note" })].value
	se := cards.sealing()
	for what, moved := range map[string][]byte{
		"into another record":     setAt(restingAt(t, cards, "c2"), "/note", note),
		"into another of its own": setAt(c1, "/secret", note),
	} {
		key := "c2"
		if strings.Contains(what, "own") {
			key = "c1"
		}
		if _, err := se.openRecord(ctx, key, moved); !errs.HasCode(err, CodeSealOpen) {
			t.Errorf("a box moved %s: %v", what, err)
		}
	}
	if c, err := se.openRecord(ctx, "c1", c1); err != nil || c.Note != "n1" || c.Secret != "s1" {
		t.Errorf("the box where it lies: %+v, %v", c, err)
	}
}

// badge is keyed by its subject: its key reads a member kit seals.
type badge struct {
	ID   string `json:"id" kit:"subject"`
	Name string `json:"name" kit:"personal"`
}

// A store whose key reads a member kit seals keeps that member in clear —
// the key is in its files anyway —, seals the rest, says so, and opens
// again at the next start.
func TestAKeyThatReadsASealedMemberStaysInClear(t *testing.T) {
	pinDataKeyWithoutFileStore(t)
	dir := t.TempDir()
	svc := NewService("badges", "Badges keyed by their holder, for the sealing tests.")
	badges := svc.Store("badges", func(b badge) string { return b.ID })
	app := NewApp("badges", svc).With(DataDir(dir), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	must(t, app.Start(t.Context()))
	stopAtCleanup(t, app)
	ctx := t.Context()
	must(t, badges.Insert(ctx, badge{ID: "holder-1", Name: "Ann Holder"}))
	must(t, badges.Insert(ctx, badge{ID: "holder-2", Name: "Bob Holder"}))
	d, err := badges.sealing().inner.Get(ctx, "holder-2")
	must(t, err)
	if !bytes.Contains(d.raw, []byte(`"id":"holder-2"`)) || bytes.Contains(d.raw, []byte("Bob")) {
		t.Errorf("at rest: %s", d.raw)
	}
	said := 0
	for _, dg := range app.Graph().Diagnostics {
		if strings.Contains(dg.Message, "its key reads /id") {
			said++
		}
	}
	if said != 1 {
		t.Errorf("the store says it %d times", said)
	}
	must(t, app.Stop(context.Background()))
	must(t, app.Start(ctx))
	if b, err := badges.Get(ctx, "holder-1"); err != nil || b.Name != "Ann Holder" {
		t.Errorf("after a restart: %+v, %v", b, err)
	}
}

// A record about nobody is sealed under a data key of its own, which goes
// with it: deleted, nothing opens what it sealed. A person's key stays
// when one of their records is deleted by the product.
func TestADeletedRecordTakesItsOwnKey(t *testing.T) {
	app, cards := sealedCards(t, "ownkeys", t.TempDir())
	ctx := t.Context()
	keys := app.privacyKeyStore()
	count := func() int {
		n, err := keys.engine().Count(ctx)
		must(t, err)
		return n
	}
	must(t, cards.Insert(ctx, card{ID: "orphan", Note: "nobody's"}))
	must(t, cards.Insert(ctx, card{ID: "owned", Owner: "ann", Note: "ann's"}))
	must(t, cards.Insert(ctx, card{ID: "owned-too", Owner: "ann", Note: "ann's too"}))
	if n := count(); n != 2 {
		t.Fatalf("%d data keys for a record about nobody and a person's two", n)
	}
	orphan := restingAt(t, cards, "orphan")
	must(t, cards.Delete(ctx, "orphan"))
	must(t, cards.Delete(ctx, "owned"))
	if n := count(); n != 1 {
		t.Errorf("%d data keys after the deletions, want the person's", n)
	}
	if c, err := cards.sealing().openRecord(ctx, "orphan", orphan); err != nil || c.Note != "" {
		t.Errorf("a copy of the deleted record: %+v, %v", c, err)
	}
}

// legacyNote is kept by a store that sealed nothing before.
type legacyNote struct {
	ID    string `json:"id"`
	Owner string `json:"owner" kit:"subject"`
	Body  string `json:"body" kit:"personal,history=2"`
}

var legacySvc = NewService("legacy", "Notes written before kit sealed them, for the sealing tests.")

var legacyNotes = legacySvc.Store("notes", func(n legacyNote) string { return n.ID })

// A member stored before its field was sealed is read as written, sealed at
// its record's next write; `privacy seal` seals the rest of the store at
// once, its former values too, and says what it did.
func TestAPlainMemberIsSealedAtItsNextWrite(t *testing.T) {
	pinDataKeyWithoutFileStore(t)
	dir := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(dir, "legacy"), 0o700))
	for file, content := range map[string]string{
		"notes.json":         `{"n1":{"id":"n1","owner":"ann@x.dev","body":"old words one"},"n2":{"id":"n2","owner":"bob@x.dev","body":"old words two"}}`,
		"notes.history.json": `{"n1":{"key":"n1","fields":{"/body":[{"value":"older words","until":"2026-09-27T09:00:00Z"}]}}}`,
	} {
		must(t, os.WriteFile(filepath.Join(dir, "legacy", file), []byte(content), 0o600))
	}
	app := NewApp("legacy", legacySvc).With(DataDir(dir), Listen("127.0.0.1:0"), Env(EnvProduction), Logs(io.Discard))
	must(t, app.Start(t.Context()))
	ctx := t.Context()
	if n, err := legacyNotes.Get(ctx, "n1"); err != nil || n.Body != "old words one" {
		t.Fatalf("a plain member, read as written: %+v, %v", n, err)
	}
	_, err := legacyNotes.Update(ctx, "n2", func(n *legacyNote) error { n.Body = "new words two"; return nil })
	must(t, err)
	if raw := string(restingOf(t, legacyNotes, "n2")); strings.Contains(raw, "words") || !strings.Contains(raw, boxPrefix) {
		t.Errorf("a record written again: %s", raw)
	}
	must(t, app.Stop(context.Background()))
	var out, errOut bytes.Buffer
	cmd := NewApp("legacy", legacySvc).With(DataDir(dir), Env(EnvProduction), Listen("127.0.0.1:1"), Logs(io.Discard))
	if code := cmd.privacyCommand(ctx, []string{"seal", "legacy/store/notes"}, &out, &errOut); code != 0 || out.String() != "legacy/store/notes: 1 of 2 records sealed now, the others were already\n" {
		t.Fatalf("privacy seal: %d %q %q", code, out.String(), errOut.String())
	}
	if held := restingFiles(t, dir, "words", "ann@x.dev", "bob@x.dev"); len(held) > 0 {
		t.Errorf("sealed, yet in clear in %v", held)
	}
	must(t, app.Start(ctx))
	stopAtCleanup(t, app)
	if former, err := legacyNotes.Former(ctx, "n1", "/body"); err != nil || len(former) != 1 || string(former[0].Value) != `"older words"` {
		t.Errorf("a former value sealed by the command: %+v, %v", former, err)
	}
	if n, err := legacyNotes.Get(ctx, "n1"); err != nil || n.Body != "old words one" {
		t.Errorf("a record sealed by the command: %+v, %v", n, err)
	}
}

// restingOf is the record under key as a store keeps it at rest.
func restingOf[T any](t *testing.T, s *StoreService[T], key string) []byte {
	t.Helper()
	d, err := s.sealing().inner.Get(t.Context(), key)
	must(t, err)
	return d.raw
}

// restingFiles lists the files under dir, the secrets' aside, that hold any
// of words.
func restingFiles(t *testing.T, dir string, words ...string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.Contains(path, besideData) {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(words, func(w string) bool { return bytes.Contains(raw, []byte(w)) }) {
			out = append(out, path)
		}
		return nil
	})
	must(t, err)
	return out
}

// The password policy works on hashes sealed at rest: set, changed,
// verified, a former one refused — and no hash, current or former, rests
// in clear.
func TestThePasswordPolicyVerifiesSealedHashes(t *testing.T) {
	pinDataKeyWithoutFileStore(t)
	f := useFakeHashing(t)
	dir := t.TempDir()
	l := startLocks(t, "sealedlocks", []PasswordConfigurer{NotReused(2)}, DataDir(dir))
	ctx := t.Context()
	must(t, l.accounts.Insert(ctx, lockAccount{ID: "a1"}))
	must(t, l.policy.Set(ctx, "a1", pw("one")))
	must(t, l.policy.Change(ctx, "a1", pw("one"), pw("two")))
	if ok, err := l.policy.Verify(ctx, "a1", pw("two")); !ok || err != nil {
		t.Errorf("a sealed hash verifies: %t %v", ok, err)
	}
	if err := l.policy.Set(ctx, "a1", pw("one")); !isInvalid(err) {
		t.Errorf("a former hash, sealed, refuses its password again: %v", err)
	}
	f.counts()
	must(t, l.app.Stop(context.Background()))
	for _, file := range []string{"accounts.json", "accounts.history.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, "sealedlocks", file))
		must(t, err)
		if bytes.Contains(raw, []byte("$test$")) {
			t.Errorf("%s holds a hash in clear: %s", file, raw)
		}
	}
}

// letter2 is a message the internal sealing tests queue.
type letter2 struct {
	To   string `json:"to" kit:"subject"`
	Body string `json:"body" kit:"personal"`
}

// A dead letter about a person is sealed under their key: once they are
// erased, it reads as empty.
func TestADeadLetterOfAPersonErasedReadsEmpty(t *testing.T) {
	pinDataKeyWithoutFileStore(t)
	svc := NewService("post2", "Letters no one accepts, for the sealing tests.")
	letters := svc.Topic[letter2]("letters")
	svc.Subscribe("refuse", letters, func(context.Context, letter2) error { return errors.New("refused") }, MaxDeliveries(1))
	people := svc.Store("people", func(l letter2) string { return l.To })
	forget := svc.Command("forget", func(ctx context.Context, in struct {
		ID string `json:"id" kit:"personal"`
	},
	) (Erasure, error) {
		return Erase(ctx, "asked", in.ID)
	})
	app := NewApp("post2", svc).With(DataDir(t.TempDir()), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	must(t, app.Start(t.Context()))
	stopAtCleanup(t, app)
	ctx := t.Context()
	must(t, people.Insert(ctx, letter2{To: "ann@x.dev", Body: "ann's file"}))
	must(t, letters.Publish(ctx, letter2{To: "ann@x.dev", Body: "a letter to ann"}))
	sub := letters.subs[0]
	var dead []queue.DeadLetter
	eventuallyInternal(t, "the dead letter", func() bool {
		var err error
		dead, err = sub.broker.(queue.DeadLetterReader).DeadLetters(ctx, 10)
		return err == nil && len(dead) == 1
	})
	if msg, _, derr, oerr := letters.decode(ctx, app, dead[0].Message.Payload); derr != nil || oerr != nil || msg.Body != "a letter to ann" {
		t.Fatalf("the dead letter before the erasure: %+v %v %v", msg, derr, oerr)
	}
	_, err := forget.Dispatch(ctx, struct {
		ID string `json:"id" kit:"personal"`
	}{ID: "ann@x.dev"})
	must(t, err)
	if msg, _, derr, oerr := letters.decode(ctx, app, dead[0].Message.Payload); derr != nil || oerr != nil || msg.Body != "" || msg.To != "" {
		t.Errorf("the dead letter after the erasure: %+v %v %v", msg, derr, oerr)
	}
}

// Where the data keys are, against what they seal: data-key in memory with
// a store on disk is refused — pinned, it starts —, and a database that
// keeps the data keys beside a store they seal is warned of. SQLite never
// keeps them: the transaction of a write that seals holds the file's one
// writer, which a data key made apart would wait for; kit.Keeps(kit.Privacy)
// puts the holds and the journal there, and the keys stay in the data
// directory — a store that seals starts beside them.
func TestTheDataKeysArePlacedApartFromWhatTheySeal(t *testing.T) {
	t.Setenv("KIT_SECRETS", "memory")
	svc := NewService("placed", "Cards placed by the app, for the sealing tests.")
	svc.Store("cards", func(c card) string { return c.ID })
	start := func(opts ...AppConfigurer) (*App, error) {
		app := NewApp("placed", svc).With(append([]AppConfigurer{DataDir(t.TempDir()), Listen("127.0.0.1:0"), Env(EnvProduction), Logs(io.Discard)}, opts...)...)
		err := app.Start(t.Context())
		if err == nil {
			stopAtCleanup(t, app)
		}
		return app, err
	}
	if _, err := start(); err == nil || !strings.Contains(err.Error(), "KIT_DATA_KEY") {
		t.Errorf("data-key in memory, a store on disk: %v", err)
	}
	t.Setenv("KIT_DATA_KEY", "a data key too short")
	if _, err := start(); err == nil || !strings.Contains(err.Error(), "exactly 32 bytes") {
		t.Errorf("a data-key of 20 bytes: %v", err)
	}
	t.Setenv("KIT_DATA_KEY", "a data key of exactly 32 bytes..")
	t.Setenv("PLACED_VAULT_URL", "fake://kit:pw@db.internal:5432/vault?tls=verify-full")
	app, err := start(Database("vault", NewFakeDB(sql.DialectPostgres).Engine(), Keeps(Privacy)))
	must(t, err)
	if beside(app.Graph()) {
		t.Error("a database that keeps the keys alone is warned of")
	}
	must(t, app.Stop(context.Background()))
	app, err = start(Database("vault", NewFakeDB(sql.DialectPostgres).Engine(), Keeps(Privacy, svc)))
	must(t, err)
	if !beside(app.Graph()) {
		t.Error("a database that keeps the keys beside what they seal is not warned of")
	}
	must(t, app.Stop(context.Background()))
	app, err = start(Database("vault", NewFakeDB(sql.DialectSQLite).Engine(), Keeps(Privacy, svc)))
	must(t, err)
	holds, _ := app.privacyStores()
	if keys, held := app.placementOf(app.privacyKeyStore()), app.placementOf(holds); keys.db != nil || keys.memory || held.db == nil {
		t.Errorf("kit.Privacy on SQLite: the keys %+v, the holds %+v", keys, held)
	}
	g := app.Graph()
	if beside(g) {
		t.Error("SQLite, which keeps no data keys, is warned of")
	}
	if n := g.Node(privacyService + "/store/keys"); n == nil || n.Store.Backend != "file" || n.Store.Database != "" || n.Store.Table != "" {
		t.Errorf("the keys, as the graph says them: %+v", n)
	}
}

// beside reports whether the graph warns of a database keeping the data
// keys beside what they seal.
func beside(g *model.Graph) bool {
	return slices.ContainsFunc(g.Diagnostics, func(d model.Diagnostic) bool {
		return d.Severity == "warning" && strings.Contains(d.Message, "keeps kit's data keys")
	})
}

// eventuallyInternal polls cond until it holds or the deadline passes.
func eventuallyInternal(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A hold moves its record under a data key of its own — its person's
// erasure never reaches it —, and a write of the held record keeps it
// there.
func TestAHeldRecordRestsUnderItsOwnKey(t *testing.T) {
	app, cards := sealedCards(t, "heldcards", t.TempDir())
	ctx := t.Context()
	must(t, cards.Insert(ctx, card{ID: "c1", Owner: "ann", Note: "kept for a court"}))
	keys, err := app.referenceKeys(ctx)
	must(t, err)
	refOf := func() string {
		members, _ := objectMembers(restingAt(t, cards, "c1"))
		for _, m := range members {
			if string(m.name) == "note" {
				ref, _ := boxRef(m.value)
				return ref
			}
		}
		return ""
	}
	if got := refOf(); got != keys.personRef("ann") {
		t.Fatalf("a record about ann rests under %q", got)
	}
	must(t, cards.Hold(ctx, "c1", "a court case"))
	if got := refOf(); got != keys.ownRef(cards.id, "c1") {
		t.Errorf("a held record rests under %q, want its own key", got)
	}
	_, err = cards.Update(ctx, "c1", func(c *card) error { c.Note = "still kept"; return nil })
	must(t, err)
	if got := refOf(); got != keys.ownRef(cards.id, "c1") {
		t.Errorf("a held record written again rests under %q, want its own key", got)
	}
	must(t, cards.Release(ctx, "c1"))
	_, err = cards.Update(ctx, "c1", func(c *card) error { c.Note = "released"; return nil })
	must(t, err)
	if got := refOf(); got != keys.personRef("ann") {
		t.Errorf("a released record written again rests under %q, want its person's", got)
	}
}

// Erasing one record of a person keeps their key: their other records
// open. Erasing their last destroys it — a copy of that record taken
// before opens nothing —, and the journal says so by reference.
func TestErasingAPersonsLastRecordDestroysTheirKey(t *testing.T) {
	app, cards := sealedCards(t, "lastcards", t.TempDir())
	ctx := t.Context()
	must(t, cards.Insert(ctx, card{ID: "c1", Owner: "ann", Note: "first"}))
	must(t, cards.Insert(ctx, card{ID: "c2", Owner: "ann", Note: "second"}))
	must(t, cards.Erase(ctx, "c1", "asked"))
	if c, err := cards.Get(ctx, "c2"); err != nil || c.Note != "second" {
		t.Fatalf("ann's other record, after one erased: %+v, %v", c, err)
	}
	copyOfC2 := restingAt(t, cards, "c2")
	must(t, cards.Erase(ctx, "c2", "asked"))
	if c, err := cards.sealing().openRecord(ctx, "c2", copyOfC2); err != nil || c.Note != "" || c.Owner != "" {
		t.Errorf("a copy of ann's last record, after its erasure: %+v, %v", c, err)
	}
	shreds := 0
	entries, err := app.journalEntries(ctx, 50)
	must(t, err)
	for _, e := range entries {
		if e.Op == model.JournalShred {
			shreds++
		}
	}
	if shreds != 1 {
		t.Errorf("%d shred entries, want one: her last record", shreds)
	}
}

// relabeled is what the store of cards holds once its fields lose their
// classes: the same JSON, no tag.
type relabeled struct {
	ID     string `json:"id"`
	Owner  string `json:"owner,omitempty"`
	Secret string `json:"secret,omitempty"`
	Note   string `json:"note,omitempty"`
}

// A store whose fields lose their classes still opens what kit sealed: the
// data directory holds kit's data keys, so kit's own service stays, and the
// next write keeps the record in clear.
func TestAFieldThatLosesItsClassStillOpens(t *testing.T) {
	dir := t.TempDir()
	app, cards := sealedCards(t, "relabel", dir)
	must(t, cards.Insert(t.Context(), card{ID: "c1", Owner: "ann", Secret: "s1", Note: "sealed once"}))
	must(t, app.Stop(context.Background()))
	svc := NewService("relabel", "The same cards, their classes gone, for the sealing tests.")
	plain := svc.Store("cards", func(c relabeled) string { return c.ID })
	again := NewApp("relabel", svc).With(DataDir(dir), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	must(t, again.Start(t.Context()))
	stopAtCleanup(t, again)
	ctx := t.Context()
	if c, err := plain.Get(ctx, "c1"); err != nil || c.Note != "sealed once" || c.Secret != "s1" || c.Owner != "ann" {
		t.Fatalf("a record sealed before its fields lost their classes: %+v, %v", c, err)
	}
	_, err := plain.Update(ctx, "c1", func(c *relabeled) error { c.Note = "in clear now"; return nil })
	must(t, err)
	if raw := restingOf(t, plain, "c1"); bytes.Contains(raw, []byte(boxPrefix)) || !bytes.Contains(raw, []byte("in clear now")) {
		t.Errorf("written again: %s", raw)
	}
}

// stopAtCleanup stops app when the test ends, and fails it when the stop
// does.
func stopAtCleanup(tb testing.TB, app *App) {
	tb.Cleanup(func() {
		if err := app.Stop(context.Background()); err != nil {
			tb.Errorf("stop: %v", err)
		}
	})
}
