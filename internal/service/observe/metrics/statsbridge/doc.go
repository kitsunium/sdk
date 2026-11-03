// Package statsbridge turns the Stats value of each SDK engine that has one
// into a metrics Observer (#871), so its counters reach an exporter through
// RegisterObservers without the product copying them by hand. It adapts a
// Stats FUNCTION — the engine's own method value — rather than the engine
// itself, so no engine imports the metrics domain and the kernel cache, which
// may import nothing of the core, is covered too. Covered: the kernel cache
// (and the data/cache engines built on it), the docstore, the store-and-forward
// queue (app/forward) and the queue brokers (core/data/queue Counter). Every
// name is kitsunium.<engine>.<statistic>.
package statsbridge
