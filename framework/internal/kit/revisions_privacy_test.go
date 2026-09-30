package kit_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// What ADR 0006 asks of a record's versions (ADR 0007 §4): sealed at rest as
// the record is, cleared by an erasure — the person's and the retention's —,
// kept from pruning by a hold, carried by an export.

var Folk = kit.NewService("folk", "People whose records keep their revisions, for the revisions' privacy tests (ADR 0007 §4).")

// Member is a person's record.
type Member struct {
	ID     string     `json:"id"`
	Email  string     `json:"email" kit:"subject"`
	Name   string     `json:"name" kit:"personal"`
	Bio    string     `json:"bio,omitempty"`
	Code   string     `json:"code,omitempty" kit:"secret"`
	Left   *time.Time `json:"left,omitempty"`
	Erased *time.Time `json:"erased,omitempty" kit:"erased"`
}

func (m Member) Key() string { return m.ID }

// leftAt is when a member left: the retention erases them 30 days after.
func leftAt(m Member) (time.Time, bool) {
	if m.Left == nil {
		return time.Time{}, false
	}
	return *m.Left, true
}

var Folks = Folk.Store("members", Member.Key, kit.Revisions(2),
	kit.EraseAfter(30*24*time.Hour, leftAt),
	kit.Purpose("Keep the members' pages"))

// Handle is a person's handle, whose name keeps its two former values, in a
// store that keeps two versions: a field's history and the record's
// versions side by side (ADR 0007 §1, §3).
type Handle struct {
	ID    string `json:"id"`
	Email string `json:"email" kit:"subject"`
	Name  string `json:"name" kit:"personal,history=2"`
}

var Handles = Folk.Store("handles", func(h Handle) string { return h.ID }, kit.Revisions(2), kit.Purpose("Name the members"))

var (
	FolkExport = Folk.Query("export", func(ctx context.Context, in Identities) (kit.PersonalData, error) {
		return kit.Export(ctx, in.IDs...)
	})
	FolkErase = Folk.Command("erase", func(ctx context.Context, in Identities) (kit.Erasure, error) {
		return kit.Erase(ctx, "asked by the person", in.IDs...)
	})
)

// startFolk runs the folk in dev: in memory unless opts give a data
// directory.
func startFolk(t *testing.T, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	t.Setenv("KIT_SMTP_URL", "")
	pinDataKeyWithoutFileStore(t)
	app := kit.NewApp("folk", Folk).With(append([]kit.AppConfigurer{
		kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard),
	}, opts...)...)
	run(t, app)
	return app
}

// renamed renames a member.
func renamed(name string) func(*Member) error {
	return func(m *Member) error { m.Name = name; return nil }
}

// memberNames are the names of a member's versions, newest first.
func memberNames(t *testing.T, key string) []string {
	t.Helper()
	revs, err := Folks.Revisions(t.Context(), key)
	if err != nil {
		t.Fatalf("revisions of %s: %v", key, err)
	}
	out := []string{}
	for _, r := range revs {
		out = append(out, r.Value.Name)
	}
	return out
}

// A member's versions rest sealed, as the record does, and open again after
// a restart; the bio, which kit does not seal, rests in clear.
func TestVersionsRestSealed(t *testing.T) {
	needsFileStore(t)
	dir := t.TempDir()
	app := startFolk(t, kit.DataDir(dir))
	ctx := t.Context()
	must(t, Folks.Insert(ctx, Member{ID: "m1", Email: "ann@folk.test", Name: "Annabel Quist", Bio: "bio-one", Code: "code-one"}))
	_, err := Folks.Update(ctx, "m1", func(m *Member) error { m.Name, m.Bio, m.Code = "Annabel Vesper", "bio-two", "code-two"; return nil })
	must(t, err)
	_, err = Folks.Update(ctx, "m1", renamed("Annabel Lowmoor"))
	must(t, err)
	stopClinic(t, app)
	if held := clinicFiles(t, dir, "Quist", "Vesper", "Lowmoor", "code-one", "code-two", "ann@folk.test"); len(held) > 0 {
		t.Errorf("files hold a version's sealed values in clear: %v", held)
	}
	if len(clinicFiles(t, dir, "bio-one")) == 0 {
		t.Error("a former version's bio, which kit does not seal, is nowhere")
	}
	must(t, app.Start(ctx))
	if got := strings.Join(memberNames(t, "m1"), ","); got != "Annabel Lowmoor,Annabel Vesper,Annabel Quist" {
		t.Errorf("versions after a restart: %s", got)
	}
}

