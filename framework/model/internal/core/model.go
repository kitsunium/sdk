//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/model/internal/core .

// Package core is the Product Graph under the names the role rule asks for:
// every struct carries its role (NodeEntity, GraphMessage, EndpointSpec, …).
// framework/model is its public face — an alias per type under the graph's
// own names (Node, Graph, EndpointInfo, …), its constants and its functions —
// and the one package a caller imports.
package core

import "time"

// Version is the schema version of every document in this package.
//
// Version 2 adds the auth, mailer and loop kinds, the sends and wakes edges,
// the code level of every node ([CodeResult]), the C4 context and containers
// ([ArchitectureMessage]) and the daemon's own machinery ([RuntimeMessage]).
//
// Version 3 adds the secret kind ([SecretSpec]), the uses edge, the secret
// span operation, the rotation loop and the store origin of a setting.
//
// Version 4 is the one bump the decisions of issue #17 share. It adds the
// port kind ([PortSpec]) and its binding, a declared edge from the port to
// what it calls; the endpoint that implements a port
// ([EndpointSpec.Implements]), which has no method and no path; and the
// replace mock mode ([MockReplace]). ADR 0004 adds the databases: the store's
// database ([StoreSpec.Database]), the database container, the database
// connector and its adapters, [RuntimeMessage.Databases] and [SettingMessage.Database].
// ADR 0008 adds the modules the app mounts ([GraphMessage.Modules]), the module of a
// node ([NodeEntity.Module]) and the Go module of a position outside the product's
// ([SourceMessage.GoModule]); and the watch, a subscription fed by stores: its mark
// and the stores that feed it ([SubscriptionSpec.Mark],
// [SubscriptionSpec.Stores]), each drawing a declared delivers edge to it.
// ADR 0005 adds the command and query kinds ([CommandSpec], [QuerySpec]),
// the dispatches and asks edges, the endpoint that exposes one
// ([EndpointSpec.Exposes]), the read model ([StoreSpec.ReadModel]), the
// dispatch, ask and handle span operations and the dispatch and ask
// controls.
//
// Version 5 is the first the SDK's framework module publishes (ADR 0143). It
// adds what a product is made of beyond its services: the binary
// ([KindBinary]) and its process roles ([KindRole]), the short CLI command
// ([KindCLI]), the listener that is not HTTP ([KindListener]), the library
// shared between components ([KindLibrary]) and the presentation
// ([KindPresentation]); the edge between two roles of a product, which
// carries a versioned contract ([EdgeContracts], [EdgeMessage.Contract]); and the
// grammar every ID follows, written once ([IDValue], [ParseID]). It removes what
// the Studio no longer does (D13): the respond, fail and delay mock modes,
// the control event and its payload — a [MockMessage] is a test's replacement only.
const Version int = 5

// ExternalID is the id of the node standing for the outside world.
const ExternalID = "external"

// GraphMessage is the whole product.
// It is the JSON document the runtime serves and the analyzer writes; Version
// names its shape.
type GraphMessage struct {
	// Version is always [Version] for a document this package produced.
	Version int `json:"version"`
	// Revision changes whenever the structure changes (nodes, edges, sources),
	// never when only observed statistics move. A reader caches layout on it.
	Revision string `json:"revision"`
	// App identifies the product.
	App AppMessage `json:"app"`
	// Nodes are sorted by ID.
	Nodes []NodeEntity `json:"nodes"`
	// Edges are sorted by ID.
	Edges []EdgeMessage `json:"edges"`
	// Catalog lists the generic mechanics this kit version offers.
	Catalog []MechanicMessage `json:"catalog,omitempty"`
	// Diagnostics are problems found while building the graph.
	Diagnostics []DiagnosticMessage `json:"diagnostics,omitempty"`
	// Runtime is present when a running process describes itself.
	Runtime *RuntimeMessage `json:"runtime,omitempty"`
	// Analysis reports the static analysis that enriched this graph.
	Analysis *AnalysisResult `json:"analysis,omitempty"`
	// Architecture is the product at C4's two outer levels: who uses it and
	// what it depends on, and what it is deployed as.
	Architecture *ArchitectureMessage `json:"architecture,omitempty"`
	// Connectors are what the product plugs into, derived from its nodes by
	// [GraphMessage.Normalize]: the Studio shows their pages and widgets only.
	Connectors []ConnectorMessage `json:"connectors,omitempty"`
	// ConnectorCatalog is every connector this kit version ships, used or
	// not: what the product may plug into, and what brings each.
	ConnectorCatalog []ConnectorSpec `json:"connectorCatalog,omitempty"`
	// Modules are the modules the app mounts (ADR 0008), sorted by name.
	Modules []ModuleMessage `json:"modules,omitempty"`
}

// AppMessage identifies the product and the machine that described it.
// It names the module, the environment and the build the graph was taken
// from.
type AppMessage struct {
	// Name is the product name given to kit.NewApp.
	Name string `json:"name"`
	// Module is the Go module path of the product.
	Module string `json:"module,omitempty"`
	// Root is the absolute module root on the machine that produced the
	// graph. Present in dev only: it is what an editor link needs, and what a
	// production graph must not leak.
	Root string `json:"root,omitempty"`
	// Env is "dev" or "production".
	Env string `json:"env,omitempty"`
	// Kit is the kit version that produced the graph.
	Kit string `json:"kit,omitempty"`
	// Go is the Go version the product was built with.
	Go string `json:"go,omitempty"`
	// StartedAt is when the process started, for a runtime graph.
	StartedAt *time.Time `json:"startedAt,omitempty"`
	// Build says what the binary was built from: the product, kit and the
	// SDK kit depends on.
	Build *BuildMessage `json:"build,omitempty"`
}

