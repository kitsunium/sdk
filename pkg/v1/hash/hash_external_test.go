package hash_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/hash"
)

func TestSum(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		alg     hash.Algorithm
		wantLen int
		wantErr bool
	}
	tests := []tc{
		{"sha256 → 32 bytes", hash.SHA256, 32, false},
		{"sha512 → 64 bytes", hash.SHA512, 64, false},
		{"sha3-256 → 32 bytes", hash.SHA3256, 32, false},
		{"crc32c → 4 bytes", hash.CRC32C, 4, false},
		{"fnv1a-64 → 8 bytes", hash.FNV1a64, 8, false},
		{"unknown algorithm errors", "no-such-hash", 0, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := hash.Sum(c.alg, []byte("payload"))
		//: the failure arm must error and yield no digest.
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: Sum accepted an unregistered algorithm", c.name)
			}
			return
		}
		//: digest length must match the algorithm; result must be deterministic.
		if err != nil || len(got) != c.wantLen {
			t.Fatalf("%s: Sum=(%d bytes,%v) want (%d,nil)", c.name, len(got), err, c.wantLen)
		}
		again, againErr := hash.Sum(c.alg, []byte("payload"))
		//: the second Sum must also succeed and match — digests are deterministic.
		if againErr != nil || !bytes.Equal(got, again) {
			t.Errorf("%s: Sum is not deterministic (err=%v)", c.name, againErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestSumHex(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []byte
		want string
	}
	tests := []tc{
		//: the canonical SHA-256 of the empty input — a frozen known-answer.
		{"sha256 of empty is the known vector", []byte{}, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := hash.SumHex(hash.SHA256, c.in)
		//: lowercase hex must match the published SHA-256 test vector exactly.
		if err != nil || got != c.want {
			t.Errorf("%s: SumHex=%q (%v) want %q", c.name, got, err, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"streaming New matches one-shot Sum"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		h, err := hash.New(hash.SHA256)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		//: streaming Write then Sum must equal the one-shot Sum.
		if _, werr := h.Write([]byte("payload")); werr != nil {
			t.Fatalf("Write: %v", werr)
		}
		stream := h.Sum(nil)
		oneShot, oneErr := hash.Sum(hash.SHA256, []byte("payload"))
		//: the one-shot Sum must succeed and match the streamed digest.
		if oneErr != nil || !bytes.Equal(stream, oneShot) {
			t.Errorf("streaming digest %x != one-shot %x (err=%v)", stream, oneShot, oneErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
