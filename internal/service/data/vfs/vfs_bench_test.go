package vfs_test

import (
	"bytes"
	"io/fs"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	svcvfs "github.com/kitsunium/sdk/internal/service/data/vfs"
)

// benchName is the file every benchmark reads or replaces.
const benchName string = "payload.bin"

// benchErrSink and benchDataSink keep measured results from being optimised
// away. Every verb here returns something the loop would otherwise discard.
var (
	benchErrSink  error
	benchDataSink []byte
)

// benchPayloads are the three sizes that separate the two costs in this domain:
// the per-call overhead (guards, temporary name, syscalls) and the per-byte
// one. Publication's fixed cost is two flushes to the device, so the small
// payload is where that fixed cost is visible undiluted.
var benchPayloads = []int{256, 4 << 10, 64 << 10}

// newBenchMem builds a memory filesystem seeded with one file of size bytes.
func newBenchMem(b *testing.B, size int) corevfs.FullFS {
	b.Helper()
	filesystem := svcvfs.NewMem()
	if err := filesystem.WriteFile(benchName, bytes.Repeat([]byte{'x'}, size), 0o644); err != nil {
		b.Fatalf("seeding: %v", err)
	}
	//: ready to read or overwrite.
	return filesystem
}

// newBenchOS builds a disk filesystem over a fresh temporary directory, seeded
// the same way, and skips where NewOS refuses to exist at all.
func newBenchOS(b *testing.B, size int) corevfs.FullFS {
	b.Helper()
	if !nativePlatforms[runtime.GOOS] {
		b.Skipf("%s has no NewOS to benchmark", runtime.GOOS)
	}
	filesystem, newErr := svcvfs.NewOS(b.TempDir())
	if newErr != nil {
		b.Fatalf("NewOS: %v", newErr)
	}
	if err := filesystem.WriteFile(benchName, bytes.Repeat([]byte{'x'}, size), 0o644); err != nil {
		b.Fatalf("seeding: %v", err)
	}
	//: ready to read or overwrite.
	return filesystem
}

// benchEachSize runs one verb across every payload size against both
// filesystems, so the memory and disk columns of BENCH.md are produced by
// literally the same loop and stay comparable.
func benchEachSize(b *testing.B, run func(b *testing.B, filesystem corevfs.FullFS, payload []byte)) {
	b.Helper()
	builders := []struct {
		name  string
		build func(b *testing.B, size int) corevfs.FullFS
	}{
		{"mem", newBenchMem},
		{"os", newBenchOS},
	}
	for _, builder := range builders {
		for _, size := range benchPayloads {
			b.Run(builder.name+"/"+strconv.Itoa(size), func(b *testing.B) {
				filesystem := builder.build(b, size)
				payload := bytes.Repeat([]byte{'y'}, size)
				b.SetBytes(int64(size))
				b.ReportAllocs()
				run(b, filesystem, payload)
			})
		}
	}
}

// BenchmarkReadFile measures the read path through the fs.ReadFileFS shortcut,
// which is what fs.ReadFile takes when it is available.
func BenchmarkReadFile(b *testing.B) {
	benchEachSize(b, func(b *testing.B, filesystem corevfs.FullFS, _ []byte) {
		var data []byte
		for b.Loop() {
			data, benchErrSink = fs.ReadFile(filesystem, benchName)
		}
		benchDataSink = data
	})
}

// BenchmarkWriteFile measures the in-place write: one truncating open and one
// write, with no temporary and no flush. It is the baseline BenchmarkWriteAtomic
// is compared against, and the verb TestOnlyTheAtomicWriterSurvivesAConcurrentReader
// shows a concurrent reader can catch halfway through.
func BenchmarkWriteFile(b *testing.B) {
	benchEachSize(b, func(b *testing.B, filesystem corevfs.FullFS, payload []byte) {
		var err error
		for b.Loop() {
			err = filesystem.WriteFile(benchName, payload, 0o644)
		}
		benchErrSink = err
	})
}

// BenchmarkWriteAtomic measures publication: a temporary created beside the
// target, the payload written, the FILE flushed, the rename, and the parent
// DIRECTORY flushed.
//
// On the disk filesystem the two flushes are the number: they are device round
// trips, they do not shrink with the payload, and they are what the price of
// atomicity actually is. On the memory filesystem there is no device, so the
// same call is one map assignment under the write lock — which is why the two
// columns differ by orders of magnitude rather than by a constant.
func BenchmarkWriteAtomic(b *testing.B) {
	benchEachSize(b, func(b *testing.B, filesystem corevfs.FullFS, payload []byte) {
		var err error
		for b.Loop() {
			err = filesystem.WriteAtomic(benchName, payload, 0o644)
		}
		benchErrSink = err
	})
}

// BenchmarkWriteAtomic_Parallel measures publication from many goroutines
// into ONE directory on the disk engine, each replacing its own file: the
// shape whose directory flush is shared (#803, ADR 0207). Without the sharing
// every publication pays its own flush of the directory; with it, those that
// reach step 5 while a flush runs share the next one.
func BenchmarkWriteAtomic_Parallel(b *testing.B) {
	filesystem := newBenchOS(b, 256)
	payload := bytes.Repeat([]byte{'y'}, 256)
	var next atomic.Int64
	b.SetParallelism(4)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		name := "parallel-" + strconv.FormatInt(next.Add(1), 10) + ".bin"
		for pb.Next() {
			//: a failed publication fails the benchmark, whichever goroutine.
			if err := filesystem.WriteAtomic(name, payload, 0o644); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkWriteAtomicAll measures a batch of n 256-byte files published in
// one call into one directory (issue #827): n file flushes and ONE directory
// flush, against WriteAtomic's two flushes per file. ns/file is the figure to
// set beside BenchmarkWriteAtomic's ns/op.
func BenchmarkWriteAtomicAll(b *testing.B) {
	builders := []struct {
		name  string
		build func(b *testing.B, size int) corevfs.FullFS
	}{
		{"mem", newBenchMem},
		{"os", newBenchOS},
	}
	for _, builder := range builders {
		for _, n := range []int{1, 16} {
			b.Run(builder.name+"/"+strconv.Itoa(n), func(b *testing.B) {
				batcher, ok := builder.build(b, 256).(corevfs.AtomicBatchWriter)
				if !ok {
					b.Fatal("does not implement vfs.AtomicBatchWriter")
				}
				files := make([]corevfs.FileValue, n)
				for index := range files {
					files[index] = corevfs.FileValue{Name: "batch-" + strconv.Itoa(index) + ".bin", Data: bytes.Repeat([]byte{'y'}, 256)}
				}
				b.ReportAllocs()
				var err error
				for b.Loop() {
					_, err = batcher.WriteAtomicAll(files, 0o644)
				}
				benchErrSink = err
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/file")
			})
		}
	}
}

// BenchmarkStat measures metadata resolution on its own, with no bytes moving,
// so the guard-plus-syscall overhead every verb pays is visible undiluted.
func BenchmarkStat(b *testing.B) {
	builders := []struct {
		name  string
		build func(b *testing.B, size int) corevfs.FullFS
	}{
		{"mem", newBenchMem},
		{"os", newBenchOS},
	}
	for _, builder := range builders {
		b.Run(builder.name, func(b *testing.B) {
			filesystem := builder.build(b, 256)
			b.ReportAllocs()
			var info fs.FileInfo
			for b.Loop() {
				info, benchErrSink = fs.Stat(filesystem, benchName)
			}
			//: consume the result so the call cannot be elided.
			if info == nil && benchErrSink == nil {
				b.Fatal("Stat returned neither info nor error")
			}
		})
	}
}
