package kit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
)

// toolsPos is a position in another Go module of the test binary —
// github.com/kitsunium/sdk/pkg, which the framework links —: the test binary is one Go
// module, so a module here says it is that one's.
func toolsPos(t *testing.T) pos {
	t.Helper()
	fn := runtime.FuncForPC(reflect.ValueOf(clock.NewManualClock).Pointer())
	file, line := fn.FileLine(fn.Entry())
	if !filepath.IsAbs(file) {
		t.Skip("a -trimpath build: no file to read")
	}
	pkg, _ := packageOf(fn.Name())
	return pos{file: file, line: line, pkg: pkg}
}

func TestPackageOf(t *testing.T) {
	for fn, want := range map[string]string{
		"main.init": "main",
		"github.com/kitsunium/sdk/framework/kit.NewApp": "github.com/kitsunium/sdk/framework/kit",
		"example.com/shop/orders.(*Store[...]).Put":     "example.com/shop/orders",
		"example.com/shop/orders.init.func1":            "example.com/shop/orders",
		"gopkg.in/yaml%2ev3.Unmarshal":                  "gopkg.in/yaml.v3",
		"nothing":                                       "",
	} {
		if got, _ := packageOf(fn); got != want {
			t.Errorf("packageOf(%q) = %q, want %q", fn, got, want)
		}
	}
}

// A module's services take declarations from the module's Go module only:
// one made from another is refused, naming it; the module's own are not.
func TestAModuleRefusesAForeignDeclaration(t *testing.T) {
	home := toolsPos(t)
	desk := NewService("desk", "A module's service.")
	own := desk.Store("own", func(s string) string { return s })
	m := NewModule("tools", "A module of another Go module.", desk)
	m.decl.pkg, desk.decl.pkg, own.decl.pkg = home.pkg, home.pkg, home.pkg
	desk.Store("foreign", func(s string) string { return s })
	err := NewApp("x").With(m, InMemory(), Listen("127.0.0.1:0"), Logs(io.Discard)).Start(t.Context())
	var de *DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("Start = %v, want the foreign declaration refused", err)
	}
	var said []string
	for _, d := range de.Diagnostics {
		said = append(said, d.Message)
	}
	want := "tools.desk/store/foreign is declared on tools.desk, a service of module tools, from outside its Go module github.com/kitsunium/sdk/pkg"
	if len(said) != 1 || !strings.HasPrefix(said[0], want) {
		t.Errorf("the start said %q, want only %q", said, want)
	}
}

// toolsAccounts is a module of another Go module — github.com/kitsunium/sdk/pkg, as
// its declarations say — whose store keeps accounts and their passwords'
// hashes.
func toolsAccounts(t *testing.T) (*Module, *StoreService[lockAccount]) {
	t.Helper()
	home := toolsPos(t)
	desk := NewService("desk", "A module's service.")
	accounts := desk.Store("accounts", func(a lockAccount) string { return a.ID })
	m := NewModule("tools", "A module of another Go module.", desk)
	m.decl.pkg, desk.decl.pkg, accounts.decl = home.pkg, home.pkg, home
	return m, accounts
}

// A module's stores take their password policies from the module's Go
// module only (ADR 0007, ADR 0008): a product's policy on one is refused at
// the policy's line, naming the store and where it is declared.
func TestAModuleRefusesAForeignPasswordPolicy(t *testing.T) {
	m, accounts := toolsAccounts(t)
	policy := accounts.Passwords(func(a *lockAccount) *string { return &a.Password })
	err := NewApp("x").With(m, InMemory(), Listen("127.0.0.1:0"), Logs(io.Discard)).Start(t.Context())
	var de *DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("Start = %v, want the foreign policy refused", err)
	}
	mod, _ := goModuleOf(accounts.decl.pkg)
	want := fmt.Sprintf("tools.desk/store/accounts, declared at %s/v1/clock/clock.go:%d, of module tools, "+
		"is given a password policy from outside its Go module github.com/kitsunium/sdk/pkg:", mod.id, accounts.decl.line)
	if len(de.Diagnostics) != 1 || !strings.HasPrefix(de.Diagnostics[0].Message, want) {
		t.Fatalf("the start said %+v, want only %q", de.Diagnostics, want)
	}
	if src := de.Diagnostics[0].Source; src == nil || src.File != "internal/kit/module_internal_test.go" || src.Line != policy.decl.line {
		t.Errorf("the refusal is at %+v, want the policy's line, %d", src, policy.decl.line)
	}
}

