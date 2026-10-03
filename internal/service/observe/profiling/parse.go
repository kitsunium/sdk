// Package profiling — hosts Parse: a pprof profile's bytes, gzipped or not,
// decoded into a ProfileValue with its string, function and location tables
// resolved.
package profiling

import (
	"bytes"
	"time"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	coreprofiling "github.com/kitsunium/sdk/internal/core/observe/profiling"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Field numbers of profile.proto's messages that Parse reads. Any other field
// — mappings, drop and keep frames, the documentation URL — is skipped.
const (
	fieldSampleType    uint64 = 1
	fieldSample        uint64 = 2
	fieldLocation      uint64 = 4
	fieldFunction      uint64 = 5
	fieldStringTable   uint64 = 6
	fieldTimeNanos     uint64 = 9
	fieldDurationNanos uint64 = 10
	fieldPeriodType    uint64 = 11
	fieldPeriod        uint64 = 12
	fieldComment       uint64 = 13
	fieldDefaultType   uint64 = 14
)

// gzipAlgorithm is the transform scheme a gzip profile is inflated through:
// the one internal/service/data/transform registers, which the facade links.
const gzipAlgorithm coretransform.Algorithm = "gzip"

var (
	// gzipMagic opens every gzip stream; runtime/pprof compresses what it
	// writes.
	gzipMagic = []byte{0x1f, 0x8b}

	// profileReaders maps each Profile field Parse reads to its reader.
	profileReaders = map[uint64]func(d *decoder, w *wire, kind uint64) error{
		fieldSampleType:    (*decoder).readSampleType,
		fieldSample:        (*decoder).readSample,
		fieldLocation:      (*decoder).readLocation,
		fieldFunction:      (*decoder).readFunction,
		fieldStringTable:   (*decoder).readString,
		fieldTimeNanos:     (*decoder).readTime,
		fieldDurationNanos: (*decoder).readDuration,
		fieldPeriodType:    (*decoder).readPeriodType,
		fieldPeriod:        (*decoder).readPeriod,
		fieldComment:       (*decoder).readComment,
		fieldDefaultType:   (*decoder).readDefaultType,
	}
)

// rawType is a ValueType before its strings are resolved.
type rawType struct {
	typ, unit int64
}

// rawLabel is a Label before its strings are resolved.
type rawLabel struct {
	key, str, num int64
}

// rawSample is a Sample before its locations are resolved.
type rawSample struct {
	locations []uint64
	values    []int64
	labels    []rawLabel
}

// rawLine is one Line of a Location.
type rawLine struct {
	function uint64
	line     int64
}

// rawLocation is a Location before its functions are resolved.
type rawLocation struct {
	lines   []rawLine
	address uint64
}

// rawFunction is a Function before its strings are resolved.
type rawFunction struct {
	name, file, start int64
}

// decoder accumulates a profile's tables as they come: the string table is
// written LAST by runtime/pprof, so nothing can be resolved until the end.
type decoder struct {
	locations     map[uint64]rawLocation
	functions     map[uint64]rawFunction
	strings       []string
	types         []rawType
	samples       []rawSample
	comments      []int64
	periodType    rawType
	timeNanos     int64
	durationNanos int64
	period        int64
	defaultType   int64
}

// Parse decodes a pprof profile — the gzipped protocol-buffer stream
// runtime/pprof writes, or the same stream uncompressed. It is written from
// profile.proto with the standard library alone.
//
// It refuses input over [MaxProfileBytes], compressed or not, and stacks that
// add up to more than [MaxFrames] frames, with [coreprofiling.ProfileTooLarge];
// and anything that is not a well-formed profile — a truncated field, a varint
// past 64 bits, a string, function or location index that points nowhere, a
// sample with the wrong number of values — with
// [coreprofiling.ProfileMalformed]. No refusal quotes the input.
func Parse(data []byte) (*coreprofiling.ProfileValue, error) {
	//: the compressed input is bounded before anything is read.
	if len(data) > MaxProfileBytes {
		//: the size is a field; the bytes are not.
		return nil, errs.Wrap(coreprofiling.ProfileTooLarge, errs.WrapParams{}, errs.Int("bytes", len(data)))
	}
	raw, err := inflate(data)
	//: not gzip after all, or inflating past the bound.
	if err != nil {
		//: already typed.
		return nil, err
	}
	d := &decoder{locations: make(map[uint64]rawLocation), functions: make(map[uint64]rawFunction)}
	//: every top-level field of the Profile message.
	if err := fields(raw, d.profileField); err != nil {
		//: already typed.
		return nil, err
	}
	//: the tables are complete: resolve.
	return d.resolve(MaxFrames)
}

// inflate returns data decompressed when it is gzip, unchanged otherwise.
//
// A gzip stream is inflated through the transform port rather than by a
// decoder of this package's own: the scheme registered as "gzip" is asked for
// at most MaxProfileBytes, so the bounded drain, the trailer check and the
// reader pool are that scheme's (internal/service/data/transform), reached
// through internal/core/data/transform — a core port, not an edge to another
// engine. The facade links the scheme; a program that reaches this package
// without it is told so by the transform domain's UNKNOWN_COMPRESSOR.
func inflate(data []byte) ([]byte, error) {
	//: an uncompressed stream is read as it is.
	if !bytes.HasPrefix(data, gzipMagic) {
		//: nothing to inflate.
		return data, nil
	}
	scheme, found := coretransform.Lookup(gzipAlgorithm)
	bounded, isBounded := scheme.(coretransform.BoundedDecompressor)
	//: no gzip scheme linked, or one that cannot be told a ceiling.
	if !found || !isBounded {
		//: a wiring fault, named as the transform domain names it.
		return nil, errs.Wrap(coretransform.UnknownCompressor, errs.WrapParams{},
			errs.String("algorithm", string(gzipAlgorithm)))
	}
	out, err := bounded.DecompressBounded(nil, data, int64(MaxProfileBytes))
	//: inflated within the bound, checksum and trailer checked.
	if err == nil {
		//: the protocol-buffer bytes.
		return out, nil
	}
	//: a stream that inflates past the bound is refused whole.
	if errs.HasCode(err, coretransform.CodeDecompressedTooLarge) {
		//: nothing past the bound was kept.
		return nil, coreprofiling.ProfileTooLarge
	}
	//: a header, a body or a trailer that does not decode.
	return nil, errs.Wrap(libraryCause(err), malformedParams("gzip"))
}

// libraryCause returns the decoding library's own error beneath the transform
// scheme's sentinel, nil when it carried none. A wrap's origin wins (rule 6),
// so wrapping the scheme's *errs.Error would leave the refusal under the
// scheme's code; the library's error is what this package wrapped before the
// scheme did the decoding, and the refusal stays PROFILE_MALFORMED around it.
func libraryCause(err error) error {
	cause := err
	//: peel the SDK layers until the library's error, or nothing, is left.
	for {
		sdkErr, isSDK := cause.(*errs.Error)
		//: not an SDK error (the library's own, or nil), or a typed nil one,
		//: which Wrap treats as no cause at all.
		if !isSDK || sdkErr == nil {
			//: what compress/gzip reported.
			return cause
		}
		//: one layer down.
		cause = sdkErr.Source()
	}
}

// malformedParams wraps a decoding library's error as ProfileMalformed.
func malformedParams(what string) errs.WrapParams {
	//: the library's error goes to Private through the chain, never to Public.
	return errs.WrapParams{
		Code: coreprofiling.CodeProfileMalformed, Reason: "PROFILE_MALFORMED", Public: coreprofiling.ProfileMalformed.Public(),
		Private: coreprofiling.ProfileMalformed.Private() + " (" + what + ")",
	}
}

// fields walks a message's fields, handing each to read; a field read does
// not handle is skipped.
func fields(buf []byte, read func(w *wire, field, kind uint64) (bool, error)) error {
	w := wire{buf: buf}
	//: every field of the message, in the order written.
	for w.more() {
		field, kind, err := w.key()
		//: a key that does not parse ends the walk.
		if err != nil {
			//: already typed.
			return err
		}
		handled, err := read(&w, field, kind)
		//: the handler found the field malformed.
		if err != nil {
			//: already typed.
			return err
		}
		//: a field the decoder does not read.
		if !handled {
			//: skipped by its wire type, bounds-checked.
			if err := w.skip(kind); err != nil {
				//: already typed.
				return err
			}
		}
	}
	//: the whole message.
	return nil
}

// scalar reads an int64 field, which must be a varint.
func scalar(w *wire, kind uint64) (int64, error) {
	//: a scalar written as anything else is not this field.
	if kind != wireVarint {
		//: the field cannot be read as declared.
		return 0, malformed("scalar")
	}
	v, err := w.varint()
	//: two's complement: a negative int64 is a ten-byte varint.
	return int64(v), err
}

// payload reads a message or a string field, which must be length-delimited.
func payload(w *wire, kind uint64) ([]byte, error) {
	//: an embedded message written as anything else is not this field.
	if kind != wireBytes {
		//: the field cannot be read as declared.
		return nil, malformed("message")
	}
	//: the bytes of the embedded message or string.
	return w.bytes()
}

// profileField reads one top-level field.
func (d *decoder) profileField(w *wire, field, kind uint64) (bool, error) {
	read, known := profileReaders[field]
	//: a field Parse does not read.
	if !known {
		//: the caller skips it.
		return false, nil
	}
	//: read by its own reader.
	return true, read(d, w, kind)
}

// readString appends one entry of the string table.
func (d *decoder) readString(w *wire, kind uint64) error {
	s, err := payload(w, kind)
	d.strings = append(d.strings, string(s))
	//: a copy, so the profile does not pin the input buffer.
	return err
}

// readComment appends the string indexes of comments, packed or not.
func (d *decoder) readComment(w *wire, kind uint64) error {
	values, err := w.varints(nil, kind)
	//: each one a string index.
	for _, v := range values {
		d.comments = append(d.comments, int64(v))
	}
	//: malformed or not, as the run says.
	return err
}

// readSampleType appends one sample type.
func (d *decoder) readSampleType(w *wire, kind uint64) error {
	t, err := valueType(w, kind)
	d.types = append(d.types, t)
	//: malformed or not, as the message says.
	return err
}

// readPeriodType reads the period's value type.
func (d *decoder) readPeriodType(w *wire, kind uint64) error {
	t, err := valueType(w, kind)
	d.periodType = t
	//: malformed or not, as the message says.
	return err
}

// readTime reads when the collection started.
func (d *decoder) readTime(w *wire, kind uint64) error {
	v, err := scalar(w, kind)
	d.timeNanos = v
	//: malformed or not, as the field says.
	return err
}

// readDuration reads how long the collection lasted.
func (d *decoder) readDuration(w *wire, kind uint64) error {
	v, err := scalar(w, kind)
	d.durationNanos = v
	//: malformed or not, as the field says.
	return err
}

// readPeriod reads the sampling period.
func (d *decoder) readPeriod(w *wire, kind uint64) error {
	v, err := scalar(w, kind)
	d.period = v
	//: malformed or not, as the field says.
	return err
}

// readDefaultType reads the default sample type's string index.
func (d *decoder) readDefaultType(w *wire, kind uint64) error {
	v, err := scalar(w, kind)
	d.defaultType = v
	//: malformed or not, as the field says.
	return err
}

// valueType decodes a ValueType message.
func valueType(w *wire, kind uint64) (rawType, error) {
	buf, err := payload(w, kind)
	//: not a message.
	if err != nil {
		//: already typed.
		return rawType{}, err
	}
	var t rawType
	//: type is field 1, unit field 2; both string indexes.
	err = fields(buf, func(w *wire, field, kind uint64) (bool, error) {
		//: the two fields of a ValueType.
		switch field {
		//: what is measured.
		case 1:
			var ferr error
			t.typ, ferr = scalar(w, kind)
			//: read.
			return true, ferr
		//: its unit.
		case 2:
			var ferr error
			t.unit, ferr = scalar(w, kind)
			//: read.
			return true, ferr
		}
		//: anything else is skipped.
		return false, nil
	})
	//: the value type, or the malformation.
	return t, err
}

// timeOf turns the profile's time_nanos into an instant, a zero field meaning
// "not recorded".
func timeOf(nanos int64) time.Time {
	//: not recorded.
	if nanos == 0 {
		//: the zero time says so.
		return time.Time{}
	}
	//: Unix nanoseconds, in UTC.
	return time.Unix(0, nanos).UTC()
}
