package storekit

import "github.com/kitsunium/sdk/framework/internal/kit/plug"

// Enable plugs the stores' engines, kit's scope on a database and the
// migrations of kit's tables into kit.
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
