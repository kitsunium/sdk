package mysql

import (
	"context"
	stdsql "database/sql"
	"net"
	"net/url"
	"strings"

	driver "github.com/go-sql-driver/mysql"
	"github.com/kitsunium/sdk/framework/kit"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

const (
	// driverName is how the graph names what speaks to the server.
	driverName string = "go-sql-driver/mysql"

	// defaultPort is MySQL's, which a URL may leave out.
	defaultPort string = "3306"
)

// errMalformed is a URL the driver cannot read. It never quotes the URL:
// the driver's own error could.
var errMalformed = errs.New(CodeURLMalformed, "URL_MALFORMED", "mysql: the URL is not one go-sql-driver/mysql reads",
	"mysql connector: the driver refused the URL; the URL is never quoted, the driver's text would name its user and host")

// engine is the connector kit.Database drives; it holds nothing.
type engine struct{}

// quiet is a driver logger that says nothing.
type quiet struct{}

// Engine is the MySQL engine, for kit.Database.
func Engine() kit.Engine { return engine{} }

// Dialect is the SQL the database speaks.
func (engine) Dialect() sql.Dialect { return sql.DialectMySQL }

// Describe says where the URL points — host:port, or the socket; the
// database; the tls as the URL writes it — and never who connects or with
// what password.
func (engine) Describe(u secret.Value) (kit.DatabaseURL, error) {
	cfg, err := config(u.RevealString())
	if err != nil {
		return kit.DatabaseURL{}, errMalformed
	}
	return kit.DatabaseURL{
		Driver: driverName, Address: cfg.Addr, Database: cfg.DBName, TLS: cfg.TLSConfig,
		Plaintext: cfg.TLSConfig == "false", Networked: cfg.Net == "tcp" || cfg.Net == "tcp4" || cfg.Net == "tcp6",
	}, nil
}

// Open opens the pool the URL names, through the driver's connector. Before
// each new connection the driver calls back, and the engine asks current
// for the URL as it is now and takes its credentials: a password rotated
// where it lives is used by the next connection, and <name>-max-lifetime
// bounds how long one opened with the previous password lives.
func (engine) Open(u secret.Value, current func(context.Context) (secret.Value, error)) (*stdsql.DB, error) {
	cfg, err := config(u.RevealString())
	if err != nil {
		return nil, errMalformed
	}
	if err := cfg.Apply(driver.BeforeConnect(func(ctx context.Context, c *driver.Config) error {
		now, err := current(ctx)
		if err != nil {
			return err
		}
		fresh, err := config(now.RevealString())
		if err != nil {
			return errMalformed
		}
		withCredentials(c, fresh)
		return nil
	})); err != nil {
		return nil, errMalformed
	}
	connector, err := driver.NewConnector(cfg)
	if err != nil {
		return nil, errMalformed
	}
	return stdsql.OpenDB(connector), nil
}

// config reads a URL — mysql:// or mariadb:// — or the driver's own DSN.
// The driver's own logger is silenced: a kit product logs through the SDK
// alone, and the driver's lines would name the server.
func config(raw string) (*driver.Config, error) {
	cfg, err := parse(raw)
	if err != nil {
		return nil, err
	}
	cfg.Logger = quiet{}
	return cfg, nil
}

// Print drops the driver's line.
func (quiet) Print(_ ...any) {}

// parse reads a URL or a DSN.
func parse(raw string) (*driver.Config, error) {
	scheme, _, isURL := strings.Cut(raw, "://")
	if !isURL {
		return driver.ParseDSN(raw)
	}
	if scheme != "mysql" && scheme != "mariadb" {
		return nil, errMalformed
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errMalformed
	}
	// The query holds the driver's own parameters: its DSN parser reads
	// them, the URL gives the rest.
	cfg, err := driver.ParseDSN("/?" + u.RawQuery)
	if err != nil {
		return nil, err
	}
	cfg.Net, cfg.Addr, cfg.DBName = "tcp", u.Host, strings.TrimPrefix(u.Path, "/")
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		cfg.Addr = net.JoinHostPort(u.Hostname(), defaultPort)
	}
	if u.User != nil {
		cfg.User = u.User.Username()
		cfg.Passwd, _ = u.User.Password()
	}
	// The DSN parser built the TLS configuration for its own address: the
	// connector builds it again for this one, whose host the server's
	// certificate must name.
	cfg.TLS = nil
	return cfg, nil
}
