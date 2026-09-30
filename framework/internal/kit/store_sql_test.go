package kit_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// Stores on a database (ADR 0004), on kit's fake one: a table per store, its
// index keys hashed, its history beside it in the record's transaction, a
// workflow's journal on it; and, on every backend, the watch's notice and a
// workflow's hooks waiting for the commit.

// member is what the register keeps: an e-mail under a unique index, and a
// nickname that remembers its last two values.
type member struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Nick  string `json:"nick" kit:"history=2"`
	State string `json:"state"`
}

func (m member) key() string { return m.ID }

// register is a service of members, with a workflow over them, declared
// afresh for each test.
type register struct {
	svc       *kit.Service
	members   *kit.StoreService[member]
	lifecycle *kit.WorkflowService[member, string]
	moved     *kit.TopicService[entry]
	heard     *heardIDs
}

func newRegister() register {
	r := register{svc: kit.NewService("register", "The register, for the SQL stores' tests."), heard: &heardIDs{}}
	r.members = r.svc.Store("members", member.key, kit.Unique("email", func(m member) string { return m.Email }))
	r.moved = r.svc.Topic[entry]("moved")
	r.svc.Subscribe("heard", r.moved, func(_ context.Context, e entry) error { r.heard.add(e.ID); return nil })
	r.lifecycle = r.svc.Workflow("lifecycle", r.members, func(m *member) *string { return &m.State }).
		Initial("new").
		On("admit", "new", "admitted").
		OnTransition(func(ctx context.Context, c kit.ChangeEvent[member, string]) error {
			return r.moved.Publish(ctx, entry{ID: c.Key + ":" + c.Event})
		})
	return r
}

// runRegister runs the register on db, until the test ends.
func runRegister(t *testing.T, r register, db *kit.FakeDB) *kit.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	t.Setenv("KIT_SECRETS", "memory")
	t.Setenv("REGISTER_DATABASE_URL", verifiedURL())
	app := kit.NewApp("register", r.svc).With(kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false),
		kit.Logs(&syncBuffer{}), kit.DataDir(t.TempDir()), kit.Database("database", db.Engine()))
	run(t, app)
	return app
}

// A store on a database keeps its entities as the JSON kit encoded, in its
// table, and its index keys as keyed hashes: an e-mail never reaches the
// table, and a lookup still finds it.
func TestAStoreOnADatabaseHashesItsIndexKeys(t *testing.T) {
	db, r := kit.NewFakeDB(sql.DialectPostgres), newRegister()
	app := runRegister(t, r, db)
	ada := member{ID: "m1", Email: "ada@example.com", Nick: "ada", State: "new"}
	if err := r.members.Put(t.Context(), ada); err != nil {
		t.Fatal(err)
	}
	raw := db.Documents("register__members")["m1"]
	var back member
	if err := json.Unmarshal([]byte(raw), &back); err != nil || back != ada {
		t.Errorf("the table holds %q", raw)
	}
	for _, key := range db.IndexKeys("register__members") {
		if strings.Contains(key, "ada@example.com") {
			t.Error("an index key reached the table as it is")
		}
	}
	if got, err := r.members.Lookup(t.Context(), "email", "ada@example.com"); err != nil || got.ID != "m1" {
		t.Errorf("lookup: %+v %v", got, err)
	}
	if err := r.members.Insert(t.Context(), member{ID: "m2", Email: "ada@example.com"}); !isConflict(err) {
		t.Errorf("a second holder of a unique key: %v", err)
	}
	st := app.Graph().Node("register/store/members").Store
	if st.Backend != "postgres" || st.Database != "database" || st.Table != "register__members" || st.Count == nil || *st.Count != 1 {
		t.Errorf("the store as the graph says it: %+v", st)
	}
}

// isConflict reports a kit Conflict.
func isConflict(err error) bool {
	var ke *kit.Error
	return errors.As(err, &ke) && ke.Code == kit.WireConflict
}

