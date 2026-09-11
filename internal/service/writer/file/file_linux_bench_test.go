// Package file_test — what the other default writer costs, and how much of that
// is this package rather than the filesystem underneath it.
//
// The trap this file is built around is that a file benchmark measures the
// FILESYSTEM by default. Every row therefore names the filesystem it ran on,
// detected by statfs rather than assumed from the path:
//
//	tmpfs   /dev/shm — RAM. A write is a memcpy into the page cache and an
//	        fsync has nothing to push, so this is where the PACKAGE's cost is
//	        visible without the device drowning it.
//	ext4    a real block device, named by SDK_BENCH_DISK_DIR. This is where
//	        durability has a price, and it is the only row that can state one.
//	tmpdir  whatever b.TempDir() resolves to, labelled with its real type. It
//	        is here because on this machine that is /tmp and /tmp is tmpfs —
//	        so this package's whole test suite, including every assertion about
//	        Flush, has never touched a durable device.
//
// A row that carries no filesystem label is not measuring a file.
//
// The file name carries the `_linux` suffix, and it is load-bearing rather than
// decorative: `syscall.Statfs` and its magic numbers are Linux, and so is the
// `O_NOFOLLOW` the sink opens with on this platform. e2e-cross.yml CROSS-COMPILES
// test binaries for every supported GOOS, so a file that assumes Linux has to
// say so in its name or it breaks the Windows build. The writer itself is
// cross-platform; this measurement of it is not.
package file_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/logger/level"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	servicelogger "github.com/kitsunium/sdk/internal/service/logger"
	"github.com/kitsunium/sdk/internal/service/logger/encoder"
	_ "github.com/kitsunium/sdk/internal/service/writer/file"
)

// diskDirEnv names the environment variable pointing at a directory on a real
// block device. It is not guessed: /tmp is tmpfs on the machine this report was
// produced on, and a benchmark that silently measured RAM while claiming to
// measure durability would be worse than one that skips.
const diskDirEnv string = "SDK_BENCH_DISK_DIR"

// openFlagsForRaw mirrors the flags service/logger/sink/file opens with, so the
// raw-syscall control is the same descriptor the sink holds and the difference
// between the two rows is this SDK's code and nothing else.
const openFlagsForRaw int = os.O_APPEND | os.O_CREATE | os.O_WRONLY | syscall.O_NOFOLLOW

// The statfs magic numbers this report can encounter. Only these are named;
// anything else is reported as unrecognised rather than silently mislabelled.
const (
	// magicTmpfs is TMPFS_MAGIC — the page cache, with no device behind it.
	magicTmpfs int64 = 0x01021994
	// magicExt is EXT2/3/4's shared magic; this machine's /home is ext4.
	magicExt int64 = 0xEF53
	// magicOverlay is OVERLAYFS_SUPER_MAGIC, which a container's writable
	// layer is.
	magicOverlay int64 = 0x794C7630
	// magicXFS is XFS_SUPER_MAGIC, common on cloud instances.
	magicXFS int64 = 0x58465342
)

// benchLine is the payload every row carries unless it says otherwise: the text
// encoder's output for a short message with three attributes. Its length is
// what SetBytes reports; no row restates it.
var benchLine = []byte(
	`2026-09-10T20:15:11.482Z INFO msg="request served" method=GET status=200 dur=1.4ms` + "\n",
)

// benchN keeps the byte counts reachable from outside the loops so no call can
// be eliminated.
var benchN int

// noopSink terminates the end-to-end rows so the handler and the encoder can be
// measured without a transport under them.
type noopSink struct{}

func (noopSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	//: accept the whole payload with no work.
	return len(p), nil
}
func (noopSink) Flush(_ context.Context) error { return nil }
func (noopSink) Close() error                  { return nil }

// fsName maps a statfs magic number onto the filesystem name a row is labelled
// with. Only the types this report can encounter are named; anything else is
// reported as its hex magic so an unexpected row is visibly unexpected rather
// than silently mislabelled.
func fsName(b *testing.B, dir string) string {
	b.Helper()
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		b.Logf("statfs %s: %v", dir, err)
		//: an unreadable filesystem type is stated, never guessed.
		return "unknown"
	}
	//: Statfs_t.Type is int64 on 64-bit Linux and int32 on 386, so the
	//: comparison is made at one width rather than at the platform's.
	switch int64(st.Type) {
	//: the page cache, no device behind it.
	case magicTmpfs:
		return "tmpfs"
	//: this machine's /home.
	case magicExt:
		return "ext4"
	//: a container's writable layer.
	case magicOverlay:
		return "overlay"
	//: common on cloud instances.
	case magicXFS:
		return "xfs"
	//: anything else is visibly unrecognised, and the magic goes to the log
	//: rather than into a row name nobody can compare across machines.
	default:
		b.Logf("statfs %s: unrecognised magic %#x", dir, int64(st.Type))
		return "unrecognised"
	}
}

