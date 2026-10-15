package kit_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// The trash and privacy (ADR 0178 C4): a deletion by the product keeps the
// record 30 days, restorable, and its person's data key with it; a person's
// erasure leaves nothing of them in the trash, journaled; a record in the
// trash leaves it at its retention's instant when that comes first; and
// <store>-trash set to 0 deletes at once. The trash's own contract — every
// engine, a store without revisions — is the conformance suite's
// (kit/storetest, "trash").

// trashDays is how long a store with revisions keeps what the product
// deletes, unless its setting says otherwise.
const trashDays = 30 * 24 * time.Hour

// trashedKeys are the keys in the members' trash.
func trashedKeys(t *testing.T) []string {
	t.Helper()
	stones, err := Folks.Trashed(t.Context())
	if err != nil {
		t.Fatalf("the trash: %v", err)
	}
	out := []string{}
	for _, st := range stones {
		out = append(out, st.Key)
	}
	return out
}

// journalOps are the operations the privacy journal holds for the members.
func journalOps(t *testing.T, app *kit.App) []string {
	t.Helper()
	var ops []string
	for _, e := range privacyOf(t, app).Journal {
		if e.Store == "folk/store/members" || e.Op == model.JournalShred {
			ops = append(ops, e.Op)
		}
	}
	return ops
}

// A person's erasure purges what the product deleted of them from the
// trash, at once, and journals each as a deletion: nothing of them is left
// to restore, no version to read, and nothing rests on disk. Another
// person's deleted record stays in the trash.
func TestAnErasureLeavesNothingInTheTrash(t *testing.T) {
	eachFolk(t, func(t *testing.T, app *kit.App, dir string, _ *clock.ManualClock) {
		ctx := t.Context()
		must(t, Folks.Insert(ctx, Member{ID: "m1", Email: "ann@folk.test", Name: "Annabel Quist"}))
		_, err := Folks.Update(ctx, "m1", renamed("Annabel Vesper"))
		must(t, err)
		must(t, Folks.Insert(ctx, Member{ID: "m2", Email: "bob@folk.test", Name: "Bob Hale"}))
		must(t, Folks.Delete(ctx, "m1"))
		must(t, Folks.Delete(ctx, "m2"))
		if got := trashedKeys(t); !slices.Equal(got, []string{"m1", "m2"}) {
			t.Fatalf("the trash after the deletions: %q", got)
		}

		out, err := FolkErase.Dispatch(kit.WithUser(ctx, "dpo", Who{}), Identities{IDs: []string{"ann@folk.test"}})
		must(t, err)
		if len(out.Stores) != 1 || out.Stores[0].Store != "folk/store/members" || !slices.Equal(out.Stores[0].Deleted, []string{"m1"}) {
			t.Errorf("the erasure: %+v, want m1 deleted from the members' trash", out)
		}
		if got := trashedKeys(t); !slices.Equal(got, []string{"m2"}) {
			t.Errorf("the trash after ann's erasure: %q, want bob's alone", got)
		}
		_, err = Folks.Untrash(ctx, "m1")
		expectCode(t, "an untrash after the erasure", err, kit.WireNotFound)
		_, err = Folks.Revisions(ctx, "m1")
		expectCode(t, "the versions after the erasure", err, kit.WireNotFound)
		if ops := journalOps(t, app); !slices.Contains(ops, model.JournalDelete) {
			t.Errorf("the journal after the erasure: %q, want the trashed record's deletion", ops)
		}
		if back, err := Folks.Untrash(ctx, "m2"); err != nil || back.Name != "Bob Hale" {
			t.Errorf("another person's record after the erasure: %+v %v", back, err)
		}
		if dir == "" {
			return
		}
		stopClinic(t, app)
		if held := clinicFiles(t, dir, "Quist", "Vesper", "ann@folk.test"); len(held) > 0 {
			t.Errorf("an erased person's trashed record rests in clear in %v", held)
		}
	})
}

