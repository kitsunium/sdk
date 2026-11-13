// Package docstore_test — the change feed (kitsunium/sdk#831): every write a
// store keeps appends one record to the stream its configuration names. The
// file engine appends before it publishes, and its feed replayed over an
// empty store rebuilds the documents byte for byte; the SQL engine appends
// in the write's own transaction, so a rolled-back write leaves no record
// and a committed one exactly one, which a consumer group reads.
package docstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/docstore"
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
)

// errAppend is the failure a refusing stream answers.
var errAppend = errors.New("the stream refused the batch")

// changeStream opens a memory stream of four partitions, so a feed spreads
// its keys over several.
func changeStream(t *testing.T) corestream.Stream {
	t.Helper()
	s, err := svcstream.NewMemory(svcstream.MemoryConfig{Policy: corestream.PolicyValue{Partitions: 4}})
	if err != nil {
		t.Fatalf("NewMemory() = %v", err)
	}
	return s
}

// readChanges reads every record of s, partition by partition in offset
// order, as the changes they hold, each checked to be keyed by its document.
func readChanges(t *testing.T, s corestream.Stream) []coredocstore.ChangeValue {
	t.Helper()
	var changes []coredocstore.ChangeValue
	for p := range s.Partitions() {
		bounds, err := s.Bounds(t.Context(), p)
		if err != nil {
			t.Fatalf("Bounds(%d) = %v", p, err)
		}
		entries, err := s.Read(t.Context(), p, bounds.First, int(bounds.Next-bounds.First)+1)
		if err != nil {
			t.Fatalf("Read(%d) = %v", p, err)
		}
		for _, entry := range entries {
			changes = append(changes, decodeChange(t, entry))
		}
	}
	return changes
}

// decodeChange decodes one entry's change, and fails the test when the
// record is not keyed by the document's key.
func decodeChange(t *testing.T, entry corestream.EntryValue) coredocstore.ChangeValue {
	t.Helper()
	var change coredocstore.ChangeValue
	if err := json.Unmarshal(entry.Record.Payload, &change); err != nil {
		t.Fatalf("a change record is not a ChangeValue: %v", err)
	}
	if string(entry.Record.Key) != change.Key {
		t.Fatalf("record keyed %q holds the change of %q", entry.Record.Key, change.Key)
	}
	return change
}

// opsOf returns, for each key, the operations and versions its changes
// record, in order.
func opsOf(changes []coredocstore.ChangeValue) map[string][]string {
	byKey := map[string][]string{}
	for _, c := range changes {
		byKey[c.Key] = append(byKey[c.Key], string(c.Op)+"@"+strconv.FormatUint(c.Version, 10))
	}
	return byKey
}

// refusingStream is a stream whose Append always fails.
type refusingStream struct{ corestream.Stream }

// Append refuses every batch.
func (refusingStream) Append(context.Context, []corestream.RecordValue) (corestream.AppendValue, error) {
	return corestream.AppendValue{}, errAppend
}