// benchRoot is one directory a set of rows runs in, already labelled with the
// filesystem it lives on.
type benchRoot struct {
	// label is the row-name prefix, e.g. "tmpfs" or "ext4".
	label string
	// dir is the directory the row's files are created in.
	dir string
}

// benchRoots collects every directory this machine can offer, labelled by its
// real filesystem. /dev/shm is included when it exists and really is tmpfs;
// the block-device root is included only when SDK_BENCH_DISK_DIR names one,
// because inventing a path would be exactly the guess this file exists to
// refuse.
func benchRoots(b *testing.B) []benchRoot {
	b.Helper()
	roots := make([]benchRoot, 0, 3)
	//: b.TempDir() first: whatever it resolves to is what the package's own
	//: tests use, and its label is the point.
	tmp := b.TempDir()
	roots = append(roots, benchRoot{label: "tmpdir(" + fsName(b, tmp) + ")", dir: tmp})
	//: /dev/shm is RAM by construction, and is where the package's own cost
	//: is visible without a device under it.
	if shm, ok := benchSubdir(b, "/dev/shm"); ok {
		roots = append(roots, benchRoot{label: fsName(b, shm), dir: shm})
	}
	//: the only root that can price durability.
	if disk, ok := benchSubdir(b, os.Getenv(diskDirEnv)); ok {
		roots = append(roots, benchRoot{label: fsName(b, disk), dir: disk})
	}
	//: every root this machine could actually offer.
	return roots
}

// benchSubdir creates a private 0700 directory under parent and registers its
// removal. The second result is false when parent is empty or unusable, so a
// machine without the root simply produces fewer rows. The mode is not
// inherited: a group- or world-writable parent would otherwise hand the
// benchmark a directory the file sink's own hardening rules would refuse.
func benchSubdir(b *testing.B, parent string) (dir string, ok bool) {
	b.Helper()
	if parent == "" {
		//: the root was not offered on this machine.
		return "", false
	}
	dir, err := os.MkdirTemp(parent, "sdk-writer-bench-")
	if err != nil {
		b.Logf("skipping %s: %v", parent, err)
		//: unusable root — fewer rows, never a wrong row.
		return "", false
	}
	if cerr := os.Chmod(dir, 0o700); cerr != nil {
		b.Logf("skipping %s: chmod: %v", parent, cerr)
		//: an un-narrowable directory is not used at all.
		return "", false
	}
	b.Cleanup(func() {
		if rerr := os.RemoveAll(dir); rerr != nil {
			b.Logf("removing %s: %v", dir, rerr)
		}
	})
	//: a private directory on the requested filesystem.
	return dir, true
}

// factorySink builds the sink a consumer actually gets — writer.Open("file",
// cfg) — over a fresh file in dir. The rows measure the shipped composition
// (levelgate over the hardened append-only sink) rather than a reassembly of it
// that could drift from the factory.
func factorySink(b *testing.B, dir, name string, cfg writer.FileConfig) corelogger.Sink {
	b.Helper()
	cfg.Path = filepath.Join(dir, name)
	s, err := writer.Open("file", cfg)
	if err != nil {
		b.Fatalf("writer.Open(file, %s): %v", cfg.Path, err)
	}
	b.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			b.Logf("closing %s: %v", cfg.Path, cerr)
		}
	})
	//: the real product of the real factory.
	return s
}

// factorySinkAt is factorySink for a path that is not inside a benchmark
// directory — /dev/null, which is a character device and therefore neither a
// filesystem row nor a durability row. It is the syscall FLOOR: the kernel
// accepts the bytes and drops them, so what is left of the measurement is this
// SDK's code, which the ~1 000 ns filesystem rows cannot resolve.
func factorySinkAt(b *testing.B, path string) corelogger.Sink {
	b.Helper()
	s, err := writer.Open("file", writer.FileConfig{Path: path})
	if err != nil {
		b.Fatalf("writer.Open(file, %s): %v", path, err)
	}
	b.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			b.Logf("closing %s: %v", path, cerr)
		}
	})
	//: the real product of the real factory, over a destination that costs
	//: the syscall and nothing more.
	return s
}

