package streamaead_test

import (
	"bytes"
	"io"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const streamAlgo corecrypto.Algorithm = "aes-256-gcm-stream"

func streamKey(t *testing.T, b byte) corecrypto.Key {
	t.Helper()
	//: deterministic 32-byte key for repeatable streams.
	key, err := corecrypto.NewKey(bytes.Repeat([]byte{b}, corecrypto.KeyLen))
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	return key
}

// sealStream runs a plaintext through SealStream and returns the wire bytes.
func sealStream(t *testing.T, key corecrypto.Key, pt, aad []byte) []byte {
	t.Helper()
	var dst bytes.Buffer
	w, err := corecrypto.SealStream(streamAlgo, key, &dst, aad)
	if err != nil {
		t.Fatalf("SealStream: %v", err)
	}
	if _, werr := w.Write(pt); werr != nil {
		t.Fatalf("Write: %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	return dst.Bytes()
}

func TestStream_roundTrip(t *testing.T) {
	t.Parallel()
	const chunk int = 64 * 1024
	type tc struct {
		name string
		size int
	}
	tests := []tc{
		{"empty", 0},
		{"sub-chunk", 100},
		{"exactly one chunk", chunk},
		{"multi-chunk with remainder", chunk*2 + 7},
		{"exactly two chunks", chunk * 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		key := streamKey(t, 0x11)
		//: a deterministic byte pattern keeps the assertion exact.
		pt := make([]byte, c.size)
		for i := range pt {
			pt[i] = byte(i % 251)
		}
		aad := []byte("stream-ctx")
		wire := sealStream(t, key, pt, aad)
		//: open the wire back and expect the original plaintext exactly.
		r, oerr := corecrypto.OpenStream(streamAlgo, key, bytes.NewReader(wire), aad)
		if oerr != nil {
			t.Fatalf("OpenStream: %v", oerr)
		}
		got, rerr := io.ReadAll(r)
		if rerr != nil || !bytes.Equal(got, pt) {
			t.Errorf("round-trip size=%d: err=%v equal=%v", c.size, rerr, bytes.Equal(got, pt))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestStream_tamperedChunk(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		at   int
	}{
		{"flip a header-adjacent ciphertext byte", 20},
		{"flip a mid-stream byte", 40},
	}
	runCase := func(t *testing.T, at int) {
		t.Helper()
		key := streamKey(t, 0x22)
		wire := sealStream(t, key, bytes.Repeat([]byte{0xAB}, 200), []byte("ctx"))
		//: corrupt one ciphertext/tag byte so GCM authentication must fail.
		if at < len(wire) {
			wire[at] ^= 0xff
		}
		r, oerr := corecrypto.OpenStream(streamAlgo, key, bytes.NewReader(wire), []byte("ctx"))
		if oerr != nil {
			t.Fatalf("OpenStream: %v", oerr)
		}
		//: reading the tampered stream must surface the non-oracle DecryptionFailed.
		_, rerr := io.ReadAll(r)
		if !errs.HasCode(rerr, corecrypto.CodeDecryptionFailed) {
			t.Errorf("ReadAll err=%v want DecryptionFailed", rerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.at)
		})
	}
}

func TestStream_truncated(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		drop int
	}{
		{"drop the trailing final chunk", 20},
		{"drop into the header", 50},
	}
	runCase := func(t *testing.T, drop int) {
		t.Helper()
		key := streamKey(t, 0x33)
		//: a multi-chunk stream so cutting the tail removes the final-flag chunk.
		wire := sealStream(t, key, bytes.Repeat([]byte{0xCD}, 64*1024+10), []byte("ctx"))
		//: lop off the tail so EOF arrives before the final-flag chunk verifies.
		cut := wire[:len(wire)-drop]
		r, oerr := corecrypto.OpenStream(streamAlgo, key, bytes.NewReader(cut), []byte("ctx"))
		if oerr != nil {
			t.Fatalf("OpenStream: %v", oerr)
		}
		//: a truncated stream must surface StreamTruncated, never accepted plaintext.
		_, rerr := io.ReadAll(r)
		if !errs.HasCode(rerr, corecrypto.CodeStreamTruncated) && !errs.HasCode(rerr, corecrypto.CodeDecryptionFailed) {
			t.Errorf("ReadAll err=%v want StreamTruncated or DecryptionFailed", rerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.drop)
		})
	}
}

func TestStream_holdBack_noPlaintextBeforeAuth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"a tampered multi-chunk stream yields zero plaintext bytes before the error"},
	}
	runCase := func(t *testing.T) {
		t.Helper()
		key := streamKey(t, 0x44)
		//: two full chunks; tamper the FIRST chunk's ciphertext.
		wire := sealStream(t, key, bytes.Repeat([]byte{0x5A}, 64*1024*2), []byte("ctx"))
		//: flip a byte well inside the first chunk's ciphertext.
		wire[100] ^= 0x01
		r, oerr := corecrypto.OpenStream(streamAlgo, key, bytes.NewReader(wire), []byte("ctx"))
		if oerr != nil {
			t.Fatalf("OpenStream: %v", oerr)
		}
		//: the very first Read must fail; no unverified plaintext may surface.
		buf := make([]byte, 4096)
		n, rerr := r.Read(buf)
		if n != 0 || !errs.HasCode(rerr, corecrypto.CodeDecryptionFailed) {
			t.Errorf("first Read=(%d,%v) want (0,DecryptionFailed) — hold-back violated", n, rerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t)
		})
	}
}

