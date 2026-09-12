package entitlement

import (
	"encoding/binary"
	"errors"
	"testing"
)

// Test_roughtimeTag pins the ordering the wire format sorts by.
//
// Tags are compared as little-endian uint32 over their ASCII bytes, which is
// not the order they read in. Getting it wrong produces a request a server
// rejects silently — and silence is what a filtered port looks like too, so the
// bug would hide behind the network for as long as anyone cared to look.
func Test_roughtimeTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		before string
		after  string
		reason string
	}{
		{name: "VER precedes NONC", before: "VER", after: "NONC", reason: "NUL-padding puts the short tag first, which reading left to right would not suggest"},
		{name: "NONC precedes ZZZZ", before: "NONC", after: "ZZZZ", reason: "the padding tag sorts last, which is why it can carry the slack"},
		{name: "SIG precedes PATH", before: "SIG", after: "PATH", reason: "the response's own field order depends on this"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if roughtimeTag(tt.before) >= roughtimeTag(tt.after) {
				t.Errorf("roughtimeTag(%q) = %#x, want it below roughtimeTag(%q) = %#x (%s)",
					tt.before, roughtimeTag(tt.before), tt.after, roughtimeTag(tt.after), tt.reason)
			}
		})
	}
}

// Test_encodeRoughtimeMessage pins that what this writes, the decoder reads.
//
// The two halves are the only check on each other that exists here: with no
// live server to answer, a round trip through both is what establishes that the
// offset table means the same thing to the writer and the reader.
func Test_encodeRoughtimeMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		fields []roughtimeField
		reason string
	}{
		{
			name: "three fields survive a round trip",
			fields: []roughtimeField{
				{tag: roughtimeTag("VER"), value: []byte{1, 0, 0, 0}},
				{tag: roughtimeTag("NONC"), value: make([]byte, roughtimeNonceSize)},
				{tag: roughtimeTag("ZZZZ"), value: make([]byte, 16)},
			},
			reason: "the offset table describes values 1..N-1, so an off-by-one here reads a neighbour's bytes",
		},
		{
			name:   "a single field survives a round trip",
			fields: []roughtimeField{{tag: roughtimeTag("NONC"), value: make([]byte, roughtimeNonceSize)}},
			reason: "with one field the offset table is EMPTY, which is the edge the loop bound has to get right",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decoded, err := decodeRoughtimeMessage(encodeRoughtimeMessage(tt.fields))
			if err != nil {
				t.Fatalf("decodeRoughtimeMessage() error = %v, want nil (%s)", err, tt.reason)
			}
			for _, field := range tt.fields {
				if got := decoded[field.tag]; len(got) != len(field.value) {
					t.Errorf("field %#x round-tripped to %d bytes, want %d (%s)", field.tag, len(got), len(field.value), tt.reason)
				}
			}
		})
	}
}

// Test_decodeRoughtimeMessage pins that every length and offset a hostile
// server chose is checked before it is used to slice the buffer it arrived in.
//
// This is the only code in the package that indexes into attacker-sized data
// without a signature having vouched for it first — the signatures are checked
// on fields this function produced — so a missing bound here is a read of
// somebody else's memory, not merely a wrong answer.
func Test_decodeRoughtimeMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// raw is the message offered.
		raw    []byte
		reason string
	}{
		{name: "an empty buffer", raw: nil, reason: "there is not even a count to read"},
		{name: "a count with no header behind it", raw: binary.LittleEndian.AppendUint32(nil, 4), reason: "the declared header runs past the buffer"},
		{name: "zero tags", raw: binary.LittleEndian.AppendUint32(nil, 0), reason: "a message with no fields is not one"},
		{
			name:   "an absurd tag count",
			raw:    binary.LittleEndian.AppendUint32(nil, uint32(roughtimeMaxTags+1)),
			reason: "the count sizes an allocation, and the server chose it",
		},
		{
			//: Two tags, an offset pointing past the values section.
			name: "an offset past the end",
			raw: func() []byte {
				out := binary.LittleEndian.AppendUint32(nil, 2)
				out = binary.LittleEndian.AppendUint32(out, 999)
				out = binary.LittleEndian.AppendUint32(out, roughtimeTag("AAAA"))
				out = binary.LittleEndian.AppendUint32(out, roughtimeTag("BBBB"))
				return append(out, 1, 2, 3, 4)
			}(),
			reason: "this is the slice that would read past the buffer",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := decodeRoughtimeMessage(tt.raw); !errors.Is(err, ErrCIUnverifiable) {
				t.Errorf("decodeRoughtimeMessage() error = %v, want ErrCIUnverifiable (%s)", err, tt.reason)
			}
		})
	}
}

// Test_roughtimeUint64 pins that a field too short to hold the number it claims
// to be is refused rather than read past.
func Test_roughtimeUint64(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   []byte
		wantErr bool
		reason  string
	}{
		{name: "eight bytes read", value: make([]byte, 8), reason: "the ordinary case"},
		{name: "seven bytes refused", value: make([]byte, 7), wantErr: true, reason: "reading the eighth would be reading a neighbour"},
		{name: "an absent field refused", value: nil, wantErr: true, reason: "a tag the server omitted decodes to nil, not to zero"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := roughtimeUint64(tt.value, "field")
			if (err != nil) != tt.wantErr {
				t.Errorf("roughtimeUint64() error = %v, want error = %v (%s)", err, tt.wantErr, tt.reason)
			}
		})
	}
}

