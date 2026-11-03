package pem

import (
	"bytes"
	stdpem "encoding/pem"
	"errors"
	"io"
	"slices"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corepem "github.com/kitsunium/sdk/internal/core/data/codec/pem"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Stream decoder bounds.
const (
	// maxStreamBlockBytes caps the bytes the stream decoder holds before it
	// can decode a block: past it the block is refused, SIZE_EXCEEDED, and
	// the stream read no further.
	maxStreamBlockBytes int = 10 << 20
	// streamReadSize is the most one Read is asked for.
	streamReadSize int = 32 << 10
	// maxEmptyReads is how many Reads may return no byte and no error before
	// the reader is treated as stuck, as bufio treats one.
	maxEmptyReads int = 100
)

// pemDecoder reads one *pem.Block per Decode (#901): the blocks a loop of
// encoding/pem.Decode over the whole stream finds, in order, without holding
// the whole stream. pem.Decode decides a block at its END line — the first
// "\n-----END " after the last BEGIN line — so the decoder reads until a
// whole END line is held, hands what it holds to pem.Decode, and keeps only
// what follows: the block's rest, or what follows the END lines that decoded
// nothing, which pem.Decode itself would skip.
type pemDecoder struct {
	// r is the stream.
	r io.Reader
	// buf holds the bytes read and not yet decoded or skipped.
	buf []byte
	// searchFrom is where the search for an END line resumes in buf.
	searchFrom int
	// eof is set once r has returned io.EOF.
	eof bool
	// next is the block More or Decode read ahead, not yet handed out.
	next *stdpem.Block
	// err ends the stream: io.EOF, a failing reader, a truncated or
	// oversized block. Decode returns it from then on.
	err error
	// reported is set once Decode has returned err, so More reports false.
	reported bool
}

// NewDecoder wraps r in a streaming codec.Decoder handing one *pem.Block per
// Decode into a **pem.Block target.
func (*pemCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: the buffer grows to what one block and the text before it take.
	return &pemDecoder{r: r}
}

// Decode stores the next block of the stream in *v, a non-nil **pem.Block. It
// returns io.EOF, unwrapped, once no block is left; UNMARSHAL_FAILED for a
// block the stream ends inside of or a failing reader, SIZE_EXCEEDED for a
// block past 10 MiB — both end the stream; VALUE_INVALID for another target,
// which reads nothing.
func (d *pemDecoder) Decode(v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: the target Unmarshal takes.
	dst, ok := v.(**stdpem.Block)
	//: refused before anything is read.
	if !ok || dst == nil {
		//: the dedicated sentinel.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    corepem.CodePEMValueInvalid,
			Reason:  "VALUE_INVALID",
			Public:  "PEM codec requires a non-nil **pem.Block target",
			Private: "service/data/codec/pem.Decoder.Decode: target is not a non-nil **pem.Block",
		})
	}
	//: read ahead unless More already did.
	d.advance()
	//: no block: the end, or the failure, from now on.
	if d.next == nil {
		//: More reports false once the caller has been told.
		d.reported = true
		//: io.EOF unwrapped, or the wrapped failure.
		return d.err
	}
	//: hand the block over.
	*dst, d.next = d.next, nil
	//: one block decoded.
	return nil
}

// More reports whether Decode has something to return: a block, or a failure
// the caller has not been told of yet. It reads ahead to the next block, so
// a stream's last block is followed by false.
func (d *pemDecoder) More() bool {
	//: read ahead to the next block, once.
	d.advance()
	//: a block, or a failure Decode has yet to report.
	return d.next != nil || (!d.reported && !errors.Is(d.err, io.EOF))
}

// advance reads until the next block decodes, unless one is already waiting
// or the stream has ended.
func (d *pemDecoder) advance() {
	//: a block waits, or the stream is over.
	for d.next == nil && d.err == nil {
		//: a whole END line is held: pem.Decode can decide.
		if end := d.lastWholeEnd(); end >= 0 {
			//: a block, or the bytes up to that END line skipped.
			d.decodeHeld(end)
			//: decide again with what is left.
			continue
		}
		//: nothing left to read: what is held decides how the stream ends.
		if d.eof {
			//: io.EOF, or a block the stream ended inside of.
			d.finish()
			//: the stream is over.
			return
		}
		//: refused once more is held than a block may take.
		if len(d.buf) > maxStreamBlockBytes {
			//: the stream ends here, typed.
			d.err = errs.Wrap(corepem.SizeExceeded, errs.WrapParams{}, errs.Int("cap", maxStreamBlockBytes))
			//: read no further.
			return
		}
		//: more of the stream.
		d.fill()
	}
}

