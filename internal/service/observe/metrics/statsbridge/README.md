# statsbridge

Package `statsbridge` turns the Stats value of each SDK engine that has one
into a metrics `Observer` (#871), for `metrics.RegisterObservers`, which reads
it once per collection: the kernel cache (and the data/cache engines on it),
the docstore, the store-and-forward queue and the queue brokers' `Counter`.
It adapts the engine's Stats method value, so no engine imports the metrics
domain. See `CLAUDE.md`.
