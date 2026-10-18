package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Store is a typed, keyed collection of entities. Every read returns a copy
// and every write stores one: an entity is kept as its JSON encoding, so what
// a handler holds can never alias what the store holds, and the memory and
// file backends behave identically.
//
// With a data directory (<PRODUCT>_DATA_DIR; ".app/data" in dev) the store persists
// every write before returning, atomically — a crash never leaves a torn
// file — and a write costs one entity, whatever the store holds. Without one,
// it lives in memory. A store is a port (ADR 0004): what it runs on is its
// engine (store_engine.go), today the SDK's document store (docstore); the framework
// makes it a node of the graph, observes every call, and speaks for its
// refusals.
type Store[T any] = iapp.StoreService[T]

// StoreOption configures a store.
type StoreOption = iapp.StoreConfigurer

// ReadModel marks a store as derived from others (ADR 0005): written by
// projections — the subscriptions that keep it from the write side's
// events —, read by queries. It changes nothing of how the store runs; the
// diagram draws it on its domain's read side, and the static analysis warns
// of a read model written by anything but a subscription. The framework's topics are
// queues, not logs: a read model cannot be replayed from its events, and one
// fed by a topic lags the commands that feed it — a query that must read its
// own writes reads the write side.
//
//	var Summaries = Service.Store("summaries", Summary.Key, app.ReadModel())
func ReadModel() StoreOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.ReadModel()
}
