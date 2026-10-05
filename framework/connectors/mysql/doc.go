// Package mysql — the credentials a new connection is opened with, read
// again from the URL each time.
//
// Package mysql is kit's MySQL and MariaDB engine (ADR 0004): the one module
// a kit product imports to keep a database on MySQL, and the only code of the
// product that imports a MySQL driver — go-sql-driver/mysql, through
// database/sql. The product's main declares the database with it:
//
//	import "github.com/kitsunium/sdk/framework/connectors/mysql"
//
//	var App = kit.NewApp("vigie", intake.Service, desk.Service).With(
//		kit.Database("database", mysql.Engine()),
//	)
//
// The database's URL, in the variable <APP>_<NAME>_URL, is either a URL —
// mysql://user:password@host:3306/database?tls=true, mariadb:// alike — or
// the driver's own DSN, user:password@tcp(host:3306)/database?tls=true; its
// parameters are the driver's. Outside dev kit refuses one that does not
// write its tls: the driver's default is no TLS at all. tls=false is
// accepted when written, for a private network. The engine reads the URL
// again before each new connection, so a password rotated where it lives is
// used by the next one.
//
// On MySQL a DDL statement commits by itself: a migration of two DDL
// statements whose second fails leaves the first applied. Write one per
// migration (ADR 0055 D8 of the SDK).
package mysql
