package docstore

import (
	"context"

	coredocstore "github.com/kitsunium/sdk/internal/core/data/docstore"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
)

// sqlBatchOp is one write a SQL batch records: a prepared document and its
// mode, or a deletion of key.
type sqlBatchOp struct {
	// p is the prepared document; nil for a deletion.
	p *sqlPrepared
	// key is the deleted key.
	key string
	// mode is what the write expects to find under its key.
	mode writeMode
}

// sqlBatchWriter records a SQL batch's writes, each prepared at once, before
// any transaction opens.
type sqlBatchWriter[T any] struct {
	// store prepares each document.
	store *SQLStore[T]
	// ops are the writes, in order.
	ops []sqlBatchOp
}

// Put records v, created or replacing whatever is under its key.
func (w *sqlBatchWriter[T]) Put(v T) error {
	//: anything under the key is fine.
	return w.record(v, upsert)
}

// Insert records v, which must be new.
func (w *sqlBatchWriter[T]) Insert(v T) error {
	//: nothing may be under the key.
	return w.record(v, insertOnly)
}

// Replace records v over the document under its key, which must be there.
func (w *sqlBatchWriter[T]) Replace(v T) error {
	//: a document must be under the key.
	return w.record(v, replaceOnly)
}

// Delete records the deletion of the document under key.
func (w *sqlBatchWriter[T]) Delete(key string) error {
	w.ops = append(w.ops, sqlBatchOp{key: key})
	//: decided when the batch commits.
	return nil
}

// record prepares v and records its write.
func (w *sqlBatchWriter[T]) record(v T, mode writeMode) error {
	p, err := w.store.prepare(v)
	//: DocumentKeyEmpty, DocumentUnencodable or KeyTooLong, answered at once.
	if err != nil {
		//: nothing recorded.
		return err
	}
	w.ops = append(w.ops, sqlBatchOp{p: p, key: p.key, mode: mode})
	//: recorded.
	return nil
}

// Batch writes what fn records as one transaction — a savepoint of the
// one ctx carries, or one of the store's own — each write sending the
// statements it sends alone; a refusal or a failure rolls the whole
// batch back. The hooks run once the transaction committed.
func (s *SQLStore[T]) Batch(ctx context.Context, fn func(w coredocstore.BatchWriter[T]) error) error {
	w := &sqlBatchWriter[T]{store: s}
	//: the caller's own error, returned as it is: nothing was sent.
	if err := fn(w); err != nil {
		//: as fn returned it.
		return err
	}
	//: nothing recorded, nothing to send.
	if len(w.ops) == 0 {
		//: an empty batch.
		return nil
	}
	err := s.run(ctx, true, func(txCtx context.Context, ex coresql.Executor) error {
		//: in order, each write seeing the ones before it in the transaction.
		for _, op := range w.ops {
			//: a deletion, or a write.
			if op.p == nil {
				//: DocumentNotFound, or a failure: the batch rolls back.
				if removeErr := s.remove(txCtx, ex, op.key); removeErr != nil {
					//: the first refusal refuses the batch.
					return removeErr
				}
				continue
			}
			//: DocumentExists, DocumentNotFound, a collision, or a failure.
			if writeErr := s.writeIn(txCtx, ex, op.p, op.mode, coredocstore.StampValue{}); writeErr != nil {
				//: the first refusal refuses the batch.
				return writeErr
			}
		}
		//: every write sent.
		return nil
	})
	//: nothing changed.
	if err != nil {
		//: the refusal, or the classified failure.
		return s.settle(ctx, err)
	}
	//: write by write, in order, once committed.
	for _, op := range w.ops {
		//: a deletion, or a write.
		if op.p == nil {
			s.announce(ctx, &s.onDelete, op.key)
			continue
		}
		s.announce(ctx, &s.onWrite, op.key)
	}
	//: written.
	return nil
}
