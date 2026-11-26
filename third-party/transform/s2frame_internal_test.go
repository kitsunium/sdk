package transform

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/klauspost/compress/s2"
)

// s2FrameOutcome is what one reader made of a stream: the bytes it handed
// over and the error that stopped it, nil at the end.
type s2FrameOutcome struct {
	out []byte
	err error
}

// s2LibraryOutcome reads stream through s2.Reader, the oracle s2Frame
// follows, reused as s2Frame is.
func s2LibraryOutcome(lib *s2.Reader, stream []byte) s2FrameOutcome {
	lib.Reset(bytes.NewReader(stream))
	out, err := io.ReadAll(lib)
	return s2FrameOutcome{out, err}
}

// s2FrameReadOutcome reads stream through s2Frame.Read in uneven steps, the
// decoder reused from a previous stream, as the pool hands it out.
func s2FrameReadOutcome(f *s2Frame, stream []byte) s2FrameOutcome {
	f.reset(bytes.NewReader(stream))
	var out []byte
	p := make([]byte, 1000)
	for {
		n, err := f.Read(p)
		out = append(out, p[:n]...)
		//: the end is not an error of the stream's.
		if err == io.EOF {
			return s2FrameOutcome{out, nil}
		}
		//: a fault stops the read.
		if err != nil {
			return s2FrameOutcome{out, err}
		}
	}
}

// s2FrameWriteToOutcome drains stream through s2Frame.WriteTo.
func s2FrameWriteToOutcome(f *s2Frame, stream []byte) s2FrameOutcome {
	f.reset(bytes.NewReader(stream))
	var out bytes.Buffer
	_, err := f.WriteTo(&out)
	return s2FrameOutcome{out.Bytes(), err}
}

// sameS2Outcome says whether two outcomes are the same bytes and the same
// error — the library's own sentinel, or the same nil.
func sameS2Outcome(got, want s2FrameOutcome) bool {
	return bytes.Equal(got.out, want.out) && errors.Is(got.err, want.err) && (got.err == nil) == (want.err == nil)
}

// s2Written is plain through s2.NewWriter built with opts.
func s2Written(tb testing.TB, plain []byte, opts ...s2.WriterOption) []byte {
	tb.Helper()
	var buf bytes.Buffer
	w := s2.NewWriter(&buf, opts...)
	if _, err := w.Write(plain); err != nil {
		tb.Fatalf("s2 Write: %v", err)
	}
	if err := w.Close(); err != nil {
		tb.Fatalf("s2 Close: %v", err)
	}
	return buf.Bytes()
}

// s2RawChunk is one chunk of the framing format: its type, then its body.
func s2RawChunk(kind byte, body []byte) []byte {
	return append([]byte{kind, byte(len(body)), byte(len(body) >> 8), byte(len(body) >> 16)}, body...)
}

// s2StoredChunk is a stored block chunk holding block under its right CRC.
func s2StoredChunk(block []byte) []byte {
	c := crc32.Update(0, s2CRCTable, block)
	sum := binary.LittleEndian.AppendUint32(nil, (c>>15|c<<17)+s2CRCMask)
	return s2RawChunk(s2ChunkUncompressed, append(sum, block...))
}

// s2Streams is the differential table's input: streams the library writes,
// under every option that changes the bytes, and streams built by hand to
// reach each refusal of the format.
func s2Streams(tb testing.TB) map[string][]byte {
	tb.Helper()
	r := rand.New(rand.NewPCG(892, 892))
	random := make([]byte, 600<<10)
	for i := range random {
		random[i] = byte(r.Uint32())
	}
	text := bytes.Repeat([]byte("a stream of blocks, each one checked "), 30000)
	id := s2RawChunk(s2ChunkStreamID, []byte(s2Magic))
	snappyID := s2RawChunk(s2ChunkStreamID, []byte(s2MagicSnappy))
	stored := s2StoredChunk([]byte("stored block"))
	badCRC := bytes.Clone(stored)
	badCRC[len(badCRC)-1] ^= 1
	valid := s2Written(tb, text, s2.WriterBlockSize(s2StreamBlockSize))
	return map[string][]byte{
		"empty input":             nil,
		"empty stream":            s2Written(tb, nil),
		"text, default blocks":    s2Written(tb, text),
		"text, 256 KiB blocks":    valid,
		"text, 4 KiB blocks":      s2Written(tb, text, s2.WriterBlockSize(4<<10)),
		"text, 4 MiB blocks":      s2Written(tb, text, s2.WriterBlockSize(4<<20)),
		"random, stored blocks":   s2Written(tb, random, s2.WriterBlockSize(s2StreamBlockSize)),
		"snappy framing":          s2Written(tb, text, s2.WriterSnappyCompat()),
		"padded":                  s2Written(tb, text, s2.WriterPadding(1<<10)),
		"indexed":                 s2Written(tb, text, s2.WriterAddIndex()),
		"no identifier":           stored,
		"another identifier":      append(s2RawChunk(s2ChunkStreamID, []byte("nOtS2!")), stored...),
		"identifier too long":     s2RawChunk(s2ChunkStreamID, []byte(s2Magic+"!")),
		"identifier cut short":    id[:7],
		"a header cut short":      append(bytes.Clone(id), 0x01, 0x10),
		"a chunk cut short":       append(bytes.Clone(id), stored[:len(stored)-3]...),
		"shorter than its CRC":    append(bytes.Clone(id), s2RawChunk(s2ChunkCompressed, []byte{1, 2})...),
		"stored shorter than CRC": append(bytes.Clone(id), s2RawChunk(s2ChunkUncompressed, []byte{1})...),
		"past any 4 MiB block":    append(bytes.Clone(id), 0x00, 0xff, 0xff, 0xff),
		"stored past 4 MiB":       append(bytes.Clone(id), 0x01, 0xff, 0xff, 0xff),
		"a block header refused":  append(bytes.Clone(id), s2RawChunk(s2ChunkCompressed, []byte{0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})...),
		"a body short of header":  append(bytes.Clone(id), s2RawChunk(s2ChunkCompressed, []byte{0, 0, 0, 0, 0x20, 0x00})...),
		"a CRC mismatch":          append(bytes.Clone(id), badCRC...),
		"snappy past 64 KiB":      append(bytes.Clone(snappyID), s2StoredChunk(make([]byte, s2MaxSnappyBlock+1))...),
		"reserved unskippable":    append(bytes.Clone(id), s2RawChunk(0x02, []byte("x"))...),
		"reserved skippable":      append(append(bytes.Clone(id), s2RawChunk(0x80, make([]byte, 70<<10))...), stored...),
		"skippable cut short":     append(bytes.Clone(id), 0x80, 0x00, 0x10, 0x00, 1, 2, 3),
		"blocks then corruption":  append(bytes.Clone(valid), badCRC...),
		"truncated library bytes": valid[:len(valid)/2],
	}
}

