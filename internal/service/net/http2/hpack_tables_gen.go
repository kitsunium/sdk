//go:build ignore

// hpack_tables_gen.go writes the two tables hpack_static_table.go and
// hpack_huffman_table.go hold: RFC 7541 Appendix A's static table and the
// nibble automaton of Appendix B's code, written out once as static data so
// that no entry is computed at init (ADR 0172). The data below is the RFC's.
//
//	go run hpack_tables_gen.go
package main

import (
	"bytes"
	"fmt"
	"go/format"
	"log"
	"os"
	"strings"
)

// staticEntry is one entry of Appendix A, index one first.
type staticEntry struct{ name, value string }

var staticEntries = [61]staticEntry{
	{":authority", ""},
	{":method", "GET"},
	{":method", "POST"},
	{":path", "/"},
	{":path", "/index.html"},
	{":scheme", "http"},
	{":scheme", "https"},
	{":status", "200"},
	{":status", "204"},
	{":status", "206"},
	{":status", "304"},
	{":status", "400"},
	{":status", "404"},
	{":status", "500"},
	{"accept-charset", ""},
	{"accept-encoding", "gzip, deflate"},
	{"accept-language", ""},
	{"accept-ranges", ""},
	{"accept", ""},
	{"access-control-allow-origin", ""},
	{"age", ""},
	{"allow", ""},
	{"authorization", ""},
	{"cache-control", ""},
	{"content-disposition", ""},
	{"content-encoding", ""},
	{"content-language", ""},
	{"content-length", ""},
	{"content-location", ""},
	{"content-range", ""},
	{"content-type", ""},
	{"cookie", ""},
	{"date", ""},
	{"etag", ""},
	{"expect", ""},
	{"expires", ""},
	{"from", ""},
	{"host", ""},
	{"if-match", ""},
	{"if-modified-since", ""},
	{"if-none-match", ""},
	{"if-range", ""},
	{"if-unmodified-since", ""},
	{"last-modified", ""},
	{"link", ""},
	{"location", ""},
	{"max-forwards", ""},
	{"proxy-authenticate", ""},
	{"proxy-authorization", ""},
	{"range", ""},
	{"referer", ""},
	{"refresh", ""},
	{"retry-after", ""},
	{"server", ""},
	{"set-cookie", ""},
	{"strict-transport-security", ""},
	{"transfer-encoding", ""},
	{"user-agent", ""},
	{"vary", ""},
	{"via", ""},
	{"www-authenticate", ""},
}

