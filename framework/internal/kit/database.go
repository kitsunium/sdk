// Package kit — databases: the SQL engines a store can be kept on.
package kit

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/secret"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// The tuning kit declares for every database, by the suffix it adds to the
// database's name; and its URL's.
const (
	dbURL          = "url"
	dbMaxOpen      = "max-open"
	dbMaxIdle      = "max-idle"
	dbMaxLifetime  = "max-lifetime"
	dbMaxIdleTime  = "max-idle-time"
	dbTimeout      = "timeout"
	dbCheckTimeout = "check-timeout"
	dbMigrate      = "migrate"
)

// The values of <name>-migrate.
const (
	migrateAtStart = "start"
	migrateManual  = "manual"
)

// maxDatabaseName bounds a database's name so that every name derived from
// it — <name>-check-timeout, the longest — stays within the 63 characters of
// a secret's or a setting's.
const maxDatabaseName int = secret.MaxNameLen - len("-"+dbCheckTimeout)

// Databases (ADR 0004). The app — the composition root — declares the
// databases the product keeps its stores in, and which database keeps which
// store: a service never names one, its handle is its store. A database runs
// on an engine, a Go module of its own that the product's main imports and
// the only code that imports a driver. Its URL is a secret of the product's,
// <APP>_<NAME>_URL first, and its tuning is settings kit declares for it.
//
// A database opens when the app starts — after the secrets, before the
// stores —, runs its migrations, answers its check, is a readiness check and
// is drawn. The stores it keeps stay in the data directory until kit keeps
// stores on SQL, which needs the SDK's SQL document store (ADR 0004, step 2);
// the start says so.

// DatabaseURLValue is what a database's URL points at, as its engine describes it
// for the graph, the Studio, the logs and every error: never a user, never a
// password, never a parameter.
type DatabaseURLValue struct {
	// Driver is the Go driver the engine speaks through: "pgx".
	Driver string
	// Address is where the server listens, host:port; a local socket's
	// path; empty for a file.
	Address string
	// Database is the database on the server, or the file.
	Database string
	// TLS is the TLS mode as the URL writes it — "verify-full", "true" —,
	// empty when the URL leaves it to the driver.
	TLS string
	// Plaintext says the mode the URL writes turns TLS off — sslmode=disable,
	// tls=false —: accepted, for a private network, and drawn "no TLS".
	Plaintext bool
	// Networked says the connection crosses a network, over TCP: outside
	// dev, its URL must write its TLS mode.
	Networked bool
}

type databaseOptions struct {
	// keeps are what Keeps named; keepsGiven says Keeps was given, even
	// with nothing: the database is then not the default.
	keeps      []Keeper
	keepsGiven bool
	migrations []sql.Migration
}

type databaseOption func(o *databaseOptions)

// kept is what Keeps was given: a service, a store by name, or a module.
type kept struct {
	svc    *Service
	store  *nodeBase
	module *Module
}

// database is a database the app declares: what kit.Database was given, and
// the settings kit declares for it. It holds no run: an App keeps its own
// (databaseRun), so one declaration may serve several apps.
type database struct {
	name   string
	engine Engine
	decl   pos
	opts   databaseOptions

	maxOpen, maxIdle                                *SettingService[int]
	maxLifetime, maxIdleTime, timeout, checkTimeout *SettingService[time.Duration]
	migrate                                         *SettingService[string]
}

// location is where the database is, as the diagram says it in dev:
// host:port/database, or the file.
func (u *DatabaseURLValue) location() string {
	switch {
	case u.Address == "":
		return u.Database
	case u.Database == "":
		return u.Address
	}
	return u.Address + "/" + u.Database
}

// tlsMode is the TLS mode as runtime.databases says it: the mode written,
// "none" when it is written off, "" when left to the driver.
func (u *DatabaseURLValue) tlsMode() string {
	if u.Plaintext {
		return "none"
	}
	return u.TLS
}

// databaseConfigure sets the option on what it configures.
func (f databaseOption) databaseConfigure(o *databaseOptions) { f(o) }

// keep names the service as what kit.Keeps keeps: every store it declares.
func (s *Service) keep() kept { return kept{svc: s} }

