// Package telemetry — the exporter: a bounded ring producers never wait on,
// drained to the clients attached to a private socket, which it never reads.
package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"math/bits"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/ipc"
)

// NewExporter checks cfg and builds an exporter that listens nowhere yet:
// Start opens its socket.
func NewExporter(cfg *ExporterConfig) (*Exporter, error) {
	size, err := ringSize(cfg)
	if err != nil {
		return nil, err
	}
	refs, err := nodeRefs(cfg.Hello.Nodes)
	if err != nil {
		return nil, err
	}
	var id [8]byte
	//: crypto/rand.Read never fails on a supported platform (Go 1.24+); the
	//: instance only tells two runs of one binary apart.
	_, _ = rand.Read(id[:])
	e := &Exporter{
		cfg: *cfg, refs: refs, instance: hex.EncodeToString(id[:]),
		ring: make(ring, size), mask: uint64(size - 1), wake: make(chan struct{}, 1),
		buf: make([]byte, 0, batchRecords*RecordSize),
	}
	for i := range e.ring {
		e.ring[i].seq.Store(uint64(i))
	}
	return e, nil
}

// ringSize is the ring's size cfg asks for, a power of two within bounds.
func ringSize(cfg *ExporterConfig) (int, error) {
	switch {
	case cfg.Path == "":
		return 0, errs.Wrap(Misconfigured, errs.WrapParams{}, errs.String("rule", "no socket path"))
	case cfg.Buffer != 0 && (cfg.Buffer < MinBuffer || cfg.Buffer > MaxBuffer):
		return 0, errs.Wrap(Misconfigured, errs.WrapParams{}, errs.String("rule", "buffer out of bounds"), errs.Int("buffer", cfg.Buffer))
	case cfg.Buffer == 0:
		return DefaultBuffer, nil
	}
	return 1 << bits.Len(uint(cfg.Buffer-1)), nil
}

// nodeRefs numbers the node table from 1, refusing an ID outside the grammar.
func nodeRefs(nodes []string) (map[string]NodeRef, error) {
	refs := make(map[string]NodeRef, len(nodes))
	for i, id := range nodes {
		if _, err := model.ParseID(id); err != nil {
			return nil, errs.Wrap(Misconfigured, errs.WrapParams{}, errs.String("rule", "a node ID outside the grammar"), errs.Int("index", i))
		}
		refs[id] = NodeRef(i + 1)
	}
	return refs, nil
}

// Ref is the reference of node id in the handshake's table; zero when the
// table does not hold it.
func (e *Exporter) Ref(id string) NodeRef { return e.refs[id] }

// Instance is the process's instance identifier, in the handshake.
func (e *Exporter) Instance() string { return e.instance }

// Dropped is how many events a full ring refused.
func (e *Exporter) Dropped() uint64 { return e.dropped.Load() }

// Sent is how many records reached at least one client.
func (e *Exporter) Sent() uint64 { return e.sent.Load() }

// Emit copies ev into the ring, or drops it when the ring is full. It never
// blocks and never allocates.
func (e *Exporter) Emit(ev *Event) {
	pos := e.tail.Load()
	for {
		s := &e.ring[pos&e.mask]
		seq := s.seq.Load()
		switch diff := int64(seq) - int64(pos); {
		case diff == 0:
			if e.tail.CompareAndSwap(pos, pos+1) {
				s.ev = *ev
				s.seq.Store(pos + 1)
				select {
				case e.wake <- struct{}{}:
				default:
				}
				return
			}
			pos = e.tail.Load()
		case diff < 0:
			e.dropped.Add(1)
			return
		default:
			pos = e.tail.Load()
		}
	}
}

// take removes the oldest event from the ring, if any. One goroutine takes.
func (e *Exporter) take(ev *Event) bool {
	s := &e.ring[e.head&e.mask]
	if s.seq.Load() != e.head+1 {
		return false
	}
	*ev = s.ev
	s.seq.Store(e.head + e.mask + 1)
	e.head++
	return true
}