// Test_roughtimeUint32 pins the same bound for the narrower fields.
func Test_roughtimeUint32(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   []byte
		wantErr bool
		reason  string
	}{
		{name: "four bytes read", value: make([]byte, roughtimeTagSize), reason: "the ordinary case"},
		{name: "three bytes refused", value: make([]byte, 3), wantErr: true, reason: "reading the fourth would be reading a neighbour"},
		{name: "an absent field refused", value: nil, wantErr: true, reason: "a tag the server omitted decodes to nil, not to zero"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := roughtimeUint32(tt.value, "field")
			if (err != nil) != tt.wantErr {
				t.Errorf("roughtimeUint32() error = %v, want error = %v (%s)", err, tt.wantErr, tt.reason)
			}
		})
	}
}

// Test_roughtimeHeaderSize pins the arithmetic an off-by-one hides in: a count,
// then N-1 offsets, then N tags.
//
// Getting it wrong by four bytes shifts every value by one field, which does
// not crash and does not obviously misbehave — it simply reads each tag's
// neighbour, and every signature then fails for a reason that looks like a
// forgery.
func Test_roughtimeHeaderSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// tags is the declared count.
		tags int
		// size is how many bytes actually arrived.
		size int
		// want is the expected header length, or -1 when it must be refused.
		want   int
		reason string
	}{
		{name: "one tag needs count plus one tag", tags: 1, size: 64, want: roughtimeTagSize * 2, reason: "with one field the offset table is EMPTY, which is the edge the formula has to get right"},
		{name: "two tags need one offset between them", tags: 2, size: 64, want: roughtimeTagSize * 4, reason: "count, then N-1 offsets, then N tags"},
		{name: "five tags scale linearly", tags: 5, size: 128, want: roughtimeTagSize * 10, reason: "the response carries five"},
		{name: "a header past the buffer is refused", tags: 8, size: 12, want: -1, reason: "the declared header must fit in what actually arrived"},
		{name: "zero tags is not a message", tags: 0, size: 64, want: -1, reason: "a message with no fields is not one"},
		{name: "an absurd count is refused", tags: roughtimeMaxTags + 1, size: 4096, want: -1, reason: "the count sizes an allocation, and the server chose it"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw := binary.LittleEndian.AppendUint32(nil, uint32(tt.tags))
			raw = append(raw, make([]byte, max(tt.size-roughtimeTagSize, 0))...)

			_, header, err := roughtimeHeaderSize(raw)
			//: A negative want marks the rows that must be refused.
			if tt.want < 0 {
				if !errors.Is(err, ErrCIUnverifiable) {
					t.Errorf("roughtimeHeaderSize() error = %v, want ErrCIUnverifiable (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("roughtimeHeaderSize() error = %v, want nil (%s)", err, tt.reason)
			}
			if header != tt.want {
				t.Errorf("roughtimeHeaderSize() header = %d, want %d (%s)", header, tt.want, tt.reason)
			}
		})
	}
}

// Test_roughtimeValueBounds pins the implicit first offset and every way the
// table can fail to describe the buffer it arrived in.
//
// The first value's offset is absent from the table — it is always zero — so
// reading it from the table anyway shifts every field by one. The remaining
// rows are the slices that would read past the buffer, which is the only place
// in this package where attacker-sized data is indexed without a signature
// having vouched for it first.
func Test_roughtimeValueBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// offset is what the table says for the SECOND value.
		offset uint32
		// index is which field is being resolved.
		index int
		// wantStart is the expected start, or -1 when it must be refused.
		wantStart int
		reason    string
	}{
		{name: "the first value starts at zero", offset: 4, index: 0, wantStart: 0, reason: "its offset is implicit and absent from the table"},
		{name: "the second value starts where the table says", offset: 4, index: 1, wantStart: 4, reason: "the table describes values 1..N-1"},
		{name: "an offset past the values section is refused", offset: 999, index: 1, wantStart: -1, reason: "this is the slice that would read past the buffer"},
		{name: "a descending table is refused", offset: 8, index: 0, wantStart: -1, reason: "value 0 would end before it began, which is a negative length"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw := binary.LittleEndian.AppendUint32(nil, 2)
			raw = binary.LittleEndian.AppendUint32(raw, tt.offset)
			raw = append(raw, make([]byte, 16)...)

			//: A values section of 6 bytes, so offset 8 is descending for
			//: value 0 and offset 999 is past the end for value 1.
			start, _, err := roughtimeValueBounds(raw, 2, tt.index, 6)
			//: A negative want marks the rows that must be refused.
			if tt.wantStart < 0 {
				if !errors.Is(err, ErrCIUnverifiable) {
					t.Errorf("roughtimeValueBounds() error = %v, want ErrCIUnverifiable (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("roughtimeValueBounds() error = %v, want nil (%s)", err, tt.reason)
			}
			if start != tt.wantStart {
				t.Errorf("roughtimeValueBounds() start = %d, want %d (%s)", start, tt.wantStart, tt.reason)
			}
		})
	}
}
