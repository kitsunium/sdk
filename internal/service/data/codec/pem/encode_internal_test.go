package pem

import (
	"bytes"
	stdpem "encoding/pem"
	"testing"
)

// TestAppendBlockIsEncodingPEM pins the hand-written encoder to
// encoding/pem.EncodeToMemory, byte for byte, and blockLen to what it writes:
// bodies around every line boundary, headers with and without Proc-Type.
func TestAppendBlockIsEncodingPEM(t *testing.T) {
	t.Parallel()
	headerSets := []map[string]string{
		nil,
		{"Proc-Type": "4,ENCRYPTED"},
		{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-128-CBC,00", "B": "", "a": "x y"},
		{"z": "1", "Z": "2", "é": "3"},
	}
	for size := range 200 {
		for _, headers := range headerSets {
			block := &stdpem.Block{Type: "T Y", Headers: headers, Bytes: bytes.Repeat([]byte{byte(size)}, size)}
			want := stdpem.EncodeToMemory(block)
			got := appendBlock([]byte("PRE"), block)
			if !bytes.Equal(got[3:], want) {
				t.Fatalf("size %d headers %v: wrote\n%q\nencoding/pem writes\n%q", size, headers, got[3:], want)
			}
			if n := blockLen(block); n != len(want) {
				t.Fatalf("size %d headers %v: blockLen = %d, written %d", size, headers, n, len(want))
			}
		}
	}
}

// FuzzAppendBlockIsEncodingPEM holds the same equality on any type, header
// and body encoding/pem accepts.
func FuzzAppendBlockIsEncodingPEM(f *testing.F) {
	f.Add("CERTIFICATE", "Proc-Type", "4,ENCRYPTED", []byte("payload"))
	f.Add("", "", "", []byte{})
	f.Fuzz(func(t *testing.T, typ, key, value string, body []byte) {
		block := &stdpem.Block{Type: typ, Bytes: body}
		if key != "" {
			block.Headers = map[string]string{key: value, "Other": value}
		}
		want := stdpem.EncodeToMemory(block)
		if err := checkBlock(block, "fuzz"); (err != nil) != (want == nil) {
			t.Fatalf("blockOf refused = %v, encoding/pem refused = %v", err != nil, want == nil)
		}
		if want == nil {
			return
		}
		if got := appendBlock(nil, block); !bytes.Equal(got, want) {
			t.Fatalf("wrote %q, encoding/pem writes %q", got, want)
		}
		if n := blockLen(block); n != len(want) {
			t.Fatalf("blockLen = %d, written %d", n, len(want))
		}
	})
}
