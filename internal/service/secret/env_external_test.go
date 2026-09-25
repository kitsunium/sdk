package secret_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsecret "github.com/kitsunium/sdk/internal/service/secret"
)

// envPrefix namespaces every variable these tests set, so the process
// environment's own variables never take part.
const envPrefix string = "KSECRETTEST"

// newEnvStore builds the environment store under envPrefix.
func newEnvStore(t *testing.T) coresecret.Store {
	t.Helper()
	store, err := svcsecret.NewEnv(svcsecret.EnvConfig{Prefix: envPrefix})
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	return store
}

// writeSecretFile writes content to a fresh file and returns its path.
func writeSecretFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestEnvStoreReadsTheVariable pins the name-to-variable mapping and the
// shape of what the environment can say: one version, no creation instant.
// It mutates the process environment, so it cannot run in parallel.
func TestEnvStoreReadsTheVariable(t *testing.T) {
	t.Setenv(envPrefix+"_SMTP_URL", "smtp://user:pw@relay:587")
	store := newEnvStore(t)
	current, err := store.Get(t.Context(), "smtp-url")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if current.Value.RevealString() != "smtp://user:pw@relay:587" || current.Version != 1 || !current.Created.IsZero() {
		t.Fatalf("Get = %+v %q, want version 1, zero Created", current, current.Value.RevealString())
	}
	versions, err := store.Versions(t.Context(), "smtp-url")
	if err != nil || len(versions) != 1 {
		t.Fatalf("Versions = (%d, %v), want one", len(versions), err)
	}
	//: a trailing underscore on the prefix names the same namespace.
	underscored, err := svcsecret.NewEnv(svcsecret.EnvConfig{Prefix: envPrefix + "_"})
	if err != nil {
		t.Fatalf("NewEnv(prefix_): %v", err)
	}
	if again, getErr := underscored.Get(t.Context(), "smtp-url"); getErr != nil || !again.Value.Equal(current.Value) {
		t.Fatalf("a trailing underscore changed the namespace: %v", getErr)
	}
}

