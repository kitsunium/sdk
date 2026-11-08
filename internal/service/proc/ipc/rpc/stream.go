package rpc

import (
	"context"
	"sync"
	"sync/atomic"

	corerpc "github.com/kitsunium/sdk/internal/core/proc/ipc/rpc"
)

// clientStream is a Stream's reading end: the items the connection's reader
// queued for it, until its end, its context's or Close.
type clientStream struct {
	// c is the client the stream runs on.
	c *Client
	// id is the stream's.
	id uint64
	// p holds the queued frames and the gone signal.
	p *pendingCall
	// ctx is the caller's context; its end abandons the stream.
	ctx context.Context
	// stop releases the context's watch.
	stop func() bool
	// once abandons the stream once.
	once sync.Once
	// ended is set once the end or an error was read; abandon reads it from
	// the context's goroutine.
	ended atomic.Bool
}

// Stream sends in to method and returns the items the server sends back,
// until its end, ctx's or Close. Its items are bounded by MaxFrameBytes as
// any frame; a reader slower than the server holds the connection's other
// calls behind it, so a long stream wants a client of its own.
func (c *Client) Stream(ctx context.Context, method string, in any) (corerpc.Stream, error) {
	id, p, err := c.open(ctx, method, true)
	//: refused before a byte is sent.
	if err != nil {
		return nil, err
	}
	frame, err := c.format.Append(head(kindStream, id, method, budgetOf(ctx, 0)), in)
	//: not sent: in did not encode, or the connection could not carry it.
	if err := c.send(id, frame, err); err != nil {
		return nil, err
	}
	s := &clientStream{c: c, id: id, p: p, ctx: ctx}
	s.stop = context.AfterFunc(ctx, s.abandon)
	return s, nil
}

// Recv decodes the next item into v and reports true; at the stream's end it
// reports false and a nil error.
func (s *clientStream) Recv(v any) (more bool, err error) {
	//: the end was already read.
	if s.ended.Load() {
		return false, nil
	}
	body, more, err := s.next()
	//: the end, or what ended the stream.
	if !more {
		return false, err
	}
	//: the item is not a value of v's shape in this format.
	if err := s.c.format.Unmarshal(body, v); err != nil {
		return true, codecFailed("decode", err)
	}
	return true, nil
}

// next waits for the stream's next frame: an item's body and true, or false
// with the end's nil, the server's error, the abandonment's or the
// connection's.
func (s *clientStream) next() (body []byte, more bool, err error) {
	select {
	case f := <-s.p.answers:
		return s.read(f)
	case <-s.p.gone:
		s.ended.Store(true)
		return nil, false, s.goneErr()
	case <-s.c.done:
		s.ended.Store(true)
		return nil, false, s.c.closedErr()
	}
}

// read is one queued frame: an item's body, the end, or the server's error.
func (s *clientStream) read(f frameValue) (body []byte, more bool, err error) {
	//: an item.
	if f.kind == kindItem {
		return f.body, true, nil
	}
	s.ended.Store(true)
	s.stop()
	//: the server's end of the stream.
	if f.kind == kindEnd {
		return nil, false, nil
	}
	_, err = s.c.answerBody(f, kindEnd)
	return nil, false, err
}

// goneErr is why a stream was abandoned: its context's end, or Close.
func (s *clientStream) goneErr() error {
	//: the context ended it.
	if s.ctx.Err() != nil {
		return contextErr(s.ctx)
	}
	return connClosed()
}

// Close abandons the stream: the server is sent a cancel frame for it and the
// items still on the way are dropped. It is idempotent.
func (s *clientStream) Close() error {
	s.stop()
	s.abandon()
	return nil
}

// abandon forgets the stream, tells the server unless it already ended, and
// releases the connection's reader if it waits on this stream.
func (s *clientStream) abandon() {
	s.once.Do(func() {
		close(s.p.gone)
		//: an ended stream has nothing left on the server.
		if !s.ended.Load() {
			s.c.abandon(s.id)
		}
	})
}
