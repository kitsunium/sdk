package storeengine

import "github.com/kitsunium/sdk/framework/internal/app/plug"

// Enable plugs the stores' engines, the framework's scope on a database and the
// migrations of the framework's tables into the framework.
func Enable() {
	plug.Stores.Store(&plug.StoreDriverConfig{Open: open, Database: openDatabase, TableMigration: tableMigration})
}

// open opens the engine of one store where cfg places it: on its database,
// else the SDK's document store in the data directory or in memory.
//
// IFACE-PLUGIN: kit reaches a store's engine through plug.StoreEngine; these
// are its implementations.
func open(cfg plug.StoreConfig) (plug.StoreEngine, error) {
	if cfg.Database != nil {
		return openSQL(&cfg)
	}
	return openFile(&cfg)
}
