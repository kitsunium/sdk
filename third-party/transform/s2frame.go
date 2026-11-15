package transform

import (
	"hash/crc32"
	"io"

	"github.com/klauspost/compress/s2"
)

// The s2 framing format (snappy's framing_format.txt with s2's changes), as
// s2.Reader reads it: a stream identifier, then chunks of a 1-byte type and a
// 3-byte little-endian length, a block chunk opening with the CRC-32C of its
// decoded bytes.
const (
	// s2ChunkCompressed is a block in the s2 block format.
	s2ChunkCompressed byte = 0x00
	// s2ChunkUncompressed is a block stored as its bytes.
	s2ChunkUncompressed byte = 0x01
	// s2ChunkLastUnskippable closes the reserved types a reader must refuse.
	s2ChunkLastUnskippable byte = 0x7f
	// s2ChunkStreamID is the stream identifier, first in every stream.
	s2ChunkStreamID byte = 0xff
	// s2HeaderSize is a chunk's type and length.
	s2HeaderSize int = 4
	// s2ChecksumSize is a block chunk's leading CRC.
	s2ChecksumSize int = 4
	// s2MaxBlock is the largest decoded block a chunk may carry: 4 MiB.
	s2MaxBlock int = 4 << 20
	// s2MaxSnappyBlock is that bound in a snappy-identified stream: 64 KiB.
	s2MaxSnappyBlock int = 1 << 16
	// s2SkipStep is the most a skipped chunk reads at once.
	s2SkipStep int = 64 << 10
	// s2CRCMask is the constant the masked checksum adds (framing_format.txt §3).
	s2CRCMask uint32 = 0xa282ead8
	// s2Magic and s2MagicSnappy are the stream identifier's two bodies.
	s2Magic       = "S2sTwO"
	s2MagicSnappy = "sNaPpY"
	// s2StreamBlockSize is the block NewWriter cuts a stream into: 256 KiB.
	// The library's default, 1 MiB, outgrows a core's L2, so a reader's CRC
	// and copy reread a decoded block from L3 (#892); 256 KiB costs 0.24 % of
	// JSON's ratio.
	s2StreamBlockSize int = 256 << 10
)

var (
	// s2CRCTable is the Castagnoli table, the CRC-32C the format masks.
	s2CRCTable = crc32.MakeTable(crc32.Castagnoli)
	// s2MaxChunk is the longest block chunk accepted before anything is
	// allocated for it: a 4 MiB block's worst encoding and its checksum.
	s2MaxChunk int = s2.MaxEncodedLen(s2MaxBlock) + s2ChecksumSize
)

// s2Frame decodes the s2 framing format as s2.Reader does — the same chunks
// accepted, the same bounds checked before an allocation, the same errors —
// for one reason: WriteTo hands each verified block to the caller's writer
// from where it was decoded, where s2.Reader can only copy it into a buffer
// (#892). It never runs a goroutine, and every block's CRC-32C is checked
// before a byte of it leaves (ADR 0213 D3).
type s2Frame struct {
	// r is the caller's reader, nil once released.
	r io.Reader
	// err is the sticky outcome: io.EOF, or the first fault.
	err error
	// chunk holds a compressed chunk as read, and is scratch for a skip.
	chunk []byte
	// block holds the decoded, verified block; block[i:j] is not handed over.
	block []byte
	i, j  int
	// started says the stream identifier was read.
	started bool
	// snappy says it named snappy's framing, whose blocks are 64 KiB at most.
	snappy bool
	// header is a chunk's type and length, then an identifier's body.
	header [s2HeaderSize + len(s2Magic)]byte
}

// reset binds the decoder to r, keeping its buffers.
func (f *s2Frame) reset(r io.Reader) {
	f.r, f.err = r, nil
	f.i, f.j = 0, 0
	f.started, f.snappy = false, false
}

