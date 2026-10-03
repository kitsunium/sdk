package kit_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// Sealing in a transaction (ADR 0004, ADR 0006): a data key is written apart
// from the transaction of the write that seals under it — a rollback never
// takes back a key the SDK's cache still seals under —, and a key an erasure
// or a deletion destroys goes once the transaction commits — a rollback puts
// back what was sealed under it.

// vault is a product that seals what it keeps: people, under their data
// key, and memos about nobody, each under its own.
type vault struct {
	svc    *kit.Service
	people *kit.StoreService[vaultPerson]
	memos  *kit.StoreService[vaultMemo]
}

type vaultPerson struct {
	ID    string `json:"id"`
	Email string `json:"email" kit:"subject"`
	Name  string `json:"name" kit:"personal"`
}

type vaultMemo struct {
	ID   string `json:"id"`
	Body string `json:"body" kit:"personal"`
}

// errTakeBack rolls a vault's transaction back.
var errTakeBack = errors.New("the transaction is taken back")

// eachSealingBackend runs test where a vault keeps what it seals at rest:
// the data directory, and a database. data-key and index-key are pinned: a
// restart reads what the last run sealed.
func eachSealingBackend(t *testing.T, test func(t *testing.T, v *vault, app *kit.App)) {
	for _, b := range []backend{
		{"files", func(*testing.T) []kit.AppConfigurer { return nil }},
		{"database", func(t *testing.T) []kit.AppConfigurer {
			t.Setenv("VAULT_DATABASE_URL", verifiedURL())
			return []kit.AppConfigurer{kit.Database("database", kit.NewFakeDB(sql.DialectPostgres).Engine())}
		}},
	} {
		t.Run(b.name, func(t *testing.T) {
			needsFileStore(t)
			t.Setenv("KIT_SMTP_URL", "")
			t.Setenv("KIT_SECRETS", "memory")
			t.Setenv("KIT_DATA_KEY", "a data key of exactly 32 bytes..")
			t.Setenv("KIT_INDEX_KEY", "an index key of more than 16 bytes")
			v := &vault{svc: kit.NewService("vault", "Sealed records, for the transactions' tests.")}
			v.people = v.svc.Store("people", func(p vaultPerson) string { return p.ID }, kit.Purpose("Keep the people"))
			v.memos = v.svc.Store("memos", func(m vaultMemo) string { return m.ID }, kit.Purpose("Keep the memos"))
			app := kit.NewApp("vault", v.svc).With(append([]kit.AppConfigurer{
				kit.DataDir(t.TempDir()),
				kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard),
			}, b.opts(t)...)...)
			run(t, app)
			test(t, v, app)
		})
	}
}

// restart stops app and starts it again: the SDK's cache of data keys goes
// with the run, and what was sealed opens from what kit kept.
func restart(t *testing.T, app *kit.App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
}

// A person's data key made in a transaction that rolls back stays: a record
// sealed under it after the rollback reads back after a restart.
func TestADataKeyOutlivesItsRollback(t *testing.T) {
	eachSealingBackend(t, func(t *testing.T, v *vault, app *kit.App) {
		ctx := t.Context()
		err := kit.Transact(ctx, func(ctx context.Context) error {
			if err := v.people.Insert(ctx, vaultPerson{ID: "p1", Email: "ann@vault.test", Name: "Annabel"}); err != nil {
				return err
			}
			return errTakeBack
		})
		if !errors.Is(err, errTakeBack) {
			t.Fatalf("the transaction: %v", err)
		}
		if _, err := v.people.Get(ctx, "p1"); !isNotFound(err) {
			t.Fatalf("the rolled back record: %v", err)
		}
		again := vaultPerson{ID: "p2", Email: "ann@vault.test", Name: "Annabel again"}
		must(t, v.people.Insert(ctx, again))
		restart(t, app)
		if got, err := v.people.Get(ctx, "p2"); err != nil || got != again {
			t.Fatalf("after the restart: %+v %v", got, err)
		}
	})
}

// A deletion rolled back leaves the record's own data key: the record, put
// back, reads as it was.
func TestARolledBackDeletionKeepsItsKey(t *testing.T) {
	eachSealingBackend(t, func(t *testing.T, v *vault, app *kit.App) {
		ctx := t.Context()
		memo := vaultMemo{ID: "m1", Body: "memo words"}
		must(t, v.memos.Insert(ctx, memo))
		err := kit.Transact(ctx, func(ctx context.Context) error {
			if err := v.memos.Delete(ctx, "m1"); err != nil {
				return err
			}
			return errTakeBack
		})
		if !errors.Is(err, errTakeBack) {
			t.Fatalf("the transaction: %v", err)
		}
		restart(t, app)
		if got, err := v.memos.Get(ctx, "m1"); err != nil || got != memo {
			t.Fatalf("the memo: %+v %v", got, err)
		}
	})
}

// An erasure rolled back leaves the person's data key: their record, put
// back, reads as it was. One that commits destroys it.
func TestARolledBackErasureKeepsTheKey(t *testing.T) {
	eachSealingBackend(t, func(t *testing.T, v *vault, app *kit.App) {
		ctx := t.Context()
		ann := vaultPerson{ID: "p1", Email: "ann@vault.test", Name: "Annabel"}
		must(t, v.people.Insert(ctx, ann))
		err := kit.Transact(ctx, func(ctx context.Context) error {
			if err := v.people.Erase(ctx, "p1", "asked by the person"); err != nil {
				return err
			}
			return errTakeBack
		})
		if !errors.Is(err, errTakeBack) {
			t.Fatalf("the transaction: %v", err)
		}
		restart(t, app)
		if got, err := v.people.Get(ctx, "p1"); err != nil || got != ann {
			t.Fatalf("after the rolled back erasure: %+v %v", got, err)
		}
		must(t, kit.Transact(ctx, func(ctx context.Context) error {
			return v.people.Erase(ctx, "p1", "asked by the person")
		}))
		if got, err := v.people.Get(ctx, "p1"); err != nil || got != (vaultPerson{ID: "p1"}) {
			t.Fatalf("after the erasure: %+v %v", got, err)
		}
		if n := shredded(t, app); n != 1 {
			t.Errorf("%d data keys destroyed, want 1", n)
		}
	})
}

// shredded counts the data keys the privacy journal says were destroyed.
func shredded(t *testing.T, app *kit.App) int {
	t.Helper()
	n := 0
	for _, e := range privacyOf(t, app).Journal {
		if e.Op == model.JournalShred {
			n++
		}
	}
	return n
}

// isNotFound reports whether err is kit's NotFound.
func isNotFound(err error) bool {
	var ke *kit.Error
	return errors.As(err, &ke) && ke.Code == kit.WireNotFound
}
