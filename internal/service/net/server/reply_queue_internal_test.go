// Package server — BatchReplies' portable queue (ADR 0192).
package server

import (
	"errors"
	stdnet "net"
	"testing"
)

// countingConn records every datagram written, and fails those it is told to.
type countingConn struct {
	fakePacketConn
	// sent are the payloads written, in order.
	sent []string
	// failEvery fails every write when set.
	failEvery bool
}

// WriteTo implements net.PacketConn.
func (c *countingConn) WriteTo(p []byte, _ stdnet.Addr) (int, error) {
	//: a socket that has gone away.
	if c.failEvery {
		//: nothing left.
		return 0, errors.New("gone")
	}
	c.sent = append(c.sent, string(p))
	//: the whole datagram.
	return len(p), nil
}

// Test_queuedSink pins BatchReplies where sendmmsg is not: inside a batch a
// reply is copied and reported sent, and leaves at flush, in order; a failure
// there is counted; outside a batch a reply is written at once with the write's
// own error.
func Test_queuedSink(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// fail makes every write fail.
		fail bool
		// batch opens a batch first.
		batch bool
	}
	tests := []tc{
		{name: "a batch", batch: true},
		{name: "a batch whose writes fail", batch: true, fail: true},
		{name: "outside a batch"},
		{name: "outside a batch, failing", fail: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		conn := &countingConn{failEvery: c.fail}
		counters := &datagramCounters{}
		w := newQueuedSink(conn, counters)
		if c.batch {
			w.begin()
		}
		buf := []byte("one")
		n1, err1 := w.reply(buf, nil)
		copy(buf, "xxx")
		n2, err2 := w.reply([]byte("two"), nil)
		if c.batch {
			if n1 != 3 || err1 != nil || n2 != 3 || err2 != nil || len(conn.sent) != 0 {
				t.Fatalf("queued replies = %d %v, %d %v, sent %v, want queued", n1, err1, n2, err2, conn.sent)
			}
			w.flush()
		} else if c.fail != (err1 != nil) {
			t.Fatalf("reply outside a batch = %d, %v, want the write's answer", n1, err1)
		}
		switch {
		case c.batch && c.fail:
			if counters.replyFailures.Load() != 2 {
				t.Fatalf("ReplyFailures = %d, want 2", counters.replyFailures.Load())
			}
		case c.batch:
			//: copied when queued: the handler's reuse of its buffer is invisible.
			if len(conn.sent) != 2 || conn.sent[0] != "one" || conn.sent[1] != "two" {
				t.Fatalf("sent %v, want [one two]", conn.sent)
			}
		case !c.fail:
			if len(conn.sent) != 2 || counters.replyFailures.Load() != 0 {
				t.Fatalf("sent %v at once, failures %d", conn.sent, counters.replyFailures.Load())
			}
		}
		if len(w.queue) != 0 || w.used != 0 {
			t.Fatal("the queue was not emptied")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_queuedSink_full pins that a full queue sends what it holds early, and
// that a reply larger than the arena is written at once.
func Test_queuedSink_full(t *testing.T) {
	t.Parallel()
	conn := &countingConn{}
	w := newQueuedSink(conn, &datagramCounters{})
	w.begin()
	for range replyQueue + 1 {
		_, _ = w.reply([]byte("r"), nil)
	}
	if len(conn.sent) != replyQueue || len(w.queue) != 1 {
		t.Fatalf("sent %d early and queued %d, want %d and 1", len(conn.sent), len(w.queue), replyQueue)
	}
	if n, err := w.reply(make([]byte, replyArena+1), nil); n != replyArena+1 || err != nil || len(conn.sent) != replyQueue+1 {
		t.Fatalf("an oversized reply = %d, %v, sent %d, want written at once", n, err, len(conn.sent))
	}
	w.flush()
	if len(conn.sent) != replyQueue+2 {
		t.Fatalf("sent %d, want %d", len(conn.sent), replyQueue+2)
	}
}
