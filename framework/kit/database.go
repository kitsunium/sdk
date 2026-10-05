package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// DatabaseURL is what a database's URL points at, as its engine describes it
// for the graph, the Studio, the logs and every error: never a user, never a
// password, never a parameter.
type DatabaseURL = ikit.DatabaseURLValue

// keeps is Keeps's body: decl_gen.go writes Keeps, from the
// design, as one call of it.
func keeps(things ...Keepable) DatabaseOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Keeps(things...)
}

// migrations is Migrations's body: decl_gen.go writes Migrations, from the
// design, as one call of it.
func migrations(m ...sql.Migration) interface {
	DatabaseOption
	ModulePart
} {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Migrations(m...)
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
func Database(name string, engine Engine, opts ...DatabaseOption) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Database(name, engine, opts...)
}
