package http2_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// newDecoder is a decoder and its table memory.
func newDecoder(table, maxList int) (*http2.Decoder, []byte) {
	d := http2.NewDecoder(table, maxList)
	return &d, make([]byte, http2.TableMem(table))
}

// decode runs one block through a fresh request value and a roomy scratch.
func decode(d *http2.Decoder, mem, block []byte) (*corenet.HTTP1RequestValue, []byte, error) {
	var req corenet.HTTP1RequestValue
	scratch, err := d.Decode(mem, block, make([]byte, 0, 256), &req)
	return &req, scratch, err
}

// fieldsOf reads req.Fields as strings.
func fieldsOf(req *corenet.HTTP1RequestValue) []string {
	out := make([]string, 0, req.NFields)
	for i := range req.NFields {
		out = append(out, string(req.Fields[i].Name)+"="+string(req.Fields[i].Value))
	}
	return out
}

// appendHpackInt appends n as an HPACK integer of the given prefix width, the
// first byte's high bits set by top.
func appendHpackInt(dst []byte, prefix uint8, top byte, n int) []byte {
	mask := 1<<prefix - 1
	if n < mask {
		return append(dst, top|byte(n))
	}
	dst = append(dst, top|byte(mask))
	n -= mask
	for n >= 128 {
		dst = append(dst, byte(n%128)|0x80)
		n /= 128
	}
	return append(dst, byte(n))
}

// indexedName writes a literal without indexing naming a static entry.
func indexedName(dst []byte, index int, value string) []byte {
	dst = appendHpackInt(dst, 4, 0, index)
	dst = appendHpackInt(dst, 7, 0, len(value))
	return append(dst, value...)
}

// plainLiteral writes a literal with incremental indexing.
func plainLiteral(dst []byte, name, value string) []byte {
	dst = append(dst, 0x40)
	dst = appendHpackInt(dst, 7, 0, len(name))
	dst = append(dst, name...)
	dst = appendHpackInt(dst, 7, 0, len(value))
	return append(dst, value...)
}

// TestDecodeRefusesCompression holds RFC 7541 §6's malformed representations:
// each is a COMPRESSION_ERROR on the connection.
func TestDecodeRefusesCompression(t *testing.T) {
	longInt := []byte{0x7f}
	for range 6 {
		longInt = append(longInt, 0xff)
	}
	cases := map[string][]byte{
		"indexed zero":             {0x80},
		"index past the tables":    {0xff, 0x80, 0x01},
		"index past the dynamics":  {0xbe},
		"truncated integer":        {0xff},
		"oversized integer":        longInt,
		"string past the block":    {0x40, 0x0a, 'a'},
		"truncated string length":  {0x40, 0xff},
		"nothing after the prefix": {0x40},
	}
	for name, block := range cases {
		d, mem := newDecoder(4096, 1<<20)
		if _, _, err := decode(d, mem, block); !errors.Is(err, corehttp2.CompressionFailed) {
			t.Errorf("%s: %v, want CompressionFailed", name, err)
		}
	}
}

// TestDecodeSizeUpdate holds RFC 7541 §4.2: only at the block's start, never
// past the advertised bound.
func TestDecodeSizeUpdate(t *testing.T) {
	d, mem := newDecoder(4096, 1<<20)
	// size updates at the start are allowed, and shrink the table to 16 (empty).
	if _, _, err := decode(d, mem, []byte{0x20 | 0x10}); err != nil {
		t.Fatalf("update at the start: %v", err)
	}
	d2, mem2 := newDecoder(4096, 1<<20)
	if _, _, err := decode(d2, mem2, []byte{0x82, 0x20}); !errors.Is(err, corehttp2.CompressionFailed) {
		t.Fatalf("update after a field: %v", err)
	}
	d3, mem3 := newDecoder(4096, 1<<20)
	large := []byte{0x3f, 0x80, 0x80, 0x04} // 16415 > 4096
	if _, _, err := decode(d3, mem3, large); !errors.Is(err, corehttp2.CompressionFailed) {
		t.Fatalf("update past the bound: %v", err)
	}
}

