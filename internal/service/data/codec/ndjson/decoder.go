package ndjson

import (
	"bufio"
	"bytes"
	"errors"
	"io"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corendjson "github.com/kitsunium/sdk/internal/core/data/codec/ndjson"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// ndjsonDecoder reads one record per Decode from a stream of lines (#901):
// the lines Unmarshal would decode, read one at a time through a bufio.Reader,
// blank ones skipped, none longer than scannerMaxCapacity.
type ndjsonDecoder struct {
	// r is the buffered stream.
	r *bufio.Reader
	// long holds a line that outgrew r's buffer, read in pieces; reused.
	long []byte
	// line is the next record, trimmed, when ready: it aliases r's buffer or
	// long, and stays valid until the next read.
	line []byte
	// ready reports that line holds a record no Decode has taken yet.
	ready bool
	// err ends the stream: io.EOF, a failing reader or an oversized line.
	// Decode returns it from then on.
	err error
	// reported is set once Decode has returned err, so More reports false.
	reported bool
}

// NewDecoder wraps r in a streaming codec.Decoder handing one record per
// Decode, as Unmarshal decodes each line.
func (*ndjsonCodec) NewDecoder(r io.Reader) codec.Decoder {
	//: one buffered reader for the stream's lifetime.
	return &ndjsonDecoder{r: bufio.NewReader(r)}
}

// Decode decodes the next record into v, which must be a non-nil pointer, as
// encoding/json.Unmarshal decodes a line. It returns io.EOF, unwrapped, once
// the stream is drained; SIZE_EXCEEDED for a line past 10 MiB, which ends the
// stream; UNMARSHAL_FAILED for a line v cannot hold, after which the next
// Decode reads the line after it.
func (d *ndjsonDecoder) Decode(v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: read ahead unless More already did.
	d.advance()
	//: no record: the end, or the failure, from now on.
	if !d.ready {
		//: More reports false once the caller has been told.
		d.reported = true
		//: io.EOF unwrapped, or the wrapped failure.
		return d.err
	}
	//: the record is taken whatever its decode does: the next Decode reads on.
	d.ready = false
	//: decode the line as Unmarshal decodes each line.
	if uerr := decodeRecord(d.line, v); uerr != nil {
		//: wrap the stdlib error for reason-based matching.
		return errs.Wrap(uerr, errs.WrapParams{
			Code:    corendjson.CodeNDJSONUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "NDJSON decoding failed",
			Private: "service/data/codec/ndjson.Decoder.Decode: encoding/json returned an error",
		})
	}
	//: one record decoded.
	return nil
}

// More reports whether Decode has something to return: a record, or a
// failure the caller has not been told of yet. It reads ahead to the next
// non-blank line, so a stream's last record is followed by false.
func (d *ndjsonDecoder) More() bool {
	//: read ahead to the next record, once.
	d.advance()
	//: a record, or a failure Decode has yet to report.
	return d.ready || (!d.reported && !errors.Is(d.err, io.EOF))
}

// advance reads lines until the next non-blank one, unless a record is
// already waiting or the stream has ended.
func (d *ndjsonDecoder) advance() {
	//: a record waits, or the stream is over.
	for !d.ready && d.err == nil {
		//: the next line, the last one without its terminator included.
		line, err := d.readLine()
		//: a line with something but whitespace is the next record.
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			d.line, d.ready = trimmed, true
		}
		//: the end, or a failure, after the line read with it.
		d.err = err
	}
}

// readLine returns the next line without its '\n', and io.EOF with the last
// one when it has no terminator. A line that fits the reader's buffer is
// returned in place; a longer one is gathered into long, and refused once it
// passes scannerMaxCapacity — the stream is read no further.
func (d *ndjsonDecoder) readLine() (line []byte, err error) {
	//: the common case: the whole line in the buffer, no copy.
	frag, err := d.r.ReadSlice('\n')
	//: anything but a full buffer ends the line here.
	if !errors.Is(err, bufio.ErrBufferFull) {
		//: the line, its terminator dropped, and how the read ended.
		return lineEnd(frag, err)
	}
	//: a long line: gather its pieces, bounded.
	d.long = append(d.long[:0], frag...)
	//: piece by piece until its terminator, the end, or the ceiling.
	for errors.Is(err, bufio.ErrBufferFull) {
		//: refused once it is longer than a record may be.
		if len(d.long) > scannerMaxCapacity {
			//: the stream ends here, typed.
			return nil, sizeExceeded(len(d.long))
		}
		//: the next piece.
		frag, err = d.r.ReadSlice('\n')
		d.long = append(d.long, frag...)
	}
	//: the gathered line, checked against the ceiling with its last piece.
	line, err = lineEnd(d.long, err)
	//: the ceiling applies to the line, without its terminator.
	if len(line) > scannerMaxCapacity {
		//: refused, as a line that never ended.
		return nil, sizeExceeded(len(line))
	}
	//: the whole line.
	return line, err
}

// lineEnd drops a line's '\n' and turns the read's error into the stream's:
// nil after a terminator, io.EOF at the end, a wrapped failure otherwise.
func lineEnd(frag []byte, err error) (line []byte, end error) {
	//: terminated: the line without its '\n'.
	if err == nil {
		//: more may follow.
		return frag[:len(frag)-1], nil
	}
	//: the end of the stream: the last line, if any.
	if errors.Is(err, io.EOF) {
		//: io.EOF unwrapped, as Decode returns it.
		return frag, io.EOF
	}
	//: a failing reader ends the stream.
	return frag, errs.Wrap(err, errs.WrapParams{
		Code:    corendjson.CodeNDJSONUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "NDJSON decoding failed",
		Private: "service/data/codec/ndjson.Decoder: the reader failed",
	})
}

// sizeExceeded is the refusal of a stream line longer than a record may be.
func sizeExceeded(n int) error {
	//: the sentinel's code and texts, with the length read and the ceiling;
	//: UNMARSHAL_FAILED joins the trail, as for every bound refusal (#894).
	return errs.Wrap(corendjson.SizeExceeded, errs.WrapParams{Code: corendjson.CodeNDJSONUnmarshalFailed},
		errs.Int("len", n), errs.Int("cap", scannerMaxCapacity))
}