// TestFileChangeFeedRecordsEveryWrite runs every write of the file engine —
// on a filesystem and in memory — and reads one record per write that stood,
// keyed by its document, in the order its writes stood, with the version
// each one left; a batch records each key it wrote, once.
func TestFileChangeFeedRecordsEveryWrite(t *testing.T) {
	t.Parallel()
	for name, fsys := range map[string]func() *docstore.Config[account]{
		"memory": func() *docstore.Config[account] { cfg := accountConfig(nil); return &cfg },
		"file":   func() *docstore.Config[account] { cfg := accountConfig(memFS()); return &cfg },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			feed := changeStream(t)
			cfg := fsys()
			cfg.Versions, cfg.Clock, cfg.Changes = 3, clock.NewManualClock(epoch), feed
			store, err := openWith(*cfg)
			must(t, err)
			t.Cleanup(func() { must(t, store.Close()) })
			if got, ok := store.ChangeStream(); !ok || got != feed {
				t.Fatalf("ChangeStream() = (%v, %v), want the configured stream", got, ok)
			}
			must(t, store.Put(account{ID: "a", Email: "a@x"}))
			must(t, store.Put(account{ID: "b"}))
			_, err = store.Update("a", func(a *account) error { a.Name = "Ada"; return nil })
			must(t, err)
			must(t, store.Delete("b"))
			must(t, store.Put(account{ID: "c"}))
			must(t, store.Trash("c"))
			must(t, store.Restore("c"))
			must(t, store.Batch(func(w coredocstore.BatchWriter[account]) error {
				if putErr := w.Put(account{ID: "d"}); putErr != nil {
					return putErr
				}
				if putErr := w.Put(account{ID: "d", Name: "twice"}); putErr != nil {
					return putErr
				}
				return w.Delete("a")
			}))
			must(t, store.Trash("c"))
			must(t, store.Purge("c"))
			if err := store.Insert(account{ID: "d"}); !errs.HasCode(err, coredocstore.CodeDocumentExists) {
				t.Fatalf("Insert over a taken key = %v, want DOCUMENT_EXISTS", err)
			}
			got := opsOf(readChanges(t, feed))
			want := map[string][]string{
				"a": {"put@1", "put@2", "delete@0"},
				"b": {"put@1", "delete@0"},
				"c": {"put@1", "trash@1", "restore@1", "trash@1", "delete@0"},
				"d": {"put@2"}, // two writes in the batch, one record of the final state
			}
			for key, ops := range want {
				if !slices.Equal(got[key], ops) {
					t.Errorf("changes of %q = %v, want %v", key, got[key], ops)
				}
			}
			if len(got) != len(want) {
				t.Errorf("changes of %d keys, want %d: %v", len(got), len(want), got)
			}
		})
	}
}

// TestFileChangeFeedReplaysIntoTheSameDocuments rebuilds a store from its
// feed alone: every record applied, in offset order, to an empty store gives
// a snapshot equal byte for byte to the one the store closed with.
func TestFileChangeFeedReplaysIntoTheSameDocuments(t *testing.T) {
	t.Parallel()
	feed := changeStream(t)
	sourceFS := memFS()
	cfg := accountConfig(sourceFS)
	cfg.Versions, cfg.Changes, cfg.ChangesWithDocument = 2, feed, true
	source, err := openWith(cfg)
	must(t, err)
	for i := range 40 {
		id := string(rune('a'+i%13)) + string(rune('a'+i%7))
		must(t, source.Put(account{ID: id, Name: id, Teams: []string{"t" + string(rune('0'+i%3))}}))
		switch i % 5 {
		case 1:
			must(t, source.Delete(id))
		case 2:
			_, err = source.Update(id, func(a *account) error { a.Name = "renamed <&>"; return nil })
			must(t, err)
		case 3:
			must(t, source.Trash(id))
		case 4:
			must(t, source.Trash(id))
			must(t, source.Restore(id))
		}
	}
	must(t, source.Batch(func(w coredocstore.BatchWriter[account]) error {
		if putErr := w.Put(account{ID: "zz", Name: "batched"}); putErr != nil {
			return putErr
		}
		return w.Put(account{ID: "zy"})
	}))
	must(t, source.Close())

	replicaFS := memFS()
	replica, err := openWith(accountConfig(replicaFS))
	must(t, err)
	for _, change := range readChanges(t, feed) {
		switch change.Op {
		case coredocstore.ChangePut, coredocstore.ChangeRestore:
			var doc account
			if decodeErr := json.Unmarshal(change.Document, &doc); decodeErr != nil {
				t.Fatalf("a %s record carries no document: %v", change.Op, decodeErr)
			}
			must(t, replica.Put(doc))
		case coredocstore.ChangeDelete, coredocstore.ChangeTrash:
			if deleteErr := replica.Delete(change.Key); deleteErr != nil && !errs.HasCode(deleteErr, coredocstore.CodeDocumentNotFound) {
				t.Fatalf("Delete() = %v", deleteErr)
			}
		default:
			t.Fatalf("unknown operation %q", change.Op)
		}
	}
	must(t, replica.Close())
	want, got := readFile(t, sourceFS, cfg.Path), readFile(t, replicaFS, cfg.Path)
	if want != got {
		t.Fatalf("the replayed snapshot differs:\n got %s\nwant %s", got, want)
	}
}