// decodeHeld hands what is held up to the end of its last whole END line to
// pem.Decode — end is just past that line's marker: a block is kept for
// Decode and the bytes before its rest dropped; no block drops the bytes up
// to that END marker, which pem.Decode would skip. pem.Decode never sees a
// line it could read differently once more of the stream arrives.
func (d *pemDecoder) decodeHeld(end int) {
	//: the end of that END line: its '\n', or the end of the stream.
	cut := len(d.buf)
	//: a terminated line stops at its newline.
	if nl := bytes.IndexByte(d.buf[end:], '\n'); nl >= 0 {
		cut = end + nl + 1
	}
	//: encoding/pem's own reading of the whole lines.
	block, rest := stdpem.Decode(d.buf[:cut])
	//: no block up to that END line: skip it, as pem.Decode would.
	if block == nil {
		//: what follows the END marker, where pem.Decode resumes.
		d.drop(end)
		//: nothing for Decode.
		return
	}
	//: the block, and what follows it.
	d.next = block
	d.drop(cut - len(rest))
}

// drop forgets the first n bytes held.
func (d *pemDecoder) drop(n int) {
	//: move the rest to the front, so the buffer is reused.
	d.buf = d.buf[:copy(d.buf, d.buf[n:])]
	//: search what is left from its start.
	d.searchFrom = 0
}

// lastWholeEnd returns the offset just past the "\n-----END " of the last END
// line held whole — followed by a '\n', or by the end of the stream — or -1.
// pem.Decode reads an END line to its end, so a block is decided only once
// its END line is whole. The search resumes where an earlier one stopped.
func (d *pemDecoder) lastWholeEnd() int {
	//: the marker as pem.Decode looks for it, after a newline.
	marker := []byte("\n" + pemEnd)
	//: the last whole END line found.
	last := -1
	//: each marker from where the last search stopped.
	for i := d.searchFrom; ; {
		//: the next marker.
		j := bytes.Index(d.buf[i:], marker)
		//: none left: resume past what was searched, minus a partial marker.
		if j < 0 {
			d.searchFrom = max(i, len(d.buf)-len(marker)+1)
			//: the last whole one, or -1.
			return last
		}
		//: past the marker.
		after := i + j + len(marker)
		//: its line is not whole yet: resume at it next time.
		if !d.eof && bytes.IndexByte(d.buf[after:], '\n') < 0 {
			d.searchFrom = i + j
			//: the last whole one before it, or -1.
			return last
		}
		//: a whole END line.
		last, i = after, after
	}
}

// finish ends the stream once the reader is drained and no whole END line is
// held: io.EOF, or a truncated block when a BEGIN line is still held.
func (d *pemDecoder) finish() {
	//: a BEGIN without its END: the stream ended inside a block.
	if bytes.Contains(d.buf, []byte(pemBegin)) {
		//: what Unmarshal says of input holding no whole block.
		d.err = errs.Wrap(nil, errs.WrapParams{
			Code:    corepem.CodePEMUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "PEM decoding failed",
			Private: "service/data/codec/pem.Decoder: the stream ended inside a block",
		})
		//: refused.
		return
	}
	//: io.EOF unwrapped, as Decode returns it.
	d.err = io.EOF
}

// fill reads more of the stream onto buf: io.EOF sets eof, a failure ends
// the stream as UNMARSHAL_FAILED, a reader returning nothing too often as
// io.ErrNoProgress.
func (d *pemDecoder) fill() {
	//: room for one read.
	d.buf = slices.Grow(d.buf, streamReadSize)
	//: a few empty reads are tolerated, as bufio tolerates them.
	for range maxEmptyReads {
		//: read into the free space.
		n, err := d.r.Read(d.buf[len(d.buf):cap(d.buf)][:streamReadSize])
		d.buf = d.buf[:len(d.buf)+n]
		//: the end of the stream.
		if errors.Is(err, io.EOF) {
			d.eof = true
			//: what was read with it is held.
			return
		}
		//: a failing reader ends the stream.
		if err != nil {
			d.err = readFailed(err)
			//: the stream is over.
			return
		}
		//: progress.
		if n > 0 {
			return
		}
	}
	//: a reader that never returns anything.
	d.err = readFailed(io.ErrNoProgress)
}

// readFailed wraps a reader's failure as UNMARSHAL_FAILED.
func readFailed(err error) error {
	//: the stream's failure, typed.
	return errs.Wrap(err, errs.WrapParams{
		Code:    corepem.CodePEMUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "PEM decoding failed",
		Private: "service/data/codec/pem.Decoder: the reader failed",
	})
}
