// Package kit — where the secrets are kept, per environment.
package kit

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/crypto"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
	"github.com/kitsunium/sdk/pkg/v1/proc"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// Where an environment keeps its secrets. The environment itself is read
// first — the product's variables (TODO_STRIPE_KEY, or TODO_STRIPE_KEY_FILE
// naming a file) and kit's (KIT_SMTP_URL) — and then the store it keeps,
// where kit writes the secrets it generates and `secrets set` the ones an
// operator gives it. KIT_SECRETS chooses that store:
//
//	(unset)     the secrets follow the data: an encrypted file store beside
//	            it, <data>/.secrets/store, and in memory when the data is
//	            in memory. Its key is KIT_SECRETS_KEY, or the key file made
//	            on first use beside the store, <data>/.secrets/key
//	env         no store: a generated secret must be pinned by its variable
//	memory      the process's memory: a generated secret is made again at
//	            every start
//	file:<dir>  an encrypted file store; its key is KIT_SECRETS_KEY — 32
//	            bytes in base64 — or the file KIT_SECRETS_KEY_FILE names,
//	            and in dev the key beside the data

// The kinds of store KIT_SECRETS chooses.
const (
	secretsEnv    = "env"
	secretsMemory = "memory"
	secretsFile   = "file"
	// secretsStore is the store the product's code gave: [SecretStore].
	secretsStore = "store"
)

// besideData is the directory, in the data directory, of the store the
// secrets keep by default — store/ — and of its key — key: beside the data
// they go with, and in no working tree a test or a product runs from.
const besideData = ".secrets"

// secretsConfig is KIT_SECRETS, resolved.
type secretsConfig struct {
	// kind is one of the secrets constants.
	kind string
	// dir is a file store's directory, as given; "" for the one beside the
	// data.
	dir string
	// from is where the choice came from: a model.Setting constant.
	from string
	// problem says what is wrong with KIT_SECRETS, or "".
	problem string
	// store is the store the product's code gave.
	store secret.Store
}

// SecretStore keeps the secrets in store instead of the environment's: a
// test gives a store holding what its provided secrets hold. The
// environment's variables are still read first.
func SecretStore(store secret.Store) AppConfigurer {
	return appOption(func(o *appOptions) { o.secrets = store })
}

// resolveSecrets reads KIT_SECRETS. An app kept in memory — a test — keeps
// its secrets in memory too, whatever the shell exports.
func resolveSecrets(o *appOptions, getenv func(string) string) secretsConfig {
	raw := strings.TrimSpace(getenv("KIT_SECRETS"))
	dir, isFile := strings.CutPrefix(raw, "file:")
	switch {
	case o.secrets != nil:
		return secretsConfig{kind: secretsStore, store: o.secrets, from: model.SettingOption}
	case o.memory:
		return secretsConfig{kind: secretsMemory, from: model.SettingOption}
	case raw == "":
		return secretsConfig{kind: secretsFile, from: model.SettingDefault}
	case raw == secretsEnv || raw == secretsMemory:
		return secretsConfig{kind: raw, from: model.SettingEnv}
	case isFile && strings.TrimSpace(dir) != "":
		return secretsConfig{kind: secretsFile, dir: strings.TrimSpace(dir), from: model.SettingEnv}
	}
	return secretsConfig{kind: secretsEnv, from: model.SettingEnv, problem: "KIT_SECRETS must be env, memory or file:<dir>"}
}

// describe is KIT_SECRETS as the Studio shows it.
func (c *secretsConfig) describe(dataDir string) string {
	switch {
	case c.kind == secretsFile && c.dir == "" && dataDir == "":
		return secretsMemory
	case c.kind == secretsFile && c.dir == "":
		return "file:" + filepath.Join(dataDir, besideData, "store")
	case c.kind == secretsFile:
		return "file:" + c.dir
	}
	return c.kind
}

// secretStores is where a running app finds its secrets.
type secretStores struct {
	// env reads the product's variables, <APP>_<NAME>; nil when the app's
	// name makes no prefix. kitEnv reads kit's, KIT_<NAME>.
	env, kitEnv secret.Store
	// kept is the store the environment keeps, nil when it keeps none; from
	// is one of the model.SecretFrom constants for it.
	kept secret.Store
	from string
	// warnings are what the choice of store costs, said at every start.
	warnings []phrase
}

