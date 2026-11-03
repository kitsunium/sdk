package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/framework/connectors/sqlite"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// BenchmarkACommitUnderFullAndNormal measures what #999's default buys: one
// transaction writing one row and committing, on a file opened by the
// engine, under synchronous=FULL — SQLite's own default, a WAL fsync at
// every commit — and under the engine's NORMAL — a sync at the checkpoint
// only. The difference is the fsync a commit no longer pays; it depends on
// the disk, which BENCH.md's envelope names.
func BenchmarkACommitUnderFullAndNormal(b *testing.B) {
	for _, mode := range []string{"FULL", "NORMAL"} {
		b.Run(mode, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "bench.sqlite")
			v := secret.FromString("file:" + path + "?_synchronous=" + mode)
			db, err := sqlite.Engine().Open(v, func(context.Context) (secret.Value, error) { return v, nil })
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() {
				if err := db.Close(); err != nil {
					b.Errorf("closing the pool: %v", err)
				}
			})
			ctx := b.Context()
			if _, err := db.ExecContext(ctx, "CREATE TABLE entries (id INTEGER PRIMARY KEY, doc TEXT NOT NULL)"); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := tx.ExecContext(ctx, "INSERT INTO entries (doc) VALUES (?)", `{"name":"a record"}`); err != nil {
					b.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