func TestStream_rejectsBoxLeadByte(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"OpenStream rejects a whole-buffer box (0x01) lead byte"},
	}
	runCase := func(t *testing.T) {
		t.Helper()
		key := streamKey(t, 0x55)
		//: a buffer whose lead byte is the box version 0x01, padded past the header.
		boxLike := append([]byte{0x01, 0x01}, bytes.Repeat([]byte{0x00}, 64)...)
		r, oerr := corecrypto.OpenStream(streamAlgo, key, bytes.NewReader(boxLike), nil)
		if oerr != nil {
			t.Fatalf("OpenStream: %v", oerr)
		}
		//: a 0x01 lead byte is the wrong format — non-oracle DecryptionFailed.
		_, rerr := io.ReadAll(r)
		if !errs.HasCode(rerr, corecrypto.CodeDecryptionFailed) {
			t.Errorf("ReadAll err=%v want DecryptionFailed for a 0x01 stream", rerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t)
		})
	}
}

func TestStream_wrongAAD(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"a mismatched aad fails authentication"},
	}
	runCase := func(t *testing.T) {
		t.Helper()
		key := streamKey(t, 0x66)
		wire := sealStream(t, key, []byte("payload"), []byte("right-ctx"))
		//: opening with a different aad must fail the GCM check.
		r, oerr := corecrypto.OpenStream(streamAlgo, key, bytes.NewReader(wire), []byte("wrong-ctx"))
		if oerr != nil {
			t.Fatalf("OpenStream: %v", oerr)
		}
		_, rerr := io.ReadAll(r)
		if !errs.HasCode(rerr, corecrypto.CodeDecryptionFailed) {
			t.Errorf("ReadAll err=%v want DecryptionFailed for wrong aad", rerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t)
		})
	}
}

func TestStream_unknownAlgorithm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"an unregistered streaming algorithm surfaces UnknownAlgorithm"},
	}
	runCase := func(t *testing.T) {
		t.Helper()
		key := streamKey(t, 0x77)
		var dst bytes.Buffer
		//: streaming reuses the AEAD UnknownAlgorithm on a lookup miss.
		_, err := corecrypto.SealStream("nope-not-registered", key, &dst, nil)
		if !errs.HasCode(err, corecrypto.CodeUnknownAlgorithm) {
			t.Errorf("SealStream err=%v want UnknownAlgorithm", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t)
		})
	}
}
