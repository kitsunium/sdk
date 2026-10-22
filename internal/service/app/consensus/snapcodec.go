package consensus

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"hash/crc32"
	"io"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// A snapshot stream is a sequence of chunks, each compressed on its own:
// encoded length (u32) | decoded length (u32) | CRC-32C of the encoded bytes
// (u32) | bytes, little-endian (D4, R8-21). The SHA-256 covers the decoded
// stream. A resumed transfer restarts on a chunk boundary.

const (
	// chunkHeaderLen is the three u32 fields that precede every chunk's
	// bytes: encoded length, decoded length and CRC-32C.
	chunkHeaderLen = 12
	// defaultChunkSize is the decoded size of a chunk: large enough that the
	// header and the codec's framing are noise, small enough that a resumed
	// transfer loses at most 1 MiB.
	defaultChunkSize = 1 << 20
)

// castagnoli is the CRC-32C table every checksum of the log, the stamp and
// the snapshot stream is computed with: built once, read concurrently.
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// chunkWriter takes the decoded stream a state machine writes and emits
// encoded chunks to w.
type chunkWriter struct {
	w       io.Writer
	codec   coretransform.BoundedDecompressor
	buf     []byte
	enc     []byte
	size    int
	sum     hash.Hash
	encoded int64
	decoded int64
	// limit, when not 0, bounds both the decoded and the encoded stream, as
	// a reader bounds them: a chunk that would pass it is refused
	// SNAPSHOT_TOO_LARGE before any of it is written.
	limit int64
}

// newChunkWriter returns a writer that encodes every chunk bytes of decoded
// stream with codec and emits it to w; the last, partial chunk is emitted
// by Close.
func newChunkWriter(w io.Writer, codec coretransform.BoundedDecompressor, chunk int) *chunkWriter {
	return &chunkWriter{w: w, codec: codec, size: chunk, sum: sha256.New(), buf: make([]byte, 0, chunk)}
}