// Read implements io.Reader: it copies the current block into p, decoding the
// next one when it is spent.
func (f *s2Frame) Read(p []byte) (n int, err error) {
	//: a block is decoded once the last one is handed over.
	for f.i == f.j {
		//: the end, or a fault: nothing more to decode.
		if f.err != nil {
			return 0, f.err
		}
		f.next()
	}
	n = copy(p, f.block[f.i:f.j])
	f.i += n
	return n, nil
}

// WriteTo implements io.WriterTo: every block, once its checksum holds, is
// written to w in one call, from where it was decoded. The end returns nil,
// as io.Copy expects; a fault of w stops the stream and is returned as is.
func (f *s2Frame) WriteTo(w io.Writer) (n int64, err error) {
	for {
		//: what is decoded goes out before anything else is read.
		if f.i < f.j {
			written, werr := f.write(w)
			n += written
			//: w refused it: the stream stops where w did.
			if werr != nil {
				return n, werr
			}
		}
		//: the end is a success, a fault is the stream's.
		if f.err != nil {
			return n, f.settled()
		}
		f.next()
	}
}

// write hands block[i:j] to w, a short write an error as io.Copy reads one.
func (f *s2Frame) write(w io.Writer) (n int64, err error) {
	pending := f.block[f.i:f.j]
	m, werr := w.Write(pending)
	//: a count outside what was given is a writer's fault, nothing written.
	if m < 0 || m > len(pending) {
		m, werr = 0, io.ErrShortWrite
	}
	f.i += m
	//: a writer that took less without saying why.
	if werr == nil && m < len(pending) {
		werr = io.ErrShortWrite
	}
	return int64(m), werr
}

// settled is the WriteTo outcome of a sticky error: none at the end.
func (f *s2Frame) settled() error {
	//: the end is not an error to a WriterTo.
	if f.err == io.EOF {
		return nil
	}
	return f.err
}

// next reads one chunk and acts on it: a block decoded and verified into
// block[0:n], an identifier checked, a skippable chunk skipped. It sets err
// on the end or on a fault.
func (f *s2Frame) next() {
	hdr := f.header[:s2HeaderSize]
	//: the end between two chunks is the stream's end.
	if !f.readFull(hdr, true) {
		return
	}
	kind := hdr[0]
	size := int(hdr[1]) | int(hdr[2])<<8 | int(hdr[3])<<16
	//: a stream opens with its identifier, or it is not one.
	if !f.started && kind != s2ChunkStreamID {
		f.err = s2.ErrCorrupt
		return
	}
	f.started = true
	switch {
	//: a block in the block format.
	case kind == s2ChunkCompressed:
		f.compressed(size)
	//: a block stored as its bytes.
	case kind == s2ChunkUncompressed:
		f.uncompressed(size)
	//: the identifier, again or for the first time.
	case kind == s2ChunkStreamID:
		f.identifier(size)
	//: a reserved type a reader must understand, and does not.
	case kind <= s2ChunkLastUnskippable:
		f.err = s2.ErrUnsupported
	//: padding, an index, or a reserved skippable type.
	default:
		f.skip(size)
	}
}

// compressed decodes a block chunk of size bytes into block.
func (f *s2Frame) compressed(size int) {
	//: shorter than its checksum, or longer than any 4 MiB block encodes to.
	if size < s2ChecksumSize || size > s2MaxChunk {
		f.err = s2.ErrCorrupt
		return
	}
	f.chunk = grown(f.chunk, size)
	chunk := f.chunk[:size]
	//: a chunk cut short is a corrupt stream.
	if !f.readFull(chunk, false) {
		return
	}
	n, lerr := s2.DecodedLen(chunk[s2ChecksumSize:])
	//: a block header the block format refuses.
	if lerr != nil {
		f.err = lerr
		return
	}
	//: a block past the stream's bound, refused before it is allocated.
	if !f.admits(n) {
		return
	}
	f.block = grown(f.block, n)
	_, derr := s2.Decode(f.block[:n], chunk[s2ChecksumSize:])
	//: a block whose body does not honour its header.
	if derr != nil {
		f.err = derr
		return
	}
	f.verify(chunk[:s2ChecksumSize], n)
}

