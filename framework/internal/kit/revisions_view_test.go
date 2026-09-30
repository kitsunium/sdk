package kit_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// What the Studio sees of a record's versions (ADR 0007 §5, §6): each as the
// data browser shows a record, what changed between two with a personal or
// secret member's values left out. Restoring one is the command line's
// (revisionscmd_internal_test.go): the Studio reads (ADR 0010, D13).

// shownVersion is a version's value as the Studio shows it, member by member.
func shownVersion(t *testing.T, v model.RecordVersion) Member {
	t.Helper()
	var m Member
	if err := json.Unmarshal(v.Value, &m); err != nil {
		t.Fatalf("version %d: %v", v.Number, err)
	}
	return m
}

// The Studio's versions show a personal member redacted — sealed on disk —
// and a secret never; what changed says a personal or secret member
// changed, never what, and shows the rest.
func TestTheStudioShowsVersionsRedacted(t *testing.T) {
	for _, b := range []struct {
		name, hidden string
		opts         func(t *testing.T) []kit.AppConfigurer
	}{
		{"memory", "[redacted]", func(*testing.T) []kit.AppConfigurer { return []kit.AppConfigurer{kit.InMemory()} }},
		{"files", model.SealedPlaceholder, func(t *testing.T) []kit.AppConfigurer {
			needsFileStore(t)
			return []kit.AppConfigurer{kit.DataDir(t.TempDir())}
		}},
	} {
		t.Run(b.name, func(t *testing.T) {
			app := startFolk(t, b.opts(t)...)
			ctx := kit.WithUser(t.Context(), "editor", Who{})
			must(t, Folks.Insert(ctx, Member{ID: "m1", Email: "ann@folk.test", Name: "Annabel Quist", Bio: "line one\nline two", Code: "code-one"}))
			_, err := Folks.Update(ctx, "m1", func(m *Member) error {
				m.Name, m.Bio, m.Code = "Annabel Vesper", "line one\nline three", "code-two"
				return nil
			})
			must(t, err)

			r := call(t, app, "GET /_kit/api/revisions?store=folk/store/members&key=m1&from=1&to=2", noBody)
			if r.status != http.StatusOK {
				t.Fatalf("revisions: %d %s", r.status, r.body)
			}
			if leaked := mentions(string(r.body), "Quist", "Vesper", "code-one", "code-two", "ann@folk.test"); leaked != nil {
				t.Errorf("the Studio shows %v: %s", leaked, r.body)
			}
			var got model.RecordVersions
			r.json(t, &got)
			if len(got.Versions) != 2 || got.Versions[0].Number != 2 || got.Versions[0].By != "editor" {
				t.Fatalf("versions: %+v", got.Versions)
			}
			if m := shownVersion(t, got.Versions[1]); m.Name != b.hidden || m.Bio != "line one\nline two" {
				t.Errorf("the first version shows %+v, want the name %s and the bio", m, b.hidden)
			}
			var edits []string
			for _, e := range got.Edits {
				edits = append(edits, fmt.Sprintf("%s %s %s %s", e.Op, e.Path, string(e.From), string(e.To)))
			}
			want := []string{`replace /bio "line one\nline two" "line one\nline three"`, `replace /code  `, `replace /name  `}
			if got.From != 1 || got.To != 2 || !slices.Equal(edits, want) {
				t.Errorf("edits %d→%d %q, want %q", got.From, got.To, edits, want)
			}

			for _, bad := range []struct{ method, path, body string }{
				{"GET", "/_kit/api/revisions?store=folk/store/nothing&key=m1", ""},
				{"GET", "/_kit/api/revisions?store=folk/store/members&key=nobody", ""},
				{"GET", "/_kit/api/revisions?store=folk/store/members&key=m1&from=1", ""},
				{"GET", "/_kit/api/revisions?store=folk/store/members&key=m1&from=1&to=9", ""},
			} {
				if r := call(t, app, bad.method+" "+bad.path, bad.body); r.status < 400 || r.status >= 500 {
					t.Errorf("%s %s %s answers %d %s", bad.method, bad.path, bad.body, r.status, r.body)
				}
			}
		})
	}
}

// The model says how many versions a store keeps, and a password policy
// that it refuses the most common passwords.
func TestTheModelSaysWhatAStoreKeeps(t *testing.T) {
	app := startFolk(t, kit.InMemory())
	n := app.Graph().Node("folk/store/members")
	if n == nil || n.Store.History == nil || n.Store.History.Revisions != 2 {
		t.Fatalf("the members' history: %+v", n)
	}
	if h := app.Graph().Node("press/store/pages"); h != nil {
		t.Errorf("another app's store is in the graph: %s", h.ID)
	}
	keeper := startKeeper(t, kit.InMemory())
	holders := keeper.Graph().Node("keeper/store/holders").Store.History
	if len(holders.Passwords) != 1 || !holders.Passwords[0].NotCommon || holders.Revisions != 0 {
		t.Errorf("the holders' history: %+v", holders)
	}
}
