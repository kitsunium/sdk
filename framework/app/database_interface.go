package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Engine is an SQL engine a database runs on. An engine module returns one —
// github.com/kitsunium/sdk/framework/connectors/postgres, …/mysql, …/sqlite —;
// the framework calls it when the database starts. Like an SDK port, it grows by
// sibling interfaces, never by a method.
type Engine = iapp.Engine

// LockWaiter is an [Engine] whose connections wait a bounded time for a lock
// another connection holds — SQLite's busy timeout —, a sibling of Engine
// (ADR 0178 C2). The framework opens such an engine's pool with OpenWaiting instead
// of Open, and gives it a wait under <name>-timeout, so that a lost race on
// the lock comes back as the engine's refusal before the call's own
// deadline. An engine that does not implement it keeps its own wait.
type LockWaiter = iapp.LockWaiter

// DatabaseOption configures a database: [Keeps], [Migrations].
type DatabaseOption = iapp.DatabaseConfigurer

// Classifier is an [Engine] that reads its driver's errors: Transient says
// whether a failed transaction is one the database asks to run again. The
// framework runs such a transaction again, its outermost level, up to
// <name>-tx-retries times — never one a [Service].Go body opens.
type Classifier = iapp.Classifier

// TransactOption asks a transaction for an isolation level or for
// read-only: [Serializable], [RepeatableRead], [ReadOnly].
type TransactOption = iapp.TransactOption

// Keepable is what a database keeps: a service — every store it declares —,
// a store, or a module (ADR 0008) — every store of its services.
type Keepable = iapp.Keeper