// A store that remembers keeps its former values on its database, beside
// its table, written in the record's transaction.
func TestAStoreOnADatabaseRemembers(t *testing.T) {
	db, r := kit.NewFakeDB(sql.DialectPostgres), newRegister()
	runRegister(t, r, db)
	for _, nick := range []string{"one", "two", "three"} {
		if err := r.members.Put(t.Context(), member{ID: "m1", Email: "m@example.com", Nick: nick, State: "new"}); err != nil {
			t.Fatal(err)
		}
	}
	former, err := r.members.Former(t.Context(), "m1", "/nick")
	if err != nil || len(former) != 2 || string(former[0].Value) != `"two"` || string(former[1].Value) != `"one"` {
		t.Fatalf("former values: %+v %v", former, err)
	}
	if !slices.Contains(db.Tables(), "register__members__history") {
		t.Errorf("the tables: %v", db.Tables())
	}
	err = kit.Transact(t.Context(), func(ctx context.Context) error {
		if err := r.members.Put(ctx, member{ID: "m1", Email: "m@example.com", Nick: "four", State: "new"}); err != nil {
			return err
		}
		return errMove
	})
	if !errors.Is(err, errMove) {
		t.Fatal(err)
	}
	if former, err := r.members.Former(t.Context(), "m1", "/nick"); err != nil || len(former) != 2 || string(former[0].Value) != `"two"` {
		t.Errorf("a rolled back write left its history: %+v %v", former, err)
	}
}

// A workflow over a store on a database keeps its records there, and a
// transition is one transaction: its record and its entity commit
// together, and its OnTransition hooks run once they did.
func TestAWorkflowOnADatabase(t *testing.T) {
	db, r := kit.NewFakeDB(sql.DialectPostgres), newRegister()
	runRegister(t, r, db)
	if _, err := r.lifecycle.Start(t.Context(), member{ID: "m1", Email: "m@example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.lifecycle.Fire(t.Context(), "m1", "admit"); err != nil {
		t.Fatal(err)
	}
	var inst model.Instance
	if err := json.Unmarshal([]byte(db.Documents("register__lifecycle__workflow")["m1"]), &inst); err != nil || inst.State != "admitted" {
		t.Errorf("the journal on the database: %+v %v", inst, err)
	}
	eventually(t, "the hook's message", func() bool { return slices.Contains(r.heard.list(), "m1:admit") })
}

// A workflow fired inside a transaction that rolls back leaves the entity
// as it was, and its OnTransition hooks never run.
func TestAWorkflowWakesAtTheCommit(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			r := newRegister()
			t.Setenv("KIT_SMTP_URL", "")
			t.Setenv("KIT_SECRETS", "memory")
			t.Setenv("REGISTER_DATABASE_URL", verifiedURL())
			app := kit.NewApp("register", r.svc).With(append([]kit.AppConfigurer{
				kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev),
				kit.Analyze(false), kit.Logs(&syncBuffer{}),
			}, b.opts(t)...)...)
			run(t, app)
			if _, err := r.lifecycle.Start(t.Context(), member{ID: "m1", Email: "m@example.com"}); err != nil {
				t.Fatal(err)
			}
			err := kit.Transact(t.Context(), func(ctx context.Context) error {
				if _, err := r.lifecycle.Fire(ctx, "m1", "admit"); err != nil {
					return err
				}
				return errMove
			})
			if !errors.Is(err, errMove) {
				t.Fatal(err)
			}
			if m, err := r.members.Get(t.Context(), "m1"); err != nil || m.State != "new" {
				t.Errorf("after the rollback: %+v %v", m, err)
			}
			if err := r.moved.Publish(t.Context(), entry{ID: "after"}); err != nil {
				t.Fatal(err)
			}
			eventually(t, "the message after", func() bool { return slices.Contains(r.heard.list(), "after") })
			if slices.Contains(r.heard.list(), "m1:admit") {
				t.Error("a rolled back transition's hook ran")
			}
		})
	}
}

// A watch's notice waits for the commit of the write's transaction: one
// rolled back is never heard.
func TestAWatchNoticeWaitsForTheCommit(t *testing.T) {
	resetHeard()
	app := startReviews(t)
	err := kit.Transact(t.Context(), func(ctx context.Context) error {
		if err := Items.Put(ctx, Item{ID: "rolled", Name: "a lamp rolled back", Price: 1, State: Draft}); err != nil {
			return err
		}
		return errMove
	})
	if !errors.Is(err, errMove) {
		t.Fatal(err)
	}
	if err := kit.Transact(t.Context(), func(ctx context.Context) error {
		return Items.Put(ctx, Item{ID: "committed", Name: "a lamp committed", Price: 1, State: Draft})
	}); err != nil {
		t.Fatal(err)
	}
	settled(t, app)
	if got := noticesOf("shop/store/items", "rolled"); len(got) != 0 {
		t.Errorf("a rolled back write was heard: %+v", got)
	}
	if got := noticesOf("shop/store/items", "committed"); len(got) != 1 {
		t.Errorf("a committed write's notices: %+v", got)
	}
}
