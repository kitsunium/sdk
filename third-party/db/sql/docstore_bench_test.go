//go:build integration

// Package sql_test — what a call to the document store over SQL costs on each
// real engine, and what joining the caller's transaction adds to a write.
// BENCH.md in this directory has the numbers and how to refresh them.
package sql_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/docstore"
	"github.com/kitsunium/sdk/pkg/v1/sql"
)

// benchEngines are the engines every benchmark runs on.
var benchEngines = map[string]func(testing.TB) *engine{
	"sqlite":   func(tb testing.TB) *engine { return sqliteEngine(tb, "") },
	"postgres": postgresEngine,
	"mysql":    mysqlEngine,
}

// BenchmarkSQLStore measures each call once per iteration on each engine, over
// a store of 1 000 documents: a read, a write outside any transaction, the
// same write inside the caller's transaction, an Update, and a write to a
// store without indexes.
func BenchmarkSQLStore(b *testing.B) {
	for name, open := range benchEngines {
		b.Run(name, func(b *testing.B) {
			runStoreBenchmarks(b, open)
		})
	}
}

// runStoreBenchmarks runs every call's benchmark on one engine.
func runStoreBenchmarks(b *testing.B, open func(testing.TB) *engine) {
	b.Helper()
	fx, bare := fillForBench(b, open, 1000)
	ctx := b.Context()
	b.Run("Get", func(b *testing.B) {
		for i := 0; b.Loop(); i++ {
			if _, err := fx.store.Get(ctx, benchKey(i%1000)); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Put", func(b *testing.B) {
		for i := 0; b.Loop(); i++ {
			if err := fx.store.Put(ctx, benchMember(i%1000)); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Put/joined", func(b *testing.B) {
		for i := 0; b.Loop(); i++ {
			err := sql.Transact(ctx, fx.tm, func(ctx context.Context, _ sql.Executor) error {
				return fx.store.Put(ctx, benchMember(i%1000))
			})
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Update", func(b *testing.B) {
		for i := 0; b.Loop(); i++ {
			_, err := fx.store.Update(ctx, benchKey(i%1000), func(m *member) error {
				m.Name = "renamed"
				return nil
			})
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("Put/no-indexes", func(b *testing.B) {
		for i := 0; b.Loop(); i++ {
			if err := bare.Put(ctx, benchMember(i%1000)); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// fillForBench opens a members store and a store without indexes on the
// engine, both holding n documents.
func fillForBench(b *testing.B, open func(testing.TB) *engine, n int) (*storeFixture, *docstore.SQLStore[member]) {
	b.Helper()
	e := open(b)
	fx := openMembers(b, e)
	table := uniqueName("bare__")
	migrateStore(b, e, table)
	bare, err := docstore.OpenSQL(docstore.SQLConfig[member]{
		Key: func(m member) string { return m.ID }, Transactor: fx.tm, Dialect: e.dialect, Table: table,
	})
	if err != nil {
		b.Fatal(err)
	}
	for i := range n {
		if putErr := fx.store.Put(b.Context(), benchMember(i)); putErr != nil {
			b.Fatal(putErr)
		}
		if putErr := bare.Put(b.Context(), benchMember(i)); putErr != nil {
			b.Fatal(putErr)
		}
	}
	return fx, bare
}

// benchKey is the key of the i-th benchmark document.
func benchKey(i int) string { return fmt.Sprintf("m%06d", i) }

// benchMember is the i-th benchmark document.
func benchMember(i int) member {
	return member{ID: benchKey(i), Email: fmt.Sprintf("user%06d@example.com", i), Name: "An ordinary name", Teams: []string{"red"}}
}

// BenchmarkSQLStoreVersions measures, on each engine, what keeping versions
// adds to a call (ADR 0143): a Put and an Update on a store keeping ten former
// versions of each of its 100 documents — every document already holding ten,
// so each write makes one and prunes one — and a read of every version of a
// document.
func BenchmarkSQLStoreVersions(b *testing.B) {
	for name, open := range benchEngines {
		b.Run(name, func(b *testing.B) {
			const docs int = 100
			fx := openVersioned(b, open(b), 10)
			ctx := b.Context()
			for round := range 11 {
				for i := range docs {
					doc := benchMember(i)
					doc.Name = fmt.Sprintf("round %d", round)
					if err := fx.store.Put(ctx, doc); err != nil {
						b.Fatal(err)
					}
				}
			}
			b.Run("Put", func(b *testing.B) {
				for i := 0; b.Loop(); i++ {
					doc := benchMember(i % docs)
					doc.Name = fmt.Sprintf("put %d", i)
					if err := fx.store.Put(ctx, doc); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("Update", func(b *testing.B) {
				for i := 0; b.Loop(); i++ {
					_, err := fx.store.Update(ctx, benchKey(i%docs), func(m *member) error {
						m.Name = fmt.Sprintf("update %d", i)
						return nil
					})
					if err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("Versions", func(b *testing.B) {
				for i := 0; b.Loop(); i++ {
					if _, err := fx.store.Versions(ctx, benchKey(i%docs)); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
