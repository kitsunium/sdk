// Package storetest is the conformance suite of kit's stores (ADR 0004): what
// a store does on every backend — in memory, in the data directory, on each
// database engine —, run by kit's tests on memory, files and kit's fake
// database, and by each engine module's tests on its engine. It is what
// keeps the test double honest: a product tested in memory sees what its
// database does.
//
//	func TestConformance(t *testing.T) {
//		storetest.Run(t, storetest.BackendConfig{Name: "sqlite", Options: func(t *testing.T, app string) []kit.AppOption {
//			return []kit.AppOption{kit.DataDir(t.TempDir()), kit.Database("database", sqlite.Engine())}
//		}})
//	}
//
// Each case declares a service of its own, with a name of its own, so the
// cases of one run can share one database: their tables never meet.
package storetest