var huffCodes = [256]uint32{
	0x00001ff8, 0x007fffd8, 0x0fffffe2, 0x0fffffe3, 0x0fffffe4, 0x0fffffe5,
	0x0fffffe6, 0x0fffffe7, 0x0fffffe8, 0x00ffffea, 0x3ffffffc, 0x0fffffe9,
	0x0fffffea, 0x3ffffffd, 0x0fffffeb, 0x0fffffec, 0x0fffffed, 0x0fffffee,
	0x0fffffef, 0x0ffffff0, 0x0ffffff1, 0x0ffffff2, 0x3ffffffe, 0x0ffffff3,
	0x0ffffff4, 0x0ffffff5, 0x0ffffff6, 0x0ffffff7, 0x0ffffff8, 0x0ffffff9,
	0x0ffffffa, 0x0ffffffb, 0x00000014, 0x000003f8, 0x000003f9, 0x00000ffa,
	0x00001ff9, 0x00000015, 0x000000f8, 0x000007fa, 0x000003fa, 0x000003fb,
	0x000000f9, 0x000007fb, 0x000000fa, 0x00000016, 0x00000017, 0x00000018,
	0x00000000, 0x00000001, 0x00000002, 0x00000019, 0x0000001a, 0x0000001b,
	0x0000001c, 0x0000001d, 0x0000001e, 0x0000001f, 0x0000005c, 0x000000fb,
	0x00007ffc, 0x00000020, 0x00000ffb, 0x000003fc, 0x00001ffa, 0x00000021,
	0x0000005d, 0x0000005e, 0x0000005f, 0x00000060, 0x00000061, 0x00000062,
	0x00000063, 0x00000064, 0x00000065, 0x00000066, 0x00000067, 0x00000068,
	0x00000069, 0x0000006a, 0x0000006b, 0x0000006c, 0x0000006d, 0x0000006e,
	0x0000006f, 0x00000070, 0x00000071, 0x00000072, 0x000000fc, 0x00000073,
	0x000000fd, 0x00001ffb, 0x0007fff0, 0x00001ffc, 0x00003ffc, 0x00000022,
	0x00007ffd, 0x00000003, 0x00000023, 0x00000004, 0x00000024, 0x00000005,
	0x00000025, 0x00000026, 0x00000027, 0x00000006, 0x00000074, 0x00000075,
	0x00000028, 0x00000029, 0x0000002a, 0x00000007, 0x0000002b, 0x00000076,
	0x0000002c, 0x00000008, 0x00000009, 0x0000002d, 0x00000077, 0x00000078,
	0x00000079, 0x0000007a, 0x0000007b, 0x00007ffe, 0x000007fc, 0x00003ffd,
	0x00001ffd, 0x0ffffffc, 0x000fffe6, 0x003fffd2, 0x000fffe7, 0x000fffe8,
	0x003fffd3, 0x003fffd4, 0x003fffd5, 0x007fffd9, 0x003fffd6, 0x007fffda,
	0x007fffdb, 0x007fffdc, 0x007fffdd, 0x007fffde, 0x00ffffeb, 0x007fffdf,
	0x00ffffec, 0x00ffffed, 0x003fffd7, 0x007fffe0, 0x00ffffee, 0x007fffe1,
	0x007fffe2, 0x007fffe3, 0x007fffe4, 0x001fffdc, 0x003fffd8, 0x007fffe5,
	0x003fffd9, 0x007fffe6, 0x007fffe7, 0x00ffffef, 0x003fffda, 0x001fffdd,
	0x000fffe9, 0x003fffdb, 0x003fffdc, 0x007fffe8, 0x007fffe9, 0x001fffde,
	0x007fffea, 0x003fffdd, 0x003fffde, 0x00fffff0, 0x001fffdf, 0x003fffdf,
	0x007fffeb, 0x007fffec, 0x001fffe0, 0x001fffe1, 0x003fffe0, 0x001fffe2,
	0x007fffed, 0x003fffe1, 0x007fffee, 0x007fffef, 0x000fffea, 0x003fffe2,
	0x003fffe3, 0x003fffe4, 0x007ffff0, 0x003fffe5, 0x003fffe6, 0x007ffff1,
	0x03ffffe0, 0x03ffffe1, 0x000fffeb, 0x0007fff1, 0x003fffe7, 0x007ffff2,
	0x003fffe8, 0x01ffffec, 0x03ffffe2, 0x03ffffe3, 0x03ffffe4, 0x07ffffde,
	0x07ffffdf, 0x03ffffe5, 0x00fffff1, 0x01ffffed, 0x0007fff2, 0x001fffe3,
	0x03ffffe6, 0x07ffffe0, 0x07ffffe1, 0x03ffffe7, 0x07ffffe2, 0x00fffff2,
	0x001fffe4, 0x001fffe5, 0x03ffffe8, 0x03ffffe9, 0x0ffffffd, 0x07ffffe3,
	0x07ffffe4, 0x07ffffe5, 0x000fffec, 0x00fffff3, 0x000fffed, 0x001fffe6,
	0x003fffe9, 0x001fffe7, 0x001fffe8, 0x007ffff3, 0x003fffea, 0x003fffeb,
	0x01ffffee, 0x01ffffef, 0x00fffff4, 0x00fffff5, 0x03ffffea, 0x007ffff4,
	0x03ffffeb, 0x07ffffe6, 0x03ffffec, 0x03ffffed, 0x07ffffe7, 0x07ffffe8,
	0x07ffffe9, 0x07ffffea, 0x07ffffeb, 0x0ffffffe, 0x07ffffec, 0x07ffffed,
	0x07ffffee, 0x07ffffef, 0x07fffff0, 0x03ffffee,
}

var huffLens = [256]uint8{
	13, 23, 28, 28, 28, 28, 28, 28, 28, 24, 30, 28, 28, 30, 28, 28, 28, 28, 28, 28,
	28, 28, 30, 28, 28, 28, 28, 28, 28, 28, 28, 28, 6, 10, 10, 12, 13, 6, 8, 11,
	10, 10, 8, 11, 8, 6, 6, 6, 5, 5, 5, 6, 6, 6, 6, 6, 6, 6, 7, 8,
	15, 6, 12, 10, 13, 6, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7,
	7, 7, 7, 7, 7, 7, 7, 7, 8, 7, 8, 13, 19, 13, 14, 6, 15, 5, 6, 5,
	6, 5, 6, 6, 6, 5, 7, 7, 6, 6, 6, 5, 6, 7, 6, 5, 5, 6, 7, 7,
	7, 7, 7, 15, 11, 14, 13, 28, 20, 22, 20, 20, 22, 22, 22, 23, 22, 23, 23, 23,
	23, 23, 24, 23, 24, 24, 22, 23, 24, 23, 23, 23, 23, 21, 22, 23, 22, 23, 23, 24,
	22, 21, 20, 22, 22, 23, 23, 21, 23, 22, 22, 24, 21, 22, 23, 23, 21, 21, 22, 21,
	23, 22, 23, 23, 20, 22, 22, 22, 23, 22, 22, 23, 26, 26, 20, 19, 22, 23, 22, 25,
	26, 26, 26, 27, 27, 26, 24, 25, 19, 21, 26, 27, 27, 26, 27, 24, 21, 21, 26, 26,
	28, 27, 27, 27, 20, 24, 20, 21, 22, 21, 21, 23, 22, 22, 25, 25, 24, 24, 26, 23,
	26, 27, 26, 26, 27, 27, 27, 27, 27, 28, 27, 27, 27, 27, 27, 26,
}

