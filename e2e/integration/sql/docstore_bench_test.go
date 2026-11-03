//go:build integration

// Package sql_test — what a call to the document store over SQL costs on each
// real engine, and what joining the caller's transaction adds to a write;
// what a batch costs against the same writes one by one (#1057); what the
// trash index saves Tombstones over 110 000 version rows (#1054).
// BENCH.md in this directory has the numbers and how to refresh them.
package sql_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
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

// BenchmarkSQLStoreBatch measures, on each engine, 100 writes of existing
// documents as one Batch — one transaction — against the same 100 as Puts,
// each its own transaction, in a store of 1 000 documents (#1057).
func BenchmarkSQLStoreBatch(b *testing.B) {
	for name, open := range benchEngines {
		b.Run(name, func(b *testing.B) {
			fx, _ := fillForBench(b, open, 1000)
			ctx := b.Context()
			b.Run("batch=100", func(b *testing.B) {
				for round := 0; b.Loop(); round++ {
					err := fx.store.Batch(ctx, func(w docstore.BatchWriter[member]) error {
						for i := range 100 {
							m := benchMember(i)
							m.Name = fmt.Sprintf("batch %d", round)
							if err := w.Put(m); err != nil {
								return err
							}
						}
						return nil
					})
					if err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("puts=100", func(b *testing.B) {
				for round := 0; b.Loop(); round++ {
					for i := range 100 {
						m := benchMember(i)
						m.Name = fmt.Sprintf("puts %d", round)
						if err := fx.store.Put(ctx, m); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		})
	}
}

// tombstoneDocs and tombstoneVersions shape the versions table
// BenchmarkSQLStoreTombstones reads: 10 000 documents, each with ten former
// versions and its head — 110 000 rows —, and trashed, every hundredth
// of them, a trash row more.
const (
	tombstoneDocs     int = 10_000
	tombstoneVersions int = 10
	tombstoneEvery    int = 100
)

// BenchmarkSQLStoreTombstones measures Tombstones on each engine over a
// versions table of 110 000 version rows and 100 trash rows, before the
// trash index and after SQLVersionsTrashIndexMigration adds it (#1054): a
// scan of the whole table against a read of the index. The rows are
// inserted as the store lays them out, in multi-row statements, rather than
// written one document at a time.
func BenchmarkSQLStoreTombstones(b *testing.B) {
	for name, open := range benchEngines {
		b.Run(name, func(b *testing.B) {
			e := open(b)
			fx := openVersioned(b, e, tombstoneVersions)
			ctx := b.Context()
			fillVersionRows(b, e, fx.table+"___vs")
			read := func(b *testing.B) {
				for b.Loop() {
					stones, err := fx.store.Tombstones(ctx)
					if err != nil || len(stones) != tombstoneDocs/tombstoneEvery {
						b.Fatalf("Tombstones() = %d, %v", len(stones), err)
					}
				}
			}
			b.Run("index=none", read)
			migration, err := docstore.SQLVersionsTrashIndexMigration(e.dialect, fx.table, 3)
			must(b, err)
			must(b, migration.Up(ctx, e.db))
			b.Run("index=trash", read)
		})
	}
}

// fillVersionRows inserts the versions table vs of BenchmarkSQLStoreTombstones:
// per document, its former versions — numbered from 1, each holding a
// document —, its head — the next number, NULL where the documents' table
// holds it —, and every hundredth document's trash row, numbered 0.
func fillVersionRows(b *testing.B, e *engine, vs string) {
	b.Helper()
	doc := []byte(`{"id":"m","email":"user@example.com","name":"An ordinary name","teams":["red"]}`)
	cols := []string{"doc_key", "num", "made_at", "made_ns", "meta", "doc"}
	rows := make([][]any, 0, tombstoneDocs*(tombstoneVersions+1)+tombstoneDocs/tombstoneEvery)
	for i := range tombstoneDocs {
		key := []byte(benchKey(i))
		for n := 1; n <= tombstoneVersions; n++ {
			rows = append(rows, []any{key, int64(n), int64(1_790_000_000 + n), int64(0), nil, doc})
		}
		rows = append(rows, []any{key, int64(tombstoneVersions + 1), int64(1_790_000_100), int64(0), nil, nil})
		if i%tombstoneEvery == 0 {
			rows = append(rows, []any{key, int64(0), int64(1_790_000_200), int64(0), nil, doc})
		}
	}
	err := sql.Transact(b.Context(), transactor(b, e), func(ctx context.Context, ex sql.Executor) error {
		_, err := sql.InsertRows(ctx, ex, e.dialect, sql.InsertSpec{Table: e.dialect.QuoteIdent(vs), Columns: cols, PerStatement: 500}, rows)
		return err
	})
	must(b, err)
}
