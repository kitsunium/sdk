// Package docstore_test — what a write costs as the store grows, against the
// whole-file rewrite it replaces; what a read and an open cost.
package docstore_test

import (
	"encoding/json"
	"fmt"
	"runtime"
	"testing"

	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	"github.com/kitsunium/sdk/internal/service/docstore"
	svcvfs "github.com/kitsunium/sdk/internal/service/vfs"
)

// benchSizes are the store sizes every write benchmark runs at: a hundred
// documents, ten thousand, a hundred thousand.
var benchSizes = []int{100, 10_000, 100_000}

// benchSink keeps results alive across the loop.
var benchSink any

// benchDoc is a document of a realistic size for a service's own data.
func benchDoc(i int) account {
	return account{
		ID:    fmt.Sprintf("acc_%07d", i),
		Email: fmt.Sprintf("user%07d@example.com", i),
		Name:  "A reasonably ordinary display name",
		Teams: []string{"red", "blue"},
	}
}

// filledStore opens an accounts store over fsys (in memory when nil) holding
// n documents, and folds it so every run starts at rest.
func filledStore(b *testing.B, fsys corevfs.FullFS, n int) *docstore.Store[account] {
	b.Helper()
	store, err := openWith(accountConfig(fsys))
	if err != nil {
		b.Fatalf("Open() = %v", err)
	}
	for i := range n {
		if err := store.Put(benchDoc(i)); err != nil {
			b.Fatalf("Put() = %v", err)
		}
	}
	if err := store.Fold(); err != nil {
		b.Fatalf("Fold() = %v", err)
	}
	return store
}

// BenchmarkPut measures one durable write — an existing document rewritten —
// as the store grows, over the in-memory filesystem so the device is out of
// the number and only the work the store does per write is left: the entry's
// encoding and publication, and the folds that write triggers, amortised. A
// flat row across sizes is the claim.
func BenchmarkPut(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("docs=%d", n), func(b *testing.B) {
			store := filledStore(b, svcvfs.NewMem(), n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				doc := benchDoc(i % n)
				doc.Name = "rewritten"
				if err := store.Put(doc); err != nil {
					b.Fatalf("Put() = %v", err)
				}
			}
		})
	}
}

// BenchmarkRewriteEverything measures what the whole-file store this package
// replaces did on every write: encode every document into one indented
// object and publish it. It is the baseline BenchmarkPut is read against.
func BenchmarkRewriteEverything(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("docs=%d", n), func(b *testing.B) {
			fsys := svcvfs.NewMem()
			docs := make(map[string]json.RawMessage, n)
			for i := range n {
				raw, err := json.Marshal(benchDoc(i))
				if err != nil {
					b.Fatalf("Marshal() = %v", err)
				}
				docs[benchDoc(i).ID] = raw
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				raw, err := json.MarshalIndent(docs, "", "  ")
				if err != nil {
					b.Fatalf("MarshalIndent() = %v", err)
				}
				if err := fsys.WriteAtomic("accounts.json", raw, 0o600); err != nil {
					b.Fatalf("WriteAtomic() = %v", err)
				}
			}
		})
	}
}

// BenchmarkPutMemory measures the same write in a store with no filesystem:
// the encoding, the index maintenance and the locks, and nothing else.
func BenchmarkPutMemory(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("docs=%d", n), func(b *testing.B) {
			store := filledStore(b, nil, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				doc := benchDoc(i % n)
				doc.Name = "rewritten"
				if err := store.Put(doc); err != nil {
					b.Fatalf("Put() = %v", err)
				}
			}
		})
	}
}

// BenchmarkPutDisk measures a durable write on the operating system's
// filesystem, where the device's two flushes are the cost. Windows has no
// disk filesystem in the SDK (ADR 0056).
func BenchmarkPutDisk(b *testing.B) {
	if runtime.GOOS == "windows" {
		b.Skip("vfs.NewOS refuses windows by design")
	}
	for _, n := range []int{100, 10_000} {
		b.Run(fmt.Sprintf("docs=%d", n), func(b *testing.B) {
			fsys, err := svcvfs.NewOS(b.TempDir())
			if err != nil {
				b.Fatalf("NewOS() = %v", err)
			}
			store := filledStore(b, fsys, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				doc := benchDoc(i % n)
				doc.Name = "rewritten"
				if err := store.Put(doc); err != nil {
					b.Fatalf("Put() = %v", err)
				}
			}
		})
	}
}

// BenchmarkGet measures a read: one map lookup under the readers' lock and a
// decode outside it.
func BenchmarkGet(b *testing.B) {
	store := filledStore(b, nil, 10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		v, err := store.Get(benchDoc(i % 10_000).ID)
		if err != nil {
			b.Fatalf("Get() = %v", err)
		}
		benchSink = v
	}
}

// BenchmarkGet_Parallel measures reads from every P at once: the readers'
// lock is shared, so they do not queue behind one another.
func BenchmarkGet_Parallel(b *testing.B) {
	store := filledStore(b, nil, 10_000)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if _, err := store.Get(benchDoc(i % 10_000).ID); err != nil {
				b.Errorf("Get() = %v", err)
				return
			}
			i++
		}
	})
}

// BenchmarkLookup measures a unique-index read.
func BenchmarkLookup(b *testing.B) {
	store := filledStore(b, nil, 10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		v, err := store.Lookup("email", benchDoc(i%10_000).Email)
		if err != nil {
			b.Fatalf("Lookup() = %v", err)
		}
		benchSink = v
	}
}

// BenchmarkOpen measures opening a store at rest: the snapshot read, every
// document decoded to rebuild the two indexes.
func BenchmarkOpen(b *testing.B) {
	for _, n := range []int{100, 10_000} {
		b.Run(fmt.Sprintf("docs=%d", n), func(b *testing.B) {
			fsys := svcvfs.NewMem()
			if err := filledStore(b, fsys, n).Close(); err != nil {
				b.Fatalf("Close() = %v", err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				store, err := openWith(accountConfig(fsys))
				if err != nil {
					b.Fatalf("Open() = %v", err)
				}
				benchSink = store
			}
		})
	}
}
