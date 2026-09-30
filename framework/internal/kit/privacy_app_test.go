package kit_test

import (
	"context"
	"io"
	"maps"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// kit's own service in an app (ADR 0006, ADR 0004): where its stores are
// kept, and a copy of an app mounting its own.

// privacyMember is a person, kept by a product that places its stores.
type privacyMember struct {
	ID    string `json:"id"`
	Email string `json:"email" kit:"subject"`
}

func (m privacyMember) key() string { return m.ID }

// kit's own stores are placed by name only: the default database never
// takes them, and kit.Keeps(kit.Privacy) places them like any service. The
// secrets live in memory: data-key is pinned, so that the sealed members
// open at the next start.
func TestKitsOwnStoresArePlacedByName(t *testing.T) {
	needsFileStore(t)
	t.Setenv("KIT_SECRETS", "memory")
	t.Setenv("KIT_DATA_KEY", "a data key of exactly 32 bytes..")
	t.Setenv("LEDGER_DATABASE_URL", verifiedURL())
	t.Setenv("LEDGER_VAULT_URL", verifiedURL())
	for keeps, want := range map[bool]string{false: "", true: "vault"} {
		people := kit.NewService("people", "People.")
		people.Store("members", privacyMember.key)
		opts := []kit.AppConfigurer{
			kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvProduction), kit.DataDir(t.TempDir()), kit.Logs(io.Discard),
			kit.Database("database", kit.NewFakeDB(sql.DialectPostgres).Engine()),
		}
		if keeps {
			opts = append(opts, kit.Database("vault", kit.NewFakeDB(sql.DialectPostgres).Engine(), kit.Keeps(kit.Privacy)))
		}
		app := kit.NewApp("ledger", people).With(opts...)
		run(t, app)
		g := app.Graph()
		got := map[string]string{}
		for _, id := range []string{"people/store/members", "kit.privacy/store/keys", "kit.privacy/store/holds", "kit.privacy/store/journal"} {
			got[id] = g.Node(id).Store.Database
		}
		exp := map[string]string{"people/store/members": "database", "kit.privacy/store/keys": want, "kit.privacy/store/holds": want, "kit.privacy/store/journal": want}
		if !maps.Equal(got, exp) {
			t.Errorf("Keeps(kit.Privacy) %v: %v, want %v", keeps, got, exp)
		}
	}
}

// A copy of an app (With) that kept personal data mounts kit's own service
// anew, once: the original's copy stays the original's.
func TestACopyOfAnAppMountsItsOwnPrivacy(t *testing.T) {
	app := startPrivacy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	must(t, app.Stop(ctx))
	cp := app.With(kit.Logs(io.Discard))
	must(t, cp.Start(t.Context()))
	mounted := 0
	for _, n := range cp.Graph().Nodes {
		if n.ID == "kit.privacy" {
			mounted++
		}
	}
	must(t, cp.Stop(ctx))
	if mounted != 1 {
		t.Errorf("the copy mounts kit.privacy %d times", mounted)
	}
	// The original runs again, for its cleanup to stop it.
	must(t, app.Start(t.Context()))
}