// A person's erasure clears their records' versions as it clears the
// records — sealed or not —: a restore brings nothing personal back, and
// nothing personal rests in the files.
func TestAnErasureClearsTheVersions(t *testing.T) {
	for _, b := range []struct {
		name string
		opts func(t *testing.T) (string, []kit.AppConfigurer)
	}{
		{"memory", func(*testing.T) (string, []kit.AppConfigurer) { return "", []kit.AppConfigurer{kit.InMemory()} }},
		{"files", func(t *testing.T) (string, []kit.AppConfigurer) {
			needsFileStore(t)
			dir := t.TempDir()
			return dir, []kit.AppConfigurer{kit.DataDir(dir)}
		}},
	} {
		t.Run(b.name, func(t *testing.T) {
			dir, opts := b.opts(t)
			app := startFolk(t, opts...)
			ctx := t.Context()
			must(t, Folks.Insert(ctx, Member{ID: "m1", Email: "ann@folk.test", Name: "Annabel Quist", Bio: "bio-one"}))
			_, err := Folks.Update(ctx, "m1", func(m *Member) error { m.Name, m.Bio = "Annabel Vesper", "bio-two"; return nil })
			must(t, err)
			must(t, Folks.Insert(ctx, Member{ID: "m2", Email: "bob@folk.test", Name: "Bob Hale"}))
			_, err = Folks.Update(ctx, "m2", renamed("Robert Hale"))
			must(t, err)

			_, err = FolkErase.Dispatch(kit.WithUser(ctx, "dpo", Who{}), Identities{IDs: []string{"ann@folk.test"}})
			must(t, err)
			revs, err := Folks.Revisions(ctx, "m1")
			must(t, err)
			for _, r := range revs {
				if r.Value.Name != "" || r.Value.Email != "" || r.Value.Erased == nil {
					t.Errorf("version %d after the erasure: %+v", r.Number, r.Value)
				}
			}
			if len(revs) < 3 || revs[1].Value.Bio != "bio-two" || revs[2].Value.Bio != "bio-one" {
				t.Errorf("an erasure keeps what it does not clear: %+v", revs)
			}
			back, err := Folks.Restore(ctx, "m1", revs[len(revs)-1].Number)
			must(t, err)
			if back.Name != "" || back.Email != "" {
				t.Errorf("a restore after the erasure brings back %+v", back)
			}
			if got := strings.Join(memberNames(t, "m2"), ","); got != "Robert Hale,Bob Hale" {
				t.Errorf("another person's versions: %s", got)
			}
			if dir == "" {
				return
			}
			stopClinic(t, app)
			if held := clinicFiles(t, dir, "Quist", "Vesper", "ann@folk.test"); len(held) > 0 {
				t.Errorf("an erased person's versions rest in clear in %v", held)
			}
		})
	}
}

// A hold keeps a record's versions from pruning, and moves them under the
// record's own key: its person's erasure leaves them readable. Its first
// write after the release prunes them.
func TestAHoldKeepsTheVersions(t *testing.T) {
	needsFileStore(t)
	startFolk(t, kit.DataDir(t.TempDir()))
	ctx := t.Context()
	must(t, Folks.Insert(ctx, Member{ID: "m1", Email: "ann@folk.test", Name: "n1"}))
	must(t, Folks.Hold(ctx, "m1", "a court's order"))
	for _, name := range []string{"n2", "n3", "n4", "n5"} {
		_, err := Folks.Update(ctx, "m1", renamed(name))
		must(t, err)
	}
	if got := strings.Join(memberNames(t, "m1"), ","); got != "n5,n4,n3,n2,n1" {
		t.Errorf("a held record's versions: %s, want every one", got)
	}
	out, err := FolkErase.Dispatch(ctx, Identities{IDs: []string{"ann@folk.test"}})
	must(t, err)
	if len(out.Stores) != 1 || len(out.Stores[0].Held) != 1 {
		t.Fatalf("the erasure of a held record: %+v", out)
	}
	if got := strings.Join(memberNames(t, "m1"), ","); got != "n5,n4,n3,n2,n1" {
		t.Errorf("a held record's versions after its person's erasure: %s", got)
	}
	must(t, Folks.Release(ctx, "m1"))
	_, err = Folks.Update(ctx, "m1", renamed("n6"))
	must(t, err)
	if got := strings.Join(memberNames(t, "m1"), ","); got != "n6,n5,n4" {
		t.Errorf("versions after the release's first write: %s, want two former ones", got)
	}
}

