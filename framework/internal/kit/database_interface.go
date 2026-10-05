package kit

// DatabaseConfigurer configures a database: [Keeps], [Migrations].
type DatabaseConfigurer interface {
	databaseConfigure(o *databaseOptions)
}

// Keeper is what a database keeps: a service — every store it declares —,
// a store, or a module (ADR 0008) — every store of its services.
type Keeper interface {
	keep() kept
}
