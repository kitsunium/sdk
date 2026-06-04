//go:build integration

package encwrite_test

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"slices"
	"sync"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	corelogger "github.com/kitsunium/sdk/internal/core/logger"

	_ "github.com/kitsunium/sdk/internal/service/crypto/aesgcm"
	_ "github.com/kitsunium/sdk/internal/service/crypto/hkdfsha256"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/encwrite"
)

// tcpSink is a terminal Sink that writes each framed sealed box to a live TCP
// connection — the real I/O boundary the EncWriter feeds in production.
type tcpSink struct {
	// conn is the live client connection to the in-process echo server.
	conn net.Conn
}

func (s *tcpSink) Write(_ context.Context, _ corelogger.RecordEvent, p []byte) (int, error) {
	//: ship the framed sealed box across the real socket.
	return s.conn.Write(p)
}

func (s *tcpSink) Flush(_ context.Context) error {
	//: a TCP stream has nothing buffered above the kernel send buffer.
	return nil
}

func (s *tcpSink) Close() error {
	//: closing the connection signals EOF to the server-side reader.
	return s.conn.Close()
}

func Test_EncWriter_Integration_RealSocket(t *testing.T) {
	t.Parallel()
	//: a real in-process TCP listener is the end-to-end I/O boundary.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	//: the loopback listener must bind for the round-trip to run.
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	plaintext := []byte("end-to-end record over a real socket")
	info := "integration"

	//: the server collects every box it receives until the client closes.
	var (
		wg    sync.WaitGroup
		boxes [][]byte
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv, aerr := ln.Accept()
		//: a failed Accept would strand the round-trip; surface it.
		if aerr != nil {
			t.Errorf("Accept: %v", aerr)
			return
		}
		defer func() { _ = srv.Close() }()
		//: drain framed boxes until the client closes the connection.
		for {
			prefix := make([]byte, 4)
			if _, rerr := io.ReadFull(srv, prefix); rerr != nil {
				//: EOF after the final frame is the normal termination.
				return
			}
			box := make([]byte, binary.BigEndian.Uint32(prefix))
			if _, rerr := io.ReadFull(srv, box); rerr != nil {
				t.Errorf("read box: %v", rerr)
				return
			}
			boxes = append(boxes, box)
		}
	}()

	conn, derr := net.Dial("tcp", ln.Addr().String())
	//: the client must connect for the production Write to have a destination.
	if derr != nil {
		t.Fatalf("Dial: %v", derr)
	}

	s, nerr := encwrite.NewEncWriter(encwrite.Config{Sink: &tcpSink{conn: conn}, Key: newKey(t), Info: info})
	//: a valid config must construct without error.
	if nerr != nil {
		t.Fatalf("NewEncWriter: %v", nerr)
	}
	//: drive the production Write end-to-end across the real socket.
	if _, werr := s.Write(t.Context(), corelogger.RecordEvent{}, plaintext); werr != nil {
		t.Fatalf("Write: %v", werr)
	}
	//: Close zeroizes the keys and closes the connection, signalling EOF.
	if cerr := s.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	wg.Wait()

	//: exactly one framed box must have crossed the wire.
	if len(boxes) != 1 {
		t.Fatalf("boxes=%d want=1", len(boxes))
	}
	//: re-derive the same subkey to open the box the server received.
	raw, kdferr := corecrypto.Subkey("hkdf-sha256", newKey(t).Bytes(), nil, info, corecrypto.KeyLen)
	//: re-derivation must reproduce the sealing subkey.
	if kdferr != nil {
		t.Fatalf("Subkey: %v", kdferr)
	}
	subkey, kerr := corecrypto.NewKey(raw)
	//: the derived subkey must materialise as a Key.
	if kerr != nil {
		t.Fatalf("NewKey: %v", kerr)
	}
	out, oerr := corecrypto.Open(subkey, boxes[0], nil)
	//: opening the wire box must recover the exact plaintext that was written.
	if oerr != nil || !slices.Equal(out, plaintext) {
		t.Fatalf("Open=(%q,%v) want (%q,nil)", out, oerr, plaintext)
	}
}
