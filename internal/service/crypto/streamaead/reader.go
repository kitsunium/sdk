// Package streamaead (reader.go) — the streaming-AEAD read path: header verify,
// chunk-by-chunk decrypt with hold-back, and one-byte EOF look-ahead.
package streamaead

import (
	"io"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

const (
	// stateStarted marks that the header has been read + verified.
	stateStarted uint8 = 1 << iota
	// statePeeked marks that peek holds a valid look-ahead byte.
	statePeeked
	// stateDone marks that the final-flag chunk verified (EOF reached).
	stateDone
)

// streamReader is the io.Reader returned by streamAEAD.Reader. It reads + verifies
// the header lazily on the first Read, then decrypts chunk-by-chunk, holding back
// each chunk's plaintext until its GCM tag verifies. EOF is returned only after
// the flag=0x01 final chunk verifies; a stream cut short surfaces as
// StreamTruncated.
type streamReader struct {
	// key is the caller's master key; the per-stream key derives from it + salt.
	key corecrypto.Key
	// src is the wire source the header and sealed chunks are read from.
	src io.Reader
	// gcm is the AES-256-GCM mode keyed by the HKDF-derived per-stream key; nil
	// until the header is read.
	gcm interface {
		Open(dst, nonce, ciphertext, aad []byte) ([]byte, error)
	}
	// aad is the associated data bound into every chunk's tag.
	aad []byte
	// plain holds verified-but-not-yet-returned plaintext from the current chunk.
	plain []byte
	// counter is the per-chunk nonce counter, incremented after each open.
	counter uint64
	// peek holds the single look-ahead byte read past a full wire chunk; valid
	// only when the statePeeked bit is set.
	peek [1]byte
	// state packs the started / peeked / done lifecycle bits (stateStarted, …).
	state uint8
}

// newReader constructs a lazily-initialised streamReader; the header is read on
// the first Read so a wrong-format lead byte or missing data surfaces at use.
func newReader(key corecrypto.Key, src io.Reader, aad []byte) *streamReader {
	//: defer all I/O to Read; construction never fails.
	return &streamReader{key: key, src: src, aad: aad}
}

// Read fills p with verified plaintext, decrypting further chunks as needed. It
// returns io.EOF only after the final-flag chunk verifies; a truncated stream
// returns StreamTruncated, and any authentication fault returns DecryptionFailed.
func (r *streamReader) Read(p []byte) (n int, err error) {
	//: read + verify the header exactly once before any chunk.
	if r.state&stateStarted == 0 {
		//: a bad lead byte or short header surfaces here, not at construction.
		if herr := r.readHeader(); herr != nil {
			//: propagate the typed header fault.
			return 0, herr
		}
	}
	//: serve buffered plaintext first; only decrypt a new chunk when drained.
	for len(r.plain) == 0 {
		//: a verified final chunk means the stream is legitimately finished.
		if r.state&stateDone != 0 {
			//: clean EOF only ever follows a flag=0x01 chunk.
			return 0, io.EOF
		}
		//: decrypt the next chunk into r.plain (or set done on the final chunk).
		if derr := r.nextChunk(); derr != nil {
			//: surface truncation / authentication faults unchanged.
			return 0, derr
		}
	}
	//: copy out as much verified plaintext as fits; never expose unverified bytes.
	n = copy(p, r.plain)
	//: retain the unread remainder for the next Read.
	r.plain = r.plain[n:]
	//: hand back the verified bytes copied this call.
	return n, nil
}

// readHeader reads the fixed header, rejects the wrong-format lead byte and a bad
// algID, and derives the per-stream GCM mode from the master key + header salt.
func (r *streamReader) readHeader() error {
	//: read the fixed header in full; a short read is a truncated stream.
	var header [headerLen]byte
	if _, err := io.ReadFull(r.src, header[:]); err != nil {
		//: too short to even name the format — truncated.
		return corecrypto.StreamTruncated
	}
	//: reject a box (0x01) fed to the streaming Open, and any unknown version.
	if header[0] != streamVersion || header[1] != algID {
		//: wrong format is a non-oracle decryption failure, not truncation.
		return corecrypto.DecryptionFailed
	}
	//: derive the per-stream GCM mode from the master key + the header salt.
	gcm, gerr := newGCM(r.key.Bytes(), header[versionFieldLen:headerLen])
	//: an (unreachable) derivation fault collapses to the non-oracle error.
	if gerr != nil {
		//: never distinguish derivation faults.
		return gerr
	}
	//: latch the mode and mark the header consumed.
	r.gcm = gcm
	r.state |= stateStarted
	//: header verified; the reader is ready to decrypt chunks.
	return nil
}

// nextChunk reads one wire chunk, decides final vs non-final via a one-byte
// look-ahead, opens it under the correct flag, and stores the plaintext. It sets
// the done bit when the final-flag chunk verifies.
func (r *streamReader) nextChunk() error {
	//: read up to a full wire chunk (ct of chunkSize || tag), plus the prior peek.
	wire, atEOF, rerr := r.readWire()
	//: a read fault below EOF is a truncated stream.
	if rerr != nil {
		//: surface truncation rather than a generic error.
		return rerr
	}
	//: a wire chunk must carry at least a GCM tag to be openable.
	if len(wire) < gcmTagLen {
		//: too short to authenticate — the stream was cut mid-chunk.
		return corecrypto.StreamTruncated
	}
	//: a chunk is final iff no further wire bytes follow it (look-ahead at EOF).
	flag := flagMore
	//: the last chunk in the stream carries the final flag in its nonce.
	if atEOF {
		//: mark this chunk's nonce as the terminal one.
		flag = flagFinal
	}
	//: open under the chosen flag; GCM verifies the whole chunk before returning.
	return r.openChunk(wire, flag, atEOF)
}

// openChunk builds the nonce, opens the wire chunk, and on success stores the
// plaintext and advances the counter; the done bit is set on the final chunk.
func (r *streamReader) openChunk(wire []byte, flag byte, atEOF bool) error {
	//: build the nonce for this chunk; a uint64 counter can never overflow it.
	nonce := chunkNonce(r.counter, flag)
	//: GCM Open authenticates ct||tag + aad and returns plaintext only on success.
	pt, oerr := r.gcm.Open(nil, nonce[:], wire, r.aad)
	//: a failed tag/aad/flag check is a non-oracle decryption failure.
	if oerr != nil {
		//: never surface unverified plaintext; collapse to the single error.
		return corecrypto.DecryptionFailed
	}
	//: a verified final chunk ends the stream; a verified non-final continues.
	if atEOF {
		//: the next Read after draining r.plain returns clean EOF.
		r.state |= stateDone
	}
	//: stage the verified plaintext and advance the per-chunk counter.
	r.plain = pt
	r.counter++
	//: chunk verified and staged for the caller.
	return nil
}

// readWire reads the next wire chunk and reports whether it is the last one. It
// uses a one-byte look-ahead: a full chunk followed by more data is non-final; a
// short read or an immediate EOF after a full chunk marks the final chunk.
func (r *streamReader) readWire() (wire []byte, atEOF bool, err error) {
	//: the maximum wire chunk is a full plaintext chunk plus the GCM tag.
	maxWire := chunkSize + gcmTagLen
	//: start the buffer with any byte peeked past the previous full chunk.
	buf := make([]byte, 0, maxWire)
	//: a pending peek byte is the first byte of this chunk.
	if r.state&statePeeked != 0 {
		//: consume the held look-ahead byte.
		buf = append(buf, r.peek[0])
		r.state &^= statePeeked
	}
	//: fill the rest of the chunk from src up to the max wire size.
	return r.fillWire(buf, maxWire)
}

// fillWire reads from src until buf holds maxWire bytes or src is exhausted, then
// peeks one further byte to distinguish a non-final chunk (more data follows)
// from the final chunk (EOF). A read error below EOF is a truncated stream.
func (r *streamReader) fillWire(buf []byte, maxWire int) (wire []byte, atEOF bool, err error) {
	//: grow buf up to maxWire bytes from the source.
	tmp := make([]byte, maxWire-len(buf))
	//: ReadFull tolerates a short final chunk via the EOF branches below.
	got, rerr := io.ReadFull(r.src, tmp)
	//: accumulate whatever was read onto the (possibly peek-seeded) buffer.
	buf = append(buf, tmp[:got]...)
	//: a short read means src ended mid- or at-end-of this final chunk.
	if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
		//: no more bytes can follow, so this is the final chunk.
		return buf, true, nil
	}
	//: any other read fault below EOF is a genuine truncation/transport error.
	if rerr != nil {
		//: surface truncation rather than a generic error.
		return nil, false, corecrypto.StreamTruncated
	}
	//: a full chunk was read; peek one byte to see whether another chunk follows.
	return r.peekNext(buf)
}

// peekNext reads a single look-ahead byte after a full wire chunk: its presence
// means another chunk follows (non-final); EOF means this was the final chunk.
func (r *streamReader) peekNext(buf []byte) (wire []byte, atEOF bool, err error) {
	//: read exactly one byte past the full chunk.
	got, perr := io.ReadFull(r.src, r.peek[:])
	//: a byte present means at least one more chunk follows this one.
	if got == 1 {
		//: hold the peek so the next chunk read consumes it first.
		r.state |= statePeeked
		//: this chunk is non-final — another chunk follows.
		return buf, false, nil
	}
	//: EOF with no further byte means this full chunk was the final chunk.
	if perr == io.EOF || perr == io.ErrUnexpectedEOF {
		//: the full chunk is final.
		return buf, true, nil
	}
	//: any other fault is a truncation/transport error.
	return nil, false, corecrypto.StreamTruncated
}