// close releases the kept store: a file store holds its directory open.
func (st *secretStores) close() error {
	if c, ok := st.kept.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// closeSecrets closes the stores the app opened, once it no longer runs.
func (a *App) closeSecrets() {
	if st := a.secrets.Swap(nil); st != nil {
		if err := st.close(); err != nil {
			logger.Warn(context.Background(), a.log, "the secret store did not close cleanly", logger.String("error", errs.PublicOf(err)))
		}
	}
}

// errNoFileStore is the platform refusing the SDK's file store: its file
// modes are not access lists.
var errNoFileStore = explain(CodeSecretStore, "SECRETS_PLATFORM", "this platform has no protected file store: its file modes are not access lists", nil)

// envStores reads the environment only: what describing an app that does
// not run, or runs without a secret to find, may read.
func envStores(app string) *secretStores {
	st := &secretStores{}
	if env, err := secret.NewEnv(secret.EnvConfig{Prefix: appPrefix(app)}); err == nil {
		st.env = env
	}
	// A constant prefix the environment store accepts.
	if env, err := secret.NewEnv(secret.EnvConfig{Prefix: "KIT"}); err == nil {
		st.kitEnv = env
	}
	return st
}

// secretsNow returns the stores the app opened, or the environment alone.
func (a *App) secretsNow() *secretStores {
	if st := a.secrets.Load(); st != nil {
		return st
	}
	return envStores(a.name)
}

// needsSecrets reports whether the app has a secret to find: a declared
// secret, a mailer's SMTP URL, a database's URL, or kit's index key when it
// keeps personal data (privacy_keys.go).
func (a *App) needsSecrets() bool {
	if (len(a.opts.databases) > 0 && !a.opts.memory) || a.keepsPersonalData() {
		return true
	}
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if k := n.base().kind; k == model.KindSecret || k == model.KindMailer {
				return true
			}
		}
	}
	return false
}

// openSecrets opens the environment's stores, when the app has a secret to
// find in them.
func (a *App) openSecrets(ctx context.Context) error {
	a.closeSecrets()
	if !a.needsSecrets() {
		return nil
	}
	cfg := a.cfg.secrets
	if cfg.problem != "" {
		return failure(CodeSecretStore, "SECRETS_INVALID", cfg.problem, nil)
	}
	st := envStores(a.name)
	if st.env == nil && a.declaresSecrets() {
		return failure(CodeSecretStore, "SECRETS_PREFIX", "the app's name makes no environment variable prefix: name it with letters first", nil, errs.String("app", a.name))
	}
	switch cfg.kind {
	case secretsStore:
		st.kept, st.from = cfg.store, model.SecretFromStore
	case secretsMemory:
		st.kept, st.from = secret.NewMemory(secret.MemoryConfig{Clock: a.clock}), model.SecretFromMemory
	case secretsFile:
		if err := a.openFileStore(ctx, &cfg, st); err != nil {
			return err
		}
	default:
		// The environment alone, as envStores opened it.
	}
	a.secrets.Store(st)
	return nil
}

// openFileStore keeps st's secrets in the encrypted file store cfg names —
// or in memory, with the data, or where no file can be protected.
func (a *App) openFileStore(ctx context.Context, cfg *secretsConfig, st *secretStores) error {
	inMemory := func() {
		st.kept, st.from = secret.NewMemory(secret.MemoryConfig{Clock: a.clock}), model.SecretFromMemory
	}
	if cfg.dir == "" && a.dataDir == "" {
		// The data is in memory: so are the secrets that go with it.
		inMemory()
		if a.cfg.env != EnvDev && a.generatesSecrets() {
			st.warnings = append(st.warnings, say("secrets.memory"))
		}
		return nil
	}
	kept, besideKey, err := a.openFileSecrets(ctx, cfg, st.kitEnv)
	if cfg.dir == "" && errors.Is(err, errNoFileStore) {
		// A platform whose file modes are not access lists: nothing is
		// written that could not be protected.
		inMemory()
		st.warnings = append(st.warnings, say("secrets.no-protected-store"))
		return nil
	}
	if err != nil {
		return err
	}
	st.kept, st.from = kept, model.SecretFromFile
	if besideKey && a.cfg.env != EnvDev {
		st.warnings = append(st.warnings, say("secrets.key-beside"))
	}
	return nil
}