// The retention's erasure clears the versions as the person's does.
func TestTheRetentionClearsTheVersions(t *testing.T) {
	clk := clock.NewManualClock(epoch)
	startFolk(t, kit.InMemory(), kit.Clock(clk))
	ctx := t.Context()
	must(t, Folks.Insert(ctx, Member{ID: "m1", Email: "ann@folk.test", Name: "Annabel Quist"}))
	_, err := Folks.Update(ctx, "m1", func(m *Member) error { m.Name, m.Left = "Annabel Vesper", at(0); return nil })
	must(t, err)
	armed(t, clk, 1)
	clk.Advance(30*24*time.Hour + time.Second)
	eventually(t, "the retention's erasure", func() bool {
		m, err := Folks.Get(ctx, "m1")
		return err == nil && m.Name == "" && m.Erased != nil
	})
	for _, name := range memberNames(t, "m1") {
		if name != "" {
			t.Errorf("a version keeps %q after the retention erased the record", name)
		}
	}
}

// An export carries a person's records' versions, their secret members left
// out.
func TestAnExportCarriesTheVersions(t *testing.T) {
	startFolk(t, kit.InMemory())
	ctx := kit.WithUser(t.Context(), "editor", Who{})
	must(t, Folks.Insert(ctx, Member{ID: "m1", Email: "ann@folk.test", Name: "n1", Code: "code-one"}))
	_, err := Folks.Update(ctx, "m1", renamed("n2"))
	must(t, err)
	data, err := FolkExport.Ask(ctx, Identities{IDs: []string{"ann@folk.test"}})
	must(t, err)
	if len(data.Stores) != 1 || len(data.Stores[0].Versions) != 1 {
		t.Fatalf("export: %+v", data)
	}
	versions := data.Stores[0].Versions[0]
	if versions.Record != 0 || len(versions.Versions) != 2 || versions.Versions[0].By != "editor" || versions.Versions[1].Number != 1 {
		t.Errorf("exported versions: %+v", versions)
	}
	raw, err := json.Marshal(data)
	if err != nil || strings.Contains(string(raw), "code-one") {
		t.Errorf("an export carries a secret member of a version: %v", err)
	}
	var first Member
	must(t, json.Unmarshal(versions.Versions[1].Value, &first))
	if first.Name != "n1" {
		t.Errorf("the first version exported: %+v", first)
	}
}

// Held, a record keeps its versions whatever writes it: kit's own write in
// place makes no version either.
func TestAHoldMakesNoVersion(t *testing.T) {
	needsFileStore(t)
	startFolk(t, kit.DataDir(t.TempDir()))
	ctx := context.Background()
	must(t, Folks.Insert(ctx, Member{ID: "m1", Email: "ann@folk.test", Name: "n1"}))
	must(t, Folks.Hold(ctx, "m1", "a court's order"))
	if got := memberNames(t, "m1"); len(got) != 1 {
		t.Errorf("a hold, which seals the record again under its own key, made versions: %q", got)
	}
}

// A field's former values and the record's versions, side by side, sealed:
// each records a change once, neither records a write of the same record,
// and a person's erasure takes both.
func TestFormerValuesAndVersionsSideBySide(t *testing.T) {
	needsFileStore(t)
	startFolk(t, kit.DataDir(t.TempDir()))
	ctx := t.Context()
	must(t, Handles.Insert(ctx, Handle{ID: "h1", Email: "ann@folk.test", Name: "n1"}))
	for _, name := range []string{"n2", "n3"} {
		_, err := Handles.Update(ctx, "h1", func(h *Handle) error { h.Name = name; return nil })
		must(t, err)
	}
	cur, err := Handles.Get(ctx, "h1")
	must(t, err)
	must(t, Handles.Put(ctx, cur))
	former, err := Handles.Former(ctx, "h1", "/name")
	must(t, err)
	revs, err := Handles.Revisions(ctx, "h1")
	must(t, err)
	if len(former) != 2 || len(revs) != 3 || revs[0].Number != 3 || revs[2].Value.Name != "n1" {
		t.Fatalf("former values %+v, versions %+v", former, revs)
	}
	_, err = FolkErase.Dispatch(ctx, Identities{IDs: []string{"ann@folk.test"}})
	must(t, err)
	if former, err := Handles.Former(ctx, "h1", "/name"); err != nil || len(former) != 0 {
		t.Errorf("former values after the erasure: %+v %v", former, err)
	}
	revs, err = Handles.Revisions(ctx, "h1")
	must(t, err)
	for _, r := range revs {
		if r.Value.Name != "" || r.Value.Email != "" {
			t.Errorf("version %d after the erasure: %+v", r.Number, r.Value)
		}
	}
}
