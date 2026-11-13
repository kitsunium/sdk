// Package server — the copies a stream connection hands to its socket (#970).
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"io"
	stdnet "net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// copyingSocket is a net.Conn with its own ReadFrom and WriteTo, counting them:
// a stand-in for the *net.TCPConn whose methods choose splice and sendfile.
type copyingSocket struct {
	fakeSocket
	// readFroms and writeTos count the copies that reached the socket.
	readFroms, writeTos int
	// taken is what the socket's ReadFrom consumed.
	taken bytes.Buffer
	// source is what the socket's WriteTo sends.
	source []byte
}

// ReadFrom implements io.ReaderFrom.
func (s *copyingSocket) ReadFrom(r io.Reader) (n int64, err error) {
	s.readFroms++
	//: the bytes reach the socket's own copy.
	return io.Copy(&s.taken, r)
}

// WriteTo implements io.WriterTo.
func (s *copyingSocket) WriteTo(w io.Writer) (n int64, err error) {
	s.writeTos++
	written, err := w.Write(s.source)
	//: the socket's own copy.
	return int64(written), err
}

// bytesSocket is a net.Conn with neither copy, reading a fixed source and
// keeping what is written.
type bytesSocket struct {
	fakeSocket
	// source is what Read returns, then io.EOF.
	source *bytes.Reader
	// sink keeps what Write receives.
	sink bytes.Buffer
}

// Read implements net.Conn.
func (s *bytesSocket) Read(p []byte) (n int, err error) {
	//: the fixed source, then its end.
	return s.source.Read(p)
}

// Write implements net.Conn.
func (s *bytesSocket) Write(p []byte) (n int, err error) {
	//: kept for the comparison.
	return s.sink.Write(p)
}

// onlyReader hides a source's WriteTo, as a socket or a pipe has none, so
// io.Copy asks the destination's ReadFrom.
type onlyReader struct {
	io.Reader
}

// acquiredOver hands sock to the engine as a group with these timeouts would.
func acquiredOver(t *testing.T, sock stdnet.Conn, timeouts corenet.TimeoutsValue) *conn {
	t.Helper()
	s := New()
	group := &StreamGroup{name: "copy", timeouts: timeouts}
	c := s.acquire(sock, group)
	t.Cleanup(func() { s.release(c) })
	//: as a handler receives it.
	return c
}

// TestConnForwardsZeroCopy pins that a copy in each direction reaches the
// socket's own method once, so the standard library can choose its zero-copy
// path, and that no deadline is installed for it on a group with no bound.
func TestConnForwardsZeroCopy(t *testing.T) {
	sock := &copyingSocket{source: []byte("outbound")}
	c := acquiredOver(t, sock, corenet.TimeoutsValue{})
	if n, err := io.Copy(c, onlyReader{strings.NewReader("inbound")}); err != nil || n != 7 {
		t.Fatalf("io.Copy(c, r) = %d, %v", n, err)
	}
	var out bytes.Buffer
	if n, err := io.Copy(&out, c); err != nil || n != 8 {
		t.Fatalf("io.Copy(w, c) = %d, %v", n, err)
	}
	if sock.readFroms != 1 || sock.writeTos != 1 {
		t.Fatalf("socket saw %d ReadFrom and %d WriteTo, want one each", sock.readFroms, sock.writeTos)
	}
	if sock.taken.String() != "inbound" || out.String() != "outbound" {
		t.Fatalf("copied %q and %q", sock.taken.String(), out.String())
	}
	if sock.reads() != 0 || sock.writes() != 0 {
		t.Fatalf("no bound configured, yet %d read and %d write deadlines were installed", sock.reads(), sock.writes())
	}
}