// TestS2FrameReadsWhatTheLibraryReads holds s2Frame to s2.Reader, read
// through Read and through WriteTo: on every stream, the same bytes handed
// over and the same error — the library's own sentinel — or the same end.
func TestS2FrameReadsWhatTheLibraryReads(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		stream []byte
	}
	f, lib := new(s2Frame), s2.NewReader(nil)
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		want := s2LibraryOutcome(lib, c.stream)
		if got := s2FrameReadOutcome(f, c.stream); !sameS2Outcome(got, want) {
			t.Fatalf("%s: Read handed over %d bytes then %v; s2.Reader %d then %v", c.name, len(got.out), got.err, len(want.out), want.err)
		}
		if got := s2FrameWriteToOutcome(f, c.stream); !sameS2Outcome(got, want) {
			t.Fatalf("%s: WriteTo wrote %d bytes then %v; s2.Reader %d then %v", c.name, len(got.out), got.err, len(want.out), want.err)
		}
	}
	for name, stream := range s2Streams(t) {
		runCase(t, tc{name, stream})
	}
}

// TestS2FrameStopsWhereItsWriterDoes: a writer's failure stops WriteTo with
// that very error, a writer that takes less without one with
// io.ErrShortWrite, and what was not taken is still there for the next read.
func TestS2FrameStopsWhereItsWriterDoes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		w    *s2StubbornWriter
		want error
	}
	plain := bytes.Repeat([]byte("written, then refused "), 1000)
	stream := s2Written(t, plain, s2.WriterBlockSize(4<<10))
	boom := io.ErrClosedPipe
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		f := new(s2Frame)
		f.reset(bytes.NewReader(stream))
		n, err := f.WriteTo(c.w)
		if err != c.want || n != int64(c.w.got.Len()) {
			t.Fatalf("%s: wrote %d (%d taken), err=%v, want %v", c.name, n, c.w.got.Len(), err, c.want)
		}
		rest, rerr := io.ReadAll(f)
		if rerr != nil || !bytes.Equal(append(c.w.got.Bytes(), rest...), plain) {
			t.Fatalf("%s: the rest read back: %d bytes, err=%v", c.name, len(rest), rerr)
		}
	}
	for _, c := range []tc{
		{"a failing writer", &s2StubbornWriter{take: 100, fail: boom}, boom},
		{"a short writer", &s2StubbornWriter{take: 100}, io.ErrShortWrite},
		{"a writer that lies", &s2StubbornWriter{take: -1}, io.ErrShortWrite},
	} {
		runCase(t, c)
	}
}

// s2StubbornWriter takes take bytes of its first write — none when negative,
// reporting one more than given — and returns fail, keeping what it took.
type s2StubbornWriter struct {
	got  bytes.Buffer
	take int
	fail error
}

// Write implements io.Writer, stubbornly.
func (w *s2StubbornWriter) Write(p []byte) (n int, err error) {
	//: a count it never wrote.
	if w.take < 0 {
		return len(p) + 1, nil
	}
	n = min(w.take, len(p))
	w.got.Write(p[:n])
	return n, w.fail
}

// FuzzS2FrameAgainstTheLibrary is the differential behind s2Frame (#892):
// on any bytes, it hands over what s2.Reader hands over and stops with the
// same error, through Read and through WriteTo, and never panics.
func FuzzS2FrameAgainstTheLibrary(f *testing.F) {
	for _, stream := range s2Streams(f) {
		//: the seeds stay small enough to mutate.
		if len(stream) < 64<<10 {
			f.Add(stream)
		}
	}
	dec, lib := new(s2Frame), s2.NewReader(nil)
	f.Fuzz(func(t *testing.T, stream []byte) {
		want := s2LibraryOutcome(lib, stream)
		if got := s2FrameReadOutcome(dec, stream); !sameS2Outcome(got, want) {
			t.Fatalf("Read: %d bytes then %v; s2.Reader %d then %v", len(got.out), got.err, len(want.out), want.err)
		}
		if got := s2FrameWriteToOutcome(dec, stream); !sameS2Outcome(got, want) {
			t.Fatalf("WriteTo: %d bytes then %v; s2.Reader %d then %v", len(got.out), got.err, len(want.out), want.err)
		}
	})
}