// rawFileAt is rawFile for an absolute path outside a benchmark directory.
func rawFileAt(b *testing.B, path string) *os.File {
	b.Helper()
	f, err := os.OpenFile(path, openFlagsForRaw, 0o600)
	if err != nil {
		b.Fatalf("opening %s: %v", path, err)
	}
	b.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			b.Logf("closing %s: %v", path, cerr)
		}
	})
	//: the same descriptor the sink holds, with nothing wrapped around it.
	return f
}

// rawFile opens a descriptor with the sink's own flags and mode, so the control
// row differs from the sink row by this SDK's code and nothing else.
func rawFile(b *testing.B, dir, name string) *os.File {
	b.Helper()
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, openFlagsForRaw, 0o600)
	if err != nil {
		b.Fatalf("opening %s: %v", path, err)
	}
	b.Cleanup(func() {
		if cerr := f.Close(); cerr != nil {
			b.Logf("closing %s: %v", path, cerr)
		}
	})
	//: the same descriptor the sink holds, with nothing wrapped around it.
	return f
}

// writeRow builds the measured closure OUTSIDE the loop that varies, so no
// benchmark parameter is captured by a closure declared in a range body.
func writeRow(ctx context.Context, sink corelogger.Sink, rec corelogger.RecordEvent, payload []byte) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		b.SetBytes(int64(len(payload)))
		b.ReportAllocs()
		b.ResetTimer()
		total := 0
		for range b.N {
			n, err := sink.Write(ctx, rec, payload)
			if err != nil {
				b.Fatalf("write: %v", err)
			}
			total += n
		}
		benchN = total
	}
}

// rawControl holds the bare descriptor every sink row is measured against.
//
// It holds *os.File CONCRETELY, and that is the whole point rather than an
// oversight: taking an io.Writer here would put an interface call in the
// control arm, and an interface call is 2.9 ns on this machine
// (../console/BENCH.md §2) against a sink-minus-raw difference of 17.8 ns. The
// control would then be measuring the thing it exists to isolate, and the
// difference would shrink by a sixth for a reason no row would show.
type rawControl struct {
	// f is the descriptor, opened with the sink's own flags and mode.
	f *os.File
}

// row is writeRow over the bare descriptor.
func (r rawControl) row(payload []byte) func(*testing.B) {
	//: the returned closure captures the receiver, not a loop variable.
	return func(b *testing.B) {
		b.SetBytes(int64(len(payload)))
		b.ReportAllocs()
		b.ResetTimer()
		total := 0
		for range b.N {
			n, err := r.f.Write(payload)
			if err != nil {
				b.Fatalf("write: %v", err)
			}
			total += n
		}
		benchN = total
	}
}

// parallelRow is row driven from GOMAXPROCS goroutines, behind a mutex of its
// own so the pair differs by this SDK's code and not by the presence of mutual
// exclusion.
func (r rawControl) parallelRow() func(*testing.B) {
	//: the returned closure captures the receiver, not a loop variable.
	return func(b *testing.B) {
		var rawMu sync.Mutex
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			total := 0
			for pb.Next() {
				rawMu.Lock()
				n, err := r.f.Write(benchLine)
				rawMu.Unlock()
				if err != nil {
					b.Fatalf("write: %v", err)
				}
				total += n
			}
			benchN = total
		})
	}
}

// flushRow builds the fsync closure outside the loop that varies.
func flushRow(ctx context.Context, sink corelogger.Sink) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if err := sink.Flush(ctx); err != nil {
				b.Fatalf("flush: %v", err)
			}
		}
	}
}

// cadenceRow writes every record and flushes every `every` of them.
func cadenceRow(ctx context.Context, sink corelogger.Sink, rec corelogger.RecordEvent, every int) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		b.SetBytes(int64(len(benchLine)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := range b.N {
			if _, err := sink.Write(ctx, rec, benchLine); err != nil {
				b.Fatalf("write: %v", err)
			}
			if (i+1)%every == 0 {
				if err := sink.Flush(ctx); err != nil {
					b.Fatalf("flush: %v", err)
				}
			}
		}
	}
}

