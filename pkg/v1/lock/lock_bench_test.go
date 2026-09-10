package lock_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/lock"
)

// sinks so no acquisition or release can be proven unused and elided.
var (
	leaseSink lock.Lease
	fenceSink uint64
	okSink    bool
	errSink   error
)

// benchMemory builds the in-process locker the memory rows share.
func benchMemory(b *testing.B) lock.Locker {
	b.Helper()
	locker, err := lock.NewMemory(lock.MemoryConfig{TTL: time.Minute})
	if err != nil {
		b.Fatalf("NewMemory: %v", err)
	}
	return locker
}

// benchFile builds the flock-backed locker. It makes its own 0700 directory
// rather than handing over b.TempDir(): this container's TMPDIR carries a POSIX
// ACL that leaves directories at 0775, and the file locker REFUSES a
// world-writable non-sticky directory — correctly, since unlinking the lock
// file there would hand the next process a different inode. Skipping instead
// would leave the durable path unmeasured, which is the half that matters.
func benchFile(b *testing.B) lock.Locker {
	b.Helper()
	dir := filepath.Join(b.TempDir(), "locks")
	if err := os.Mkdir(dir, 0o700); err != nil {
		b.Fatalf("Mkdir: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		b.Fatalf("Chmod: %v", err)
	}
	locker, err := lock.NewFileLocker(lock.FileConfig{Dir: dir})
	if err != nil {
		b.Skipf("NewFileLocker unavailable on this platform: %v", err)
	}
	return locker
}

// BenchmarkMemory_AcquireRelease is the uncontended round trip a caller pays
// per critical section: take the named lease, hand it back.
func BenchmarkMemory_AcquireRelease(b *testing.B) {
	locker := benchMemory(b)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		lease, err := locker.Acquire(ctx, "bench")
		if err != nil {
			b.Fatalf("Acquire: %v", err)
		}
		if relErr := lease.Release(ctx); relErr != nil {
			b.Fatalf("Release: %v", relErr)
		}
		leaseSink = lease
	}
}

// BenchmarkFile_AcquireRelease is the same round trip through flock. The ratio
// against the memory row is what a lock that survives the process costs, and it
// is the number that decides whether this locker can sit on a per-request path.
func BenchmarkFile_AcquireRelease(b *testing.B) {
	locker := benchFile(b)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		lease, err := locker.Acquire(ctx, "bench")
		if err != nil {
			b.Fatalf("Acquire: %v", err)
		}
		if relErr := lease.Release(ctx); relErr != nil {
			b.Fatalf("Release: %v", relErr)
		}
		leaseSink = lease
	}
}

// BenchmarkMemory_TryAcquire_Free and _Taken bracket the non-blocking probe:
// the first succeeds, the second must refuse without waiting. A refusal that
// cost more than an acquisition would make contention expensive to detect.
func BenchmarkMemory_TryAcquire_Free(b *testing.B) {
	locker := benchMemory(b)
	ctx := b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		lease, ok, err := locker.TryAcquire(ctx, "bench")
		if err != nil || !ok {
			b.Fatalf("TryAcquire: ok=%v err=%v", ok, err)
		}
		if relErr := lease.Release(ctx); relErr != nil {
			b.Fatalf("Release: %v", relErr)
		}
		leaseSink = lease
	}
}

func BenchmarkMemory_TryAcquire_Taken(b *testing.B) {
	locker := benchMemory(b)
	ctx := b.Context()
	held, err := locker.Acquire(ctx, "bench")
	if err != nil {
		b.Fatalf("Acquire: %v", err)
	}
	defer func() {
		if relErr := held.Release(ctx); relErr != nil {
			b.Errorf("Release: %v", relErr)
		}
	}()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		leaseSink, okSink, errSink = locker.TryAcquire(ctx, "bench")
	}
	if okSink {
		b.Fatal("a held lock was handed out twice")
	}
}

// BenchmarkFile_TryAcquire_Taken is the same refusal through flock, and it is
// the one that matters operationally: a process polling a contended file lock
// pays this on every attempt.
func BenchmarkFile_TryAcquire_Taken(b *testing.B) {
	locker := benchFile(b)
	ctx := b.Context()
	held, err := locker.Acquire(ctx, "bench")
	if err != nil {
		b.Fatalf("Acquire: %v", err)
	}
	defer func() {
		if relErr := held.Release(ctx); relErr != nil {
			b.Errorf("Release: %v", relErr)
		}
	}()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		leaseSink, okSink, errSink = locker.TryAcquire(ctx, "bench")
	}
	if okSink {
		b.Fatal("a held lock was handed out twice")
	}
}

// BenchmarkMemory_Extend prices renewal, which a Keepalive performs on a timer
// for the whole duration of a protected section.
func BenchmarkMemory_Extend(b *testing.B) {
	locker := benchMemory(b)
	ctx := b.Context()
	lease, err := locker.Acquire(ctx, "bench")
	if err != nil {
		b.Fatalf("Acquire: %v", err)
	}
	defer func() {
		if relErr := lease.Release(ctx); relErr != nil {
			b.Errorf("Release: %v", relErr)
		}
	}()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = lease.Extend(ctx)
	}
}

// BenchmarkMemory_Fence pins the accessor: a fencing token is read once per
// protected operation by anything that compares it, so it must be a field read
// and never a computation.
func BenchmarkMemory_Fence(b *testing.B) {
	locker := benchMemory(b)
	ctx := b.Context()
	lease, err := locker.Acquire(ctx, "bench")
	if err != nil {
		b.Fatalf("Acquire: %v", err)
	}
	defer func() {
		if relErr := lease.Release(ctx); relErr != nil {
			b.Errorf("Release: %v", relErr)
		}
	}()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		fenceSink = lease.Fence()
	}
}

// BenchmarkMemory_AcquireReleaseParallel is the contended in-process case: N
// goroutines competing for one name, which is what a bulkheaded worker pool
// actually does to a lock.
func BenchmarkMemory_AcquireReleaseParallel(b *testing.B) {
	locker := benchMemory(b)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := b.Context()
		for pb.Next() {
			lease, err := locker.Acquire(ctx, "bench")
			if err != nil {
				b.Errorf("Acquire: %v", err)
				return
			}
			if relErr := lease.Release(ctx); relErr != nil {
				b.Errorf("Release: %v", relErr)
				return
			}
		}
	})
}