// TestEnvStoreReadsTheFileConvention pins the _FILE form: the file's content,
// one line ending removed, stamped with the file's modification time.
func TestEnvStoreReadsTheFileConvention(t *testing.T) {
	type tc struct {
		name    string
		content string
		want    string
	}
	tests := []tc{
		{"no line ending", "s3cr3t", "s3cr3t"},
		{"one LF", "s3cr3t\n", "s3cr3t"},
		{"one CRLF", "s3cr3t\r\n", "s3cr3t"},
		{"only one ending is the file's", "s3cr3t\n\n", "s3cr3t\n"},
		{"inner newlines are the secret's", "line1\nline2\n", "line1\nline2"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		path := writeSecretFile(t, c.content)
		stamp := time.Date(2026, time.March, 3, 3, 3, 3, 0, time.UTC)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		t.Setenv(envPrefix+"_DB_PASSWORD_FILE", path)
		current, err := newEnvStore(t).Get(t.Context(), "db-password")
		if err != nil {
			t.Fatalf("%s: Get: %v", c.name, err)
		}
		if current.Value.RevealString() != c.want || !current.Created.Equal(stamp) {
			t.Errorf("%s: Get = %q at %v, want %q at %v", c.name, current.Value.RevealString(), current.Created, c.want, stamp)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestEnvStoreRefusals pins every refusal and that none of them repeats a
// value or a path.
func TestEnvStoreRefusals(t *testing.T) {
	type tc struct {
		name  string
		setup func(t *testing.T)
		code  errs.Code
	}
	tests := []tc{
		{"neither form is set", func(*testing.T) {}, coresecret.CodeNotFound},
		{"an empty variable counts as unset", func(t *testing.T) {
			t.Setenv(envPrefix+"_TOKEN", "")
		}, coresecret.CodeNotFound},
		{"both forms are set", func(t *testing.T) {
			t.Setenv(envPrefix+"_TOKEN", "value-in-env")
			t.Setenv(envPrefix+"_TOKEN_FILE", writeSecretFile(t, "value-in-file"))
		}, svcsecret.CodeEnvRefused},
		{"the file is empty", func(t *testing.T) {
			t.Setenv(envPrefix+"_TOKEN_FILE", writeSecretFile(t, "\n"))
		}, svcsecret.CodeEnvRefused},
		{"the file is too large to be a secret", func(t *testing.T) {
			t.Setenv(envPrefix+"_TOKEN_FILE", writeSecretFile(t, strings.Repeat("x", 64<<10+1)))
		}, svcsecret.CodeEnvRefused},
		{"the file does not exist", func(t *testing.T) {
			t.Setenv(envPrefix+"_TOKEN_FILE", filepath.Join(t.TempDir(), "absent-path-marker"))
		}, coresecret.CodeStoreUnavailable},
		{"the file is a directory", func(t *testing.T) {
			t.Setenv(envPrefix+"_TOKEN_FILE", t.TempDir())
		}, coresecret.CodeStoreUnavailable},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		c.setup(t)
		_, err := newEnvStore(t).Get(t.Context(), "token")
		if !errs.HasCode(err, c.code) {
			t.Fatalf("%s: Get = %v, want %v", c.name, err, c.code)
		}
		rendered := err.Error() + errs.PrivateOf(err) + fieldText(err)
		for _, leak := range []string{"value-in-env", "value-in-file", "absent-path-marker", os.TempDir()} {
			if strings.Contains(rendered, leak) {
				t.Errorf("%s: the refusal repeats %q: %s", c.name, leak, rendered)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestEnvStoreIsReadOnly pins that the environment is never written.
func TestEnvStoreIsReadOnly(t *testing.T) {
	t.Setenv(envPrefix+"_KEPT", "kept")
	store := newEnvStore(t)
	if _, err := store.Put(t.Context(), "kept", coresecret.FromString("new")); !errs.HasCode(err, coresecret.CodeReadOnly) {
		t.Errorf("Put = %v, want ReadOnly", err)
	}
	if err := store.Prune(t.Context(), "kept", 1); !errs.HasCode(err, coresecret.CodeReadOnly) {
		t.Errorf("Prune = %v, want ReadOnly", err)
	}
	if os.Getenv(envPrefix+"_KEPT") != "kept" {
		t.Error("a refused write changed the environment")
	}
}

// TestEnvStoreNamesAndItsOwnGrammar pins Names — both forms, one name each,
// nothing it could not Get — and the one clause this store adds to the name
// grammar.
func TestEnvStoreNamesAndItsOwnGrammar(t *testing.T) {
	t.Setenv(envPrefix+"_ALPHA", "a")
	t.Setenv(envPrefix+"_BETA_KEY_FILE", writeSecretFile(t, "b"))
	t.Setenv(envPrefix+"_GAMMA", "c")
	t.Setenv(envPrefix+"_GAMMA_FILE", "")
	t.Setenv(envPrefix+"_lower", "not a name this store can serve")
	t.Setenv(envPrefix+"_TRAILING_", "maps to a name ending in '-'")
	t.Setenv(envPrefix, "the bare prefix")
	store := newEnvStore(t)
	names, err := store.Names(t.Context())
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if want := []string{"alpha", "beta-key", "gamma"}; !slices.Equal(names, want) {
		t.Fatalf("Names = %v, want %v", names, want)
	}
	if _, err := store.Get(t.Context(), "beta-key"); err != nil {
		t.Errorf("Get(beta-key) through its _FILE form: %v", err)
	}
	//: a name whose variable ends in _FILE cannot be told from the file form.
	if _, err := store.Get(t.Context(), "tls-file"); !errs.HasCode(err, coresecret.CodeInvalidName) {
		t.Errorf("Get(tls-file) = %v, want InvalidName", err)
	}
}

// TestEnvStoreRefusesAPrefixNoShellExports pins the construction refusals, and
// the unprefixed mode.
func TestEnvStoreRefusesAPrefixNoShellExports(t *testing.T) {
	for _, prefix := range []string{"app", "_", "__", "1APP", "APP-X", "APP X"} {
		if _, err := svcsecret.NewEnv(svcsecret.EnvConfig{Prefix: prefix}); !errs.HasCode(err, svcsecret.CodeInvalidConfig) {
			t.Errorf("NewEnv(%q) = %v, want InvalidConfig", prefix, err)
		}
	}
	t.Setenv("KSECRETTEST_UNPREFIXED", "bare")
	bare, err := svcsecret.NewEnv(svcsecret.EnvConfig{})
	if err != nil {
		t.Fatalf("NewEnv(no prefix): %v", err)
	}
	current, err := bare.Get(t.Context(), "ksecrettest-unprefixed")
	if err != nil || current.Value.RevealString() != "bare" {
		t.Fatalf("unprefixed Get = (%q, %v)", current.Value.RevealString(), err)
	}
}
