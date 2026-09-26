// Package profiling_test — Parse: real profiles the runtime writes, and hand
// built ones that exercise each rule of the wire format and each refusal.
package profiling_test

import (
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/profiling"
)

// pb builds protocol-buffer bytes for the fixtures.
type pb struct{ buf []byte }

// varint appends a base-128 varint.
func (b *pb) varint(v uint64) *pb {
	for v >= 0x80 {
		b.buf = append(b.buf, byte(v)|0x80)
		v >>= 7
	}
	b.buf = append(b.buf, byte(v))
	return b
}

// int appends a varint field.
func (b *pb) int(field int, v int64) *pb {
	return b.varint(uint64(field) << 3).varint(uint64(v))
}

// bytes appends a length-delimited field.
func (b *pb) bytes(field int, payload []byte) *pb {
	b.varint(uint64(field)<<3 | 2).varint(uint64(len(payload)))
	b.buf = append(b.buf, payload...)
	return b
}

// packed appends a packed repeated varint field.
func (b *pb) packed(field int, vs ...uint64) *pb {
	var run pb
	for _, v := range vs {
		run.varint(v)
	}
	return b.bytes(field, run.buf)
}

// msg returns a message built by fill.
func msg(fill func(b *pb)) []byte {
	var b pb
	fill(&b)
	return b.buf
}

// fixture is a small, valid profile: strings, one function, two locations —
// the second with an inlined call — and two samples, one written packed and
// one unpacked, one labelled.
func fixture(tweak func(b *pb)) []byte {
	var b pb
	b.bytes(1, msg(func(m *pb) { m.int(1, 1).int(2, 2) }))      // sample type samples/count
	b.bytes(1, msg(func(m *pb) { m.int(1, 3).int(2, 4) }))      // sample type cpu/nanoseconds
	b.bytes(3, msg(func(m *pb) { m.int(1, 1).int(2, 0x1000) })) // a mapping, skipped
	b.bytes(5, msg(func(m *pb) { m.int(1, 1).int(2, 5).int(4, 6).int(5, 10) }))
	b.bytes(5, msg(func(m *pb) { m.int(1, 2).int(2, 7).int(4, 6).int(5, 20) }))
	b.bytes(4, msg(func(m *pb) {
		m.int(1, 1).int(3, 0xabc).bytes(4, msg(func(l *pb) { l.int(1, 1).int(2, 12) }))
	}))
	b.bytes(4, msg(func(m *pb) {
		m.int(1, 2).bytes(4, msg(func(l *pb) { l.int(1, 1).int(2, 13) })).bytes(4, msg(func(l *pb) { l.int(1, 2).int(2, 25) }))
	}))
	b.bytes(4, msg(func(m *pb) { m.int(1, 3).int(3, 0xdead) })) // unsymbolized
	b.bytes(2, msg(func(s *pb) {
		s.packed(1, 1, 2).packed(2, 3, 30_000_000)
		s.bytes(3, msg(func(l *pb) { l.int(1, 8).int(2, 9) }))
	}))
	b.bytes(2, msg(func(s *pb) { s.int(1, 3).int(1, 2).int(2, 1).int(2, 10_000_000) }))
	b.int(9, 1_700_000_000_000_000_000).int(10, 1_000_000_000).int(12, 10_000_000).int(14, 3)
	b.int(13, 9).packed(13, 8) // two comments, unpacked then packed
	b.bytes(11, msg(func(m *pb) { m.int(1, 3).int(2, 4) }))
	b.varint(99<<3 | 5).buf = append(b.buf, 1, 2, 3, 4) // an unknown fixed32 field, skipped
	b.varint(98<<3 | 1).buf = append(b.buf, 1, 2, 3, 4, 5, 6, 7, 8)
	if tweak != nil {
		tweak(&b)
	}
	for _, s := range []string{"", "samples", "count", "cpu", "nanoseconds", "main.work", "/src/main.go", "main.inlined", "worker", "db"} {
		b.bytes(6, []byte(s))
	}
	return b.buf
}

