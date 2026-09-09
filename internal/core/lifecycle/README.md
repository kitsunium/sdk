# lifecycle

Package `lifecycle` declares the SDK ordered start/stop port: the `Start` that
brings a component up, the `Stop` that takes it down, and the `Lifecycle` that
owns an ordered set of them and drives it in both directions — plus the
`ComponentValue` / `TransitionValue` domain values, the two-valued `Phase`, and
the typed registration sentinels (InvalidComponent / DuplicateComponent /
LifecycleRunning / ComponentPanicked).

Both `Start` and `Stop` are function ports rather than interfaces, so neither
can grow a method and break a downstream implementer (ADR 0039).

The order components are added in IS the dependency order, and shutdown is its
exact reverse. There is no graph: a linear sequence already is a topological
order. A `Stop` is called only for a component whose `Start` returned nil, so a
teardown may assume its own construction succeeded.

The engine, the per-component shutdown budget and the opt-in signal /
`sd_notify` wiring live in `internal/service/lifecycle`; facade:
`pkg/v1/lifecycle`. ADR 0050. See `CLAUDE.md`.