// Start opens the socket and starts the two goroutines: one accepts clients
// and greets them, one drains the ring to them. Both end at Stop.
func (e *Exporter) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ln != nil {
		return errs.Wrap(Running, errs.WrapParams{})
	}
	ln, err := ipc.Listen(ipc.Config{Path: e.cfg.Path, AllowUIDs: e.cfg.AllowUIDs, AllowGIDs: e.cfg.AllowGIDs})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e.ln, e.cancel = ln, cancel
	e.done.Add(2)
	go func() {
		defer e.done.Done()
		e.accept(ln)
	}()
	go func() {
		defer e.done.Done()
		e.drain(ctx)
	}()
	return nil
}

// accept greets each client with the handshake and adds it to the readers,
// until the listener closes.
func (e *Exporter) accept(ln accepter) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		e.mu.Lock()
		if err := e.greet(c); err != nil {
			closeBestEffort(c)
		} else {
			e.clients = append(e.clients, c)
		}
		e.mu.Unlock()
	}
}

// greet writes the handshake line to c. The caller holds e.mu.
func (e *Exporter) greet(c writer) error {
	h := handshake{
		Protocol: Protocol, Product: e.cfg.Hello.Product, Binary: e.cfg.Hello.Binary, Role: e.cfg.Hello.Role,
		Instance: e.instance, Revision: e.cfg.Hello.Revision, ModelVersion: model.Version, Digest: e.cfg.Hello.Digest,
		RecordSize: RecordSize, Seq: e.seq + 1, Ops: OpNames[:], Nodes: e.cfg.Hello.Nodes,
	}
	line, err := json.Marshal(h)
	if err != nil {
		return err
	}
	if err := c.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	_, err = c.Write(append(line, '\n'))
	return err
}

// drain moves the ring's events to the clients, a batch at a time, until ctx
// ends. Without a client the events are taken and dropped: the ring must not
// fill because nobody listens.
func (e *Exporter) drain(ctx context.Context) {
	t := time.NewTicker(drainTick)
	defer t.Stop()
	for {
		if e.flush() {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case _, open := <-e.wake:
			if !open {
				return
			}
		case <-t.C:
		}
	}
}

// flush takes one batch from the ring and sends it; it reports whether the
// batch was full, so more may be waiting.
func (e *Exporter) flush() bool {
	var ev Event
	e.mu.Lock()
	defer e.mu.Unlock()
	e.buf = e.buf[:0]
	for len(e.buf) < cap(e.buf) && e.take(&ev) {
		e.seq++
		n := len(e.buf)
		e.buf = e.buf[:n+RecordSize]
		encode(e.buf[n:], e.seq, &ev)
	}
	if len(e.buf) > 0 && len(e.clients) > 0 {
		e.send()
	}
	return len(e.buf) == cap(e.buf)
}

// send writes the batch to every client, dropping one that does not take it
// within writeTimeout. The caller holds e.mu.
func (e *Exporter) send() {
	kept := e.clients[:0]
	for _, c := range e.clients {
		if err := e.write(c); err != nil {
			closeBestEffort(c)
			continue
		}
		kept = append(kept, c)
	}
	e.clients = kept
	if len(kept) > 0 {
		e.sent.Add(uint64(len(e.buf) / RecordSize))
	}
}

// write sends the batch to one client within writeTimeout.
func (e *Exporter) write(c writer) error {
	if err := c.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	_, err := c.Write(e.buf)
	return err
}

// Stop closes the socket and every client, and waits for the goroutines.
func (e *Exporter) Stop(ctx context.Context) error {
	e.mu.Lock()
	ln, cancel := e.ln, e.cancel
	e.ln = nil
	e.mu.Unlock()
	if ln == nil {
		return nil
	}
	cancel()
	err := ln.Close()
	done := make(chan struct{})
	go func() {
		e.done.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	e.mu.Lock()
	for _, c := range e.clients {
		closeBestEffort(c)
	}
	e.clients = nil
	e.mu.Unlock()
	return err
}

// closeBestEffort closes a client the exporter drops. Its error changes
// nothing — the client is gone either way — so it is logged, not returned.
func closeBestEffort(c io.Closer) {
	if err := c.Close(); err != nil {
		log.Printf("telemetry: closing a client: %v", err)
	}
}
