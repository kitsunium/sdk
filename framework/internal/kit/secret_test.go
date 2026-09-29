package kit_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/mail"
	"github.com/kitsunium/sdk/pkg/v1/secret"
)

// Two services with a secret each: one the operator provides, one kit
// generates and rotates every day, keeping two versions.

var Tokens = kit.NewService("tokens", "A provided secret, for the tests.")

var APIToken = Tokens.Secret("api-token")

var Keys = kit.NewService("keys", "A generated secret, for the tests.")

var SealKey = Keys.Secret("seal-key", kit.Generated(32), kit.RotateEvery(24*time.Hour), kit.KeepVersions(2))

// secretApp starts an app named "vault" — its variables are VAULT_<NAME> —
// and stops it when the test ends.
func secretApp(t *testing.T, services []*kit.Service, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	app, err := startSecretApp(t, services, opts...)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return app
}

func startSecretApp(t *testing.T, services []*kit.Service, opts ...kit.AppConfigurer) (*kit.App, error) {
	t.Helper()
	app := kit.NewApp("vault", services...).With(append([]kit.AppConfigurer{
		kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard),
	}, opts...)...)
	if err := app.Start(t.Context()); err != nil {
		return app, err
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return app, nil
}

// A provided secret is read from its variable at every use: replacing it
// where it lives takes effect at the next call, without a restart. What the
// graph says of it is where it was found — never the value.
func TestAProvidedSecretIsReadFromItsVariableAtEveryUse(t *testing.T) {
	t.Setenv("VAULT_API_TOKEN", "tok-first")
	app := secretApp(t, []*kit.Service{Tokens})
	v, err := APIToken.Value(t.Context())
	if err != nil || v.RevealString() != "tok-first" {
		t.Fatalf("Value = %v, %v", v, err)
	}
	t.Setenv("VAULT_API_TOKEN", "tok-second")
	if v, err := APIToken.Value(t.Context()); err != nil || v.RevealString() != "tok-second" {
		t.Fatalf("after the change, Value = %v, %v", v, err)
	}

	info := app.Graph().Node("tokens/secret/api-token").Secret
	if info == nil || info.Origin != model.SecretProvided || info.Variable != "VAULT_API_TOKEN" || info.From != model.SecretFromEnv || info.Version != 1 {
		t.Fatalf("secret info %+v", info)
	}
	raw, rawErr := json.Marshal(app.Graph())
	if rawErr != nil {
		t.Fatal(rawErr)
	}
	if strings.Contains(string(raw), "tok-") {
		t.Fatal("the graph shows the secret's value")
	}
}