// openFileSecrets opens the encrypted file store of cfg, and reports whether
// its key is the key file beside it.
func (a *App) openFileSecrets(ctx context.Context, cfg *secretsConfig, kitEnv secret.Store) (secret.Store, bool, error) {
	dir := cfg.dir
	if dir == "" {
		dir = filepath.Join(a.dataDir, besideData, "store")
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, false, failure(CodeSecretStore, "SECRETS_DIR", "the secret store's directory cannot be resolved", err, errs.String("dir", cfg.dir))
	}
	key, besideKey, err := a.secretsKey(ctx, cfg, kitEnv)
	if err != nil {
		return nil, false, err
	}
	store, err := secret.NewFile(secret.FileConfig{Dir: dir, Key: key, Clock: a.clock})
	switch {
	case errors.Is(err, proc.UnsupportedPlatform):
		return nil, false, errNoFileStore
	case errors.Is(err, secret.InvalidConfig):
		return nil, false, explain(CodeSecretStore, "SECRETS_OPEN", "the secret store's directory is open to other accounts: make it 0700", err, errs.String("dir", dir))
	case err != nil:
		return nil, false, explain(CodeSecretStore, "SECRETS_OPEN", "the secret store's directory cannot be opened", err, errs.String("dir", dir))
	}
	return store, besideKey, nil
}

// secretsKey is the file store's key: KIT_SECRETS_KEY, or the file
// KIT_SECRETS_KEY_FILE names — 32 bytes in base64 — and otherwise the key
// file beside the store that follows the data, <data>/.secrets/key, made on
// first use; dev's for a store KIT_SECRETS names. besideKey reports the key
// file.
func (a *App) secretsKey(ctx context.Context, cfg *secretsConfig, kitEnv secret.Store) (key crypto.Key, besideKey bool, err error) {
	v, err := kitEnv.Get(ctx, "secrets-key")
	switch {
	case err == nil:
		key, err := keyFromText(v.Value.Reveal())
		return key, false, err
	case errors.Is(err, secret.EnvRefused):
		return crypto.Key{}, false, explain(CodeSecretStore, "SECRETS_KEY", "set KIT_SECRETS_KEY or KIT_SECRETS_KEY_FILE, not both", err)
	case !errors.Is(err, secret.NotFound):
		return crypto.Key{}, false, explain(CodeSecretStore, "SECRETS_KEY", "KIT_SECRETS_KEY_FILE names a file that cannot be read", err)
	case a.dataDir != "" && (cfg.dir == "" || a.cfg.env == EnvDev):
		key, err := keyBeside(filepath.Join(a.dataDir, besideData))
		return key, err == nil, err
	default:
		return crypto.Key{}, false, failure(CodeSecretStore, "SECRETS_KEY",
			"KIT_SECRETS=file:<dir> needs KIT_SECRETS_KEY or KIT_SECRETS_KEY_FILE: 32 bytes in base64, openssl rand -base64 32", nil)
	}
}

// keyFromText reads a key written in base64, and clears what it read.
func keyFromText(text []byte) (crypto.Key, error) {
	defer clear(text)
	raw := make([]byte, base64.StdEncoding.DecodedLen(len(text)))
	defer clear(raw)
	n, decodeErr := base64.StdEncoding.Decode(raw, trimSpace(text))
	key, keyErr := crypto.NewKey(raw[:n])
	if decodeErr != nil || keyErr != nil {
		return crypto.Key{}, failure(CodeSecretStore, "SECRETS_KEY", "KIT_SECRETS_KEY must be 32 bytes in base64, as openssl rand -base64 32 writes them", nil)
	}
	return key, nil
}