// TestConnGenericCopy pins that a socket with neither method is served by the
// generic copy, byte-exact.
func TestConnGenericCopy(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789"), 20_000)
	sock := &bytesSocket{source: bytes.NewReader(payload)}
	c := acquiredOver(t, sock, corenet.TimeoutsValue{})
	if n, err := io.Copy(c, bytes.NewReader(payload)); err != nil || n != int64(len(payload)) {
		t.Fatalf("io.Copy(c, r) = %d, %v", n, err)
	}
	var out bytes.Buffer
	if n, err := io.Copy(&out, c); err != nil || n != int64(len(payload)) {
		t.Fatalf("io.Copy(w, c) = %d, %v", n, err)
	}
	if !bytes.Equal(sock.sink.Bytes(), payload) || !bytes.Equal(out.Bytes(), payload) {
		t.Fatal("the generic copy changed the bytes")
	}
}

// TestConnCopyKeepsPerOperationBound pins that a bounded direction stays
// bounded per operation: the copy goes one write or read at a time, each under
// its own deadline, and never hands the socket one deadline for the whole of
// it — the promise TimeoutsValue makes.
func TestConnCopyKeepsPerOperationBound(t *testing.T) {
	sock := &copyingSocket{source: []byte("outbound")}
	c := acquiredOver(t, sock, corenet.TimeoutsValue{
		Write: corenet.DurationValue(time.Minute),
		Read:  corenet.DurationValue(time.Minute),
	})
	payload := bytes.Repeat([]byte("x"), 100_000)
	if n, err := c.ReadFrom(onlyReader{bytes.NewReader(payload)}); err != nil || n != int64(len(payload)) {
		t.Fatalf("ReadFrom = %d, %v", n, err)
	}
	if sock.readFroms != 0 {
		t.Fatalf("a bounded direction reached the socket's ReadFrom %d times", sock.readFroms)
	}
	if got := sock.writes(); got < 2 {
		t.Fatalf("%d write deadlines for a copy of several writes, want one a write", got)
	}
	sock.fakeSocket.readErr = io.EOF
	var out bytes.Buffer
	if _, err := c.WriteTo(&out); err != nil {
		t.Fatalf("WriteTo = %v", err)
	}
	if sock.writeTos != 0 || sock.reads() == 0 {
		t.Fatalf("a bounded read direction: %d WriteTo, %d read deadlines", sock.writeTos, sock.reads())
	}
}

// TestConnBoundedCopyFromAFile pins the bounded copy of a source the kernel
// sends itself (#970): a file goes through the socket's own ReadFrom — where
// a *net.TCPConn chooses sendfile —, one step of zeroCopyStep a write
// deadline, byte-exact; the same file under an io.LimitedReader stops at the
// limit; a source in user space still goes one bounded write at a time
// (TestConnCopyKeepsPerOperationBound).
func TestConnBoundedCopyFromAFile(t *testing.T) {
	size := int(2*zeroCopyStep + zeroCopyStep/2)
	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("random: %v", err)
	}
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, tc := range []struct {
		name  string
		limit int64
		steps int
	}{{"whole", -1, 3}, {"limited", zeroCopyStep + 7, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			sock := &copyingSocket{}
			c := acquiredOver(t, sock, corenet.TimeoutsValue{Write: corenet.DurationValue(time.Minute)})
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer func() { swallowErr(f.Close()) }()
			var src io.Reader = f
			want := payload
			if tc.limit >= 0 {
				src, want = io.LimitReader(f, tc.limit), payload[:tc.limit]
			}
			if n, err := c.ReadFrom(src); err != nil || n != int64(len(want)) {
				t.Fatalf("ReadFrom = %d, %v", n, err)
			}
			if !bytes.Equal(sock.taken.Bytes(), want) {
				t.Fatalf("the socket took %d bytes, not the source's %d", sock.taken.Len(), len(want))
			}
			if sock.readFroms != tc.steps || sock.writes() != tc.steps {
				t.Fatalf("%d socket copies under %d write deadlines, want %d of each", sock.readFroms, sock.writes(), tc.steps)
			}
		})
	}
}

// serveOneConn runs a one-handler TCP group and returns its address.
func serveOneConn(t *testing.T, handle func(c corenet.Conn) error, opts ...GroupOption) string {
	t.Helper()
	srv := New()
	all := append([]GroupOption{Listen("tcp", "127.0.0.1:0")}, opts...)
	srv.Group("copy", all...).HandleFunc(func(_ context.Context, c corenet.Conn) error {
		//: the test's body is the handler.
		return handle(c)
	})
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	//: the port the kernel chose.
	return srv.State().Listeners[0].Address
}