// huffNode is a node of Appendix B's code tree.
type huffNode struct {
	child [2]int // -1: no such branch
	sym   int    // >= 0 for a leaf; 256 is EOS
}

func main() {
	nodes := []huffNode{{child: [2]int{-1, -1}, sym: -1}}
	add := func(code uint32, n uint, sym int) {
		cur := 0
		for bit := int(n) - 1; bit >= 0; bit-- {
			b := (code >> uint(bit)) & 1
			if nodes[cur].child[b] < 0 {
				nodes = append(nodes, huffNode{child: [2]int{-1, -1}, sym: -1})
				nodes[cur].child[b] = len(nodes) - 1
			}
			cur = nodes[cur].child[b]
		}
		nodes[cur].sym = sym
	}
	for s := range 256 {
		add(huffCodes[s], uint(huffLens[s]), s)
	}
	add(0x3fffffff, 30, 256)

	// State enumeration: a state is (node, bits since last symbol boundary,
	// all-ones path so far); the partial code's depth decides acceptance. Node
	// alone is a state: the depth and ones-path of a node are fixed by the tree.
	states := []int{0}
	seen := map[int]int{0: 0}
	type walked struct {
		next, emit, sym int
		fail            bool
	}
	step := func(node, nibble int) walked {
		var w walked
		w.sym = -1
		cur := node
		for b := 3; b >= 0; b-- {
			bit := (nibble >> uint(b)) & 1
			next := nodes[cur].child[bit]
			if next < 0 {
				w.fail = true
				return w
			}
			cur = next
			if nodes[cur].sym >= 0 {
				if nodes[cur].sym == 256 {
					// EOS inside the string is a decoding error (RFC 7541 §5.2).
					w.fail = true
					return w
				}
				w.emit = 1
				w.sym = nodes[cur].sym
				cur = 0
			}
		}
		w.next = cur
		return w
	}
	var table [][16]walked
	for i := 0; i < len(states); i++ {
		var row [16]walked
		for nib := range 16 {
			w := step(states[i], nib)
			if !w.fail {
				idx, ok := seen[w.next]
				if !ok {
					idx = len(states)
					seen[w.next] = idx
					states = append(states, w.next)
				}
				w.next = idx
			}
			row[nib] = w
		}
		table = append(table, row)
	}
	// A state accepts when its path from the root is at most seven one bits:
	// the padding of RFC 7541 §5.2, a prefix of EOS.
	accept := make([]bool, len(states))
	depthOnes := map[int][2]int{0: {0, 1}}
	queue := []int{0}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for b := range 2 {
			c := nodes[n].child[b]
			if c < 0 || nodes[c].sym >= 0 {
				continue
			}
			d := depthOnes[n]
			depthOnes[c] = [2]int{d[0] + 1, d[1] & b}
			queue = append(queue, c)
		}
	}
	for i, n := range states {
		d, ok := depthOnes[n]
		accept[i] = ok && d[0] <= 7 && d[1] == 1
	}
	if len(states) > 256 {
		log.Fatalf("automaton has %d states, want at most 256", len(states))
	}

	var buf bytes.Buffer
	buf.WriteString(header("Appendix B's Huffman code as a nibble automaton") + "\npackage http2\n\n")
	fmt.Fprintf(&buf, "// huffStates is how many states the automaton has.\nconst huffStates int = %d\n\n", len(states))
	buf.WriteString("var (\n\t// huffTable is the automaton's transition table: one word per (state,\n\t// nibble), indexed state<<4|nibble. A word's low eight bits are the next\n\t// state, huffEmit says a symbol ended on this nibble, the byte above them\n\t// is that symbol, and huffFail marks a nibble no code can take: an EOS, or\n\t// more than the padding.\n")
	fmt.Fprintf(&buf, "\thuffTable = [huffStates * 16]uint32{\n")
	for _, row := range table {
		buf.WriteString("\t\t")
		for _, w := range row {
			v := uint32(w.next)
			if w.emit == 1 {
				v |= 1 << 8
				v |= uint32(w.sym) << 16
			}
			if w.fail {
				v = 1 << 9
			}
			fmt.Fprintf(&buf, "0x%06x, ", v)
		}
		buf.WriteString("\n")
	}
	buf.WriteString("\t}\n\t// huffAccept reports, per state, whether a string may end there: at most\n\t// seven bits of padding, all ones, a prefix of EOS.\n")
	fmt.Fprintf(&buf, "\thuffAccept = [huffStates]bool{\n")
	for i, a := range accept {
		if a {
			fmt.Fprintf(&buf, "\t\t%d: true,\n", i)
		}
	}
	buf.WriteString("\t}\n)\n")
	write("hpack_huffman_table.go", &buf)

	// The static table: one array of bytes and, per entry, where its name
	// and its value are.
	var data bytes.Buffer
	type ref struct{ nameOff, nameLen, valOff, valLen int }
	refs := make([]ref, 0, 61)
	for _, e := range staticEntries {
		r := ref{nameOff: data.Len(), nameLen: len(e.name)}
		data.WriteString(e.name)
		r.valOff = data.Len()
		r.valLen = len(e.value)
		data.WriteString(e.value)
		refs = append(refs, r)
	}
	var out bytes.Buffer
	out.WriteString(header("Appendix A's static table") + "\npackage http2\n\n")
	// staticByLen: indices of entries grouped by name length, for the encoder.
	buckets := map[int][]int{}
	maxLen := 0
	for i, e := range staticEntries {
		buckets[len(e.name)] = append(buckets[len(e.name)], i+1)
		if len(e.name) > maxLen {
			maxLen = len(e.name)
		}
	}
	idxs := []int{}
	type span struct{ start, end int }
	spans := make([]span, maxLen+1)
	for n := 0; n <= maxLen; n++ {
		spans[n].start = len(idxs)
		idxs = append(idxs, buckets[n]...)
		spans[n].end = len(idxs)
	}
	fmt.Fprintf(&out, "const (\n\t// staticLen is the number of entries of the static table, RFC 7541\n\t// Appendix A; a dynamic entry's index starts one past it.\n\tstaticLen int = 61\n\t// staticNameMax is the longest name of the static table.\n\tstaticNameMax int = %d\n)\n\n", maxLen)
	fmt.Fprintf(&out, "var (\n\t// staticBytes holds every name and value of the static table, concatenated.\n\tstaticBytes = [...]byte{")
	for i, b := range data.Bytes() {
		if i%16 == 0 {
			out.WriteString("\n\t\t")
		}
		fmt.Fprintf(&out, "0x%02x, ", b)
	}
	out.WriteString("\n\t}\n")
	out.WriteString("\t// staticOffsets locates each entry's name and value in staticBytes: four\n\t// numbers per entry — name offset, name length, value offset, value length\n\t// — the entry of index i at 4*(i-1).\n\tstaticOffsets = [staticLen * 4]uint16{")
	for i, r := range refs {
		if i%8 == 0 {
			out.WriteString("\n\t\t")
		}
		fmt.Fprintf(&out, "%d, %d, %d, %d, ", r.nameOff, r.nameLen, r.valOff, r.valLen)
	}
	out.WriteString("\n\t}\n")
	out.WriteString("\t// staticSpans is, per name length, where the entries of that name length\n\t// are in staticSorted: a start and an end index, the length's pair at\n\t// 2*length.\n\tstaticSpans = [(staticNameMax + 1) * 2]uint8{")
	for n := 0; n <= maxLen; n++ {
		if n%16 == 0 {
			out.WriteString("\n\t\t")
		}
		fmt.Fprintf(&out, "%d, %d, ", spans[n].start, spans[n].end)
	}
	out.WriteString("\n\t}\n")
	out.WriteString("\t// staticSorted is the static table's indices grouped by name length, the\n\t// lowest index of a name first.\n")
	fmt.Fprintf(&out, "\tstaticSorted = [%d]uint8{", len(idxs))
	for i, v := range idxs {
		if i%20 == 0 {
			out.WriteString("\n\t\t")
		}
		fmt.Fprintf(&out, "%d, ", v)
	}
	out.WriteString("\n\t}\n)\n")
	write("hpack_static_table.go", &out)
}

func header(what string) string {
	return strings.Join([]string{
		"// Code generated by hpack_tables_gen.go from RFC 7541. DO NOT EDIT.",
		"",
		"// This file holds " + what + ": static data the compiler lays out, nothing",
		"// computed at init (ADR 0172).",
		"",
	}, "\n")
}

func write(name string, buf *bytes.Buffer) {
	src, err := format.Source(buf.Bytes())
	if err != nil {
		log.Fatalf("%s: %v", name, err)
	}
	if err := os.WriteFile(name, src, 0o644); err != nil {
		log.Fatal(err)
	}
}
