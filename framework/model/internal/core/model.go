package core

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
// Version 5 is the first the SDK's framework publishes (ADR 0147). It
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