// dialTimed connects with a bounded lifetime, closed by the test's cleanup.
func dialTimed(t *testing.T, addr string) *stdnet.TCPConn {
	t.Helper()
	raw, err := stdnet.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { swallowErr(raw.Close()) })
	if err := raw.SetDeadline(time.Now().Add(60 * time.Second)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	socket, ok := raw.(*stdnet.TCPConn)
	if !ok {
		t.Fatalf("dial returned a %T", raw)
	}
	//: the test's client.
	return socket
}

// TestConnCopiesAFile pins a file copied to a plaintext TCP peer byte-exact.
func TestConnCopiesAFile(t *testing.T) {
	const size = 64 << 20
	payload := make([]byte, 1<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("random: %v", err)
	}
	path := filepath.Join(t.TempDir(), "file")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for range size / len(payload) {
		if _, err := f.Write(payload); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	addr := serveOneConn(t, func(c corenet.Conn) error {
		file, oerr := os.Open(path)
		if oerr != nil {
			//: reported by the short read below.
			return oerr
		}
		defer func() { swallowErr(file.Close()) }()
		_, cerr := io.Copy(c, file)
		//: the copy's outcome.
		return cerr
	})
	client := dialTimed(t, addr)
	got := make([]byte, len(payload))
	for i := range size / len(payload) {
		if _, err := io.ReadFull(client, got); err != nil {
			t.Fatalf("read block %d: %v", i, err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("block %d differs", i)
		}
	}
	if n, err := client.Read(got); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("after the file: %d, %v", n, err)
	}
}

// TestConnEchoes pins the documented echo idiom over a real socket.
func TestConnEchoes(t *testing.T) {
	addr := serveOneConn(t, func(c corenet.Conn) error {
		_, err := io.Copy(c, c)
		//: the copy's outcome; the peer's close ends it.
		return err
	})
	client := dialTimed(t, addr)
	payload := bytes.Repeat([]byte("echo!"), 50_000)
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		if _, err := client.Write(payload); err != nil {
			t.Errorf("write: %v", err)
		}
		if err := client.CloseWrite(); err != nil {
			t.Errorf("close write: %v", err)
		}
	}()
	got, err := io.ReadAll(client)
	wait.Wait()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("echoed %d bytes, sent %d", len(got), len(payload))
	}
}

