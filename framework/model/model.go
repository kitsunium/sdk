//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/model .

// Package model is the Product Graph: the one data shape every part of the
// framework agrees on. The runtime serves it, a static analyzer produces it,
// a CLI prints it, a Studio draws it, and an AI agent or an editor extension
// reads it.
//
// A graph is a projection: of the code for what runs, and — since the
// platform's ADR 0010 — of the design (a product's design/ directory) for
// what its structure must be. The design, not the graph, is authoritative on
// structure; the graph shows what the code is and where it differs from the
// design. Every node says where it was declared ([Source]) and every edge
// says how it is known: declared by construction ([Edge].Declared), found in
// a handler body by a static analyzer ([Edge].Static), or seen at runtime
// ([Edge].Observed). A diagram that cannot say why an arrow exists is a
// drawing; this one can.
//
// # Identity
//
// Every node has an ID, and the grammar of IDs is written here once — as
// patterns a schema generator can read ([IDPattern], [SegmentPattern],
// [NamePattern]) and as a parser ([ParseID]):
//
//	todos                              a service
//	moderation.intake                  a module's service
//	todos/endpoint/Create              a node of a service
//	todos/endpoint/GET /todos/{id}     an endpoint named by its route
//	binary:statusline                  a binary
//	binary:statusline/role/daemon      one of its process roles
//	library:textwidth                  a library shared between components
//	external                           the world outside the product
//
// The runtime, the analyzer, the design files and the generator derive names
// with the same functions ([NodeID], [QualifiedService], [RoleID]), so two
// sides never disagree about what a declaration is called.
//
// # A contract
//
// The JSON shape is a contract: [Version] is bumped on any change an older
// reader would misread.
package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

const (
	// Version is the schema version of every document in this package.
	//
	// Version 2 adds the auth, mailer and loop kinds, the sends and wakes edges,
	// the code level of every node ([CodeInfo]), the C4 context and containers
	// ([Architecture]) and the daemon's own machinery ([Runtime]).
	//
	// Version 3 adds the secret kind ([SecretInfo]), the uses edge, the secret
	// span operation, the rotation loop and the store origin of a setting.
	//
	// Version 4 is the one bump the decisions of issue #17 share. It adds the
	// port kind ([PortInfo]) and its binding, a declared edge from the port to
	// what it calls; the endpoint that implements a port
	// ([EndpointInfo].Implements), which has no method and no path; and the
	// replace mock mode ([MockReplace]). ADR 0004 adds the databases: the store's
	// database ([StoreInfo].Database), the database container, the database
	// connector and its adapters, [Runtime].Databases and [Setting].Database.
	// ADR 0008 adds the modules the app mounts ([Graph].Modules), the module of a
	// node ([Node].Module) and the Go module of a position outside the product's
	// ([Source].GoModule); and the watch, a subscription fed by stores: its mark
	// and the stores that feed it ([SubscriptionInfo].Mark,
	// [SubscriptionInfo].Stores), each drawing a declared delivers edge to it.
	// ADR 0005 adds the command and query kinds ([CommandInfo], [QueryInfo]),
	// the dispatches and asks edges, the endpoint that exposes one
	// ([EndpointInfo].Exposes), the read model ([StoreInfo].ReadModel), the
	// dispatch, ask and handle span operations and the dispatch and ask
	// controls.
	//
	// Version 5 is the first the SDK's framework module publishes (ADR 0147). It
	// adds what a product is made of beyond its services: the binary
	// ([KindBinary]) and its process roles ([KindRole]), the short CLI command
	// ([KindCLI]), the listener that is not HTTP ([KindListener]), the library
	// shared between components ([KindLibrary]) and the presentation
	// ([KindPresentation]); the edge between two roles of a product, which
	// carries a versioned contract ([EdgeContracts], [Edge].Contract); and the
	// grammar every ID follows, written once ([ID], [ParseID]). It removes what
	// the Studio no longer does (D13): the respond, fail and delay mock modes,
	// the control event and its payload — a [Mock] is a test's replacement only.
	Version int = core.Version
)

const (
	// ExternalID is the id of the node standing for the outside world.
	ExternalID string = core.ExternalID
)

type (
	// Graph is the whole product.
	// It is the JSON document the runtime serves and the analyzer writes; Version
	// names its shape.
	Graph = core.GraphMessage
)

type (
	// App identifies the product and the machine that described it.
	// It names the module, the environment and the build the graph was taken
	// from.
	App = core.AppMessage
)

type (
	// Build is what a binary was built from, at its three levels.
	// Its three levels are the Go toolchain, the main module and the modules it
	// depends on.
	Build = core.BuildMessage
)

type (
	// ModuleVersion identifies one module of a build.
	// Local and Modified say when the code that ran is not the published version.
	ModuleVersion = core.ModuleVersionMessage
)

type (
	// Source locates code.
	// File is relative to its module's root; Line and EndLine bound the
	// declaration.
	Source = core.SourceMessage
)

type (
	// Edge is one relation, with the evidence for it.
	// Declared, Static and Observed say whether construction, the code or the
	// runtime knows it.
	Edge = core.EdgeMessage
)

type (
	// Stats are observed counters for a node or an edge.
	// They are counters: they never move a graph's revision.
	Stats = core.StatsMessage
)

type (
	// Diagnostic is a problem found while building the graph.
	// It names the node or the file it is about, and how serious it is.
	Diagnostic = core.DiagnosticMessage
)

type (
	// Analysis reports the status of static analysis.
	// It says whether the analyzer ran, how long it took, and the error that
	// stopped it.
	Analysis = core.AnalysisResult
)