// uncompressed reads a stored block chunk of size bytes into block.
func (f *s2Frame) uncompressed(size int) {
	//: shorter than its checksum, or longer than s2.Reader's chunk bound.
	if size < s2ChecksumSize || size > s2MaxChunk {
		f.err = s2.ErrCorrupt
		return
	}
	sum := f.header[s2HeaderSize : s2HeaderSize+s2ChecksumSize]
	//: a checksum cut short is a corrupt stream.
	if !f.readFull(sum, false) {
		return
	}
	n := size - s2ChecksumSize
	//: a block past the stream's bound, refused before it is allocated.
	if !f.admits(n) {
		return
	}
	f.block = grown(f.block, n)
	//: a block cut short is a corrupt stream.
	if !f.readFull(f.block[:n], false) {
		return
	}
	f.verify(sum, n)
}

// admits says whether a decoded block of n bytes is within the stream's
// bound, setting err when it is not.
func (f *s2Frame) admits(n int) bool {
	//: snappy's framing caps a block at 64 KiB, s2's at 4 MiB.
	if (f.snappy && n > s2MaxSnappyBlock) || n > s2MaxBlock {
		f.err = s2.ErrCorrupt
		return false
	}
	return true
}

// verify checks block[:n] against the masked CRC-32C sum carries, and hands
// the block over only when it holds.
func (f *s2Frame) verify(sum []byte, n int) {
	want := uint32(sum[0]) | uint32(sum[1])<<8 | uint32(sum[2])<<16 | uint32(sum[3])<<24
	c := crc32.Update(0, s2CRCTable, f.block[:n])
	//: a block that is not the one written.
	if (c>>15|c<<17)+s2CRCMask != want {
		f.err = s2.ErrCRC
		return
	}
	f.i, f.j = 0, n
}

// identifier checks a stream identifier chunk of size bytes: s2's, or
// snappy's, whose blocks are bounded tighter.
func (f *s2Frame) identifier(size int) {
	//: an identifier has one length.
	if size != len(s2Magic) {
		f.err = s2.ErrCorrupt
		return
	}
	body := f.header[s2HeaderSize:]
	//: an identifier cut short is a corrupt stream.
	if !f.readFull(body, false) {
		return
	}
	switch string(body) {
	//: an s2 stream.
	case s2Magic:
		f.snappy = false
	//: a snappy stream, which s2 reads.
	case s2MagicSnappy:
		f.snappy = true
	//: neither.
	default:
		f.err = s2.ErrCorrupt
	}
}

// skip reads past a skippable chunk of size bytes, through chunk as scratch.
func (f *s2Frame) skip(size int) {
	f.chunk = grown(f.chunk, min(size, s2SkipStep))
	//: a 3-byte length never passes the format's 16 MiB, so no bound to check.
	for size > 0 {
		step := min(size, len(f.chunk))
		//: a skipped chunk cut short is a corrupt stream.
		if !f.readFull(f.chunk[:step], false) {
			return
		}
		size -= step
	}
}

// readFull fills p from r. The end before p is a corrupt stream, unless
// nothing of p was read and endOK says the end may fall there.
func (f *s2Frame) readFull(p []byte, endOK bool) bool {
	_, rerr := io.ReadFull(f.r, p)
	//: p is whole.
	if rerr == nil {
		return true
	}
	//: the end inside a chunk, or between two where a chunk was due.
	if rerr == io.ErrUnexpectedEOF || (rerr == io.EOF && !endOK) {
		rerr = s2.ErrCorrupt
	}
	f.err = rerr
	return false
}

// grown returns b with a length of at least n, reallocated only when its
// capacity is short: a decoder's buffers grow to its largest block and stay.
func grown(b []byte, n int) []byte {
	//: the buffer already holds n.
	if cap(b) >= n {
		return b[:cap(b)]
	}
	return make([]byte, n)
}