// The module's own policy on its own store is the module's to declare: the
// start takes it.
func TestAModuleKeepsItsOwnPasswordPolicy(t *testing.T) {
	m, accounts := toolsAccounts(t)
	policy := accounts.Passwords(func(a *lockAccount) *string { return &a.Password })
	policy.decl.pkg = m.decl.pkg
	app := NewApp("x").With(m, InMemory(), Listen("127.0.0.1:0"), Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("a module's policy on its own store is refused: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Stop(context.Background()); err != nil {
			t.Logf("cleanup: %v", err)
		}
	})
}

// A module's code is said relative to its own Go module's root, and the
// source endpoint serves it from there — the files the graph names only.
func TestAModulesSourceIsServedFromItsGoModule(t *testing.T) {
	home := toolsPos(t)
	desk := NewService("desk", "A module's service.")
	m := NewModule("tools", "A module of another Go module.", desk)
	m.decl, desk.decl.pkg = home, home.pkg
	app := NewApp("x").With(m, InMemory(), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Stop(t.Context()); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()
	src := app.Graph().ModuleOf("tools").Source
	mod, _ := goModuleOf(home.pkg)
	if want := (model.Source{File: "v1/clock/clock.go", Line: home.line, GoModule: mod.id}); src == nil || *src != want {
		t.Fatalf("the module's source: %+v, want %+v", src, want)
	}
	servesOnlyWhatTheGraphNames(t, app, mod.id, src.File)
}

// A library's code — a service the product lists itself, declared in
// another Go module of the build — is said relative to that Go module's
// root too, and served from there, as a module's is.
func TestALibrarysSourceIsServedFromItsGoModule(t *testing.T) {
	home := toolsPos(t)
	lib := NewService("lib", "A library's service.")
	lib.decl = home
	app := NewApp("x", lib).With(InMemory(), Listen("127.0.0.1:0"), Env(EnvDev), Analyze(false), Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := app.Stop(t.Context()); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()
	src := app.Graph().Node("lib").Source
	mod, _ := goModuleOf(home.pkg)
	if want := (model.Source{File: "v1/clock/clock.go", Line: home.line, GoModule: mod.id}); src == nil || *src != want {
		t.Fatalf("the library's source: %+v, want %+v", src, want)
	}
	servesOnlyWhatTheGraphNames(t, app, mod.id, src.File)
}

// servesOnlyWhatTheGraphNames asks the source endpoint for file, which the
// graph names in Go module mod: it is served from mod's root, and neither
// under the product's root nor another file of mod is.
func servesOnlyWhatTheGraphNames(t *testing.T, app *App, mod, file string) {
	t.Helper()
	get := func(module, file string) (int, model.Snippet) {
		q := url.Values{"module": {module}, "file": {file}, "line": {"1"}}
		resp, respErr := http.Get(app.URL() + "/_kit/api/source?" + q.Encode())
		if respErr != nil {
			t.Fatal(respErr)
		}
		defer resp.Body.Close()
		var s model.Snippet
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, s
	}
	if status, s := get(mod, file); status != http.StatusOK || !slices.ContainsFunc(s.Lines, func(l string) bool { return strings.HasPrefix(l, "// Package clock") }) {
		t.Errorf("the file the graph names: %d %+v", status, s)
	}
	for _, c := range []struct{ module, file string }{{"", file}, {mod, "v1/errs/construct.go"}} {
		if status, _ := get(c.module, c.file); status != http.StatusNotFound {
			t.Errorf("%s in %q, which the graph does not name: %d", c.file, c.module, status)
		}
	}
}
