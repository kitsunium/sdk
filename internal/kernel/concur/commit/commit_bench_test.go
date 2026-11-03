package commit_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/concur/commit"
)

// syncedFile is a file in the benchmark's temporary directory whose Sync is
// the flush both rows pay: a real fsync(2) on the test's own file.
func syncedFile(b *testing.B) *os.File {
	b.Helper()
	file, err := os.Create(filepath.Join(b.TempDir(), "journal"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = file.Close() })
	if _, err := file.Write([]byte("x")); err != nil {
		b.Fatal(err)
	}
	return file
}

// BenchmarkSync_Direct_Parallel is the baseline: every goroutine fsyncs for
// itself, as every file-backed engine does today.
func BenchmarkSync_Direct_Parallel(b *testing.B) {
	file := syncedFile(b)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := file.Sync(); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkSync_Commit_Parallel routes the same fsync through a Group: the
// callers that arrive during one fsync share the next. fsyncs/op reports how
// many fsyncs the b.N calls actually cost.
func BenchmarkSync_Commit_Parallel(b *testing.B) {
	file := syncedFile(b)
	var flushes int64
	group := commit.New(func(context.Context, []int) error {
		flushes++
		return file.Sync()
	}, commit.Config{})
	defer group.Close()
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := group.Do(ctx, 1); err != nil {
				b.Error(err)
				return
			}
		}
	})
	b.StopTimer()
	group.Close()
	b.ReportMetric(float64(flushes)/float64(b.N), "fsyncs/op")
}

// BenchmarkDo_Uncontended is the price of a Do with nothing to share and a
// flush that costs nothing: one goroutine hand-off per call, the floor below
// which a Group is not worth it.
func BenchmarkDo_Uncontended(b *testing.B) {
	group := commit.New(func(context.Context, []int) error { return nil }, commit.Config{})
	defer group.Close()
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := group.Do(ctx, 1); err != nil {
			b.Fatal(err)
		}
	}
}
