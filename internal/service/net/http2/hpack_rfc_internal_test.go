package http2

import (
	"encoding/hex"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// fieldPair is one expected field or table entry.
type fieldPair struct {
	name, value string
}

// hexBytes decodes a whitespace-tolerant hex dump.
func hexBytes(t *testing.T, s string) []byte {
	t.Helper()
	s = strings.NewReplacer(" ", "", "\n", "", "\t", "").Replace(s)
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}
	return b
}

// tableState reads the dynamic table back, newest entry first.
func tableState(d *Decoder, mem []byte) []fieldPair {
	out := make([]fieldPair, 0, d.count)
	for age := range d.count {
		entry, _ := d.entryAt(mem, nil, age)
		out = append(out, fieldPair{string(entry.name), string(entry.value)})
	}
	return out
}

// tableSize computes the table's size as RFC 7541 §4.1 counts it.
func tableSize(entries []fieldPair) int {
	total := 0
	for _, e := range entries {
		total += len(e.name) + len(e.value) + 32
	}
	return total
}

// decodeBlock decodes block through d and returns the fields.
func decodeBlock(t *testing.T, d *Decoder, mem, block []byte) ([]fieldPair, error) {
	t.Helper()
	var req corenet.HTTP1RequestValue
	_, err := d.Decode(mem, block, make([]byte, 0, 1024), &req)
	out := make([]fieldPair, 0, req.NFields)
	for i := range req.NFields {
		out = append(out, fieldPair{string(req.Fields[i].Name), string(req.Fields[i].Value)})
	}
	return out, err
}

// equalPairs compares two lists of fields.
func equalPairs(a, b []fieldPair) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// rfcCase is one decode of an Appendix C sequence: the block, the fields, and
// the table state after it, newest first.
type rfcCase struct {
	name   string
	block  string
	fields []fieldPair
	table  []fieldPair
}

// runSequence decodes each block on one decoder, in order, and checks the
// fields and the table state the appendix gives.
func runSequence(t *testing.T, tableSizeLimit int, cases []rfcCase) {
	t.Helper()
	d := NewDecoder(4096, 1<<20)
	d.maxSize = int32(tableSizeLimit)
	d.limit = int32(tableSizeLimit)
	mem := make([]byte, TableMem(4096))
	for _, c := range cases {
		got, err := decodeBlock(t, &d, mem, hexBytes(t, c.block))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !equalPairs(got, c.fields) {
			t.Fatalf("%s: fields\n got %q\nwant %q", c.name, got, c.fields)
		}
		state := tableState(&d, mem)
		if !equalPairs(state, c.table) {
			t.Fatalf("%s: table\n got %q\nwant %q", c.name, state, c.table)
		}
		if int(d.size) != tableSize(c.table) {
			t.Fatalf("%s: size %d, want %d", c.name, d.size, tableSize(c.table))
		}
	}
}

// TestRFC7541C3 replays C.3: requests with no Huffman coding.
func TestRFC7541C3(t *testing.T) {
	first := []fieldPair{{":method", "GET"}, {":scheme", "http"}, {":path", "/"}, {":authority", "www.example.com"}}
	authority := fieldPair{":authority", "www.example.com"}
	cache := fieldPair{"cache-control", "no-cache"}
	custom := fieldPair{"custom-key", "custom-value"}
	runSequence(t, 4096, []rfcCase{
		{"C.3.1", "828684410f7777772e6578616d706c652e636f6d", first, []fieldPair{authority}},
		{"C.3.2", "828684be58086e6f2d6361636865", []fieldPair{{":method", "GET"}, {":scheme", "http"}, {":path", "/"}, {":authority", "www.example.com"}, cache}, []fieldPair{cache, authority}},
		{"C.3.3", "828785bf400a637573746f6d2d6b65790c637573746f6d2d76616c7565", []fieldPair{{":method", "GET"}, {":scheme", "https"}, {":path", "/index.html"}, {":authority", "www.example.com"}, custom}, []fieldPair{custom, cache, authority}},
	})
}

// TestRFC7541C4 replays C.4: the same requests, Huffman-coded.
func TestRFC7541C4(t *testing.T) {
	first := []fieldPair{{":method", "GET"}, {":scheme", "http"}, {":path", "/"}, {":authority", "www.example.com"}}
	authority := fieldPair{":authority", "www.example.com"}
	cache := fieldPair{"cache-control", "no-cache"}
	custom := fieldPair{"custom-key", "custom-value"}
	runSequence(t, 4096, []rfcCase{
		{"C.4.1", "828684418cf1e3c2e5f23a6ba0ab90f4ff", first, []fieldPair{authority}},
		{"C.4.2", "828684be5886a8eb10649cbf", []fieldPair{{":method", "GET"}, {":scheme", "http"}, {":path", "/"}, {":authority", "www.example.com"}, cache}, []fieldPair{cache, authority}},
		{"C.4.3", "828785bf408825a849e95ba97d7f8925a849e95bb8e8b4bf", []fieldPair{{":method", "GET"}, {":scheme", "https"}, {":path", "/index.html"}, {":authority", "www.example.com"}, custom}, []fieldPair{custom, cache, authority}},
	})
}

