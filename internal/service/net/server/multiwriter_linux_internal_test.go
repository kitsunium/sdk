//go:build linux

// Package server — the sendmmsg and GSO reply sink (ADR 0192).
package server

import (
	stdnet "net"
	"syscall"
	"testing"
	"time"
)

// Test_gsoSegments pins the cap: the kernel's 64, and the 16-bit IP length a
// GSO send is built as — 54 of 1200 bytes on each family.
func Test_gsoSegments(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// family and size are the socket's and the datagram's.
		family, size int
		// want is the cap.
		want int
	}
	tests := []tc{
		{name: "1200 bytes, IPv4", family: syscall.AF_INET, size: 1200, want: 54},
		{name: "1200 bytes, IPv6", family: syscall.AF_INET6, size: 1200, want: 54},
		{name: "small datagrams, the kernel's 64", family: syscall.AF_INET, size: 8, want: 64},
		{name: "an Ethernet frame, IPv4", family: syscall.AF_INET, size: 1472, want: 44},
		{name: "an Ethernet frame, IPv6", family: syscall.AF_INET6, size: 1452, want: 45},
		{name: "an empty datagram", family: syscall.AF_INET, size: 0, want: 1},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := gsoSegments(c.family, c.size)
			if got != c.want {
				t.Fatalf("gsoSegments(%d, %d) = %d, want %d", c.family, c.size, got, c.want)
			}
			hdr := ipv4HeaderBytes
			if c.family == syscall.AF_INET6 {
				hdr = ipv6HeaderBytes
			}
			if c.size > 0 && hdr+udpHeaderBytes+got*c.size > ipMaxLength {
				t.Fatalf("%d segments of %d overflow the IP length", got, c.size)
			}
		})
	}
}

// writerPair is a batched sink over a loopback socket and a plain receiver it
// replies to.
func writerPair(t *testing.T) (*multiWriter, *datagramCounters, *stdnet.UDPConn, *stdnet.UDPAddr) {
	t.Helper()
	server, _ := udpPair(t)
	peer, _ := udpPair(t)
	if err := peer.SetReadBuffer(8 << 20); err != nil {
		t.Fatal(err)
	}
	counters := &datagramCounters{}
	w, err := newMultiWriter(server, server, counters, nil)
	if err != nil {
		t.Fatalf("newMultiWriter = %v", err)
	}
	if !w.gso.Load() {
		t.Skip("this kernel has no UDP GSO (before 4.18)")
	}
	return w, counters, peer, peer.LocalAddr().(*stdnet.UDPAddr)
}

// receive counts what peer receives until it is quiet, and the sizes.
func receive(t *testing.T, peer *stdnet.UDPConn) []int {
	t.Helper()
	var sizes []int
	buf := make([]byte, 1<<16)
	for {
		if err := peer.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		n, _, err := peer.ReadFrom(buf)
		if err != nil {
			return sizes
		}
		sizes = append(sizes, n)
	}
}