// A record in the trash keeps its person's data key — it may come back —
// even when it was their last: untrashed, it opens as it was. Its purge, 30
// days after, on the app's clock, destroys the key once nothing else needs
// it, and the journal says so; nothing of the deletion itself is journaled,
// as before.
func TestATrashedRecordKeepsItsPersonsKeyUntilItsPurge(t *testing.T) {
	eachFolk(t, func(t *testing.T, app *kit.App, _ string, clk *clock.ManualClock) {
		ctx := t.Context()
		must(t, Folks.Insert(ctx, Member{ID: "m1", Email: "ann@folk.test", Name: "Annabel Quist", Code: "code-one"}))
		must(t, Folks.Delete(ctx, "m1"))
		back, err := Folks.Untrash(ctx, "m1")
		if err != nil || back.Name != "Annabel Quist" || back.Email != "ann@folk.test" {
			t.Fatalf("the last record of a person, untrashed: %+v %v", back, err)
		}
		if names := memberNames(t, "m1"); !slices.Equal(names, []string{"Annabel Quist"}) {
			t.Errorf("the versions after the untrash: %q", names)
		}
		must(t, Folks.Delete(ctx, "m1"))
		if ops := journalOps(t, app); slices.Contains(ops, model.JournalShred) || slices.Contains(ops, model.JournalDelete) {
			t.Errorf("the journal after a deletion to the trash: %q", ops)
		}
		clk.Advance(trashDays - time.Second)
		if got := trashedKeys(t); !slices.Equal(got, []string{"m1"}) {
			t.Errorf("the trash a second before the purge: %q", got)
		}
		clk.Advance(time.Second)
		eventually(t, "the purge at its instant", func() bool { return len(trashedKeys(t)) == 0 })
		if app.Graph().Node("folk/store/members").Store.Backend == "memory" {
			return // a store in memory seals nothing: no key to destroy
		}
		eventually(t, "the person's key destroyed after the purge", func() bool {
			return slices.Contains(journalOps(t, app), model.JournalShred)
		})
	})
}

// A record in the trash leaves it at its retention's instant when that
// comes before its 30 days: the trash keeps nothing longer than the store's
// retention would have.
func TestATrashedRecordLeavesAtItsRetention(t *testing.T) {
	clk := clock.NewManualClock(epoch)
	startFolk(t, kit.InMemory(), kit.Clock(clk))
	ctx := t.Context()
	must(t, Folks.Insert(ctx, Member{ID: "m1", Email: "ann@folk.test", Name: "Annabel Quist", Left: at(-20 * 24 * time.Hour)}))
	must(t, Folks.Delete(ctx, "m1"))
	stones, err := Folks.Trashed(ctx)
	must(t, err)
	if len(stones) != 1 || !stones[0].PurgeAt.Equal(epoch.Add(10*24*time.Hour)) {
		t.Fatalf("the trash: %+v, want m1 purged at its retention's instant", stones)
	}
	clk.Advance(10*24*time.Hour + time.Second)
	eventually(t, "the purge at the retention's instant", func() bool { return len(trashedKeys(t)) == 0 })
}

// <store>-trash at 0 turns the trash off: a deletion takes the record and
// its versions at once.
func TestATrashOfZeroDeletesAtOnce(t *testing.T) {
	t.Setenv("FOLK_MEMBERS_TRASH", "0s")
	startFolk(t, kit.InMemory())
	ctx := t.Context()
	must(t, Folks.Insert(ctx, Member{ID: "m1", Email: "ann@folk.test", Name: "Annabel Quist"}))
	must(t, Folks.Delete(ctx, "m1"))
	if got := trashedKeys(t); len(got) != 0 {
		t.Errorf("the trash with <store>-trash at 0: %q", got)
	}
	_, err := Folks.Revisions(ctx, "m1")
	expectCode(t, "the versions of a record deleted at once", err, kit.WireNotFound)
}

// The trash setting is declared with the store's revisions, as
// <store>-trash, 30 days by default.
func TestTheTrashIsASetting(t *testing.T) {
	app := startFolk(t, kit.InMemory())
	if s := settingOf(t, app, "FOLK_MEMBERS_TRASH"); !strings.HasPrefix(s.Value, "720h") {
		t.Errorf("members-trash: %+v, want 30 days", s)
	}
}

// expectCode checks the kit wire code of err.
func expectCode(t *testing.T, what string, err error, want string) {
	t.Helper()
	var ke *kit.Error
	if !errorsAs(err, &ke) || ke.Code != want {
		t.Errorf("%s: %v, want %s", what, err, want)
	}
}