// parallelSinkRow drives one sink from GOMAXPROCS goroutines.
func parallelSinkRow(ctx context.Context, sink corelogger.Sink, rec corelogger.RecordEvent) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			total := 0
			for pb.Next() {
				n, err := sink.Write(ctx, rec, benchLine)
				if err != nil {
					b.Fatalf("write: %v", err)
				}
				total += n
			}
			benchN = total
		})
	}
}

// factoryOpenRow times one writer.Open plus its Close.
func factoryOpenRow(path string) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			s, err := writer.Open("file", writer.FileConfig{Path: path})
			if err != nil {
				b.Fatalf("writer.Open(file): %v", err)
			}
			if cerr := s.Close(); cerr != nil {
				b.Fatalf("close: %v", cerr)
			}
		}
	}
}

// rawOpenRow is factoryOpenRow's control: os.OpenFile with identical flags.
func rawOpenRow(path string) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			f, err := os.OpenFile(path, openFlagsForRaw, 0o600)
			if err != nil {
				b.Fatalf("open: %v", err)
			}
			if cerr := f.Close(); cerr != nil {
				b.Fatalf("close: %v", cerr)
			}
		}
	}
}

// emitRow times a full record through the text encoder and the handler.
func emitRow(ctx context.Context, sink corelogger.Sink) func(*testing.B) {
	//: the returned closure captures parameters, not loop variables.
	return func(b *testing.B) {
		h, err := servicelogger.NewHandler(encoder.NewText(clock.System), sink, level.Info)
		if err != nil {
			b.Fatalf("building the handler: %v", err)
		}
		rec := corelogger.RecordEvent{
			Level:   level.Info,
			Message: "request served",
			Attrs: []corelogger.AttrValue{
				{Key: "method", Value: corelogger.StringValue("GET")},
				{Key: "status", Value: corelogger.IntValue(200)},
			},
		}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if herr := h.Handle(ctx, rec); herr != nil {
				b.Fatalf("handle: %v", herr)
			}
		}
	}
}

// BenchmarkWrite is the headline pair, run once per filesystem: the sink
// against a raw *os.File.Write on an identical descriptor. The difference
// between the two rows is everything this SDK adds — the context check, the
// mutex, two interface calls — and it is the only figure here that is a
// property of the code rather than of the device.
func BenchmarkWrite(b *testing.B) {
	ctx := context.Background()
	rec := corelogger.RecordEvent{Level: level.Info}
	pairs := make([]struct {
		// label names the destination.
		label string
		// sink is the shipped writer over it.
		sink corelogger.Sink
		// raw is an identical descriptor with nothing wrapped around it.
		raw *os.File
	}, 0, 4)
	//: the syscall floor first: it is the only pair whose difference this
	//: harness can actually resolve.
	pairs = append(pairs, struct {
		label string
		sink  corelogger.Sink
		raw   *os.File
	}{"devnull(syscall floor)", factorySinkAt(b, os.DevNull), rawFileAt(b, os.DevNull)})
	for _, root := range benchRoots(b) {
		pairs = append(pairs, struct {
			label string
			sink  corelogger.Sink
			raw   *os.File
		}{root.label, factorySink(b, root.dir, "sink.log", writer.FileConfig{}), rawFile(b, root.dir, "raw.log")})
	}
	for _, root := range pairs {
		b.Run(root.label+"/sink", writeRow(ctx, root.sink, rec, benchLine))
		b.Run(root.label+"/raw_syscall", rawControl{f: root.raw}.row(benchLine))
	}
}

// BenchmarkFlush prices the sync policy on its own: Flush is fsync(2), and the
// row exists because an fsync is not a faster write, it is a different order of
// magnitude. On tmpfs there is no device to push to and the row is the syscall
// alone; on a block device it is the number a consumer choosing a flush cadence
// needs.
func BenchmarkFlush(b *testing.B) {
	ctx := context.Background()
	for _, root := range benchRoots(b) {
		b.Run(root.label, flushRow(ctx, factorySink(b, root.dir, "flush.log", writer.FileConfig{})))
	}
}

// BenchmarkWriteThenFlush is the cadence question a consumer actually asks: how
// much does durability cost per RECORD, and how much of it is amortised away by
// syncing every N records instead of every one. The per-record row is what
// "flush after each log line" costs; the batched rows are what the same
// guarantee costs when the caller accepts losing up to N-1 lines.
func BenchmarkWriteThenFlush(b *testing.B) {
	ctx := context.Background()
	rec := corelogger.RecordEvent{Level: level.Info}
	cadences := []int{1, 16, 256}
	for _, root := range benchRoots(b) {
		for _, every := range cadences {
			sink := factorySink(b, root.dir, "cadence"+strconv.Itoa(every)+".log", writer.FileConfig{})
			b.Run(root.label+"/every_"+strconv.Itoa(every), cadenceRow(ctx, sink, rec, every))
		}
	}
}

