// Package docstore_test — what a write costs as the store grows, against the
// whole-file rewrite it replaces; what a read and an open cost; what a page
// of Documents costs (#836); what a batch of writes costs against the same
// writes one by one (#1057).
package docstore_test

import (
	"encoding/json"
	"fmt"
	"runtime"
	"testing"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
	svcvfs "github.com/kitsunium/sdk/internal/service/data/vfs"
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

// BenchmarkPutVersioned measures one durable write on a store keeping each
// document's former versions — every document already holding as many as the
// store keeps — over the in-memory filesystem. The write's entry carries the
// document AND its versions (ADR 0143), so the cost is what those versions
// weigh, and the store's size stays out of it as it does for BenchmarkPut.
func BenchmarkPutVersioned(b *testing.B) {
	const docs int = 100
	for _, versions := range []int{0, 1, 10, 100} {
		b.Run(fmt.Sprintf("versions=%d", versions), func(b *testing.B) {
			cfg := accountConfig(svcvfs.NewMem())
			cfg.Versions = versions
			store, err := openWith(cfg)
			if err != nil {
				b.Fatalf("Open() = %v", err)
			}
			for round := range versions + 1 {
				for i := range docs {
					doc := benchDoc(i)
					doc.Name = fmt.Sprintf("round %d", round)
					if err := store.Put(doc); err != nil {
						b.Fatalf("Put() = %v", err)
					}
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; b.Loop(); i++ {
				doc := benchDoc(i % docs)
				doc.Name = fmt.Sprintf("rewritten %d", i)
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
	if diskRefused() {
		b.Skipf("vfs.NewOS refuses %s by design", runtime.GOOS)
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
	keys := benchKeys(10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		v, err := store.Get(keys[i%len(keys)])
		if err != nil {
			b.Fatalf("Get() = %v", err)
		}
		benchSink = v
	}
}

// BenchmarkGetInto measures the same reads as BenchmarkGet into one value the
// loop keeps (#813): the fresh T Get returns is the difference.
func BenchmarkGetInto(b *testing.B) {
	store := filledStore(b, nil, 10_000)
	keys := benchKeys(10_000)
	var dst account
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		if err := store.GetInto(keys[i%len(keys)], &dst); err != nil {
			b.Fatalf("GetInto() = %v", err)
		}
	}
	benchSink = dst
}

// benchKeys are the keys of benchDoc(0) to benchDoc(n-1), formatted once, so
// a read benchmark counts the store's allocations and not fmt.Sprintf's.
func benchKeys(n int) []string {
	keys := make([]string, n)
	//: in the order benchDoc numbers them.
	for i := range keys {
		keys[i] = benchDoc(i).ID
	}
	return keys
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

// BenchmarkDocumentsPage measures one page of 100 documents read by
// Documents, resuming after a key in the store's first half, as the store
// grows (#836): a page costs its own documents and a pass over the keys —
// its allocations are the page's, the same at every size.
func BenchmarkDocumentsPage(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("docs=%d", n), func(b *testing.B) {
			store := filledStore(b, nil, n)
			after := benchDoc(n / 3).ID
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				for entry, err := range store.Documents(after, 100) {
					if err != nil {
						b.Fatalf("Documents() = %v", err)
					}
					benchSink = entry
				}
			}
		})
	}
}

// BenchmarkDocumentsPass measures a pass over 10 000 documents paged by
// Documents(after, 100) from the last key read, every document once,
// against List, which copies them all at once (#836).
func BenchmarkDocumentsPass(b *testing.B) {
	store := filledStore(b, nil, 10_000)
	b.Run("paged=100", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for after, more := "", true; more; {
				read := 0
				for entry, err := range store.Documents(after, 100) {
					if err != nil {
						b.Fatalf("Documents() = %v", err)
					}
					after, read = entry.Key, read+1
				}
				more = read == 100
			}
		}
	})
	b.Run("entries", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			all, err := store.Entries(0)
			if err != nil {
				b.Fatalf("Entries() = %v", err)
			}
			benchSink = all
		}
	})
}

// BenchmarkBatch measures 100 writes of existing documents as one Batch
// against the same 100 as Puts, over the in-memory filesystem and on the
// operating system's (#1057): on disk a batch pays its group entry, then
// each key's entry, then the group entry's removal — atomicity, not speed,
// as ADR 0209's amendment says.
func BenchmarkBatch(b *testing.B) {
	for _, where := range []string{"mem", "disk"} {
		b.Run("fs="+where, func(b *testing.B) {
			fsys := corevfs.FullFS(svcvfs.NewMem())
			if where == "disk" {
				if diskRefused() {
					b.Skipf("vfs.NewOS refuses %s by design", runtime.GOOS)
				}
				var err error
				if fsys, err = svcvfs.NewOS(b.TempDir()); err != nil {
					b.Fatalf("NewOS() = %v", err)
				}
			}
			store := filledStore(b, fsys, 1_000)
			b.Run("batch=100", func(b *testing.B) {
				b.ReportAllocs()
				for round := 0; b.Loop(); round++ {
					err := store.Batch(func(w coredocstore.BatchWriter[account]) error {
						for i := range 100 {
							doc := benchDoc(i)
							doc.Name = fmt.Sprintf("batch %d", round)
							if err := w.Put(doc); err != nil {
								return err
							}
						}
						return nil
					})
					if err != nil {
						b.Fatalf("Batch() = %v", err)
					}
				}
			})
			b.Run("puts=100", func(b *testing.B) {
				b.ReportAllocs()
				for round := 0; b.Loop(); round++ {
					for i := range 100 {
						doc := benchDoc(i)
						doc.Name = fmt.Sprintf("puts %d", round)
						if err := store.Put(doc); err != nil {
							b.Fatalf("Put() = %v", err)
						}
					}
				}
			})
		})
	}
}