// BuildMessage is what a binary was built from, at its three levels.
// Its three levels are the Go toolchain, the main module and the modules it
// depends on.
type BuildMessage struct {
	// Product is the product's own module.
	Product ModuleVersionMessage `json:"product"`
	// Kit is the framework.
	Kit ModuleVersionMessage `json:"kit"`
	// SDK is the SDK the framework depends on.
	SDK ModuleVersionMessage `json:"sdk"`
}

// ModuleVersionMessage identifies one module of a build.
// Local and Modified say when the code that ran is not the published version.
type ModuleVersionMessage struct {
	// Module is the module path.
	Module string `json:"module"`
	// Version is the published module version, "v0.4.6"; empty for a local
	// directory or a build without module version.
	Version string `json:"version,omitempty"`
	// Revision is the VCS commit the module was built from, when known.
	Revision string `json:"revision,omitempty"`
	// Time is that commit's time.
	Time *time.Time `json:"time,omitempty"`
	// Modified is set when the build included uncommitted changes.
	Modified bool `json:"modified,omitempty"`
	// Local is set when the module came from a local directory (a replace
	// or a workspace) rather than a published version.
	Local bool `json:"local,omitempty"`
}

// SourceMessage locates code.
// File is relative to its module's root; Line and EndLine bound the
// declaration.
type SourceMessage struct {
	// File is relative to App.Root, slash-separated — or, when GoModule is
	// set, to the root of that Go module.
	File string `json:"file"`
	// Line is 1-based.
	Line int `json:"line"`
	// EndLine, when set, closes the range [Line, EndLine].
	EndLine int `json:"endLine,omitempty"`
	// Func is the qualified Go function, when the range is a function.
	Func string `json:"func,omitempty"`
	// GoModule is set on a position outside the product's Go module — a
	// module's (ADR 0008), a library's —: that Go module, "path@version", or
	// its path alone when it is built from a local directory.
	GoModule string `json:"goModule,omitempty"`
}

// EdgeMessage is one relation, with the evidence for it.
// Declared, Static and Observed say whether construction, the code or the
// runtime knows it.
type EdgeMessage struct {
	// ID is "<from>|<kind>|<to>" or "<from>|<kind>|<to>|<label>".
	ID string `json:"id"`
	// From is the source node ID.
	From string `json:"from"`
	// To is the target node ID.
	To string `json:"to"`
	// Kind says how the two relate.
	Kind EdgeKind `json:"kind"`
	// Label refines the kind: the event of a transition ("create" for a new
	// instance), or "direct" for a call that bypasses an endpoint.
	Label string `json:"label,omitempty"`
	// Contract names the versioned contract of an [EdgeContracts] edge:
	// "<name>/v<major>", "render/v1".
	Contract string `json:"contract,omitempty"`
	// Declared is set when the edge exists by construction.
	Declared bool `json:"declared,omitempty"`
	// Static lists the call sites the analyzer found, sorted.
	Static []SourceMessage `json:"static,omitempty"`
	// Observed is present once the edge was seen at runtime.
	Observed *StatsMessage `json:"observed,omitempty"`
}

// StatsMessage are observed counters for a node or an edge.
// They are counters: they never move a graph's revision.
type StatsMessage struct {
	// Count is how many times it ran.
	Count int64 `json:"count"`
	// Errors is how many of those failed.
	Errors int64 `json:"errors"`
	// LastAt is when it last ran.
	LastAt *time.Time `json:"lastAt,omitempty"`
	// AvgMs is the mean duration in milliseconds.
	AvgMs float64 `json:"avgMs"`
	// MaxMs is the longest duration in milliseconds.
	MaxMs float64 `json:"maxMs"`
}

// DiagnosticMessage is a problem found while building the graph.
// It names the node or the file it is about, and how serious it is.
type DiagnosticMessage struct {
	// Severity is "error" or "warning". An error refuses to start the app.
	Severity string `json:"severity"`
	// Message says what is wrong and how to fix it, in English.
	Message string `json:"message"`
	// Texts is the message in each language kit speaks, by language: "en",
	// "fr". The Studio shows its reader's.
	Texts map[string]string `json:"texts,omitempty"`
	// Node is the node concerned, when there is one.
	Node string `json:"node,omitempty"`
	// Source is where the problem is.
	Source *SourceMessage `json:"source,omitempty"`
}

// AnalysisResult reports the status of static analysis.
// It says whether the analyzer ran, how long it took, and the error that
// stopped it.
type AnalysisResult struct {
	// Status is "pending", "running", "ok", "failed" or "unavailable".
	Status string `json:"status"`
	// Error explains a failed or unavailable analysis.
	Error string `json:"error,omitempty"`
	// At is when the analysis finished.
	At *time.Time `json:"at,omitempty"`
	// Packages is how many packages were analyzed.
	Packages int `json:"packages,omitempty"`
	// TookMs is how long it took.
	TookMs float64 `json:"tookMs,omitempty"`
}