// Write buffers p and emits each chunk as it fills. On a failed emit the
// count is the bytes accepted so far, and the stream is unusable: a chunk
// was half written.
func (c *chunkWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		k := min(len(p), c.size-len(c.buf))
		c.buf = append(c.buf, p[:k]...)
		p = p[k:]
		n += k
		if len(c.buf) == c.size {
			if err := c.flush(); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

// flush encodes and emits the buffered chunk, header first, and folds its
// decoded bytes into the SHA-256; an empty buffer emits nothing, so Close
// after a full chunk adds no empty one.
func (c *chunkWriter) flush() error {
	if len(c.buf) == 0 {
		return nil
	}
	c.sum.Write(c.buf)
	enc, err := c.codec.Compress(c.enc[:0], c.buf)
	if err != nil {
		return err
	}
	c.enc = enc
	if c.limit > 0 && (c.decoded+int64(len(c.buf)) > c.limit || c.encoded+int64(chunkHeaderLen+len(enc)) > c.limit) {
		return origin(coreconsensus.SnapshotTooLarge, errs.Int64("limit", c.limit),
			errs.Int64("decoded", c.decoded+int64(len(c.buf))), errs.Int64("encoded", c.encoded+int64(chunkHeaderLen+len(enc))))
	}
	var h [chunkHeaderLen]byte
	binary.LittleEndian.PutUint32(h[0:], uint32(len(enc)))
	binary.LittleEndian.PutUint32(h[4:], uint32(len(c.buf)))
	binary.LittleEndian.PutUint32(h[8:], crc32.Checksum(enc, castagnoli))
	if _, err := c.w.Write(h[:]); err != nil {
		return err
	}
	if _, err := c.w.Write(enc); err != nil {
		return err
	}
	c.encoded += int64(chunkHeaderLen + len(enc))
	c.decoded += int64(len(c.buf))
	c.buf = c.buf[:0]
	return nil
}

// Close flushes the last chunk.
func (c *chunkWriter) Close() error { return c.flush() }

// chunkReader decodes a chunk stream, bounding every chunk's output before it
// allocates it and the running total by limit (R2-11).
type chunkReader struct {
	r       io.Reader
	codec   coretransform.BoundedDecompressor
	chunk   int64
	limit   int64
	total   int64
	enc     []byte
	dec     []byte
	pos     int
	sum     hash.Hash
	encoded int64
}

// newChunkReader returns a reader decoding the chunk stream r, refusing a
// chunk larger than chunk and a stream whose decoded total exceeds limit.
func newChunkReader(r io.Reader, codec coretransform.BoundedDecompressor, chunk, limit int64) *chunkReader {
	return &chunkReader{r: r, codec: codec, chunk: chunk, limit: limit, sum: sha256.New()}
}

// snapshotCorrupt is the SNAPSHOT_CORRUPT of a stream or a spool that
// failed a check, the check named in its why field.
func snapshotCorrupt(why string) error {
	return origin(coreconsensus.SnapshotCorrupt, errs.String("why", why))
}

// Read returns decoded bytes, decoding the next chunk when the current one
// is spent; io.EOF arrives only on a chunk boundary, a stream cut anywhere
// else is SNAPSHOT_CORRUPT.
func (c *chunkReader) Read(p []byte) (int, error) {
	for c.pos == len(c.dec) {
		if err := c.next(); err != nil {
			return 0, err
		}
	}
	n := copy(p, c.dec[c.pos:])
	c.pos += n
	return n, nil
}

// next reads, checks and decodes one chunk. The announced lengths are
// bounded BEFORE anything is allocated for them (R2-11), and a chunk whose
// checksum or decoded length disagrees with its header is refused.
func (c *chunkReader) next() error {
	var h [chunkHeaderLen]byte
	if _, err := io.ReadFull(c.r, h[:]); err != nil {
		if err == io.EOF {
			return io.EOF
		}
		return snapshotCorrupt("short chunk header")
	}
	encLen := int64(binary.LittleEndian.Uint32(h[0:]))
	decLen := int64(binary.LittleEndian.Uint32(h[4:]))
	if decLen > c.chunk || encLen > c.chunk+c.chunk/8+1024 || c.total+decLen > c.limit {
		return origin(coreconsensus.SnapshotTooLarge, errs.Int64("chunk", decLen))
	}
	if int64(cap(c.enc)) < encLen {
		c.enc = make([]byte, 0, encLen)
	}
	c.enc = c.enc[:encLen]
	if _, err := io.ReadFull(c.r, c.enc); err != nil {
		return snapshotCorrupt("short chunk")
	}
	if crc32.Checksum(c.enc, castagnoli) != binary.LittleEndian.Uint32(h[8:]) {
		return snapshotCorrupt("chunk checksum")
	}
	dec, err := c.codec.DecompressBounded(c.dec[:0], c.enc, decLen)
	if err != nil || int64(len(dec)) != decLen {
		return snapshotCorrupt("chunk does not decode to its length")
	}
	c.dec, c.pos = dec, 0
	c.total += decLen
	c.encoded += chunkHeaderLen + encLen
	c.sum.Write(dec)
	return nil
}

// verifySnapshot decodes a whole snapshot and checks its sizes and digest.
func verifySnapshot(r io.Reader, meta coreconsensus.SnapshotMetaValue, codec coretransform.BoundedDecompressor, limit int64) error {
	cr := newChunkReader(r, codec, max(meta.ChunkSize, 1), limit)
	if _, err := io.Copy(io.Discard, cr); err != nil {
		return err
	}
	if cr.total != meta.DecodedSize || cr.encoded != meta.EncodedSize {
		return snapshotCorrupt("size")
	}
	var got [32]byte
	copy(got[:], cr.sum.Sum(nil))
	if got != meta.SHA256 {
		return snapshotCorrupt("sha256")
	}
	return nil
}

// maxEncodedChunk is the largest encoded chunk a stream of decoded chunks
// of chunk bytes may hold, the bound chunkReader applies.
func maxEncodedChunk(chunk int64) int64 {
	chunk = max(chunk, 1)
	return chunk + chunk/8 + 1024
}

// alignedOffsetAt is the largest chunk boundary of the encoded prefix of
// size bytes r holds, each chunk at most maxChunk encoded bytes. The writer
// never emits an empty chunk, so a length of 0 ends the prefix: a region of
// zeros (a spool never synced, an allocation a crash left unwritten) reads
// as a run of empty chunks whose CRC of nothing is 0, and a resume used to
// continue past it, to fail only on the final digest (#422). The prefix is
// read one chunk at a time into a buffer of one chunk at most: read whole,
// the spool of a full snapshot was held in memory at once, on the event
// loop, and the resume allocated more than its size (#527).
func alignedOffsetAt(r io.ReaderAt, size, maxChunk int64) (int64, error) {
	var off int64
	buf := make([]byte, min(maxChunk, size))
	for size-off >= chunkHeaderLen {
		end, err := wholeChunkAt(r, off, size, buf)
		if err != nil {
			return 0, err
		}
		if end < 0 {
			break
		}
		off = end
	}
	return off, nil
}

// wholeChunkAt is where the chunk at off of the size bytes r holds ends,
// or -1 when no whole chunk starts there: an empty or oversized length, a
// chunk past the end, or a checksum that fails. buf holds the largest chunk
// accepted.
func wholeChunkAt(r io.ReaderAt, off, size int64, buf []byte) (int64, error) {
	var h [chunkHeaderLen]byte
	if _, err := r.ReadAt(h[:], off); err != nil {
		return 0, err
	}
	n := int64(binary.LittleEndian.Uint32(h[:]))
	end := off + chunkHeaderLen + n
	if n == 0 || n > int64(len(buf)) || end > size {
		return -1, nil
	}
	if _, err := r.ReadAt(buf[:n], off+chunkHeaderLen); err != nil {
		return 0, err
	}
	if crc32.Checksum(buf[:n], castagnoli) != binary.LittleEndian.Uint32(h[8:]) {
		return -1, nil
	}
	return end, nil
}
