package cbor

import (
	"math"
	"reflect"
	"time"
)

// The struct tag options that write a time.Time field as a tagged time.
const (
	// optionTimeTag1 writes the field as tag 1, epoch seconds, falling back
	// to tag 0 when a double cannot carry the instant to the nanosecond.
	optionTimeTag1 string = "time=tag1"
	// optionTimeTag0 writes the field as tag 0, its RFC 3339 text.
	optionTimeTag0 string = "time=tag0"
)

// tagType is the reflect.Type of TagValue, which both plans recognise.
var tagType = reflect.TypeFor[TagValue]()

// TagValue is a CBOR tag (RFC 8949 §3.4): a number and the data item it encloses,
// for the tags an application defines — COSE's 16 to 18 and 96 to 98, CWT's
// 61. Encoding one writes its number then its Content as any value is
// written; a tag numbered 0 to 3 must enclose the type RFC 8949 gives it.
// Decoding into a TagValue takes any tag, its content decoded as into an
// interface; an untagged item does not fit one.
type TagValue struct {
	// Number is the tag number.
	Number uint64
	// Content is the enclosed data item.
	Content any
}

// tagEncoder writes a Tag.
type tagEncoder struct{}

// tagDecoder reads a tag into a Tag.
type tagDecoder struct{}

// timeTagEncoder writes a time.Time field carrying the time option as the
// tag it names: tagEpoch or tagDateTime.
type timeTagEncoder struct {
	// tag is tagEpoch for time=tag1, tagDateTime for time=tag0.
	tag uint64
}

// encode appends the tag head and its content.
func (tagEncoder) encode(b []byte, v reflect.Value, _ *encodePlan, at walkDepth) ([]byte, error) {
	tag, _ := reflect.TypeAssert[TagValue](v)
	//: the content is encoded as any value is.
	return appendTag(b, tag, at)
}

// empty is false: omitempty never omits a tag.
func (tagEncoder) empty(_ reflect.Value, _ *encodePlan) (bool, error) {
	//: a tag is never an empty item.
	return false, nil
}

// appendTag appends tag's number and its content, one level deeper. A tag
// 0 to 3 whose content is not of the type RFC 8949 gives it is refused,
// since the decoder would refuse it.
func appendTag(b []byte, tag TagValue, at walkDepth) ([]byte, error) {
	inner, err := at.enter()
	//: a tag is one level of nesting.
	if err != nil {
		//: too deep.
		return b, err
	}
	b = appendHead(b, majorTag, tag.Number)
	start := len(b)
	b, err = appendValue(b, tag.Content, inner)
	//: the content could not be encoded.
	if err != nil {
		//: its refusal.
		return b, err
	}
	h, _ := readHead(b, start)
	//: RFC 8949 §3.4: tags 0 to 3 admit one content type each.
	if !tagAdmits(tag.Number, h) {
		//: what the decoder would refuse is not written.
		return b, encodeFailure("a Tag numbered 0 to 3 whose content is not the type RFC 8949 gives the number")
	}
	//: written.
	return b, nil
}

// decode reads a tag into a Tag: its number, and its content decoded as into
// an interface. Null leaves the Tag as it is; an untagged item is recorded as
// not fitting.
func (tagDecoder) decode(d *decodeState, v reflect.Value, p *decodePlan) error {
	h, err := d.peek()
	//: never on validated input.
	if err != nil {
		//: corrupt.
		return err
	}
	//: null is "no tag": nothing to store.
	if isNull(h) {
		d.off += h.size
		//: unchanged.
		return nil
	}
	//: only a tag fills a Tag.
	if h.major != majorTag {
		//: recorded and skipped.
		return d.mismatch(h, p.typ)
	}
	d.off += h.size
	content, err := d.decodeAny()
	setValue(v, TagValue{Number: h.arg, Content: content})
	//: stored, or the content's failure.
	return err
}

// encode appends the time as the tag the option names; the zero time is
// null, as it is without the option.
func (e timeTagEncoder) encode(b []byte, v reflect.Value, _ *encodePlan, _ walkDepth) ([]byte, error) {
	instant, _ := reflect.TypeAssert[time.Time](v)
	//: the registered codec's null for "no time".
	if instant.IsZero() {
		//: null.
		return append(b, nullByte), nil
	}
	//: tag 1 when the instant fits its content.
	if e.tag == tagEpoch {
		//: an integer, a float, or the tag 0 fallback.
		return appendEpochTime(b, instant), nil
	}
	//: tag 0.
	return appendDateTime(b, instant), nil
}

// empty is false, as it is for a time.Time without the option.
func (timeTagEncoder) empty(_ reflect.Value, _ *encodePlan) (bool, error) {
	//: a time is never an empty item.
	return false, nil
}

// appendEpochTime appends t as tag 1: integer seconds when t holds no
// fraction, float seconds when a double carries it to the nanosecond as the
// decoder reads one back, and otherwise tag 0, so the instant is never
// rounded (RFC 8949 §3.4.2 allows tag 1 nothing finer than a double).
func appendEpochTime(b []byte, t time.Time) []byte {
	//: whole seconds.
	if t.Nanosecond() == 0 {
		//: an integer.
		return appendInt(appendHead(b, majorTag, tagEpoch), t.Unix())
	}
	seconds := float64(t.Unix()) + float64(t.Nanosecond())/nanosPerSecond
	whole, fraction := math.Modf(seconds)
	//: the instant the decoder's floatTime would give back.
	if time.Unix(int64(whole), int64(fraction*nanosPerSecond)).Equal(t) {
		//: a float, in its shortest exact form.
		return appendShortestFloat(appendHead(b, majorTag, tagEpoch), seconds)
	}
	//: a double would round it: the RFC 3339 text keeps every digit.
	return appendDateTime(b, t)
}

// appendDateTime appends t as tag 0: its RFC 3339 text to the nanosecond,
// in its own offset.
func appendDateTime(b []byte, t time.Time) []byte {
	b = appendHead(b, majorTag, tagDateTime)
	text := t.Format(time.RFC3339Nano)
	b = appendHead(b, majorText, uint64(len(text)))
	//: the text is ASCII: always valid UTF-8.
	return append(b, text...)
}
