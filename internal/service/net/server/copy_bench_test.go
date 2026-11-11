package server_test

import (
	"context"
	"io"
	stdnet "net"
	"os"
	"path/filepath"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// benchFileBytes is the file BenchmarkCopyFile sends, the size #970 names.
const benchFileBytes int64 = 64 << 20

// plainConn hides everything but the net.Conn methods, which is the handler's
// view of a connection before #970: every io.Copy ran the 32 KiB loop.
type plainConn struct {
	stdnet.Conn
}

// benchFile writes the file a copy benchmark sends.
func benchFile(b *testing.B) string {
	b.Helper()
	path := filepath.Join(b.TempDir(), "file")
	f, err := os.Create(path)
	if err != nil {
		b.Fatalf("create: %v", err)
	}
	if terr := f.Truncate(benchFileBytes); terr != nil {
		b.Fatalf("truncate: %v", terr)
	}
	if cerr := f.Close(); cerr != nil {
		b.Fatalf("close: %v", cerr)
	}
	//: a sparse file: the kernel reads zeros, so the bench measures the path.
	return path
}

// startCopyServer runs a raw TCP group over handle and dials it once.
func startCopyServer(b *testing.B, handle func(c corenet.Conn) error) stdnet.Conn {
	b.Helper()
	srv := server.New()
	srv.Group("bench", server.Listen("tcp", "127.0.0.1:0")).
		HandleFunc(func(_ context.Context, c corenet.Conn) error {
			//: the copy under measure.
			return handle(c)
		})
	if err := srv.Start(b.Context()); err != nil {
		b.Fatalf("start: %v", err)
	}
	b.Cleanup(func() { discard(srv.Close()) })
	c, err := stdnet.Dial("tcp", srv.State().Listeners[0].Address)
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	b.Cleanup(func() { discard(c.Close()) })
	//: the benchmark's one connection.
	return c
}

// benchCopyFile serves one file a round on a connection the handler holds.
func benchCopyFile(b *testing.B, path string, wrap func(corenet.Conn) io.Writer) {
	b.Helper()
	rounds := make(chan struct{})
	client := startCopyServer(b, func(c corenet.Conn) error {
		dst := wrap(c)
		for range rounds {
			f, err := os.Open(path)
			if err != nil {
				//: reported by the client's short read.
				return err
			}
			_, cerr := io.Copy(dst, f)
			discard(f.Close())
			if cerr != nil {
				//: reported by the client's short read.
				return cerr
			}
		}
		//: the benchmark ended.
		return nil
	})
	b.Cleanup(func() { close(rounds) })
	buf := make([]byte, 1<<20)
	b.SetBytes(benchFileBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		rounds <- struct{}{}
		left := benchFileBytes
		for left > 0 {
			n, err := client.Read(buf[:min(left, int64(len(buf)))])
			if err != nil {
				b.Fatalf("read: %v", err)
			}
			left -= int64(n)
		}
	}
}

// BenchmarkCopyFile measures io.Copy(c, f) of 64 MiB to a loopback peer through
// a handler: sendfile since #970.
func BenchmarkCopyFile(b *testing.B) {
	path := benchFile(b)
	b.Run("after_ReadFrom", func(b *testing.B) {
		benchCopyFile(b, path, func(c corenet.Conn) io.Writer {
			//: the connection as handed out.
			return c
		})
	})
	b.Run("before_generic", func(b *testing.B) {
		benchCopyFile(b, path, func(c corenet.Conn) io.Writer {
			//: the connection with its copies hidden.
			return plainConn{Conn: c}
		})
	})
}

// benchEcho runs n bytes through the connection's echo, per round.
func benchEcho(b *testing.B, echo func(corenet.Conn) error) {
	b.Helper()
	client := startCopyServer(b, echo)
	payload := make([]byte, 1<<20)
	got := make([]byte, len(payload))
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errs := make(chan error, 1)
		go func() {
			_, err := client.Write(payload)
			errs <- err
		}()
		if _, err := io.ReadFull(client, got); err != nil {
			b.Fatalf("read: %v", err)
		}
		if err := <-errs; err != nil {
			b.Fatalf("write: %v", err)
		}
	}
}

// BenchmarkEcho measures io.Copy(c, c) between two sockets of one connection.
func BenchmarkEcho(b *testing.B) {
	b.Run("after_splice", func(b *testing.B) {
		benchEcho(b, func(c corenet.Conn) error {
			_, err := io.Copy(c, c)
			//: the peer's close ends it.
			return err
		})
	})
	b.Run("before_generic", func(b *testing.B) {
		benchEcho(b, func(c corenet.Conn) error {
			plain := plainConn{Conn: c}
			_, err := io.Copy(plain, plain)
			//: the peer's close ends it.
			return err
		})
	})
}