// TestAHandBuiltProfileDecodesEveryField pins the decoding: both repeated
// encodings, inlined frames innermost first, an unsymbolized frame, labels,
// the header, and the fields Parse skips.
func TestAHandBuiltProfileDecodesEveryField(t *testing.T) {
	t.Parallel()
	p, err := profiling.Parse(fixture(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.SampleTypes) != 2 || p.SampleTypes[1] != (profiling.SampleTypeValue{Type: "cpu", Unit: "nanoseconds"}) {
		t.Errorf("SampleTypes = %v", p.SampleTypes)
	}
	if p.Period != 10_000_000 || p.PeriodType.Type != "cpu" || p.DefaultSampleType != "cpu" || p.Duration.Seconds() != 1 || p.Time.IsZero() {
		t.Errorf("header = %+v", p)
	}
	if len(p.Comments) != 2 || p.Comments[0] != "db" || p.Comments[1] != "worker" {
		t.Errorf("Comments = %v", p.Comments)
	}
	if len(p.Samples) != 2 {
		t.Fatalf("%d samples", len(p.Samples))
	}
	first := p.Samples[0]
	want := []profiling.FrameValue{
		{Function: "main.work", File: "/src/main.go", Line: 12, StartLine: 10},
		{Function: "main.work", File: "/src/main.go", Line: 13, StartLine: 10},
		{Function: "main.inlined", File: "/src/main.go", Line: 25, StartLine: 20},
	}
	if len(first.Stack) != len(want) {
		t.Fatalf("stack = %+v", first.Stack)
	}
	for i := range want {
		if first.Stack[i] != want[i] {
			t.Errorf("frame %d = %+v; want %+v", i, first.Stack[i], want[i])
		}
	}
	if first.Values[1] != 30_000_000 || first.Labels["worker"][0] != "db" {
		t.Errorf("first sample = %+v", first)
	}
	second := p.Samples[1]
	if second.Stack[0].Address != 0xdead || second.Stack[0].Function != "" || second.Values[1] != 10_000_000 || second.Labels != nil {
		t.Errorf("second sample = %+v", second)
	}
}

// TestParseReadsGzipAndRawAlike pins that the gzipped stream runtime/pprof
// writes and the same bytes uncompressed decode to the same profile.
func TestParseReadsGzipAndRawAlike(t *testing.T) {
	t.Parallel()
	raw := fixture(nil)
	var z bytes.Buffer
	zw := gzip.NewWriter(&z)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	a, errA := profiling.Parse(raw)
	b, errB := profiling.Parse(z.Bytes())
	if errA != nil || errB != nil || len(a.Samples) != len(b.Samples) || a.Samples[0].Stack[2] != b.Samples[0].Stack[2] {
		t.Fatalf("raw %v, gzip %v", errA, errB)
	}
}

// TestEveryMalformationIsRefusedByCode walks the refusals: nothing reads past
// the input, nothing points nowhere, and nothing panics.
func TestEveryMalformationIsRefusedByCode(t *testing.T) {
	t.Parallel()
	valid := fixture(nil)
	type tc struct {
		name string
		data []byte
	}
	cases := []tc{
		{"a truncated varint", append(append([]byte{}, valid...), 0x08, 0xff)},
		{"a length past the end", append(append([]byte{}, valid...), 0x12, 0x7f)},
		{"a group wire type", append(append([]byte{}, valid...), 0x0b)},
		{"a sample as a varint", append(append([]byte{}, valid...), 0x10, 0x01)},
		{"a string table not starting empty", msg(func(b *pb) { b.bytes(6, []byte("first")) })},
		{"a string index past the table", fixture(func(b *pb) { b.bytes(1, msg(func(m *pb) { m.int(1, 99) })) })},
		{"a sample on an unknown location", fixture(func(b *pb) {
			b.bytes(2, msg(func(s *pb) { s.int(1, 42).int(2, 1).int(2, 1) }))
		})},
		{"a line on an unknown function", fixture(func(b *pb) {
			b.bytes(4, msg(func(m *pb) { m.int(1, 9).bytes(4, msg(func(l *pb) { l.int(1, 77) })) }))
			b.bytes(2, msg(func(s *pb) { s.int(1, 9).int(2, 1).int(2, 1) }))
		})},
		{"a comment index past the table", fixture(func(b *pb) { b.int(13, 99) })},
		{"a sample with one value too few", fixture(func(b *pb) {
			b.bytes(2, msg(func(s *pb) { s.int(1, 1).int(2, 1) }))
		})},
		{"a corrupt gzip stream", []byte{0x1f, 0x8b, 0x08, 0x00, 0x01}},
		{"a varint past 64 bits", skipped(0x02)},
		{"a varint announcing an eleventh byte", append(skipped(0x81), 0x00)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p, err := profiling.Parse(c.data)
		if p != nil || !errs.HasCode(err, profiling.CodeProfileMalformed) {
			t.Fatalf("Parse() = %v, %v; want PROFILE_MALFORMED", p, err)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// skipped is the fixture followed by field 15 — one Parse skips — holding a
// ten-byte varint whose last byte is last.
func skipped(last byte) []byte {
	out := append(fixture(nil), 15<<3)
	for range 9 {
		out = append(out, 0xff)
	}
	return append(out, last)
}

// TestAVarintOfSixtyFourBitsIsRead pins the other side of the tenth byte: a
// value with its 64th bit set is read, not refused.
func TestAVarintOfSixtyFourBitsIsRead(t *testing.T) {
	t.Parallel()
	if p, err := profiling.Parse(skipped(0x01)); err != nil || len(p.Samples) != 2 {
		t.Fatalf("Parse() = %v, %v; want the fixture's two samples", p, err)
	}
}

// TestParseIsBounded pins ProfileTooLarge, before reading and once inflated.
func TestParseIsBounded(t *testing.T) {
	t.Parallel()
	if _, err := profiling.Parse(make([]byte, profiling.MaxProfileBytes+1)); !errs.HasCode(err, profiling.CodeProfileTooLarge) {
		t.Errorf("an oversized input = %v", err)
	}
	var z bytes.Buffer
	zw, err := gzip.NewWriterLevel(&z, gzip.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for range profiling.MaxProfileBytes/len(chunk) + 1 {
		if _, err := zw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := profiling.Parse(z.Bytes()); !errs.HasCode(err, profiling.CodeProfileTooLarge) {
		t.Errorf("a stream inflating past the bound = %v", err)
	}
}

// FuzzParse pins that no input makes Parse panic or read past its buffer.
func FuzzParse(f *testing.F) {
	f.Add(fixture(nil))
	f.Add([]byte{})
	f.Add([]byte{0x1f, 0x8b})
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := profiling.Parse(data)
		if err == nil && p == nil {
			t.Fatal("Parse returned neither a profile nor an error")
		}
	})
}
