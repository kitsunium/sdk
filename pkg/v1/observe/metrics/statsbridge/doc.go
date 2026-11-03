// Package statsbridge turns each SDK engine's Stats value into a metrics
// Observer, for metrics.RegisterObservers (#871): Cache (the kernel cache and
// the data/cache engines on it), Docstore, Forward (app/forward) and Queue (a
// queue broker's Counter). Each adapts the engine's Stats method value, so the
// engines know nothing of metrics; every name is kitsunium.<engine>.<statistic>,
// stamped InstanceKey=name when a name is given.
package statsbridge