// The _FILE form reads the file the variable names — the Docker and
// Kubernetes way — again at every use.
func TestAProvidedSecretIsReadFromTheFileItsVariableNames(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("from-a-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VAULT_API_TOKEN_FILE", file)
	secretApp(t, []*kit.Service{Tokens})
	if v, err := APIToken.Value(t.Context()); err != nil || v.RevealString() != "from-a-file" {
		t.Fatalf("Value = %v, %v", v, err)
	}
	if err := os.WriteFile(file, []byte("rotated-by-the-operator\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if v, err := APIToken.Value(t.Context()); err != nil || v.RevealString() != "rotated-by-the-operator" {
		t.Fatalf("after the file changed, Value = %v, %v", v, err)
	}
}

// A provided secret set nowhere stops the start, and the error names the
// variable to set.
func TestAMissingProvidedSecretStopsTheStart(t *testing.T) {
	_, err := startSecretApp(t, []*kit.Service{Tokens})
	var de *kit.DiagnosticsError
	if !errors.As(err, &de) || !strings.Contains(err.Error(), "secret tokens/secret/api-token is not set: set VAULT_API_TOKEN, or VAULT_API_TOKEN_FILE") {
		t.Fatalf("Start = %v", err)
	}
}

// A generated secret is made on the first start, rotated on the app's clock,
// and its previous version still opens what it sealed — until a rotation
// prunes it.
func TestAGeneratedSecretIsMadeRotatedAndPruned(t *testing.T) {
	start := time.Date(2031, 1, 1, 9, 0, 0, 0, time.UTC)
	clk := clock.NewManualClock(start)
	var logs syncBuffer
	app := secretApp(t, []*kit.Service{Keys}, kit.Clock(clk), kit.Logs(&logs))
	info := func() *model.SecretInfo { return app.Graph().Node("keys/secret/seal-key").Secret }
	if i := info(); i.Origin != model.SecretGenerated || i.Version != 1 || i.From != model.SecretFromMemory || i.Keep != 2 ||
		i.NextRotation == nil || !i.NextRotation.Equal(start.Add(24*time.Hour)) {
		t.Fatalf("after the start: %+v", i)
	}

	first, firstErr := SealKey.Seal(t.Context(), []byte("sealed under version 1"), []byte("ctx"))
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	// The rotation loop computes when it looks next, says it, then arms its
	// timer — the one wait on the clock: the clock moves once it is armed,
	// or the loop would wait a day from the new now.
	nextRun := func() *time.Time {
		if rt := app.Graph().Runtime; rt != nil {
			for _, l := range rt.Loops {
				if l.Node == "keys/secret/seal-key" {
					return l.NextRun
				}
			}
		}
		return nil
	}
	rotate := func(want int) {
		t.Helper()
		due := start.Add(time.Duration(want-1) * 24 * time.Hour)
		eventually(t, "the rotation loop to wait for "+due.String(), func() bool { n := nextRun(); return n != nil && n.Equal(due) })
		armed(t, clk, 1)
		clk.Advance(24 * time.Hour)
		eventually(t, "version "+strconv.Itoa(want), func() bool { return info().Version == want })
	}
	rotate(2)
	if plain, err := SealKey.Open(t.Context(), first, []byte("ctx")); err != nil || string(plain) != "sealed under version 1" {
		t.Fatalf("the previous version no longer opens its box: %q, %v", plain, err)
	}
	if _, err := SealKey.Open(t.Context(), first, []byte("another context")); !errors.Is(err, secret.SealInvalid) {
		t.Fatalf("a box opened with another aad: %v", err)
	}
	rotate(3)
	if _, err := SealKey.Open(t.Context(), first, []byte("ctx")); !errors.Is(err, secret.SealInvalid) {
		t.Fatalf("version 1 was pruned, and its box still opens: %v", err)
	}
	if i := info(); i.Versions != 2 || i.Rotations != 2 {
		t.Fatalf("after two rotations: %+v", i)
	}

	signature, signatureErr := SealKey.Sign(t.Context(), []byte("a link"))
	if signatureErr != nil {
		t.Fatal(signatureErr)
	}
	if err := SealKey.Verify(t.Context(), []byte("a link"), signature); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := SealKey.Verify(t.Context(), []byte("another link"), signature); !errors.Is(err, secret.SignatureInvalid) {
		t.Fatalf("Verify of another message: %v", err)
	}
	if !strings.Contains(logs.String(), "a secret rotated") {
		t.Errorf("the rotations were not logged:\n%s", logs.String())
	}
}

// A loop's next run, as the model hands it out, is the loop state's own
// copy. The rotation loop computes its next instant again after every
// rotation: a model handed out before — the Studio encodes it, a test
// compares it — still says what it said, and nobody reads the loop's own
// variable while the loop writes it. The reader below is a goroutine of its
// own, started once the clock moved and taking no lock before it reads:
// nothing orders its read with the loop's write, so -race reports a shared
// instant whatever the timing, and the check after the rotation sees it
// change.
func TestALoopsNextRunIsItsOwnCopy(t *testing.T) {
	start := time.Date(2031, 1, 1, 9, 0, 0, 0, time.UTC)
	clk := clock.NewManualClock(start)
	app := secretApp(t, []*kit.Service{Keys}, kit.Clock(clk))
	nextRun := func() *time.Time {
		if l := loopNamed(app.Graph(), "keys/secret/seal-key rotation"); l != nil {
			return l.NextRun
		}
		return nil
	}
	first, second := start.Add(24*time.Hour), start.Add(48*time.Hour)
	eventually(t, "the rotation loop to wait for "+first.String(), func() bool { n := nextRun(); return n != nil && n.Equal(first) })
	shown := nextRun()
	armed(t, clk, 1)
	clk.Advance(24 * time.Hour)
	read := make(chan time.Time, 1)
	go func() { read <- *shown }()
	eventually(t, "the rotation loop to wait for "+second.String(), func() bool { n := nextRun(); return n != nil && n.Equal(second) })
	<-read
	if !shown.Equal(first) {
		t.Fatalf("the next run the model showed became %v once the loop rotated", *shown)
	}
}

// A generated secret its variable pins is used as it is: kit does not
// rotate it, and says so.
func TestAGeneratedSecretPinnedByItsVariableIsNotRotated(t *testing.T) {
	t.Setenv("VAULT_SEAL_KEY", "pinned-by-the-operator-32-bytes!")
	app := secretApp(t, []*kit.Service{Keys})
	g := app.Graph()
	if i := g.Node("keys/secret/seal-key").Secret; !i.Pinned || i.From != model.SecretFromEnv || i.NextRotation != nil {
		t.Fatalf("pinned: %+v", i)
	}
	warned := false
	for _, d := range g.Diagnostics {
		warned = warned || (d.Severity == "warning" && strings.Contains(d.Message, "is pinned by VAULT_SEAL_KEY"))
	}
	if !warned {
		t.Errorf("no warning says the secret is pinned: %+v", g.Diagnostics)
	}
	if v, err := SealKey.Value(t.Context()); err != nil || v.RevealString() != "pinned-by-the-operator-32-bytes!" {
		t.Fatalf("Value = %v, %v", v, err)
	}
}

// Where the environment keeps no store, a generated secret has nowhere to
// live: the start fails and says what to do.
func TestAGeneratedSecretNeedsAStoreKitCanWrite(t *testing.T) {
	t.Setenv("KIT_SECRETS", "env")
	app := kit.NewApp("vault", Keys).With(kit.Env(kit.EnvProduction), kit.DataDir(t.TempDir()), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard))
	err := app.Start(t.Context())
	if err == nil {
		if err := app.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err == nil || !strings.Contains(err.Error(), "KIT_SECRETS=env keeps nothing kit can write") {
		t.Fatalf("Start = %v", err)
	}
}

// A file store keeps a generated secret across starts, encrypted under
// KIT_SECRETS_KEY: what one run sealed, the next opens. Without the key,
// outside dev, the start fails and says how to make one.
func TestAFileStoreKeepsASecretAcrossStarts(t *testing.T) {
	skipWithoutFileStore(t)
	dir := t.TempDir()
	t.Setenv("KIT_SECRETS", "file:"+filepath.Join(dir, "secrets"))
	run := func(during func()) error {
		t.Helper()
		app := kit.NewApp("vault", Keys).With(kit.Env(kit.EnvProduction), kit.DataDir(filepath.Join(dir, "data")), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard))
		if err := app.Start(t.Context()); err != nil {
			return err
		}
		during()
		return app.Stop(context.Background())
	}
	if err := run(func() {}); err == nil || !strings.Contains(err.Error(), "needs KIT_SECRETS_KEY or KIT_SECRETS_KEY_FILE") {
		t.Fatalf("a file store without its key: %v", err)
	}

	t.Setenv("KIT_SECRETS_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	var box []byte
	if err := run(func() {
		var err error
		if box, err = SealKey.Seal(t.Context(), []byte("kept"), nil); err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := run(func() {
		if plain, err := SealKey.Open(t.Context(), box, nil); err != nil || string(plain) != "kept" {
			t.Fatalf("the next run cannot open the box: %q, %v", plain, err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	// Nothing readable is on the disk.
	walkErr := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if bytes.Contains(raw, []byte("seal-key")) && !strings.HasSuffix(path, "seal-key") {
				t.Errorf("%s names the secret in its content", path)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
}

// In dev, the secrets live in an encrypted store beside the data, its key
// made on first use — and the directory keeps itself out of a repository.
func TestDevKeepsItsSecretsBesideTheData(t *testing.T) {
	skipWithoutFileStore(t)
	data := t.TempDir()
	app := kit.NewApp("vault", Keys).With(kit.Env(kit.EnvDev), kit.DataDir(data), kit.Listen("127.0.0.1:0"), kit.Analyze(false), kit.Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	if i := app.Graph().Node("keys/secret/seal-key").Secret; i.From != model.SecretFromFile || i.Version != 1 {
		t.Fatalf("in dev: %+v", i)
	}
	for _, f := range []string{".secrets/key", ".secrets/.gitignore", ".secrets/store"} {
		info, err := os.Stat(filepath.Join(data, f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is readable by others: %v", f, info.Mode())
		}
	}
}

// A store the code gives holds what a test's provided secrets hold.
func TestAStoreTheCodeGivesHoldsTheSecrets(t *testing.T) {
	store := secret.NewMemory(secret.MemoryConfig{})
	if _, err := store.Put(t.Context(), "api-token", secret.FromString("from-the-store")); err != nil {
		t.Fatal(err)
	}
	app := secretApp(t, []*kit.Service{Tokens}, kit.SecretStore(store))
	if v, err := APIToken.Value(t.Context()); err != nil || v.RevealString() != "from-the-store" {
		t.Fatalf("Value = %v, %v", v, err)
	}
	if i := app.Graph().Node("tokens/secret/api-token").Secret; i.From != model.SecretFromStore {
		t.Fatalf("info %+v", i)
	}
}

// A secret used outside a running app says so.
func TestASecretOutsideARunningAppIsUnavailable(t *testing.T) {
	if _, err := APIToken.Value(t.Context()); err == nil || !strings.Contains(err.Error(), "is not running") {
		t.Fatalf("Value = %v", err)
	}
}

// Declaring a secret wrong is reported with the other declaration problems.
func TestSecretDeclarationProblems(t *testing.T) {
	bad := kit.NewService("bad-secrets", "Secrets declared wrong.")
	bad.Secret("Upper_Case")
	bad.Secret("token-file")
	bad.Secret("kit-mine")
	bad.Secret("too-short", kit.Generated(8))
	bad.Secret("provided", kit.RotateEvery(time.Hour))
	bad.Secret("one-version", kit.Generated(32), kit.KeepVersions(1))
	other := kit.NewService("other-secrets", "The same name twice.")
	other.Secret("shared")
	bad.Secret("shared")
	_, err := startSecretApp(t, []*kit.Service{bad, other})
	for _, want := range []string{
		`secret name "Upper_Case" must be 1 to 63 lower-case letters`,
		`secret name "token-file" must not end in -file`,
		`secret name "kit-mine" must not start with "kit-"`,
		`secret "too-short": Generated takes 16 to 4096 bytes`,
		`secret "provided" is provided: RotateEvery and KeepVersions apply to a Generated one`,
		`secret "one-version": KeepVersions needs at least 2`,
		`secret "shared" is declared by`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

// The SMTP URL is the mail connector's secret: KIT_SMTP_URL_FILE names a
// file that holds it, and a new URL in that file takes effect at the next
// mail — no restart.
func TestTheSMTPURLChangesWithoutARestart(t *testing.T) {
	first, second := startRelay(t), startRelay(t)
	file := filepath.Join(t.TempDir(), "smtp-url")
	if err := os.WriteFile(file, []byte("smtp://"+first.addr()+"?tls=none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIT_SMTP_URL_FILE", file)
	app := kit.NewApp("mail", Members).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	send := func(subject string) {
		t.Helper()
		if _, err := Mail.Send(t.Context(), mail.Message{To: []mail.Address{{Addr: "guest@example.org"}}, Subject: subject, Text: "Hi"}); err != nil {
			t.Fatal(err)
		}
	}
	send("to the first relay")
	eventually(t, "the first relay to receive the mail", func() bool { return len(first.messages()) == 1 })

	if err := os.WriteFile(file, []byte("smtp://"+second.addr()+"?tls=none\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	send("to the second relay")
	eventually(t, "the second relay to receive the mail", func() bool { return len(second.messages()) == 1 })
	if len(first.messages()) != 1 {
		t.Errorf("the first relay received %d mails", len(first.messages()))
	}
	if info := app.Graph().Node("members/mailer/mail").Mailer; info.Server != second.addr() {
		t.Errorf("the graph shows the relay %q, not the one in use", info.Server)
	}
	var smtp model.Setting
	for _, s := range app.Graph().Runtime.Config {
		if s.Name == "KIT_SMTP_URL" {
			smtp = s
		}
	}
	if !smtp.Secret || smtp.Value != "" || smtp.From != model.SettingEnv {
		t.Errorf("KIT_SMTP_URL setting %+v", smtp)
	}
}

// skipWithoutFileStore skips where the SDK has no protected file store: a
// mode is an access list on the Unix family only.
func skipWithoutFileStore(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("the SDK's file store needs file modes that are access lists")
	}
}

// Without KIT_SECRETS, the secrets follow the data in every environment:
// beside it on disk — the key beside them, said at every start outside dev —
// and in memory when the data is.
func TestTheSecretsFollowTheData(t *testing.T) {
	skipWithoutFileStore(t)
	warned := func(g *model.Graph, text string) bool {
		for _, d := range g.Diagnostics {
			if d.Severity == "warning" && strings.Contains(d.Message, text) {
				return true
			}
		}
		return false
	}
	data := t.TempDir()
	app := kit.NewApp("vault", Keys).With(kit.Env(kit.EnvProduction), kit.DataDir(data), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	g := app.Graph()
	if i := g.Node("keys/secret/seal-key").Secret; i.From != model.SecretFromFile || i.Version != 1 {
		t.Errorf("beside the data: %+v", i)
	}
	if !warned(g, "the secret store's key lies beside it") {
		t.Errorf("no warning about the key: %+v", g.Diagnostics)
	}
	if _, err := os.Stat(filepath.Join(data, ".secrets", "key")); err != nil {
		t.Error(err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	app = kit.NewApp("vault", Keys).With(kit.Env(kit.EnvProduction), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	g = app.Graph()
	if i := g.Node("keys/secret/seal-key").Secret; i.From != model.SecretFromMemory {
		t.Errorf("without data: %+v", i)
	}
	if !warned(g, "a generated secret lives in memory") {
		t.Errorf("no warning about memory: %+v", g.Diagnostics)
	}
}

// A key given wrong stops the start with a sentence that names what to fix —
// kit's words, not the store's, which do not know which variable kit read.
func TestAKeyGivenWrongSaysWhichVariable(t *testing.T) {
	skipWithoutFileStore(t)
	start := func() error {
		t.Helper()
		app := kit.NewApp("vault", Keys).With(kit.Env(kit.EnvProduction), kit.DataDir(t.TempDir()), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard))
		err := app.Start(t.Context())
		if err == nil {
			if err := app.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		return err
	}
	t.Setenv("KIT_SECRETS", "file:"+filepath.Join(t.TempDir(), "secrets"))
	t.Setenv("KIT_SECRETS_KEY_FILE", t.TempDir()) // a directory, not a file
	if err := start(); err == nil || !strings.Contains(err.Error(), "KIT_SECRETS_KEY_FILE names a file that cannot be read") {
		t.Errorf("a key file that is a directory: %v", err)
	}
	t.Setenv("KIT_SECRETS_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err := start(); err == nil || !strings.Contains(err.Error(), "set KIT_SECRETS_KEY or KIT_SECRETS_KEY_FILE, not both") {
		t.Errorf("both forms of the key: %v", err)
	}
}
