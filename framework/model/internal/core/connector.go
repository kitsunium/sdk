// The connector table: what brings each building block, the catalog of
// adapters, and the connectors a graph uses.

package core

import (
	"maps"
	"slices"
	"strings"
)

// The kinds of connectors.
const (
	// ConnectorTransport is how the product is reached: HTTP.
	ConnectorTransport ConnectorKind = "transport"
	// ConnectorMessaging is what it sends and receives: mail, queues.
	ConnectorMessaging ConnectorKind = "messaging"
	// ConnectorData is where it keeps its data: stores, databases.
	ConnectorData ConnectorKind = "data"
)

// The roles of a connector, against the process boundary.
const (
	RoleInbound  = "inbound"
	RoleOutbound = "outbound"
	RoleStorage  = "storage"
)

// The connectors kit ships.
const (
	ConnectorHTTP  = "http"
	ConnectorMail  = "mail"
	ConnectorStore = "store"
	ConnectorQueue = "queue"
	// ConnectorDatabase is brought by a store a database keeps (ADR 0004):
	// kit.Database declares the database, an engine module reaches it.
	ConnectorDatabase = "database"
)

// The engines a database runs on (ADR 0004): one module each in kit's
// repository, the only code that imports its driver.
const (
	EnginePostgres = "postgres"
	EngineMySQL    = "mysql"
	EngineSQLite   = "sqlite"
)

var (
	// Engines are the database connector's adapters, as the catalog lists them.
	engines = []AdapterMessage{
		{ID: EngineMySQL, Name: "MySQL", Import: "github.com/kitsunium/sdk/framework/connectors/mysql", Driver: "github.com/go-sql-driver/mysql"},
		{ID: EnginePostgres, Name: "PostgreSQL", Import: "github.com/kitsunium/sdk/framework/connectors/postgres", Driver: "github.com/jackc/pgx/v5"},
		{ID: EngineSQLite, Name: "SQLite", Import: "github.com/kitsunium/sdk/framework/connectors/sqlite", Driver: "modernc.org/sqlite"},
	}

	// builtin is every connector kit ships: the one table the graph, the
	// Studio's gating and its catalog of available connectors all read. A node
	// may bring several: a store a database keeps brings the store connector
	// and the database connector.
	builtin = []ConnectorSpec{
		{
			ID: ConnectorHTTP, Kind: ConnectorTransport, Roles: []string{RoleInbound}, Protocol: "http",
			Declares: []NodeKind{KindEndpoint, KindFrontend, KindAuth},
		},
		{
			ID: ConnectorMail, Kind: ConnectorMessaging, Roles: []string{RoleOutbound},
			Settings: []string{VarSMTPURL}, Declares: []NodeKind{KindMailer},
			detail: func(n *NodeEntity) string {
				if n.Mailer == nil {
					return ""
				}
				return n.Mailer.Transport
			},
		},
		{
			ID: ConnectorQueue, Kind: ConnectorMessaging, Roles: []string{RoleStorage},
			Declares: []NodeKind{KindTopic, KindSubscription, KindCommand},
			// A command brings a queue only when it waits in one (ADR 0005).
			brings: func(n *NodeEntity) bool { return n.Kind != KindCommand || queued(n) },
			detail: func(n *NodeEntity) string {
				switch {
				case n.Topic != nil:
					return n.Topic.Broker
				case n.Command != nil:
					return n.Command.Queue
				}
				return ""
			},
		},
		{
			ID: ConnectorStore, Kind: ConnectorData, Roles: []string{RoleStorage},
			Declares: []NodeKind{KindStore},
			detail: func(n *NodeEntity) string {
				if n.Store == nil {
					return ""
				}
				return n.Store.Backend
			},
		},
		{
			ID: ConnectorDatabase, Kind: ConnectorData, Roles: []string{RoleOutbound, RoleStorage},
			Declares: []NodeKind{KindStore}, Adapters: engines,
			brings: func(n *NodeEntity) bool { return n.Store != nil && n.Store.Database != "" },
			// The engines its stores run on: none while they stay in the data
			// directory.
			detail: func(n *NodeEntity) string {
				if n.Store == nil || !IsEngine(n.Store.Backend) {
					return ""
				}
				return n.Store.Backend
			},
		},
	}
)

// ConnectorMessage is something a product plugs into, as the product declares it:
// how it is reached, what it reaches out to, where it keeps its data. The
// Studio shows a connector's pages and widgets only when the product has
// one: a command-line tool has no requests to list, a service without a
// mailer no mailbox.
//
// A connector says what the code declares, never what an environment
// supplies. The relay a mailer delivers through, the directory a store
// writes to, the address kit listens on are facts of the running
// environment — [MailerSpec], [StoreSpec], the [ArchitectureMessage] and [RuntimeMessage]
// carry those. So a connector is a pure function of the nodes
// ([ConnectorsOf]), and never moves a graph's revision.
type ConnectorMessage struct {
	// ID names the connector: [ConnectorHTTP], [ConnectorMail],
	// [ConnectorStore], [ConnectorQueue], [ConnectorDatabase].
	ID string `json:"id"`
	// Kind groups connectors by what they are for; kept for older readers,
	// Roles says more.
	Kind ConnectorKind `json:"kind"`
	// Roles place it against the process boundary: [RoleInbound] (the
	// product is reached through it), [RoleOutbound] (the product reaches out
	// through it), [RoleStorage] (the product keeps data in it). kit's
	// built-in queues run inside the process: storage only.
	Roles []string `json:"roles,omitempty"`
	// Protocol is the wire protocol the code fixes, when it does: "http".
	Protocol string `json:"protocol,omitempty"`
	// Detail is what the product uses, in kit's words: the stores'
	// backends, the mail transport, the queues' brokers.
	Detail string `json:"detail,omitempty"`
	// Settings are the settings the connector owns — the ones its page shows
	// with their value. A setting of a resource it uses (the data directory,
	// kit's port) belongs to that resource: [ContainerMessage].
	Settings []string `json:"settings,omitempty"`
	// Nodes are the nodes the connector brings to the graph, sorted.
	Nodes []string `json:"nodes"`
}

