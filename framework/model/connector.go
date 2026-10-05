// The connector table: what brings each building block, the catalog of
// adapters, and the connectors a graph uses.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// The kinds of connectors.
const (
	// ConnectorTransport is how the product is reached: HTTP.
	ConnectorTransport ConnectorKind = core.ConnectorTransport
	// ConnectorMessaging is what it sends and receives: mail, queues.
	ConnectorMessaging ConnectorKind = core.ConnectorMessaging
	// ConnectorData is where it keeps its data: stores, databases.
	ConnectorData ConnectorKind = core.ConnectorData
)

// The roles of a connector, against the process boundary.
const (
	RoleInbound  string = core.RoleInbound
	RoleOutbound string = core.RoleOutbound
	RoleStorage  string = core.RoleStorage
)

// The connectors kit ships.
const (
	ConnectorHTTP  string = core.ConnectorHTTP
	ConnectorMail  string = core.ConnectorMail
	ConnectorStore string = core.ConnectorStore
	ConnectorQueue string = core.ConnectorQueue
	// ConnectorDatabase is brought by a store a database keeps (ADR 0004):
	// kit.Database declares the database, an engine module reaches it.
	ConnectorDatabase string = core.ConnectorDatabase
)

// The engines a database runs on (ADR 0004): one module each in kit's
// repository, the only code that imports its driver.
const (
	EnginePostgres string = core.EnginePostgres
	EngineMySQL    string = core.EngineMySQL
	EngineSQLite   string = core.EngineSQLite
)

type (
	// Connector is something a product plugs into, as the product declares it:
	// how it is reached, what it reaches out to, where it keeps its data. The
	// Studio shows a connector's pages and widgets only when the product has
	// one: a command-line tool has no requests to list, a service without a
	// mailer no mailbox.
	//
	// A connector says what the code declares, never what an environment
	// supplies. The relay a mailer delivers through, the directory a store
	// writes to, the address kit listens on are facts of the running
	// environment — [MailerInfo], [StoreInfo], the [Architecture] and [Runtime]
	// carry those. So a connector is a pure function of the nodes
	// ([ConnectorsOf]), and never moves a graph's revision.
	Connector = core.ConnectorMessage
)

type (
	// ConnectorKind groups connectors by what they are for.
	ConnectorKind = core.ConnectorKind
)

type (
	// ConnectorInfo is a connector kit can plug a product into, whether the
	// product uses it or not: what it is, and the building blocks that bring it.
	ConnectorInfo = core.ConnectorSpec
)

type (
	// Adapter is a module a connector plugs a product in through: an engine,
	// imported by the product's main to reach a database, and the only code of
	// the product that imports its driver.
	Adapter = core.AdapterMessage
)

// IsEngine reports whether backend names an engine a database runs on.
func IsEngine(backend string) bool {
	return core.IsEngine(backend)
}

// connectorCatalog is ConnectorCatalog's body: decl_gen.go writes ConnectorCatalog, from the
// design, as one call of it.
func connectorCatalog() []ConnectorInfo {
	return core.ConnectorCatalog()
}

// connectorsOf is ConnectorsOf's body: decl_gen.go writes ConnectorsOf, from the
// design, as one call of it.
func connectorsOf(nodes []Node) []Connector {
	return core.ConnectorsOf(nodes)
}