// keyBeside is the key file in root, beside the data, made on first use.
func keyBeside(root string) (crypto.Key, error) {
	if err := guardSecretsDir(root); err != nil {
		return crypto.Key{}, explain(CodeSecretStore, "SECRETS_DIR", "the secret directory beside the data cannot be made", err, errs.String("dir", root))
	}
	file := filepath.Join(root, "key")
	key, err := secret.KeyFile(file)
	switch {
	case err == nil:
		return key, nil
	case errors.Is(err, proc.UnsupportedPlatform):
		return crypto.Key{}, errNoFileStore
	case errors.Is(err, secret.KeyFileInvalid):
		return crypto.Key{}, explain(CodeSecretStore, "SECRETS_KEY", "the secret store's key file does not hold one key of 32 bytes", err, errs.String("file", file))
	case errors.Is(err, secret.InvalidConfig):
		return crypto.Key{}, explain(CodeSecretStore, "SECRETS_KEY", "the secret store's key file is open to other accounts: make it 0600", err, errs.String("file", file))
	default:
		return crypto.Key{}, explain(CodeSecretStore, "SECRETS_KEY", "the secret store's key file cannot be read or made", err, errs.String("file", file))
	}
}

// trimSpace is bytes.TrimSpace without a copy: a key's text is cleared by
// its owner.
func trimSpace(b []byte) []byte { return bytes.Trim(b, " \t\n\r") }

// guardSecretsDir makes the secret directory beside the data, 0700, with a
// .gitignore that keeps its content out of a repository whatever the
// product's own .gitignore says.
func guardSecretsDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, fs.ErrNotExist) {
		return os.WriteFile(ignore, []byte("# kit's secrets and their key: never committed.\n*\n"), 0o600)
	}
	return nil
}

// resolve finds where a declared secret is, for its run: its variable first,
// then the store the environment keeps — where a generated secret kit
// rotates lives even before its first version is made, and an optional one
// set nowhere is looked for at every use.
func (st *secretStores) resolve(ctx context.Context, a *App, s *Secret) (*secretRun, error) {
	variable := s.variable(a)
	store, from, err := st.find(ctx, st.envOf(s), s.envName(), s.keptName())
	if s.opts.optional && (err == nil || errors.Is(err, secret.NotFound)) {
		// Given or not, it is looked for at every use where the start looks:
		// it may come — `secrets set` while the product runs — and go.
		return &secretRun{app: a, store: firstFound{env: st.envOf(s), kept: st.kept, envName: s.envName(), keptName: s.keptName()}, name: s.keptName()}, nil
	}
	switch {
	case err == nil:
		return s.foundRun(a, store, from), nil
	case !errors.Is(err, secret.NotFound):
		return nil, failure(CodeSecretRead, "SECRET_READ", "a secret could not be read", err, errs.String("secret", s.id), errs.String("variable", variable))
	case s.opts.generated && st.kept != nil:
		return &secretRun{app: a, store: st.kept, name: s.keptName(), from: st.from}, nil
	case s.opts.generated:
		return nil, failure(CodeSecretStore, "SECRET_NOT_KEPT", "a generated secret needs a store kit can write: KIT_SECRETS=file:<dir> or memory, or its variable to pin it", nil,
			errs.String("secret", s.id), errs.String("variable", variable))
	}
	return nil, failure(CodeSecretMissing, "SECRET_MISSING", "a provided secret is not set", nil, errs.String("secret", s.id), errs.String("variable", variable))
}

// foundRun is the run of a secret found in store, from where from says: the
// variable's name when the environment holds it, else the kept one; pinned
// when a generated secret is given by its variable.
func (s *Secret) foundRun(a *App, store secret.Store, from string) *secretRun {
	name := s.keptName()
	if from == model.SecretFromEnv {
		name = s.envName()
	}
	return &secretRun{app: a, store: store, name: name, from: from, pinned: s.opts.generated && from == model.SecretFromEnv}
}

// firstFound is an optional secret's store while the app runs: every read
// looks again where the start looks — its variable, then the store the
// environment keeps — so a secret given after the start is found at its
// next use, the variable first as ever. It only reads: `secrets set` writes
// the kept store itself.
type firstFound struct {
	env, kept         secret.Store
	envName, keptName string
}

