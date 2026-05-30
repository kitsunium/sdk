package streamaead_test

import (
	"bytes"
	"io"
	"testing"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

func benchKey(b *testing.B) corecrypto.Key {
	b.Helper()
	//: a fixed 32-byte key keeps the benchmark deterministic.
	key, err := corecrypto.NewKey(bytes.Repeat([]byte{0x9}, corecrypto.KeyLen))
	if err != nil {
		b.Fatalf("NewKey: %v", err)
	}
	return key
}

// sealOnce seals pt under key into a fresh buffer, failing the benchmark on any
// error so the discard linter stays happy and faults are never hidden.
func sealOnce(b *testing.B, key corecrypto.Key, pt []byte) []byte {
	b.Helper()
	var dst bytes.Buffer
	w, err := corecrypto.SealStream("aes-256-gcm-stream", key, &dst, nil)
	if err != nil {
		b.Fatalf("SealStream: %v", err)
	}
	if _, werr := w.Write(pt); werr != nil {
		b.Fatalf("Write: %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		b.Fatalf("Close: %v", cerr)
	}
	return dst.Bytes()
}

func BenchmarkStreamSeal_1MiB(b *testing.B) {
	key := benchKey(b)
	pt := bytes.Repeat([]byte{0xAB}, 1024*1024)
	b.ReportAllocs()
	b.SetBytes(int64(len(pt)))
	//: a package-level sink keeps the compiler from eliding the sealed output.
	for b.Loop() {
		benchWire = sealOnce(b, key, pt)
	}
}

// benchWire is a package-level sink for the seal benchmark output so the
// compiler cannot optimise the sealed bytes away.
var benchWire []byte

func BenchmarkStreamOpen_1MiB(b *testing.B) {
	key := benchKey(b)
	pt := bytes.Repeat([]byte{0xAB}, 1024*1024)
	wire := sealOnce(b, key, pt)
	b.ReportAllocs()
	b.SetBytes(int64(len(pt)))
	//: b.Loop is the Go 1.24+ benchmark loop; the body is timed automatically.
	for b.Loop() {
		r, err := corecrypto.OpenStream("aes-256-gcm-stream", key, bytes.NewReader(wire), nil)
		if err != nil {
			b.Fatalf("OpenStream: %v", err)
		}
		if _, cerr := io.Copy(io.Discard, r); cerr != nil {
			b.Fatalf("Copy: %v", cerr)
		}
	}
}