// keep names the store as what kit.Keeps keeps.
func (s *StoreService[T]) keep() kept { return kept{store: &s.nodeBase} }

// Keeps says what the database keeps: a service's stores, a store, a
// module's stores. The most precise wins: a store kept by name, then its
// service, then its module, then the default database — the one declared
// without Keeps —, then the data directory, then memory. [InMemory] wins
// over all of them, on the app as on a store. kit's data keys never live on
// SQLite: Keeps(kit.Privacy) there keeps its holds and its journal
// ([Privacy]).
func Keeps(things ...Keeper) DatabaseConfigurer {
	return databaseOption(func(o *databaseOptions) {
		o.keeps, o.keepsGiven = append(o.keeps, things...), true
	})
}

// Migrations are migrations, values of the SDK's sql.Migration, run when
// the database starts (<name>-migrate: start) or by `<product> migrate up`,
// kit's own first, each set under its own version table — and so its own
// lock. On MySQL a DDL statement commits by itself: write one per migration.
//
// Given to [Database], they are the product's, under schema_migrations.
// Given to [NewModule], they are the module's (ADR 0008), for the data it
// keeps outside kit's stores: run on the database that keeps the module,
// under <module>_migrations.
func Migrations(m ...sql.Migration) interface {
	DatabaseConfigurer
	ModuleConfigurer
} {
	return migrations(m)
}

// Database declares a database the product keeps its stores in, named name,
// on engine — an engine module's, which the product's main imports:
//
//	import (
//		"github.com/kitsunium/sdk/framework/connectors/postgres"
//		"github.com/kitsunium/sdk/framework/connectors/sqlite"
//	)
//
//	var App = kit.NewApp("vigie", intake.Service, desk.Service, audit.Service).With(
//		// Keeps every store no other database keeps.
//		kit.Database("database", postgres.Engine()),
//		// The audit trail apart.
//		kit.Database("archive", sqlite.Engine(), kit.Keeps(audit.Service)),
//	)
//
// name is 1 to 49 lower-case letters, digits and dashes, not starting with
// "kit-", unique among the app's databases. It names the database's URL, the
// secret <name>-url — the variable <APP>_<NAME>_URL, or <APP>_<NAME>_URL_FILE
// naming a file that holds it, then the environment's secret store —, and
// its settings: <name>-max-open (10), <name>-max-idle (2), <name>-max-lifetime
// (30m), <name>-max-idle-time (0: none), <name>-timeout (5s), <name>-check-timeout
// (5s) and <name>-migrate (start, or manual). Outside dev the URL is
// required and writes its TLS mode; in dev a database without one leaves its
// stores in the data directory, and a SQLite database without one is the
// file <data>/<name>.sqlite. [InMemory] on the app opens none.
//
//go:noinline
func Database(name string, engine Engine, opts ...DatabaseConfigurer) AppConfigurer {
	d := &database{name: name, engine: engine, decl: callerPos()}
	for _, o := range opts {
		if o != nil {
			o.databaseConfigure(&d.opts)
		}
	}
	d.declareSettings()
	return appOption(func(o *appOptions) { o.databases = append(o.databases, d) })
}

// declareSettings declares the database's tuning, as kit declares it for
// every database.
func (d *database) declareSettings() {
	positive := func(v any) phrase {
		if t, ok := v.(time.Duration); ok && t <= 0 {
			return say("database.positive")
		}
		return phrase{}
	}
	d.maxOpen = databaseSetting(d, dbMaxOpen, 10, func(v any) phrase {
		if n, ok := v.(int); ok && n <= 0 {
			return say("database.max-open")
		}
		return phrase{}
	})
	d.maxIdle = databaseSetting(d, dbMaxIdle, 2, nil)
	d.maxLifetime = databaseSetting(d, dbMaxLifetime, 30*time.Minute, nil)
	d.maxIdleTime = databaseSetting(d, dbMaxIdleTime, time.Duration(0), nil)
	d.timeout = databaseSetting(d, dbTimeout, 5*time.Second, positive)
	d.checkTimeout = databaseSetting(d, dbCheckTimeout, 5*time.Second, positive)
	d.migrate = databaseSetting(d, dbMigrate, migrateAtStart, func(v any) phrase {
		if s, ok := v.(string); ok && s != migrateAtStart && s != migrateManual {
			return say("database.migrate-mode", "start", migrateAtStart, "manual", migrateManual)
		}
		return phrase{}
	})
}

