package machine

import (
	"context"
	"io"
	"sync"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sqlSnapshot is a view of the database pinned by a read-only transaction
// the snapshot holds open: opened and read once on the apply goroutine, so
// it sees the state of the last batch committed, and read again by WriteTo
// on the snapshot goroutine while Apply commits the next ones.
type sqlSnapshot struct {
	m *SQL
	// write hands the transaction's goroutine the writer of WriteTo, and
	// release ends the transaction.
	write   chan *writeReq
	release chan struct{}
	once    sync.Once
	done    chan struct{}
}

// writeReq is one WriteTo, answered on done: a channel of one slot, so that the
// answer never waits for WriteTo to be ready and is sent exactly once.
type writeReq struct {
	w    io.Writer
	n    int64
	err  error
	done chan struct{}
}

// Snapshot opens the read-only transaction the view is read from and
// reads the position in it, so that the database pins its view now.
// It starts one goroutine, which holds that transaction and serves
// WriteTo in it: the goroutine ends at Release, or at once when the
// transaction does not open, and Snapshot waits for it then.
func (m *SQL) Snapshot() (coreconsensus.Snapshot, error) {
	s := &sqlSnapshot{m: m, write: make(chan *writeReq), release: make(chan struct{}), done: make(chan struct{})}
	ready := make(chan error, 1)
	go s.hold(ready)
	if err := <-ready; err != nil {
		<-s.done
		return nil, failed("snapshot", err)
	}
	return s, nil
}

// hold runs the snapshot's transaction until Release, serving WriteTo in it.
func (s *sqlSnapshot) hold(ready chan<- error) {
	defer close(s.done)
	opened := false
	opts := coresql.TxOptionsValue{Isolation: s.m.snapIso, ReadOnly: true}
	err := s.m.cfg.Transactor.Transact(context.Background(), opts, func(ctx context.Context, ex coresql.Executor) error {
		if _, _, _, err := s.m.readPosition(ctx, ex); err != nil {
			return err
		}
		opened = true
		ready <- nil
		for {
			select {
			case req := <-s.write:
				req.n, req.err = s.dump(ctx, ex, req.w)
				req.done <- struct{}{}
			case <-s.release:
				return nil
			}
		}
	})
	if !opened {
		ready <- err
	}
}

// dump writes the header, then what Dump writes, counting the bytes.
func (s *sqlSnapshot) dump(ctx context.Context, ex coresql.Executor, w io.Writer) (int64, error) {
	cw := &countingWriter{w: w}
	if _, err := cw.Write([]byte{sqlSnapshotMagic[0], sqlSnapshotMagic[1], sqlSnapshotMagic[2], sqlSnapshotMagic[3], sqlSnapshotVersion}); err != nil {
		return cw.n, err
	}
	if err := s.m.cfg.Dump(ctx, ex, cw); err != nil {
		return cw.n, failed("dump", err)
	}
	return cw.n, nil
}

// WriteTo writes the view's state to w, read in the snapshot's transaction.
func (s *sqlSnapshot) WriteTo(w io.Writer) (int64, error) {
	req := &writeReq{w: w, done: make(chan struct{}, 1)}
	select {
	case s.write <- req:
	case <-s.done:
		return 0, origin(coreconsensus.StateMachineFailed, errs.String("why", "the snapshot was written after its release"))
	}
	<-req.done
	return req.n, req.err
}

// Release ends the snapshot's transaction. It is idempotent.
func (s *sqlSnapshot) Release() {
	s.once.Do(func() { close(s.release) })
	<-s.done
}

// countingWriter counts what passes through it.
type countingWriter struct {
	w io.Writer
	n int64
}

// Write writes p and counts it.
func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