// ConnectorKind groups connectors by what they are for.
type ConnectorKind string

// ConnectorSpec is a connector kit can plug a product into, whether the
// product uses it or not: what it is, and the building blocks that bring it.
type ConnectorSpec struct {
	// ID is the connector's: [ConnectorHTTP]…
	ID string `json:"id"`
	// Kind groups it, as [ConnectorMessage.Kind].
	Kind ConnectorKind `json:"kind"`
	// Roles place it against the process boundary.
	Roles []string `json:"roles"`
	// Protocol is the wire protocol the code fixes, when it does.
	Protocol string `json:"protocol,omitempty"`
	// Settings are the settings it owns.
	Settings []string `json:"settings,omitempty"`
	// Declares are the building blocks whose declaration brings it: a
	// command brings the queue connector only when it is queued.
	Declares []NodeKind `json:"declares"`
	// Adapters are the modules a product imports to plug it into what it
	// reaches: the engines of the database connector.
	Adapters []AdapterMessage `json:"adapters,omitempty"`

	// brings narrows Declares: a node of a declared kind brings the
	// connector only when it holds. nil: every such node does.
	brings func(*NodeEntity) bool
	// detail is what a node that brings it says of it; nil: nothing.
	detail func(*NodeEntity) string
}

// AdapterMessage is a module a connector plugs a product in through: an engine,
// imported by the product's main to reach a database, and the only code of
// the product that imports its driver.
type AdapterMessage struct {
	// ID names it as the graph does: "postgres".
	ID string `json:"id"`
	// Name is what a reader calls what it reaches: "PostgreSQL".
	Name string `json:"name"`
	// Import is the package the product's main imports:
	// "github.com/kitsunium/sdk/framework/connectors/postgres".
	Import string `json:"import"`
	// Driver is the Go driver it links: "github.com/jackc/pgx/v5".
	Driver string `json:"driver,omitempty"`
}

// IsEngine reports whether backend names an engine a database runs on.
func IsEngine(backend string) bool {
	return slices.ContainsFunc(engines, func(e AdapterMessage) bool { return e.ID == backend })
}

// queued reports whether n is a command that waits in its own queue.
func queued(n *NodeEntity) bool { return n.Command != nil && n.Command.Mode == ModeQueued }

// ConnectorCatalog is every connector kit ships, sorted by ID: what a
// product may plug into.
func ConnectorCatalog() []ConnectorSpec {
	out := make([]ConnectorSpec, len(builtin))
	for i, c := range builtin {
		c.Roles = slices.Clone(c.Roles)
		c.Settings = slices.Clone(c.Settings)
		c.Declares = slices.Clone(c.Declares)
		c.Adapters = slices.Clone(c.Adapters)
		c.brings, c.detail = nil, nil
		out[i] = c
	}
	slices.SortFunc(out, func(a, b ConnectorSpec) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// connectorsFor are the built-in connectors a node brings: those that
// declare its kind, and whose predicate, when they have one, holds.
func connectorsFor(n *NodeEntity) []*ConnectorSpec {
	var out []*ConnectorSpec
	for i := range builtin {
		c := &builtin[i]
		if slices.Contains(c.Declares, n.Kind) && (c.brings == nil || c.brings(n)) {
			out = append(out, c)
		}
	}
	return out
}

// detailOf is what a node says of a connector it brings, in kit's words;
// ok is false when the connector has nothing to say or the node says
// nothing.
func detailOf(c *ConnectorSpec, n *NodeEntity) (detail string, ok bool) {
	if c.detail == nil {
		return "", false
	}
	detail = c.detail(n)
	return detail, detail != ""
}

// ConnectorsOf derives the connectors a product uses from its nodes: an
// endpoint, a frontend or an auth handler means HTTP, a mailer mail, a store
// stores — and a database when one keeps it —, a topic, a subscription or
// a queued command queues. Sorted by ID.
func ConnectorsOf(nodes []NodeEntity) []ConnectorMessage {
	type found struct {
		info    ConnectorSpec
		nodes   []string
		details []string
	}
	by := map[string]*found{}
	for i := range nodes {
		n := &nodes[i]
		for _, info := range connectorsFor(n) {
			f := by[info.ID]
			if f == nil {
				f = &found{info: *info}
				by[info.ID] = f
			}
			f.nodes = append(f.nodes, n.ID)
			if d, ok := detailOf(info, n); ok && !slices.Contains(f.details, d) {
				f.details = append(f.details, d)
			}
		}
	}
	out := make([]ConnectorMessage, 0, len(by))
	for _, id := range slices.Sorted(maps.Keys(by)) {
		f := by[id]
		slices.Sort(f.nodes)
		slices.Sort(f.details)
		out = append(out, ConnectorMessage{
			ID: id, Kind: f.info.Kind, Roles: slices.Clone(f.info.Roles), Protocol: f.info.Protocol,
			Detail: strings.Join(f.details, ", "), Settings: slices.Clone(f.info.Settings), Nodes: slices.Compact(f.nodes),
		})
	}
	return out
}