// c5Entries are the entries of the C.5 and C.6 sequences, as both name them.
var c5Entries = struct {
	status302, status307, private, date21, location, date22, gzip, setCookie fieldPair
}{
	status302: fieldPair{":status", "302"},
	status307: fieldPair{":status", "307"},
	private:   fieldPair{"cache-control", "private"},
	date21:    fieldPair{"date", "Mon, 21 Oct 2013 20:13:21 GMT"},
	location:  fieldPair{"location", "https://www.example.com"},
	date22:    fieldPair{"date", "Mon, 21 Oct 2013 20:13:22 GMT"},
	gzip:      fieldPair{"content-encoding", "gzip"},
	setCookie: fieldPair{"set-cookie", "foo=ASDJKHQKBZXOQWEOPIUAXQWEOIU; max-age=3600; version=1"},
}

// c5Cases gives the fields and table states of C.5/C.6, the blocks differing.
func c5Cases(block1, block2, block3 string) []rfcCase {
	e := c5Entries
	return []rfcCase{
		{"1", block1, []fieldPair{e.status302, e.private, e.date21, e.location}, []fieldPair{e.location, e.date21, e.private, e.status302}},
		{"2", block2, []fieldPair{e.status307, e.private, e.date21, e.location}, []fieldPair{e.status307, e.location, e.date21, e.private}},
		{"3", block3, []fieldPair{{":status", "200"}, e.private, e.date22, e.location, e.gzip, e.setCookie}, []fieldPair{e.setCookie, e.gzip, e.date22}},
	}
}

// TestRFC7541C5 replays C.5: responses with eviction at a 256-byte table.
func TestRFC7541C5(t *testing.T) {
	runSequence(t, 256, c5Cases(
		"4803333032 5807707269766174 65611d4d6f6e2c203231204f637420323031332032303a31333a323120474d546e1768747470733a2f2f7777772e6578616d706c652e636f6d",
		"4803333037c1c0bf",
		"88c1611d4d6f6e2c203231204f637420323031332032303a31333a323220474d54c05a04677a69707738666f6f3d4153444a4b48514b425a584f5157454f50495541585157454f49553b206d61782d6167653d333630303b2076657273696f6e3d31",
	))
}

// TestRFC7541C6 replays C.6: the same responses, Huffman-coded.
func TestRFC7541C6(t *testing.T) {
	runSequence(t, 256, c5Cases(
		"488264025885aec3771a4b6196d07abe941054d444a8200595040b8166e082a62d1bff6e919d29ad171863c78f0b97c8e9ae82ae43d3",
		"4883640effc1c0bf",
		"88c16196d07abe941054d444a8200595040b8166e084a62d1bffc05a839bd9ab77ad94e7821dd7f2e6c7b335dfdfcd5b3960d5af27087f3672c1ab270fb5291f9587316065c003ed4ee5b1063d5007",
	))
}

// TestRFC7541C1Integers replays C.1's integer examples through readInt.
func TestRFC7541C1Integers(t *testing.T) {
	cases := []struct {
		name   string
		block  []byte
		prefix uint8
		want   int
	}{
		{"C.1.1 10 on 5 bits", []byte{0x0a}, 5, 10},
		{"C.1.2 1337 on 5 bits", []byte{0x1f, 0x9a, 0x0a}, 5, 1337},
		{"C.1.3 42 on 8 bits", []byte{0x2a}, 8, 42},
	}
	for _, c := range cases {
		r := decodeRun{block: c.block}
		got, err := r.readInt(c.prefix)
		if err != nil || got != c.want || len(r.block) != 0 {
			t.Fatalf("%s: %d %v (left %d)", c.name, got, err, len(r.block))
		}
	}
}

// TestRFC7541C2Fields replays C.2: one representation of each kind.
func TestRFC7541C2Fields(t *testing.T) {
	cases := []struct {
		name  string
		block string
		field fieldPair
		table []fieldPair
	}{
		{"C.2.1 literal with indexing", "400a637573746f6d2d6b65790d637573746f6d2d686561646572", fieldPair{"custom-key", "custom-header"}, []fieldPair{{"custom-key", "custom-header"}}},
		{"C.2.2 literal without indexing", "040c2f73616d706c652f70617468", fieldPair{":path", "/sample/path"}, nil},
		{"C.2.3 literal never indexed", "100870617373776f726406736563726574", fieldPair{"password", "secret"}, nil},
		{"C.2.4 indexed", "82", fieldPair{":method", "GET"}, nil},
	}
	for _, c := range cases {
		d := NewDecoder(4096, 1<<20)
		mem := make([]byte, TableMem(4096))
		got, err := decodeBlock(t, &d, mem, hexBytes(t, c.block))
		if err != nil || len(got) != 1 || got[0] != c.field {
			t.Fatalf("%s: %q %v", c.name, got, err)
		}
		if state := tableState(&d, mem); !equalPairs(state, c.table) {
			t.Fatalf("%s: table %q, want %q", c.name, state, c.table)
		}
	}
}

// TestRFC7541EncoderRoundTrip checks that what the Encoder writes for the C.3
// and C.4 requests decodes back to the same fields through the Decoder. The
// Encoder writes responses, not requests, so it is reached through its field
// writers.
func TestRFC7541EncoderRoundTrip(t *testing.T) {
	fields := []fieldPair{{":method", "GET"}, {":scheme", "http"}, {":path", "/index.html"}, {":authority", "www.example.com"}, {"cache-control", "no-cache"}, {"custom-key", "custom-value"}}
	var block []byte
	for _, f := range fields {
		block = appendNamedField(block, []byte(f.name), []byte(f.value))
	}
	d := NewDecoder(4096, 1<<20)
	mem := make([]byte, TableMem(4096))
	got, err := decodeBlock(t, &d, mem, block)
	if err != nil || !equalPairs(got, fields) {
		t.Fatalf("round trip: %q %v", got, err)
	}
}
