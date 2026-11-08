package dbus

import (
	"bufio"
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fake bus: the other end of a net.Pipe that plays dbus-daemon's part of
// the protocol, byte for byte — the authentication, then each method call
// read and answered with replies and signals the test writes in either byte
// order, with an encoder of its own, apart from the client's.

// fakeBus is the bus's end of one connection.
type fakeBus struct {
	t      *testing.T
	nc     net.Conn
	r      *bufio.Reader
	serial uint32
	mu     sync.Mutex
}

// call is one method call the fake bus read.
type call struct {
	m    *message
	args []string
	u    uint32
}

// pipeConn opens a client over a pipe whose other end is the fake bus,
// which authenticates it and answers Hello with unique: the client, and the
// bus to drive.
func pipeConn(t *testing.T, unique string) (*Conn, *fakeBus) {
	t.Helper()
	client, server := net.Pipe()
	b := &fakeBus{t: t, nc: server, r: bufio.NewReader(server), serial: 1000}
	t.Cleanup(func() { _ = server.Close() })
	done := make(chan error, 1)
	go func() {
		if err := b.accept("OK 0123456789abcdef0123456789abcdef"); err != nil {
			done <- err
			return
		}
		hello := b.next()
		if hello.m.member != "Hello" {
			b.t.Errorf("first call %q, want Hello", hello.m.member)
		}
		b.reply(hello, binary.BigEndian, "s", unique)
		done <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := open(ctx, client)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the fake bus: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, b
}

// accept reads the credentials byte and AUTH EXTERNAL with no identity, asks
// for one and reads the empty DATA, answers answer, and reads BEGIN when it
// was OK.
func (b *fakeBus) accept(answer string) error {
	return b.acceptAs(true, answer)
}

// acceptAs is accept; without ask the bus answers the AUTH line itself.
func (b *fakeBus) acceptAs(ask bool, answer string) error {
	nul, err := b.r.ReadByte()
	if err != nil || nul != 0 {
		return errLine("credentials byte", strconv.Itoa(int(nul)))
	}
	line, err := b.r.ReadString('\n')
	if err != nil || line != "AUTH EXTERNAL\r\n" {
		return errLine("auth", line)
	}
	if ask {
		if _, err := b.nc.Write([]byte("DATA\r\n")); err != nil {
			return err
		}
		line, err = b.r.ReadString('\n')
		if err != nil || line != "DATA\r\n" {
			return errLine("data", line)
		}
	}
	if _, err := b.nc.Write([]byte(answer + "\r\n")); err != nil {
		return err
	}
	if !strings.HasPrefix(answer, "OK ") {
		return nil
	}
	line, err = b.r.ReadString('\n')
	if err != nil || line != "BEGIN\r\n" {
		return errLine("begin", line)
	}
	return nil
}

// lineError is an unexpected line of the authentication.
type lineError struct{ what, got string }

func (e *lineError) Error() string { return e.what + ": got " + strconv.Quote(e.got) }

func errLine(what, got string) error { return &lineError{what, got} }

// next reads the next method call, failing the test on anything else.
func (b *fakeBus) next() call {
	b.t.Helper()
	m, err := readMessage(b.r)
	if err != nil {
		b.t.Fatalf("the fake bus read: %v", err)
	}
	if m.typ != typeMethodCall || m.dest != busDriver || m.path != busPath || m.iface != busInterface {
		b.t.Fatalf("a call to %q %q %q of type %d", m.dest, m.path, m.iface, m.typ)
	}
	c := call{m: m}
	d := &decoder{b: m.body, order: m.order}
	for i := range len(m.sig) {
		switch m.sig[i] {
		case 's':
			s, ok := d.string()
			if !ok {
				b.t.Fatalf("%s: a string argument that does not parse", m.member)
			}
			c.args = append(c.args, s)
		case 'u':
			v, ok := d.u32()
			if !ok {
				b.t.Fatalf("%s: a uint32 argument that does not parse", m.member)
			}
			c.u = v
		}
	}
	return c
}

// write sends raw bytes to the client.
func (b *fakeBus) write(p []byte) {
	b.t.Helper()
	if _, err := b.nc.Write(p); err != nil {
		b.t.Errorf("the fake bus write: %v", err)
	}
}

// reply answers c with a method return whose body is sig's values.
func (b *fakeBus) reply(c call, order binary.ByteOrder, sig string, values ...any) {
	b.write(b.encode(order, typeMethodReturn, c.m.serial, "", sig, values...))
}

// fail answers c with an error message.
func (b *fakeBus) fail(c call, name string) {
	b.write(b.encode(binary.LittleEndian, typeError, c.m.serial, name, "s", "refused"))
}

// ownerChanged announces NameOwnerChanged from sender.
func (b *fakeBus) ownerChanged(order binary.ByteOrder, sender, name, old, owner string) {
	p := b.encodeFrom(order, typeSignal, 0, "", sender, "sss", name, old, owner)
	b.write(p)
}

// encode writes a message from the bus driver.
func (b *fakeBus) encode(order binary.ByteOrder, typ byte, replySerial uint32, errName, sig string, values ...any) []byte {
	return b.encodeFrom(order, typ, replySerial, errName, busDriver, sig, values...)
}

// encodeFrom writes a message: the test's own encoder, in either byte order.
func (b *fakeBus) encodeFrom(order binary.ByteOrder, typ byte, replySerial uint32, errName, sender, sig string, values ...any) []byte {
	b.mu.Lock()
	b.serial++
	serial := b.serial
	b.mu.Unlock()
	w := &testWriter{order: order}
	mark := byte('l')
	if order == binary.BigEndian {
		mark = 'B'
	}
	w.raw(mark, typ, 0, 1)
	w.u32(0)
	w.u32(serial)
	w.u32(0)
	field := func(code byte, vt byte, write func()) {
		w.align(8)
		w.raw(code, 1, vt, 0)
		write()
	}
	if typ == typeSignal {
		field(fieldPath, 'o', func() { w.str(busPath) })
		field(fieldInterface, 's', func() { w.str(busInterface) })
		field(fieldMember, 's', func() { w.str("NameOwnerChanged") })
	}
	if replySerial != 0 {
		field(fieldReplySerial, 'u', func() { w.u32(replySerial) })
	}
	if errName != "" {
		field(fieldErrorName, 's', func() { w.str(errName) })
	}
	field(fieldSender, 's', func() { w.str(sender) })
	if sig != "" {
		field(fieldSignature, 'g', func() { w.raw(byte(len(sig))); w.raw([]byte(sig)...); w.raw(0) })
	}
	order.PutUint32(w.b[12:], uint32(len(w.b)-16))
	w.align(8)
	start := len(w.b)
	for _, v := range values {
		switch v := v.(type) {
		case string:
			w.str(v)
		case uint32:
			w.u32(v)
		}
	}
	order.PutUint32(w.b[4:], uint32(len(w.b)-start))
	return w.b
}

// testWriter is the test's encoder.
type testWriter struct {
	order binary.ByteOrder
	b     []byte
}

func (w *testWriter) raw(p ...byte) { w.b = append(w.b, p...) }

func (w *testWriter) align(n int) {
	for len(w.b)%n != 0 {
		w.b = append(w.b, 0)
	}
}

func (w *testWriter) u32(v uint32) {
	w.align(4)
	var p [4]byte
	w.order.PutUint32(p[:], v)
	w.b = append(w.b, p[:]...)
}

func (w *testWriter) str(s string) {
	w.u32(uint32(len(s)))
	w.b = append(w.b, s...)
	w.b = append(w.b, 0)
}
