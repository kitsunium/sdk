package kit_test

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// What a store's versions weigh, as the model says it on a running product
// (ADR 0007 §5): how many former versions its records keep, how many records
// are in its trash, and the bytes of the JSON they hold — the SDK's document
// store's count, in memory, in the data directory and on a database — and
// the revision does not move with it.

// draft is what the scales keep: a title, rewritten.
type draft struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (d draft) key() string { return d.ID }

// weighedAs is what the model says the versions weigh, in one line.
func weighedAs(h *model.StoreHistory) (versions, trashed int, size int64, said bool) {
	if h == nil || h.Versions == nil || h.Trashed == nil || h.VersionBytes == nil {
		return 0, 0, 0, false
	}
	return *h.Versions, *h.Trashed, *h.VersionBytes, true
}

// encodedSize is the length of the JSON kit keeps of d.
func encodedSize(t *testing.T, d draft) int64 {
	t.Helper()
	raw, err := json.Marshal(d)
	must(t, err)
	return int64(len(raw))
}

func TestTheModelSaysWhatTheVersionsWeigh(t *testing.T) {
	for _, b := range []struct {
		name string
		opts func(t *testing.T) []kit.AppConfigurer
	}{
		{"memory", func(*testing.T) []kit.AppConfigurer { return []kit.AppConfigurer{kit.InMemory()} }},
		{"files", func(t *testing.T) []kit.AppConfigurer {
			needsFileStore(t)
			return []kit.AppConfigurer{kit.DataDir(t.TempDir())}
		}},
		{"database", func(t *testing.T) []kit.AppConfigurer {
			needsFileStore(t)
			t.Setenv("SCALES_DATABASE_URL", verifiedURL())
			return []kit.AppConfigurer{kit.DataDir(t.TempDir()), kit.Database("database", kit.NewFakeDB(sql.DialectPostgres).Engine())}
		}},
	} {
		t.Run(b.name, func(t *testing.T) {
			t.Setenv("KIT_SMTP_URL", "")
			t.Setenv("KIT_SECRETS", "memory")
			svc := kit.NewService("scales", "Drafts that keep two former versions, for the weight of the versions.")
			drafts := svc.Store("drafts", draft.key, kit.Revisions(2))
			svc.Store("notes", draft.key)
			app := kit.NewApp("scales", svc).With(append([]kit.AppConfigurer{
				kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard),
			}, b.opts(t)...)...)
			run(t, app)
			ctx := t.Context()

			before := app.Graph()
			if st := before.Node("scales/store/drafts").Store; b.name == "database" && st.Backend != "postgres" {
				t.Fatalf("the drafts run on %q, not on the database", st.Backend)
			}
			if v, tr, size, said := weighedAs(before.Node("scales/store/drafts").Store.History); !said || v != 0 || tr != 0 || size != 0 {
				t.Fatalf("an empty store's versions weigh %d, %d, %d (said %t)", v, tr, size, said)
			}
			for _, title := range []string{"one", "two", "three", "four"} {
				must(t, drafts.Put(ctx, draft{ID: "d1", Title: title}))
			}
			must(t, drafts.Put(ctx, draft{ID: "d2", Title: "alone"}))
			after := app.Graph()
			want := encodedSize(t, draft{ID: "d1", Title: "three"}) + encodedSize(t, draft{ID: "d1", Title: "two"})
			if v, tr, size, said := weighedAs(after.Node("scales/store/drafts").Store.History); !said || v != 2 || tr != 0 || size != want {
				t.Errorf("the versions weigh %d, %d, %d (said %t), want 2 former versions of %d bytes", v, tr, size, said, want)
			}
			if h := after.Node("scales/store/notes").Store.History; h != nil {
				t.Errorf("a store without revisions says it remembers: %+v", h)
			}
			if before.Revision != after.Revision {
				t.Errorf("the revision moved with the versions' weight: %s → %s", before.Revision, after.Revision)
			}
		})
	}
}