// BenchmarkWriteSize asks whether what this SDK adds scales with the PAYLOAD or
// only with the call, and it asks it at the /dev/null syscall floor rather than
// on a filesystem. That is not a shortcut, it is the only way the question is
// answerable here: the same sweep run on tmpfs and on ext4 produced spreads of
// 35 % to 111 % across five runs at an iteration budget this VM can afford
// (10 000), because at 4 KiB a row is dominated by page allocation and
// writeback and not by the code. Those rows were discarded; BENCH.md §6 records
// them and their spreads.
//
// At the floor the bytes cost the kernel nothing, so a constant sink-minus-raw
// difference across an 42× size range is the statement that this package's cost
// is per CALL. The per-BYTE cost of a real destination is the destination's, and
// internal/service/writer/console/BENCH.md §3 prices one that genuinely copies.
func BenchmarkWriteSize(b *testing.B) {
	ctx := context.Background()
	rec := corelogger.RecordEvent{Level: level.Info}
	sizes := []int{96, 1 << 10, 4 << 10}
	sink := factorySinkAt(b, os.DevNull)
	raw := rawFileAt(b, os.DevNull)
	for _, size := range sizes {
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = 'x'
		}
		payload[size-1] = '\n'
		b.Run(strconv.Itoa(size)+"B/sink", writeRow(ctx, sink, rec, payload))
		b.Run(strconv.Itoa(size)+"B/raw_syscall", rawControl{f: raw}.row(payload))
	}
}

// BenchmarkWriteParallel drives one sink from GOMAXPROCS goroutines. Two locks
// are in the way and only one of them is this SDK's: the sink's own mutex, and
// the *os.File's internal per-descriptor write lock. The row says what the pair
// costs; the raw control says how much of it would be there anyway.
func BenchmarkWriteParallel(b *testing.B) {
	ctx := context.Background()
	rec := corelogger.RecordEvent{Level: level.Info}
	for _, root := range benchRoots(b) {
		b.Run(root.label+"/sink", parallelSinkRow(ctx, factorySink(b, root.dir, "par-sink.log", writer.FileConfig{}), rec))
		b.Run(root.label+"/raw_syscall", rawControl{f: rawFile(b, root.dir, "par-raw.log")}.parallelRow())
	}
}

// BenchmarkOpen prices construction, which for this writer is not free: it is
// an Lstat for the symlink refusal plus an open(2). The raw control is the
// open(2) alone, so the row states what the CWE-59 hardening costs at the one
// moment it runs.
//
// Both arms include the Close, on purpose. A first version stopped the timer
// around it, and b.StopTimer/b.StartTimer cost more than the open they were
// excluding — every row read ~8.6 µs for what is a low-single-digit-microsecond
// syscall pair. Timing the whole open+close costs nothing and inflates nothing.
func BenchmarkOpen(b *testing.B) {
	for _, root := range benchRoots(b) {
		path := filepath.Join(root.dir, "open.log")
		b.Run(root.label+"/factory_open_close", factoryOpenRow(path))
		b.Run(root.label+"/raw_open_close", rawOpenRow(path))
	}
}

// BenchmarkEmit is the END-TO-END row: a full record through the text encoder
// and the generic handler, landing in this writer. The noop row is the same
// emit with the transport removed, so the difference is the writer's share of
// what a consumer pays per line — the number the "one alloc per emit" claim in
// the root CLAUDE.md has never been stated against.
func BenchmarkEmit(b *testing.B) {
	ctx := context.Background()
	roots := benchRoots(b)
	rows := make([]struct {
		// name labels the transport under the handler.
		name string
		// sink is the transport.
		sink corelogger.Sink
	}, 0, len(roots)+1)
	//: encoder + handler with no transport at all.
	rows = append(rows, struct {
		name string
		sink corelogger.Sink
	}{"noop_sink", noopSink{}})
	for _, root := range roots {
		rows = append(rows, struct {
			name string
			sink corelogger.Sink
		}{"file_" + root.label, factorySink(b, root.dir, "emit.log", writer.FileConfig{})})
	}
	for _, row := range rows {
		b.Run(row.name, emitRow(ctx, row.sink))
	}
}