// TestFileChangeFeedIsAtLeastOnce pins the order of the file engine's two
// writes: the record is appended first, so a publication that fails leaves a
// record of a write that did not land — at least once — whose version the
// store does not hold, followed by a record that restates the key as it
// stands (kitsunium/sdk#831); a stream that refuses the record refuses the
// write with nothing published.
func TestFileChangeFeedIsAtLeastOnce(t *testing.T) {
	t.Parallel()
	faulty := &faultyFS{FullFS: memFS()}
	feed := changeStream(t)
	cfg := accountConfig(faulty)
	cfg.Versions, cfg.Changes = 2, feed
	store, err := openWith(cfg)
	must(t, err)
	t.Cleanup(func() { must(t, store.Close()) })
	must(t, store.Put(account{ID: "a"}))
	faulty.set("", true, false)
	requireCode(t, store.Put(account{ID: "a", Name: "lost"}), coredocstore.CodePersistFailed, "a publication refused")
	faulty.set("", false, false)
	changes := readChanges(t, feed)
	if len(changes) != 3 || changes[1].Version != 2 || changes[2].Op != coredocstore.ChangePut || changes[2].Version != 1 {
		t.Fatalf("changes = %+v, want the landed write's, the lost one's at version 2, and version 1 restated", changes)
	}
	versions, err := store.Versions("a")
	must(t, err)
	if len(versions) != 1 || versions[0].Number != 1 {
		t.Fatalf("Versions = %+v: the lost write's version is in the store", versions)
	}

	refused, err := openWith(docstore.Config[account]{Key: accountKey, Changes: refusingStream{feed}})
	must(t, err)
	err = refused.Put(account{ID: "b"})
	requireCode(t, err, coredocstore.CodeChangeFeedFailed, "a stream that refuses")
	if !errors.Is(err, errAppend) {
		t.Fatalf("ChangeFeedFailed = %v, want the stream's error joined", err)
	}
	if _, getErr := refused.Get("b"); !errs.HasCode(getErr, coredocstore.CodeDocumentNotFound) {
		t.Fatalf("Get after a refused record = %v, want DOCUMENT_NOT_FOUND", getErr)
	}
}

// TestChangesWithDocumentNeedsChanges refuses, on both engines, a document
// asked of a feed nobody configured.
func TestChangesWithDocumentNeedsChanges(t *testing.T) {
	t.Parallel()
	_, err := openWith(docstore.Config[account]{Key: accountKey, ChangesWithDocument: true})
	requireCode(t, err, coredocstore.CodeStoreMisconfigured, "the file engine")
	engine := newSQLEngine(coresql.DialectSQLite, accountsTable)
	_, err = docstore.OpenSQL(docstore.SQLConfig[account]{
		Key: accountKey, Transactor: newSQLTransactor(t, engine, coresql.DialectSQLite), Dialect: coresql.DialectSQLite,
		Table: accountsTable, ChangesWithDocument: true,
	})
	requireCode(t, err, coredocstore.CodeStoreMisconfigured, "the SQL engine")
	store, err := openWith(docstore.Config[account]{Key: accountKey})
	must(t, err)
	if s, ok := store.ChangeStream(); ok || s != nil {
		t.Fatalf("ChangeStream() without Changes = (%v, %v), want (nil, false)", s, ok)
	}
}

// txStream is a stream joined to a transactor's transactions the way the
// stream's SQL engine is: an Append under a context carrying a transaction
// of tm is held until that transaction commits — dropped with a savepoint
// rolled back or a transaction that rolls back —, and lands at once
// otherwise. The fake SQL engine knows the store's tables only, so the
// records land in a memory stream.
type txStream struct {
	corestream.Stream
	tm coresql.Deferrer
	mu sync.Mutex
	// appends counts the Appends sent, landed or not.
	appends int
}

// Append holds records until the transaction ctx carries commits.
func (s *txStream) Append(ctx context.Context, records []corestream.RecordValue) (corestream.AppendValue, error) {
	s.mu.Lock()
	s.appends++
	s.mu.Unlock()
	held := slices.Clone(records)
	if s.tm.Defer(ctx, func() {
		if _, err := s.Stream.Append(context.Background(), held); err != nil {
			panic(err)
		}
	}) {
		return corestream.AppendValue{}, nil
	}
	return s.Stream.Append(ctx, records)
}