// databaseSetting declares one setting of a database: <name>-<suffix>.
func databaseSetting[T SettingValue](d *database, suffix string, def T, check func(any) phrase) *SettingService[T] {
	s := NewSettingService(def)
	s.name, s.decl, s.database, s.check = d.name+"-"+suffix, d.decl, d.name, check
	s.id = "database:" + d.name + "/setting/" + suffix
	return s
}

// settingDecls are the database's settings, in the table's order.
func (d *database) settingDecls() []settingDecl {
	return []settingDecl{d.maxOpen, d.maxIdle, d.maxLifetime, d.maxIdleTime, d.timeout, d.checkTimeout, d.migrate}
}

// urlSecret is the name of the database's URL, a secret of the product's.
func (d *database) urlSecret() string { return d.name + "-" + dbURL }

// derivedNames are the names the database takes: its URL's and its
// settings'.
func (d *database) derivedNames() []string {
	out := []string{d.urlSecret()}
	for _, s := range d.settingDecls() {
		out = append(out, s.base().name)
	}
	return out
}

// dialect is the engine's dialect, or the unknown one when there is no
// engine.
func (d *database) dialect() sql.Dialect {
	if d.engine == nil {
		return 0
	}
	return d.engine.Dialect()
}

// engineName is the engine as the graph names it: "postgres".
func (d *database) engineName() string {
	if d.engine == nil {
		return ""
	}
	return d.engine.Dialect().String()
}

// engineTitle is the engine as a reader names it: "PostgreSQL".
func engineTitle(engine string) string {
	switch engine {
	case model.EnginePostgres:
		return "PostgreSQL"
	case model.EngineMySQL:
		return "MySQL"
	case model.EngineSQLite:
		return "SQLite"
	}
	return engine
}

// urlVariable is the variable that carries the database's URL:
// <APP>_<NAME>_URL.
func (a *App) urlVariable(d *database) string {
	return secretVariable(appPrefix(a.name), d.urlSecret())
}

// databaseURL is the database's URL as it is now, and where it was found —
// a model.Setting constant: the variable <APP>_<NAME>_URL first, then
// <APP>_<NAME>_URL_FILE, then <name>-url in the environment's store; for a
// SQLite database set nowhere, the file <data>/<name>.sqlite, beside the
// data. It reports secret.NotFound when it is set nowhere else.
func (a *App) databaseURL(ctx context.Context, d *database) (secret.Value, string, error) {
	st := a.secretsNow()
	name := d.urlSecret()
	store, from, err := st.find(ctx, st.env, name, name)
	if err == nil {
		v, err := store.Get(ctx, name)
		if err != nil {
			return secret.Value{}, settingFrom(from), err
		}
		return v.Value, settingFrom(from), nil
	}
	if errors.Is(err, secret.NotFound) && d.dialect() == sql.DialectSQLite && a.dataDir != "" {
		return secret.FromString(filepath.Join(a.dataDir, d.name+".sqlite")), model.SettingDefault, nil
	}
	return secret.Value{}, "", err
}

// databaseSecretSettings are the databases' URLs as the configuration shows
// them: whether each is set, and where — never a value.
func (a *App) databaseSecretSettings(ctx context.Context) []model.Setting {
	out := make([]model.Setting, 0, len(a.opts.databases))
	for _, d := range a.opts.databases {
		s := model.Setting{Name: a.urlVariable(d), Secret: true, From: model.SettingDefault, Key: d.urlSecret(), Database: d.name}
		if !a.opts.memory {
			switch v, from, err := a.databaseURL(ctx, d); {
			case err == nil && from == model.SettingDefault:
				// A SQLite file beside the data: kit's choice, and no secret.
				s.Secret, s.Value = false, "file:"+v.RevealString()
			case err == nil:
				s.From = from
			}
		}
		out = append(out, s)
	}
	return out
}
