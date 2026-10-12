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
	chunkHeaderLen   = 12
	defaultChunkSize = 1 << 20
)

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
}

func newChunkWriter(w io.Writer, codec coretransform.BoundedDecompressor, chunk int) *chunkWriter {
	return &chunkWriter{w: w, codec: codec, size: chunk, sum: sha256.New(), buf: make([]byte, 0, chunk)}
}

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

func newChunkReader(r io.Reader, codec coretransform.BoundedDecompressor, chunk, limit int64) *chunkReader {
	return &chunkReader{r: r, codec: codec, chunk: chunk, limit: limit, sum: sha256.New()}
}

func snapshotCorrupt(why string) error {
	return origin(coreconsensus.SnapshotCorrupt, errs.String("why", why))
}

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
		c.enc = make([]byte, encLen)
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
func verifySnapshot(r io.Reader, meta coreconsensus.SnapshotMeta, codec coretransform.BoundedDecompressor, limit int64) error {
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

// alignedOffset is the largest chunk boundary of an encoded prefix.
func alignedOffset(prefix []byte) int64 {
	var off int64
	for int64(len(prefix))-off >= chunkHeaderLen {
		n := int64(binary.LittleEndian.Uint32(prefix[off:]))
		end := off + chunkHeaderLen + n
		if end > int64(len(prefix)) {
			break
		}
		if crc32.Checksum(prefix[off+chunkHeaderLen:end], castagnoli) != binary.LittleEndian.Uint32(prefix[off+8:]) {
			break
		}
		off = end
	}
	return off
}
