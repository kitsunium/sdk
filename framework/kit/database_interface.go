// Package kit — the interfaces of databases: the engine a connector gives, and
// what kit asks of it.
package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Engine is an SQL engine a database runs on. An engine module returns one —
// github.com/kitsunium/sdk/framework/connectors/postgres, …/mysql, …/sqlite —;
// kit calls it when the database starts. Like an SDK port, it grows by
// sibling interfaces, never by a method.
type Engine = ikit.Engine

// DatabaseOption configures a database: [Keeps], [Migrations].
type DatabaseOption = ikit.DatabaseConfigurer

// Keepable is what a database keeps: a service — every store it declares —,
// a store, or a module (ADR 0008) — every store of its services.
type Keepable = ikit.Keeper