// TestConnCopyToAStalledPeer pins that a copy to a peer that stopped reading
// fails on the group's write bound, and the connection then closes.
func TestConnCopyToAStalledPeer(t *testing.T) {
	outcome := make(chan error, 1)
	addr := serveOneConn(t, func(c corenet.Conn) error {
		_, err := c.(io.ReaderFrom).ReadFrom(endless{})
		outcome <- err
		//: the bounded write's failure.
		return err
	}, WriteTimeout(200*time.Millisecond))
	client := dialTimed(t, addr)
	select {
	case err := <-outcome:
		var timeout stdnet.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("ReadFrom ended with %v, want a timeout", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the copy to a stalled peer never ended")
	}
	swallowErr(client.SetReadDeadline(time.Now().Add(20 * time.Second)))
	if _, err := io.Copy(io.Discard, client); err != nil && !errors.Is(err, io.EOF) {
		var opErr *stdnet.OpError
		if !errors.As(err, &opErr) {
			t.Fatalf("the peer's read ended with %v", err)
		}
	}
}

// TestConnBoundedFileCopyToAStalledPeer is TestConnCopyToAStalledPeer with a
// file, which a bounded group now sends by sendfile a step at a time: a peer
// that stopped reading still ends the copy on the write bound.
func TestConnBoundedFileCopyToAStalledPeer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	//: sparse, read as zeros: far more than every buffer between the peers.
	if err := f.Truncate(256 << 20); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	swallowErr(f.Close())
	outcome := make(chan error, 1)
	addr := serveOneConn(t, func(c corenet.Conn) error {
		file, oerr := os.Open(path)
		if oerr != nil {
			outcome <- oerr
			//: reported below.
			return oerr
		}
		defer func() { swallowErr(file.Close()) }()
		_, cerr := io.Copy(c, file)
		outcome <- cerr
		//: the bounded write's failure.
		return cerr
	}, WriteTimeout(200*time.Millisecond))
	client := dialTimed(t, addr)
	select {
	case err := <-outcome:
		var timeout stdnet.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("the file copy ended with %v, want a timeout", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the file copy to a stalled peer never ended")
	}
	swallowErr(client.Close())
}

// endless is a source that never ends.
type endless struct{}

// Read implements io.Reader.
func (endless) Read(p []byte) (n int, err error) {
	clear(p)
	//: always full.
	return len(p), nil
}

// TestConnUnwrapReachesWritev pins that net.Buffers on the socket Unwrap
// returns reaches writev on a plaintext TCP group: one read sees both slices.
func TestConnUnwrapReachesWritev(t *testing.T) {
	head, body := []byte("head|"), []byte("body")
	addr := serveOneConn(t, func(c corenet.Conn) error {
		unwrapper, ok := c.(corenet.ConnUnwrapper)
		if !ok {
			t.Errorf("%T does not implement ConnUnwrapper", c)
			//: nothing to write through.
			return nil
		}
		socket := unwrapper.Unwrap()
		tcp, isTCP := socket.(*stdnet.TCPConn)
		if !isTCP {
			t.Errorf("Unwrap returned a %T, want *net.TCPConn", socket)
			//: nothing to write through.
			return nil
		}
		swallowErr(tcp.SetNoDelay(true))
		buffers := stdnet.Buffers{head, body}
		_, err := buffers.WriteTo(tcp)
		//: one writev.
		return err
	})
	client := dialTimed(t, addr)
	got := make([]byte, 64)
	n, err := client.Read(got)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got[:n]) != "head|body" {
		t.Fatalf("the first read returned %q, want head and body together", got[:n])
	}
}

// TestTrackedConnCopies pins that the close tracker forwards both copies.
func TestTrackedConnCopies(t *testing.T) {
	sock := &copyingSocket{source: []byte("out")}
	tracked := &trackedConn{Conn: sock}
	if _, err := io.Copy(tracked, onlyReader{strings.NewReader("in")}); err != nil || sock.readFroms != 1 {
		t.Fatalf("ReadFrom: %v, %d calls", err, sock.readFroms)
	}
	var out bytes.Buffer
	if _, err := io.Copy(&out, tracked); err != nil || sock.writeTos != 1 {
		t.Fatalf("WriteTo: %v, %d calls", err, sock.writeTos)
	}
	if tracked.Unwrap() != stdnet.Conn(sock) {
		t.Fatalf("Unwrap returned %T", tracked.Unwrap())
	}
	generic := &trackedConn{Conn: &bytesSocket{source: bytes.NewReader([]byte("plain"))}}
	var plain bytes.Buffer
	if _, err := io.Copy(&plain, generic); err != nil || plain.String() != "plain" {
		t.Fatalf("generic WriteTo: %q, %v", plain.String(), err)
	}
}

// TestConnUnwrapOnTLS pins that a TLS group's Unwrap is its *tls.Conn and that
// net.Buffers through it writes byte-exact.
func TestConnUnwrapOnTLS(t *testing.T) {
	id := testIdentity(t)
	addr := serveOneConn(t, func(c corenet.Conn) error {
		socket := c.(corenet.ConnUnwrapper).Unwrap()
		if _, isTLS := socket.(*tls.Conn); !isTLS {
			t.Errorf("Unwrap returned a %T on a TLS group, want *tls.Conn", socket)
		}
		buffers := stdnet.Buffers{[]byte("head|"), []byte("body")}
		_, err := buffers.WriteTo(socket)
		//: one record per slice, through crypto/tls.
		return err
	}, TLS(id))
	raw := dialTimed(t, addr)
	client := tls.Client(raw, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // a test's self-signed peer.
	got, err := io.ReadAll(client)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "head|body" {
		t.Fatalf("read %q", got)
	}
}
