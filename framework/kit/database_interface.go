package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Engine is an SQL engine a database runs on. An engine module returns one —
// github.com/kitsunium/sdk/framework/connectors/postgres, …/mysql, …/sqlite —;
// kit calls it when the database starts. Like an SDK port, it grows by
// sibling interfaces, never by a method.
type Engine = ikit.Engine

// LockWaiter is an [Engine] whose connections wait a bounded time for a lock
// another connection holds — SQLite's busy timeout —, a sibling of Engine
// (ADR 0178 C2). kit opens such an engine's pool with OpenWaiting instead
// of Open, and gives it a wait under <name>-timeout, so that a lost race on
// the lock comes back as the engine's refusal before the call's own
// deadline. An engine that does not implement it keeps its own wait.
type LockWaiter = ikit.LockWaiter

// DatabaseOption configures a database: [Keeps], [Migrations].
type DatabaseOption = ikit.DatabaseConfigurer

// Keepable is what a database keeps: a service — every store it declares —,
// a store, or a module (ADR 0008) — every store of its services.
type Keepable = ikit.Keeper