// TestSQLChangeFeedIsTheWritesTransaction runs the SQL engine's feed inside
// the caller's transaction on every dialect: a write in a Transact whose fn
// fails leaves no record, a committed write exactly one, with the version it
// left, which a consumer group reads; a refused record rolls its write back.
func TestSQLChangeFeedIsTheWritesTransaction(t *testing.T) {
	t.Parallel()
	eachDialect(t, func(t *testing.T, dialect coresql.Dialect) {
		engine := newSQLEngine(dialect, accountsTable)
		tm := newSQLTransactor(t, engine, dialect)
		feed := &txStream{Stream: changeStream(t), tm: tm.(coresql.Deferrer)}
		store := reopenSQL(t, tm, dialect, keeping(2, clock.NewManualClock(epoch)), func(cfg *docstore.SQLConfig[account]) {
			cfg.Changes, cfg.ChangesWithDocument = feed, true
		})
		ctx := t.Context()
		if got, ok := store.ChangeStream(); !ok || got != corestream.Stream(feed) {
			t.Fatalf("ChangeStream() = (%v, %v), want the configured stream", got, ok)
		}

		err := tm.Transact(ctx, coresql.TxOptionsValue{}, func(txCtx context.Context, _ coresql.Executor) error {
			if putErr := store.Put(txCtx, account{ID: "rolled", Email: "r@x"}); putErr != nil {
				return putErr
			}
			return errWork
		})
		if !errors.Is(err, errWork) {
			t.Fatalf("Transact() = %v, want the unit of work's failure", err)
		}
		if changes := readChanges(t, feed); len(changes) != 0 || feed.appends != 1 {
			t.Fatalf("a rolled-back write left %d records (%d Appends sent), want none (1)", len(changes), feed.appends)
		}

		must(t, store.Put(ctx, account{ID: "acc", Email: "a@x"}))
		must(t, tm.Transact(ctx, coresql.TxOptionsValue{}, func(txCtx context.Context, _ coresql.Executor) error {
			_, updateErr := store.Update(txCtx, "acc", func(a *account) error { a.Name = "Ada"; return nil })
			return updateErr
		}))
		must(t, store.Trash(ctx, "acc"))
		must(t, store.Restore(ctx, "acc"))
		must(t, store.Delete(ctx, "acc"))
		got := opsOf(readChanges(t, feed))
		if want := []string{"put@1", "put@2", "trash@2", "restore@2", "delete@0"}; !slices.Equal(got["acc"], want) || len(got) != 1 {
			t.Fatalf("changes = %v, want acc: %v", got, want)
		}

		refusing := reopenSQL(t, tm, dialect, func(cfg *docstore.SQLConfig[account]) { cfg.Changes = refusingStream{feed} })
		err = refusing.Put(ctx, account{ID: "never"})
		requireCode(t, err, coredocstore.CodeChangeFeedFailed, "a stream that refuses")
		if !errors.Is(err, errAppend) {
			t.Fatalf("ChangeFeedFailed = %v, want the stream's error joined", err)
		}
		if _, getErr := store.Get(ctx, "never"); !errs.HasCode(getErr, coredocstore.CodeDocumentNotFound) {
			t.Fatalf("Get after a refused record = %v, want DOCUMENT_NOT_FOUND: the write was not rolled back", getErr)
		}

		readByAGroup(t, feed.Stream, "acc", 5)
	})
}

// readByAGroup subscribes a consumer group to s and waits until it read want
// records of key, then stops it.
func readByAGroup(t *testing.T, s corestream.Stream, key string, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	var read []coredocstore.ChangeValue
	done := make(chan error, 1)
	go func() {
		done <- svcstream.Subscribe(ctx, s, svcstream.SubscriberConfig{
			Group: "projection", HandlerIsIdempotent: true, PollInterval: time.Millisecond,
			Handler: func(_ context.Context, entries []corestream.EntryValue) error {
				mu.Lock()
				defer mu.Unlock()
				for _, entry := range entries {
					read = append(read, decodeChange(t, entry))
				}
				if len(read) >= want {
					cancel()
				}
				return nil
			},
		})
	}()
	if err := <-done; err != nil {
		t.Fatalf("Subscribe() = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(read) != want || read[0].Key != key || !bytes.Contains(read[0].Document, []byte(`"a@x"`)) {
		t.Fatalf("the group read %+v, want %d records of %q, the first carrying its document", read, want, key)
	}
}