// Test_multiWriter_flush pins the batch over a real socket: replies queued to
// one peer of one size leave as GSO messages of at most the cap — 54 of 1200
// bytes in one, a 55th in a second —, every one a datagram at the peer, none
// refused for its size; a shorter last one ends a message; another peer starts
// one.
func Test_multiWriter_flush(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// sizes are the replies, in order, all to one peer.
		sizes []int
		// messages is how many messages build lays them out as.
		messages int
	}
	repeat := func(n, size int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = size
		}
		return out
	}
	tests := []tc{
		{name: "54 at the cap", sizes: repeat(54, 1200), messages: 1},
		{name: "55, one past it", sizes: repeat(55, 1200), messages: 2},
		{name: "a shorter last", sizes: append(repeat(10, 1200), 700), messages: 1},
		{name: "a shorter one ends the message", sizes: append(append(repeat(3, 1200), 700), 1200), messages: 2},
		{name: "past an Ethernet frame, alone", sizes: repeat(3, 2000), messages: 3},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		w, counters, peer, to := writerPair(t)
		w.begin()
		for _, size := range c.sizes {
			if n, err := w.reply(make([]byte, size), to); n != size || err != nil {
				t.Fatalf("reply = %d, %v, want %d queued", n, err, size)
			}
		}
		if got := w.build(0); got != c.messages {
			t.Fatalf("build laid %d replies out as %d messages, want %d", len(c.sizes), got, c.messages)
		}

		w.flush()

		got := receive(t, peer)
		if len(got) != len(c.sizes) {
			t.Fatalf("the peer received %d datagrams, want %d", len(got), len(c.sizes))
		}
		for i := range got {
			if got[i] != c.sizes[i] {
				t.Fatalf("datagram %d is %d bytes, want %d", i, got[i], c.sizes[i])
			}
		}
		if counters.gsoOversized.Load() != 0 || counters.replyFailures.Load() != 0 {
			t.Fatalf("oversized=%d failures=%d, want none", counters.gsoOversized.Load(), counters.replyFailures.Load())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_multiWriter_reply_outsideBatch pins that a reply outside a batch is
// written at once, with the write's own answer.
func Test_multiWriter_reply_outsideBatch(t *testing.T) {
	t.Parallel()
	w, _, peer, to := writerPair(t)

	if n, err := w.reply([]byte("now"), to); n != 3 || err != nil {
		t.Fatalf("reply = %d, %v", n, err)
	}

	if got := receive(t, peer); len(got) != 1 || len(w.queue) != 0 {
		t.Fatalf("received %v, queued %d, want one at once", got, len(w.queue))
	}
}

// scripted is a sendmmsg that answers errno for every GSO message, and sends
// at most perCall messages a call; it records each message it took.
type scripted struct {
	// errno is the answer to a message carrying UDP_SEGMENT.
	errno syscall.Errno
	// perCall bounds what one call takes; zero takes all.
	perCall int
	// took records how many replies each sent message carried.
	took []int
}

// send returns the multiWriter.send that w's flush drives.
func (s *scripted) send(w *multiWriter) func(uintptr, []mmsghdr) (int, syscall.Errno) {
	return func(_ uintptr, headers []mmsghdr) (int, syscall.Errno) {
		// headers is w.headers from the first message not taken yet.
		first := len(w.headers) - cap(headers)
		sent := 0
		for i := range headers {
			if s.perCall > 0 && sent == s.perCall {
				break
			}
			if s.errno != 0 && kernelLen(headers[i].hdr.Controllen) != 0 {
				if sent == 0 {
					return 0, s.errno
				}
				break
			}
			s.took = append(s.took, w.spans[first+i].count)
			sent++
		}
		return sent, 0
	}
}

// Test_multiWriter_sendAll pins what each refusal means: EIO switches GSO off
// for the socket, reported once, and every reply leaves alone; EINVAL sends
// that span alone and leaves GSO on; EMSGSIZE counts a sizing bug, drops the
// span's replies as failures and leaves GSO on; and a call that takes fewer
// messages than asked is resumed from its count, each message sent once.
func Test_multiWriter_sendAll(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// errno and perCall script sendmmsg.
		errno   syscall.Errno
		perCall int
		// wantTook is the datagrams each sent message carried, in order.
		wantTook []int
		// wantGSO, wantOff, wantOversized and wantFailures are what follows.
		wantGSO       bool
		wantOff       int
		wantOversized uint64
		wantFailures  uint64
	}
	tests := []tc{
		{name: "no offload (EIO)", errno: syscall.EIO, wantTook: []int{1, 1, 1, 1, 1}, wantOff: 1},
		{name: "past the MTU (EINVAL)", errno: syscall.EINVAL, wantTook: []int{1, 1, 1, 1, 1}, wantGSO: true},
		{name: "past the IP length (EMSGSIZE)", errno: syscall.EMSGSIZE, wantTook: []int{1}, wantGSO: true, wantOversized: 1, wantFailures: 4},
		{name: "one message a call", perCall: 1, wantTook: []int{4, 1}, wantGSO: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		w, counters, _, to := writerPair(t)
		off := 0
		w.onGSOOff = func() { off++ }
		s := &scripted{errno: c.errno, perCall: c.perCall}
		w.send = s.send(w)
		w.begin()
		//: four of one size to one peer, one GSO message, and one to another.
		for range 4 {
			_, _ = w.reply(make([]byte, 1000), to)
		}
		_, _ = w.reply(make([]byte, 1000), &stdnet.UDPAddr{IP: to.IP, Port: to.Port + 1})

		w.flush()

		if len(s.took) != len(c.wantTook) {
			t.Fatalf("sent messages carrying %v, want %v", s.took, c.wantTook)
		}
		for i := range s.took {
			if s.took[i] != c.wantTook[i] {
				t.Fatalf("sent messages carrying %v, want %v", s.took, c.wantTook)
			}
		}
		if w.gso.Load() != c.wantGSO || off != c.wantOff {
			t.Fatalf("gso=%v reported off %d times, want gso=%v and %d", w.gso.Load(), off, c.wantGSO, c.wantOff)
		}
		if counters.gsoOversized.Load() != c.wantOversized || counters.replyFailures.Load() != c.wantFailures {
			t.Fatalf("oversized=%d failures=%d, want %d and %d",
				counters.gsoOversized.Load(), counters.replyFailures.Load(), c.wantOversized, c.wantFailures)
		}
		if len(w.queue) != 0 || w.used != 0 || w.plainUntil != 0 {
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

// Test_multiWriter_encode pins the addressing: a peer of the socket's family,
// an IPv4 one mapped on IPv6, and WriteTo's for the rest.
func Test_multiWriter_encode(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// family is the socket's.
		family int
		// to is the peer.
		to stdnet.Addr
		// want is whether the sink addresses it itself.
		want bool
		// namelen is the sockaddr's length when it does.
		namelen uint32
	}
	v4 := &stdnet.UDPAddr{IP: stdnet.IPv4(127, 0, 0, 1), Port: 4000}
	tests := []tc{
		{name: "IPv4 on IPv4", family: syscall.AF_INET, to: v4, want: true, namelen: syscall.SizeofSockaddrInet4},
		{name: "IPv4 on IPv6, mapped", family: syscall.AF_INET6, to: v4, want: true, namelen: syscall.SizeofSockaddrInet6},
		{name: "IPv6 on IPv4", family: syscall.AF_INET, to: &stdnet.UDPAddr{IP: stdnet.IPv6loopback, Port: 1}},
		{name: "a zoned peer", family: syscall.AF_INET6, to: &stdnet.UDPAddr{IP: stdnet.ParseIP("fe80::1"), Zone: "lo"}},
		{name: "not UDP", family: syscall.AF_INET, to: &stdnet.UnixAddr{Name: "x", Net: "unixgram"}},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w := &multiWriter{family: c.family}
			var e pendingReply
			if got := w.encode(c.to, &e); got != c.want || (got && e.namelen != c.namelen) {
				t.Fatalf("encode = %v namelen %d, want %v and %d", got, e.namelen, c.want, c.namelen)
			}
			if c.want && (e.name.Port>>8|e.name.Port<<8) != 4000 {
				t.Fatalf("port %d, want 4000 in network order", e.name.Port)
			}
		})
	}
}
