package kit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"sync"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// A consumer's queue as a data copy moves it (ADR 0178 C7): the waiting and
// the dead messages of its file queue under the data directory, written into
// its table of the SDK's SQL broker on the database that keeps it, each with
// what the file queue recorded of it — its payload as it rests, its delivery
// count, its instants; a dead letter's reason, cause and code —, through the
// SDK's ports that read and write a message as it lies. A mailer's outbox is
// the spool's file queue, in clear: the copy seals each mail on its way into
// the table, as the mailer's sealed outbox does (mailer_outbox.go), and its
// fingerprint is the mail's, opened.

// copyPage is how many live messages the copy reads from a file queue at a
// time.
const copyPage int = 256

// copyDeadMax bounds how many dead letters the copy reads from one file
// queue: every one a queue may hold in practice. The file broker lists them
// from its directory, a name at a time.
const copyDeadMax int = 1 << 30

// copyQueue is a queue as a copy reads and writes it: its live messages
// without a lease, its dead letters, and both written as they lie.
type copyQueue interface {
	plug.Broker
	plug.MessageMover
}

// queuePart is one consumer's queue a data copy moves: its file queue, its
// table, and the directory the copy moves aside once it is done.
type queuePart struct {
	from copyQueue
	to   copyQueue
	// closeFrom releases the file queue's directory descriptors.
	closeFrom func() error
	// dir is the file queue's directory, name the table's.
	dir, name string
}

// queueParts are the queues the app keeps on r whose file queue under the
// data directory holds a message — the ones the start warned of
// (consumer.file-queue-left) —, each opened on both sides.
func (a *App) queueParts(ctx context.Context, r *databaseRun) ([]queuePart, error) {
	var parts []queuePart
	for _, n := range a.mountedNodes() {
		q, ok := n.(queueOwner)
		if !ok {
			continue
		}
		under := q.queueUnder()
		b := q.base()
		if under == "" || !a.queueKeptOn(b, r.d) {
			continue
		}
		dir := filepath.Join(a.dataDir, b.svc.name, under, b.name)
		if !fileQueueHolds(dir) {
			continue
		}
		p, err := a.queuePart(ctx, r, n, b, under, dir)
		if err != nil {
			return parts, err
		}
		parts = append(parts, p)
	}
	return parts, nil
}

// queuePart opens the file queue at dir and the table of the consumer b,
// kept under under, on r: a mailer's behind the sealing.
func (a *App) queuePart(ctx context.Context, r *databaseRun, n node, b *nodeBase, under, dir string) (queuePart, error) {
	cfg := plug.QueueConfig{Name: b.id, Policy: consumerPolicy(defaultMaxDeliveries)}
	m, mailer := n.(*Mailer)
	if mailer {
		cfg.Policy = m.outboxPolicy()
	}
	from := cfg
	from.Dir = dir
	src, err := a.openQueueWith(from)
	if err != nil {
		return queuePart{}, failure(CodeDataCopy, "DATA_COPY_QUEUE", "a file queue cannot be opened", err, errs.String("node", b.id))
	}
	closeFrom := func() error { return nil }
	if c, ok := src.(io.Closer); ok {
		closeFrom = sync.OnceValue(c.Close)
	}
	reader, ok := src.(copyQueue)
	if !ok {
		return queuePart{}, failure(CodeDataCopy, "DATA_COPY_QUEUE", "a file queue cannot be read as it lies", closeFrom(), errs.String("node", b.id))
	}
	table := queueTable(b, under)
	if err := sqlQueueConfig(&cfg, b, r, table); err != nil {
		return queuePart{}, errors.Join(err, closeFrom())
	}
	if mailer {
		if err := m.sealOutbox(ctx, a, &cfg); err != nil {
			return queuePart{}, errors.Join(err, closeFrom())
		}
	}
	dst, err := a.openQueueWith(cfg)
	if err != nil {
		return queuePart{}, errors.Join(err, closeFrom())
	}
	to, ok := dst.(copyQueue)
	if !ok {
		return queuePart{}, failure(CodeDataCopy, "DATA_COPY_QUEUE", "a queue's table cannot be written as a message lies", closeFrom(), errs.String("node", b.id))
	}
	return queuePart{from: reader, to: to, closeFrom: closeFrom, dir: dir, name: table}, nil
}

// empty reports whether the queue's table holds no message, live or dead.
func (p *queuePart) empty(ctx context.Context) (bool, error) {
	live, err := p.to.Messages(ctx, "", 1)
	if err != nil {
		return false, err
	}
	dead, err := p.to.DeadLetters(ctx, 1)
	if err != nil {
		return false, err
	}
	return len(live) == 0 && len(dead) == 0, nil
}

// copy writes every live and dead message of the file queue into the table,
// as it lies: a leased one is written waiting, visible at its lease's
// deadline, since its lease is the file queue's.
func (p *queuePart) copy(ctx context.Context) (int, error) {
	n := 0
	after := ""
	for {
		page, err := p.from.Messages(ctx, after, copyPage)
		if err != nil {
			return n, err
		}
		if len(page) == 0 {
			break
		}
		for _, m := range page {
			after = m.Message.ID
			m.Message.ID, m.Leased = "", false
			if _, err := p.to.PutMessage(ctx, m); err != nil {
				return n, err
			}
			n++
		}
	}
	dead, err := p.from.DeadLetters(ctx, copyDeadMax)
	if err != nil {
		return n, err
	}
	for _, d := range dead {
		d.Message.ID = ""
		if _, err := p.to.PutDeadLetter(ctx, d); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// messageDigests are the fingerprints of every message of r, live and dead,
// sorted: the queue's as a multiset, since the target mints its own
// identifiers and no order holds across the copy.
func messageDigests(ctx context.Context, r copyQueue) ([]digest, error) {
	var out []digest
	after := ""
	for {
		page, err := r.Messages(ctx, after, copyPage)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for _, m := range page {
			after = m.Message.ID
			out = append(out, messageDigest(m.Message.Payload, false, m.Deliveries, "", 0))
		}
	}
	dead, err := r.DeadLetters(ctx, copyDeadMax)
	if err != nil {
		return nil, err
	}
	for _, d := range dead {
		out = append(out, messageDigest(d.Message.Payload, true, d.Deliveries, d.Reason, d.Code))
	}
	slices.SortFunc(out, func(x, y digest) int { return bytes.Compare(x[:], y[:]) })
	return out, nil
}

// verify compares the fingerprints of both sides of the queue: the same
// messages, the same number of times, in any order.
func (p *queuePart) verify(ctx context.Context) error {
	src, err := messageDigests(ctx, p.from)
	if err != nil {
		return err
	}
	dst, err := messageDigests(ctx, p.to)
	if err != nil {
		return err
	}
	if !slices.Equal(src, dst) {
		return failure(CodeDataCopyDiverged, "DATA_COPY_DIVERGED", "a queue's messages differ once copied", nil,
			errs.String("table", p.name), errs.Int("source", len(src)), errs.Int("target", len(dst)))
	}
	return nil
}
