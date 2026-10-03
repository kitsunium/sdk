package kit

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/security/authz"
)

// toolsEntry is what the module's operations take.
type toolsEntry struct {
	ID string `json:"id" path:"id"`
}

// toolsDesk is a module of another Go module — github.com/kitsunium/sdk/pkg, as its
// declarations say — with a command and a query of its own.
func toolsDesk(t *testing.T) (*Module, *Command[toolsEntry, toolsEntry], *Query[toolsEntry, toolsEntry]) {
	t.Helper()
	home := toolsPos(t)
	desk := NewService("desk", "A module's service.")
	echo := func(_ context.Context, in toolsEntry) (toolsEntry, error) { return in, nil }
	cmd := desk.Command("place", echo)
	qry := desk.Query("read", echo)
	m := NewModule("tools", "A module of another Go module.", desk)
	m.decl, desk.decl, cmd.decl, qry.decl = m.decl.withPkg(home.pkg()), desk.decl.withPkg(home.pkg()), cmd.decl.withPkg(home.pkg()), qry.decl.withPkg(home.pkg())
	return m, cmd, qry
}

// A module's commands and queries are built from the module's Go module
// only (ADR 0008): a product that exposes one, or gives it a permission, a
// rule or a key, is refused, each at the line that does it — it dispatches
// the command from an endpoint of its own.
func TestAModuleRefusesForeignBuilders(t *testing.T) {
	m, cmd, qry := toolsDesk(t)
	policy := authz.Must(authz.NewRBAC(authz.RBACConfig{RolesAttr: "roles", Grants: []authz.Grant{
		{Role: "clerk", Permissions: []authz.Permission{{Action: "place", Resource: "entry"}}},
	}}))
	cmd.Allow(policy, "place", "entry").Key(func(in toolsEntry) string { return in.ID })
	qry.Authorize(func(context.Context, toolsEntry) error { return nil }).Expose("GET /entries/{id}")
	said := refusedStart(t, NewApp("x").With(m, InMemory(), Listen("127.0.0.1:0"), Logs(io.Discard)))
	for _, want := range []string{
		"tools.desk/command/place, of module tools, is given Allow from outside its Go module github.com/kitsunium/sdk/pkg",
		"tools.desk/command/place, of module tools, is given Key from outside its Go module github.com/kitsunium/sdk/pkg",
		"tools.desk/query/read, of module tools, is given Authorize from outside its Go module github.com/kitsunium/sdk/pkg",
		"tools.desk/endpoint/read is declared on tools.desk, a service of module tools, from outside its Go module github.com/kitsunium/sdk/pkg",
	} {
		if !slices.ContainsFunc(said, func(s string) bool { return strings.HasPrefix(s, want) }) {
			t.Errorf("no problem says %q:\n%s", want, strings.Join(said, "\n"))
		}
	}
	foreign := slices.DeleteFunc(slices.Clone(said), func(s string) bool { return !strings.Contains(s, "from outside its Go module") })
	if len(foreign) != 4 {
		t.Errorf("the start refused %d foreign declarations, want 4:\n%s", len(foreign), strings.Join(said, "\n"))
	}
}

// refusedStart starts app and returns what its start refused.
func refusedStart(t *testing.T, app *App) []string {
	t.Helper()
	var de *DiagnosticsError
	if err := app.Start(t.Context()); !errors.As(err, &de) {
		t.Fatalf("Start = %v, want the foreign declarations refused", err)
	}
	var said []string
	for _, d := range de.Diagnostics {
		said = append(said, d.Message)
	}
	return said
}

// A module builds its own operations: its permissions, rules and keys are
// not judged foreign.
func TestAModuleBuildsItsOwnOperations(t *testing.T) {
	m, cmd, _ := toolsDesk(t)
	cmd.Key(func(in toolsEntry) string { return in.ID })
	for i := range cmd.calls {
		cmd.calls[i].at = cmd.calls[i].at.withPkg(m.decl.pkg())
	}
	app := NewApp("x").With(m, InMemory(), Listen("127.0.0.1:0"), Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("a module keying its own command is refused: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
}