// TestDecodeSizeUpdateEvicts empties the table with a zero update.
func TestDecodeSizeUpdateEvicts(t *testing.T) {
	d, mem := newDecoder(4096, 1<<20)
	if _, _, err := decode(d, mem, plainLiteral(nil, "x-a", "1")); err != nil {
		t.Fatal(err)
	}
	if req, _, err := decode(d, mem, []byte{0xbe}); err != nil || fieldsOf(req)[0] != "x-a=1" {
		t.Fatalf("entry present: %v", err)
	}
	// size update to zero: the table is empty, index 62 no longer resolves.
	block := []byte{0x20}
	if _, _, err := decode(d, mem, block); err != nil {
		t.Fatal(err)
	}
	if _, _, err := decode(d, mem, []byte{0xbe}); !errors.Is(err, corehttp2.CompressionFailed) {
		t.Fatalf("entry after eviction: %v", err)
	}
}

// TestDecodeEviction holds RFC 7541 §4.4: an entry larger than the table empties it.
func TestDecodeEviction(t *testing.T) {
	d := http2.NewDecoder(4096, 1<<20)
	mem := make([]byte, http2.TableMem(4096))
	// shrink to 64.
	block := append([]byte{0x3f, 0x21}, plainLiteral(nil, "x-a", "1")...)
	if _, err := d.Decode(mem, block, nil, &corenet.HTTP1RequestValue{}); err != nil {
		t.Fatal(err)
	}
	// an entry past the table's size empties it, and leaves no entry.
	big := plainLiteral(nil, "x-b", string(bytes.Repeat([]byte("v"), 60)))
	if _, err := d.Decode(mem, big, nil, &corenet.HTTP1RequestValue{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Decode(mem, []byte{0xbe}, nil, &corenet.HTTP1RequestValue{}); !errors.Is(err, corehttp2.CompressionFailed) {
		t.Fatalf("after an oversized insert: %v", err)
	}
}

// TestDecodeEvictionReferencingName holds RFC 7541 §4.4's warning: an entry
// can name an entry the insertion evicts.
func TestDecodeEvictionReferencingName(t *testing.T) {
	d := http2.NewDecoder(4096, 1<<20)
	mem := make([]byte, http2.TableMem(4096))
	// shrink to the size of one entry "aaaaaaaa: bbbbbbbbbbbbbbbb" = 8+16+32 = 56.
	blk := append([]byte{0x3f, 0x19}, plainLiteral(nil, "aaaaaaaa", "bbbbbbbbbbbbbbbb")...)
	if _, err := d.Decode(mem, blk, nil, &corenet.HTTP1RequestValue{}); err != nil {
		t.Fatal(err)
	}
	// a literal with indexed name 62 (the entry) and a new value: inserting
	// evicts the entry the name comes from.
	lit := append([]byte{0x7e}, append([]byte{8}, []byte("cccccccc")...)...)
	req, _, err := decode(&d, mem, lit)
	if err != nil {
		t.Fatal(err)
	}
	if got := fieldsOf(req); len(got) != 1 || got[0] != "aaaaaaaa=cccccccc" {
		t.Fatalf("fields: %v", got)
	}
	// the single entry left is the new one, name and all.
	req2, _, err := decode(&d, mem, []byte{0xbe})
	if err != nil || fieldsOf(req2)[0] != "aaaaaaaa=cccccccc" {
		t.Fatalf("table after: %v %v", fieldsOf(req2), err)
	}
}

// TestDecodeRingWrap inserts and reads back entries across the ring's seam,
// many times over, each read back identical.
func TestDecodeRingWrap(t *testing.T) {
	d := http2.NewDecoder(4096, 1<<20)
	mem := make([]byte, http2.TableMem(4096))
	for i := range 2000 {
		name := fmt.Sprintf("x-%04d", i%97)
		value := string(bytes.Repeat([]byte{'a' + byte(i%26)}, i%300))
		if _, _, err := decode(&d, mem, plainLiteral(nil, name, value)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		req, _, err := decode(&d, mem, []byte{0xbe})
		if err != nil || len(req.Fields) == 0 || string(req.Fields[0].Name) != name || string(req.Fields[0].Value) != value {
			t.Fatalf("read back %d: %q %v", i, fieldsOf(req), err)
		}
	}
}

// TestDecodeListCeiling holds the header-list ceiling, RFC 9113 §6.5.2's
// measure: a field past it is refused.
func TestDecodeListCeiling(t *testing.T) {
	d, mem := newDecoder(4096, 100)
	// 70 bytes of name and value + 32 = 102 > 100.
	block := plainLiteral(nil, "x-a", string(bytes.Repeat([]byte("v"), 67)))
	if _, _, err := decode(d, mem, block); !errors.Is(err, corehttp2.HeadersTooLarge) {
		t.Fatalf("over the ceiling: %v", err)
	}
	// at the ceiling exactly: 3 + 65 + 32 = 100.
	d2, mem2 := newDecoder(4096, 100)
	ok := plainLiteral(nil, "x-a", string(bytes.Repeat([]byte("v"), 65)))
	if _, _, err := decode(d2, mem2, ok); err != nil {
		t.Fatalf("at the ceiling: %v", err)
	}
	// and the ceiling's error is checked before the insert: nothing went into the table.
	d3, mem3 := newDecoder(4096, 100)
	if _, _, err := decode(d3, mem3, block); !errors.Is(err, corehttp2.HeadersTooLarge) {
		t.Fatal(err)
	}
	if _, _, err := decode(d3, mem3, []byte{0xbe}); !errors.Is(err, corehttp2.CompressionFailed) {
		t.Fatalf("a refused field left an entry: %v", err)
	}
}

// TestDecodeFieldCount holds the fixed array: one field past it refuses.
func TestDecodeFieldCount(t *testing.T) {
	d, mem := newDecoder(4096, 1<<20)
	block := bytes.Repeat([]byte{0x82}, corenet.HTTP1MaxFields+1)
	if _, _, err := decode(d, mem, block); !errors.Is(err, corehttp2.HeadersTooLarge) {
		t.Fatalf("one too many: %v", err)
	}
	if _, _, err := decode(d, mem, bytes.Repeat([]byte{0x82}, corenet.HTTP1MaxFields)); err != nil {
		t.Fatalf("exactly the array: %v", err)
	}
}

// TestDecodeMalformedKeepsState holds RFC 9113 §8.2.1: a malformed field
// refuses only the stream, the block decoded to its end and the table intact.
func TestDecodeMalformedKeepsState(t *testing.T) {
	cases := map[string][]byte{
		"capital in a name":      plainLiteral(nil, "X-Bad", "v"),
		"space in a name":        plainLiteral(nil, "x bad", "v"),
		"empty name":             plainLiteral(nil, "", "v"),
		"colon in a name":        plainLiteral(nil, "x:y", "v"),
		"LF in a value":          plainLiteral(nil, "x", "a\nb"),
		"CR in a value":          plainLiteral(nil, "x", "a\rb"),
		"NUL in a value":         plainLiteral(nil, "x", "a\x00b"),
		"leading space in value": plainLiteral(nil, "x", " v"),
		"trailing tab in value":  plainLiteral(nil, "x", "v\t"),
	}
	for name, field := range cases {
		d, mem := newDecoder(4096, 1<<20)
		block := append(append([]byte(nil), field...), plainLiteral(nil, "ok", "1")...)
		req, _, err := decode(d, mem, block)
		if !errors.Is(err, corehttp2.FieldMalformed) {
			t.Fatalf("%s: %v", name, err)
		}
		if req.NFields != 2 {
			t.Fatalf("%s: stopped at %d fields", name, req.NFields)
		}
		// the table holds both, in order: the block was decoded to its end.
		if _, _, err := decode(d, mem, []byte{0xbe}); err != nil {
			t.Fatalf("%s: the table lost the valid field: %v", name, err)
		}
		if _, _, err := decode(d, mem, []byte{0xbf}); !errors.Is(err, corehttp2.FieldMalformed) {
			t.Fatalf("%s: the table lost the malformed field: %v", name, err)
		}
	}
}

// TestDecodeMalformedIndexed holds the stored verdict: a reference to a
// malformed entry is malformed, with no second scan of its bytes.
func TestDecodeMalformedIndexed(t *testing.T) {
	d, mem := newDecoder(4096, 1<<20)
	if _, _, err := decode(d, mem, plainLiteral(nil, "X-Bad", "v")); !errors.Is(err, corehttp2.FieldMalformed) {
		t.Fatal(err)
	}
	if _, _, err := decode(d, mem, []byte{0xbe}); !errors.Is(err, corehttp2.FieldMalformed) {
		t.Fatalf("a reference to the malformed entry: %v", err)
	}
	// a literal that reuses only the name judges the name again.
	lit := append([]byte{0x7e}, 1, 'v')
	if _, _, err := decode(d, mem, lit); !errors.Is(err, corehttp2.FieldMalformed) {
		t.Fatalf("a literal naming the entry: %v", err)
	}
}

// TestDecodeBomb is RFC 7541 §7.3's expansion attack: a 16 KiB block
// referencing one 4 KiB dynamic entry 4 000 times is refused at the ceiling.
func TestDecodeBomb(t *testing.T) {
	d, mem := newDecoder(4096, corehttp2.DefaultMaxHeaderList)
	value := string(bytes.Repeat([]byte("v"), 4096-32-1))
	if _, _, err := decode(d, mem, plainLiteral(nil, "x", value)); err != nil {
		t.Fatal(err)
	}
	block := bytes.Repeat([]byte{0xbe}, 16384)
	var req corenet.HTTP1RequestValue
	scratch, err := d.Decode(mem, block, make([]byte, 0, 32768), &req)
	if !errors.Is(err, corehttp2.HeadersTooLarge) {
		t.Fatalf("got %v", err)
	}
	if req.NFields > 3 || len(scratch) > 3*4096 {
		t.Fatalf("a bomb paid past the ceiling: %d fields, %d bytes of scratch", req.NFields, len(scratch))
	}
}

// TestDecodeAliases holds what the aliasing promises: a plain value is the
// block's bytes, a Huffman value the scratch's, a static value the table's.
func TestDecodeAliases(t *testing.T) {
	d, mem := newDecoder(4096, 1<<20)
	block := append([]byte{0x00}, append([]byte{1, 'x', 1, 'y'}, 0x82)...)
	req, _, err := decode(d, mem, block)
	if err != nil {
		t.Fatal(err)
	}
	if &req.Fields[0].Value[0] != &block[4] {
		t.Fatal("a plain value is not aliased from the block")
	}
	if got := string(req.Fields[1].Value); got != "GET" {
		t.Fatalf("static value: %q", got)
	}
}

// TestDecodeResetsNFields holds the reuse of a request value.
func TestDecodeResetsNFields(t *testing.T) {
	d, mem := newDecoder(4096, 1<<20)
	var req corenet.HTTP1RequestValue
	req.NFields = 7
	if _, err := d.Decode(mem, []byte{0x82}, nil, &req); err != nil {
		t.Fatal(err)
	}
	if req.NFields != 1 {
		t.Fatalf("NFields %d", req.NFields)
	}
}

// TestNewDecoderLimits holds the table's capacity: never below the protocol's
// default, since the peer may use it until it has seen our SETTINGS.
func TestNewDecoderLimits(t *testing.T) {
	if got := http2.TableMem(0); got != http2.TableMem(4096) {
		t.Fatalf("TableMem(0) = %d", got)
	}
}
