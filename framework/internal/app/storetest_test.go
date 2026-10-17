package app_test

import (
	"testing"

	"github.com/kitsunium/sdk/framework/app/storetest"
	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
)

// The stores' conformance suite (app/storetest) on memory, on the data
// directory, and on kit's fake database; each engine module runs it on its
// engine.
func TestStoresConform(t *testing.T) {
	storetest.Run(t, storetest.BackendConfig{Name: "memory", Options: func(*testing.T, string) []app.AppConfigurer {
		return []app.AppConfigurer{app.InMemory()}
	}})
	storetest.Run(t, storetest.BackendConfig{Name: "files", Options: func(t *testing.T, _ string) []app.AppConfigurer {
		needsFileStore(t)
		return []app.AppConfigurer{app.DataDir(t.TempDir())}
	}})
	storetest.Run(t, storetest.BackendConfig{Name: "fake-database", Options: func(t *testing.T, _ string) []app.AppConfigurer {
		needsFileStore(t)
		t.Setenv("STORETEST_DATABASE_URL", verifiedURL())
		return []app.AppConfigurer{app.DataDir(t.TempDir()), app.Database("database", app.NewFakeDB(sql.DialectPostgres).Engine())}
	}})
}
