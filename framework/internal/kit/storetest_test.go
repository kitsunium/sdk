package kit_test

import (
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/kit/storetest"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// The stores' conformance suite (kit/storetest) on memory, on the data
// directory, and on kit's fake database; each engine module runs it on its
// engine.
func TestStoresConform(t *testing.T) {
	storetest.Run(t, storetest.BackendConfig{Name: "memory", Options: func(*testing.T, string) []kit.AppConfigurer {
		return []kit.AppConfigurer{kit.InMemory()}
	}})
	storetest.Run(t, storetest.BackendConfig{Name: "files", Options: func(t *testing.T, _ string) []kit.AppConfigurer {
		return []kit.AppConfigurer{kit.DataDir(t.TempDir())}
	}})
	storetest.Run(t, storetest.BackendConfig{Name: "fake-database", Options: func(t *testing.T, _ string) []kit.AppConfigurer {
		t.Setenv("STORETEST_DATABASE_URL", verifiedURL())
		return []kit.AppConfigurer{kit.DataDir(t.TempDir()), kit.Database("database", kit.NewFakeDB(sql.DialectPostgres).Engine())}
	}})
}
