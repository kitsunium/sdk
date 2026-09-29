package kit

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// cmdSecrets declares one secret of each kind, and a mailer for kit's own.
var cmdSecrets = func() *Service {
	s := NewService("cmd-secrets", "Secrets the command manages, for the tests.")
	s.Secret("api-token")
	s.Secret("seal-key", Generated(32), RotateEvery(time.Hour))
	s.Mailer("mail")
	return s
}()

// secretsCmd runs the secrets command of an app named "vault", in
// production, on a file store in dir.
func secretsCmd(t *testing.T, dir, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("KIT_SECRETS", "file:"+dir)
	t.Setenv("KIT_SECRETS_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	app := NewApp("vault", cmdSecrets).With(Env(EnvProduction), Logs(io.Discard))
	var out, errOut bytes.Buffer
	code = app.secretsCommand(t.Context(), args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestTheSecretsCommand(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("the SDK's file store needs file modes that are access lists")
	}
	dir := filepath.Join(t.TempDir(), "secrets")
	code, out, _ := secretsCmd(t, dir, "", "list")
	for _, want := range []string{"api-token", "nowhere", "VAULT_API_TOKEN", "seal-key", "not made yet", "kit-smtp-url", "KIT_SMTP_URL"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("list (%d) does not say %q:\n%s", code, want, out)
		}
	}

	code, out, errOut := secretsCmd(t, dir, "tok-from-stdin\n", "set", "api-token")
	if code != 0 || out != "api-token: version 1\n" {
		t.Fatalf("set: %d %q %q", code, out, errOut)
	}
	if _, out, _ = secretsCmd(t, dir, "", "list"); !strings.Contains(out, "file") || strings.Contains(out, "tok-from-stdin") {
		t.Fatalf("after set:\n%s", out)
	}

	if code, out, _ = secretsCmd(t, dir, "", "rotate", "seal-key"); code != 0 || out != "seal-key: version 1\n" {
		t.Fatalf("the first rotation: %d %q", code, out)
	}
	if code, out, _ = secretsCmd(t, dir, "", "rotate", "-all"); code != 0 || out != "seal-key: version 2\n" {
		t.Fatalf("rotate -all: %d %q", code, out)
	}

	// What the command refuses.
	for _, c := range []struct {
		stdin string
		args  []string
		want  string
	}{
		{"", []string{"rotate", "api-token"}, "is given by the operator"},
		{"x", []string{"set", "nope"}, `no secret named "nope"`},
		{"", []string{"set", "api-token"}, "standard input is empty"},
		{"", []string{"frobnicate"}, "usage: vault secrets"},
	} {
		if code, _, errOut := secretsCmd(t, dir, c.stdin, c.args...); code == 0 || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: %d %q, want %q", c.args, code, errOut, c.want)
		}
	}

	// kit's own secret is read from its own variable, KIT_SMTP_URL.
	t.Setenv("KIT_SMTP_URL", "smtp://relay.example.com:2525?tls=none")
	if _, out, _ := secretsCmd(t, dir, "", "list"); !strings.Contains(strings.Join(strings.Fields(out), " "), "kit-smtp-url operator env KIT_SMTP_URL") {
		t.Errorf("KIT_SMTP_URL is not where the list finds kit-smtp-url:\n%s", out)
	}
	if _, _, errOut := secretsCmd(t, dir, "smtp://other.example.com:25?tls=none", "set", "kit-smtp-url"); !strings.Contains(errOut, "KIT_SMTP_URL is set") {
		t.Errorf("set under KIT_SMTP_URL: %q", errOut)
	}

	// The variable wins over the store, and set says so.
	t.Setenv("VAULT_API_TOKEN", "from-the-environment")
	if code, _, errOut := secretsCmd(t, dir, "another", "set", "api-token"); code != 0 || !strings.Contains(errOut, "VAULT_API_TOKEN is set: the environment wins") {
		t.Errorf("set under a variable: %d %q", code, errOut)
	}
	if code, _, errOut := secretsCmd(t, dir, "", "rotate", "seal-key"); code != 0 || errOut != "" {
		t.Errorf("rotate: %d %q", code, errOut)
	}
}

// Where the environment keeps no store, set says what to do instead.
func TestTheSecretsCommandWithoutAStore(t *testing.T) {
	t.Setenv("KIT_SECRETS", "env")
	app := NewApp("vault", cmdSecrets).With(Env(EnvProduction), Logs(io.Discard))
	var out, errOut bytes.Buffer
	if code := app.secretsCommand(t.Context(), []string{"set", "api-token"}, strings.NewReader("x"), &out, &errOut); code != 1 ||
		!strings.Contains(errOut.String(), "KIT_SECRETS=env keeps no store: set VAULT_API_TOKEN where the product runs") {
		t.Fatalf("set: %d %q", code, errOut.String())
	}
}

// The stores an app opened are closed when it stops: a file store holds its
// directory open.
func TestTheSecretStoresCloseWithTheApp(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("the SDK's file store needs file modes that are access lists")
	}
	app := NewApp("vault", cmdSecrets).With(Env(EnvDev), DataDir(t.TempDir()), Listen("127.0.0.1:0"), Analyze(false), Logs(io.Discard))
	t.Setenv("VAULT_API_TOKEN", "tok")
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := app.secrets.Load()
	if st == nil || st.kept == nil {
		t.Fatal("no store open while the app runs")
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if app.secrets.Load() != nil {
		t.Error("the stores are still the app's after it stopped")
	}
	if _, err := st.kept.Names(context.Background()); err == nil {
		t.Error("the file store still answers after the app stopped: it was not closed")
	}
}

// A database's URL is a secret of the product's: the command lists it and
// keeps it, and the database finds it where it was kept.
func TestTheSecretsCommandKeepsADatabasesURL(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("the SDK's file store needs file modes that are access lists")
	}
	t.Setenv("KIT_SECRETS", "file:"+filepath.Join(t.TempDir(), "secrets"))
	t.Setenv("KIT_SECRETS_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	app := NewApp("vault", cmdSecrets).With(Env(EnvProduction), Logs(io.Discard), Database("database", NewFakeDB(sql.DialectPostgres).Engine()))
	code, out := secretsOf(t, app, "", "list")
	for _, want := range []string{"database-url", "VAULT_DATABASE_URL"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("list (%d) does not say %s:\n%s", code, want, out)
		}
	}
	stored := (&url.URL{Scheme: "fake", User: url.UserPassword("vault", "pw"), Host: "db:5432", Path: "/vault", RawQuery: "tls=verify-full"}).String()
	if code, out := secretsOf(t, app, stored+"\n", "set", "database-url"); code != 0 || out != "database-url: version 1\n" {
		t.Fatalf("set: %d %q", code, out)
	}
	if got, from := keptURL(t, app); got != stored || from != model.SettingStore {
		t.Fatalf("the database's URL is found in %q", from)
	}
}

// keptURL is the URL of app's first database as the start would read it,
// and where it was found.
func keptURL(t *testing.T, app *App) (string, string) {
	t.Helper()
	if err := app.resolve(); err != nil {
		t.Fatal(err)
	}
	if err := app.openSecrets(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.closeSecrets()
	v, from, err := app.databaseURL(t.Context(), app.opts.databases[0])
	if err != nil {
		t.Fatal(err)
	}
	return v.RevealString(), from
}

// secretsOf runs the secrets command of app with stdin, and returns its
// status and what it printed.
func secretsOf(t *testing.T, app *App, stdin string, args ...string) (int, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := app.secretsCommand(t.Context(), args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String() + errOut.String()
}