// Get returns the current version: the variable's, else the kept store's.
func (f firstFound) Get(ctx context.Context, _ string) (secret.Versioned, error) {
	if f.env != nil {
		if v, err := f.env.Get(ctx, f.envName); !errors.Is(err, secret.NotFound) {
			return v, err
		}
	}
	if f.kept == nil {
		return secret.Versioned{}, secret.NotFound
	}
	return f.kept.Get(ctx, f.keptName)
}

// Versions returns the versions of whichever holds it, the variable first.
func (f firstFound) Versions(ctx context.Context, _ string) ([]secret.Versioned, error) {
	if f.env != nil {
		if vs, err := f.env.Versions(ctx, f.envName); !errors.Is(err, secret.NotFound) {
			return vs, err
		}
	}
	if f.kept == nil {
		return nil, secret.NotFound
	}
	return f.kept.Versions(ctx, f.keptName)
}

// Put is refused: the product's code never writes a provided secret.
//
//ktn:allow-unused-param: the secret.Store interface passes what a read-only store refuses unread
func (firstFound) Put(_ context.Context, _ string, _ secret.Value) (secret.Versioned, error) {
	return secret.Versioned{}, secret.ReadOnly
}

// Prune is refused: the product's code never prunes a provided secret.
//
//ktn:allow-unused-param: the secret.Store interface passes what a read-only store refuses unread
func (firstFound) Prune(_ context.Context, _ string, _ int) error { return secret.ReadOnly }

// Names is empty: an optional secret's store lists nothing.
//
//ktn:allow-unused-param: the secret.Store interface passes a context an empty listing does not take
func (firstFound) Names(_ context.Context) ([]string, error) { return nil, nil }

// find returns the store holding a secret: env under name, then the kept
// store under keptName. It reports secret.NotFound when neither holds it,
// and the first store's refusal otherwise — both forms of a variable set, a
// file that cannot be read.
func (st *secretStores) find(ctx context.Context, env secret.Store, name, keptName string) (store secret.Store, from string, err error) {
	if env != nil {
		_, err := env.Get(ctx, name)
		if err == nil {
			return env, model.SecretFromEnv, nil
		}
		if !errors.Is(err, secret.NotFound) {
			return nil, model.SecretFromEnv, err
		}
	}
	if st.kept == nil {
		return nil, "", secret.NotFound
	}
	if _, err := st.kept.Get(ctx, keptName); err != nil {
		return nil, st.from, err
	}
	return st.kept, st.from, nil
}

// kitSecret returns one of kit's own secrets — the mail connector's SMTP URL
// — as it is now: KIT_<NAME> first, then kit-<name> in the kept store. It
// reports secret.NotFound when neither holds it.
func (a *App) kitSecret(ctx context.Context, name string) (secret.Versioned, string, error) {
	st := a.secretsNow()
	store, from, err := st.find(ctx, st.kitEnv, name, kitSecretPrefix+name)
	if err != nil {
		return secret.Versioned{}, from, err
	}
	stored := name
	if from != model.SecretFromEnv {
		stored = kitSecretPrefix + name
	}
	v, err := store.Get(ctx, stored)
	return v, from, err
}

// settingFrom is where a secret came from, as a setting says it.
func settingFrom(from string) string {
	switch from {
	case "":
		return model.SettingDefault
	case model.SecretFromEnv:
		return model.SettingEnv
	}
	return model.SettingStore
}

// generatesSecrets reports whether the product declares a secret kit makes:
// kit's own data-key is not the product's, and seals nothing without a data
// directory.
func (a *App) generatesSecrets() bool {
	return a.anySecret(func(s *Secret) bool { return s.opts.generated && !s.kitOwn() })
}

// declaresSecrets reports whether the product declares a secret — under its
// own prefix, which kit's own does not use.
func (a *App) declaresSecrets() bool {
	return a.anySecret(func(s *Secret) bool { return !s.kitOwn() })
}

// anySecret reports whether one of the app's secrets satisfies ok.
func (a *App) anySecret(ok func(*Secret) bool) bool {
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if s, is := n.(*Secret); is && ok(s) {
				return true
			}
		}
	}
	return false
}
