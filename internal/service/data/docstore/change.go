package docstore

import (
	"context"
	"encoding/json"
	"errors"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// changeFeed is a store's change feed (kitsunium/sdk#831): the stream every
// write appends its change record to, and whether a record carries the
// document. The zero value is no feed.
type changeFeed struct {
	// stream takes the records; nil writes none.
	stream corestream.Stream
	// withDocument puts the document's JSON in a put's, a restore's and a
	// trash's record.
	withDocument bool
}

// on reports whether the store writes change records.
func (f changeFeed) on() bool {
	//: a feed is a stream.
	return f.stream != nil
}

// record is the stream record of one change: keyed by the document's key, so
// one partition keeps a document's changes in order, its payload the change
// as JSON. A delete never carries the document.
func (f changeFeed) record(store string, change coredocstore.ChangeValue, raw json.RawMessage) (corestream.RecordValue, error) {
	//: the document, when the store was told to carry it.
	if f.withDocument && change.Op != coredocstore.ChangeDelete {
		change.Document = raw
	}
	payload, err := json.Marshal(change)
	//: unreachable while every document is JSON the store encoded or loaded.
	if err != nil {
		//: ChangeFeedFailed, naming what failed and never what it held.
		return corestream.RecordValue{}, kerrs.Wrap(coredocstore.ChangeFeedFailed, kerrs.WrapParams{},
			kerrs.String("store", store), kerrs.String("op", string(change.Op)), kerrs.String("cause", encodeCause(err)))
	}
	//: the stream record.
	return corestream.RecordValue{Key: []byte(change.Key), Payload: payload}, nil
}

// append writes records to the stream under ctx — the write's transaction on
// the SQL engine — or refuses the write: ChangeFeedFailed, naming the store and
// the operation, the stream's own error joined for errors.Is to find.
func (f changeFeed) append(ctx context.Context, store, op string, records []corestream.RecordValue) error {
	_, err := f.stream.Append(ctx, records)
	//: the stream took them.
	if err == nil {
		//: recorded.
		return nil
	}
	//: the verdict names the store and the operation; the cause is reachable.
	return errors.Join(
		kerrs.Wrap(coredocstore.ChangeFeedFailed, kerrs.WrapParams{}, kerrs.String("store", store), kerrs.String("op", op)),
		err)
}

// validateChanges refuses a document asked of a feed that does not exist:
// a caller who sets ChangesWithDocument believes records are written. Both
// engines refuse through it.
func validateChanges(stream corestream.Stream, withDocument bool) error {
	//: a document for records nobody writes.
	if withDocument && stream == nil {
		//: StoreMisconfigured, naming the setting.
		return misconfigured("ChangesWithDocument", "set without Changes")
	}
	//: a feed the store can write.
	return nil
}

// changeVersion is the version a change record names: none for a delete and
// on a store that keeps no versions, 1 for a document stored before its
// store kept them, else the current version of record.
func (s *Store[T]) changeVersion(op coredocstore.ChangeOp, record *versionsRecord) uint64 {
	//: a deletion leaves no version, and a store that keeps none numbers none.
	if op == coredocstore.ChangeDelete || s.keep == 0 {
		//: none.
		return 0
	}
	//: never versioned since its store kept versions: version 1.
	if record == nil {
		//: the first.
		return 1
	}
	//: the current version, a trashed document's included.
	return record.Current.Number
}

// feed appends the change record of one write of the file engine, before
// the write is published: its key, the version record leaves, the instant
// and, when asked, raw. The caller holds the writers' lock, so the records of
// a key are in the order its writes stood. The file engine waits on nothing a
// caller could abandon, so the record is appended without a deadline of the
// caller's.
func (s *Store[T]) feed(op coredocstore.ChangeOp, key string, raw json.RawMessage, record *versionsRecord) error {
	//: no feed, no record.
	if !s.changes.on() {
		//: nothing to append.
		return nil
	}
	rec, err := s.changes.record(s.path, coredocstore.ChangeValue{At: s.now(), Key: key, Op: op, Version: s.changeVersion(op, record)}, raw)
	//: a document no JSON encoder takes back.
	if err != nil {
		//: ChangeFeedFailed: nothing written.
		return err
	}
	//: one record, one Append.
	return s.changes.append(context.Background(), s.path, string(op), []corestream.RecordValue{rec})
}

// feedBatch appends the change records of a planned batch, one per key it
// writes, its final state, before the group entry is published. A stream's
// Append takes one partition's records, and the keys of a batch spread over
// several, so each record is its own Append: a failure part way leaves the
// records already appended for a batch that did not land — at least once, as
// a crash does. The caller holds the writers' lock.
func (s *Store[T]) feedBatch(plan *batchPlan) error {
	//: no feed, no record.
	if !s.changes.on() {
		//: nothing to append.
		return nil
	}
	now := s.now()
	//: each key once, as the batch leaves it.
	for _, k := range plan.keys {
		op := coredocstore.ChangePut
		//: a key the batch deleted.
		if k.deleted {
			op = coredocstore.ChangeDelete
		}
		rec, err := s.changes.record(s.path, coredocstore.ChangeValue{At: now, Key: k.key, Op: op, Version: s.changeVersion(op, k.versions)}, k.raw)
		//: a document no JSON encoder takes back.
		if err != nil {
			//: ChangeFeedFailed: nothing of the batch written.
			return err
		}
		//: ChangeFeedFailed: nothing of the batch published.
		if appendErr := s.changes.append(context.Background(), s.path, "batch", []corestream.RecordValue{rec}); appendErr != nil {
			//: the batch is refused.
			return appendErr
		}
	}
	//: every key recorded.
	return nil
}

// ChangeStream returns the stream the store writes its change records
// to — [Config].Changes — and false when it was given none
// (core/data/docstore.ChangeFeeder).
func (s *Store[T]) ChangeStream() (changes corestream.Stream, ok bool) {
	//: the configured stream, nil when none.
	return s.changes.stream, s.changes.on()
}

// feedIn appends the change record of one write of the SQL engine, inside
// its transaction or savepoint: ctx carries it, so the stream's SQL engine on
// the store's transactor appends in the same transaction, and a write
// rolled back leaves no record. The version is the one the write left,
// read in the same transaction on a store that keeps versions.
func (s *SQLStore[T]) feedIn(ctx context.Context, ex coresql.Executor, op coredocstore.ChangeOp, key string, raw []byte) error {
	//: no feed, no record.
	if !s.changes.on() {
		//: nothing to append.
		return nil
	}
	var version uint64
	//: a deletion leaves no version, and a store that keeps none numbers none.
	if op != coredocstore.ChangeDelete && s.keep > 0 {
		head, _, err := s.readHead(ctx, ex, key)
		//: StatementFailed: the write rolls back.
		if err != nil {
			//: nothing written.
			return err
		}
		version = head
	}
	rec, err := s.changes.record(s.table, coredocstore.ChangeValue{At: s.clock.Now().UTC(), Key: key, Op: op, Version: version}, raw)
	//: a document no JSON encoder takes back.
	if err != nil {
		//: ChangeFeedFailed: the write rolls back.
		return err
	}
	//: in the write's transaction.
	return s.changes.append(ctx, s.table, string(op), []corestream.RecordValue{rec})
}

// ChangeStream returns the stream the store writes its change records
// to — [SQLConfig].Changes — and false when it was given none
// (core/data/docstore.ChangeFeeder).
func (s *SQLStore[T]) ChangeStream() (changes corestream.Stream, ok bool) {
	//: the configured stream, nil when none.
	return s.changes.stream, s.changes.on()
}
