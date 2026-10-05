package toml

import (
	"strconv"
	"time"
)

// Text widths of the three types.
const (
	// localDateWidth is the length of YYYY-MM-DD.
	localDateWidth int = 10
	// localTimeWidth is the length of HH:MM:SS without a fraction.
	localTimeWidth int = 8
	// zeroPadWidth is the width every field but the year is padded to.
	zeroPadWidth int = 2
)

// asTime is LocalDate.AsTime's body: decl_gen.go writes LocalDate.AsTime, from the
// design, as one call of it.
func (d LocalDate) asTime(zone *time.Location) time.Time {
	//: midnight, in the zone the caller chose.
	return time.Date(d.Year, time.Month(d.Month), d.Day, 0, 0, 0, 0, zone)
}

// String returns the day as RFC 3339 writes it, YYYY-MM-DD.
func (d LocalDate) String() string {
	//: one allocation, the string itself.
	return string(d.appendText(make([]byte, 0, localDateWidth)))
}

// marshalText is LocalDate.MarshalText's body: decl_gen.go writes LocalDate.MarshalText, from the
// design, as one call of it.
func (d LocalDate) marshalText() ([]byte, error) {
	//: the text form.
	return d.appendText(make([]byte, 0, localDateWidth)), nil
}

// UnmarshalText reads a day written YYYY-MM-DD, refusing a day the month does
// not have.
func (d *LocalDate) UnmarshalText(text []byte) error {
	var dt datetime
	//: exactly a date, nothing before or after it.
	if err := parseExactly(text, &dt, kindLocalDate); err != nil {
		//: UNMARSHAL_FAILED.
		return err
	}
	*d = localDateOf(&dt)
	//: read.
	return nil
}

// appendText appends the day to b.
func (d LocalDate) appendText(b []byte) []byte {
	b = appendPadded(b, d.Year, yearDigits)
	b = append(b, '-')
	b = appendPadded(b, d.Month, zeroPadWidth)
	b = append(b, '-')
	//: YYYY-MM-DD.
	return appendPadded(b, d.Day, zeroPadWidth)
}

// String returns the time as RFC 3339 writes it, HH:MM:SS, with Precision
// fractional digits, or the fewest that keep Nanosecond when Precision is 0.
func (t LocalTime) String() string {
	//: one allocation, the string itself.
	return string(t.appendText(make([]byte, 0, localTimeWidth+1+nanoDigits)))
}

// marshalText is LocalTime.MarshalText's body: decl_gen.go writes LocalTime.MarshalText, from the
// design, as one call of it.
func (t LocalTime) marshalText() ([]byte, error) {
	//: the text form.
	return t.appendText(make([]byte, 0, localTimeWidth+1+nanoDigits)), nil
}

// UnmarshalText reads a time written HH:MM:SS with an optional fraction.
func (t *LocalTime) UnmarshalText(text []byte) error {
	var dt datetime
	//: exactly a time, nothing before or after it.
	if err := parseExactly(text, &dt, kindLocalTime); err != nil {
		//: UNMARSHAL_FAILED.
		return err
	}
	*t = localTimeOf(&dt)
	//: read.
	return nil
}

// appendText appends the time to b.
func (t LocalTime) appendText(b []byte) []byte {
	b = appendPadded(b, t.Hour, zeroPadWidth)
	b = append(b, ':')
	b = appendPadded(b, t.Minute, zeroPadWidth)
	b = append(b, ':')
	b = appendPadded(b, t.Second, zeroPadWidth)
	//: the fraction, when there is one to write.
	return appendFraction(b, t.Nanosecond, t.Precision)
}

// AsTime returns the date-time in zone.
func (dt LocalDateTime) AsTime(zone *time.Location) time.Time {
	//: the wall clock, in the zone the caller chose.
	return time.Date(dt.Year, time.Month(dt.Month), dt.Day, dt.Hour, dt.Minute, dt.Second, dt.Nanosecond, zone)
}

// String returns the date-time as RFC 3339 writes it, with a T between the
// date and the time.
func (dt LocalDateTime) String() string {
	//: one allocation, the string itself.
	return string(dt.appendText(make([]byte, 0, localDateWidth+1+localTimeWidth+1+nanoDigits)))
}

// marshalText is LocalDateTime.MarshalText's body: decl_gen.go writes LocalDateTime.MarshalText, from the
// design, as one call of it.
func (dt LocalDateTime) marshalText() ([]byte, error) {
	//: the text form.
	return dt.appendText(make([]byte, 0, localDateWidth+1+localTimeWidth+1+nanoDigits)), nil
}

// UnmarshalText reads a date-time written YYYY-MM-DDTHH:MM:SS with an optional
// fraction; a space or a t may stand for the T.
func (dt *LocalDateTime) UnmarshalText(text []byte) error {
	var parsed datetime
	//: exactly a local date-time, nothing before or after it.
	if err := parseExactly(text, &parsed, kindLocalDateTime); err != nil {
		//: UNMARSHAL_FAILED.
		return err
	}
	*dt = LocalDateTime{LocalDate: localDateOf(&parsed), LocalTime: localTimeOf(&parsed)}
	//: read.
	return nil
}

// appendText appends the date-time to b.
func (dt LocalDateTime) appendText(b []byte) []byte {
	b = dt.LocalDate.appendText(b)
	b = append(b, 'T')
	//: date, T, time.
	return dt.LocalTime.appendText(b)
}

// appendPadded appends v in decimal, zero-padded to width.
func appendPadded(b []byte, v, width int) []byte {
	//: a negative field is not RFC 3339; written as strconv writes it.
	if v < 0 {
		//: unpadded.
		return strconv.AppendInt(b, int64(v), int(decimalBase))
	}
	digits := len(strconv.AppendInt(make([]byte, 0, width), int64(v), int(decimalBase)))
	//: the zeros the value is short of.
	for range width - digits {
		b = append(b, '0')
	}
	//: the value.
	return strconv.AppendInt(b, int64(v), int(decimalBase))
}

// appendFraction appends the fraction of a second: precision digits, or the
// fewest that keep nanosecond when precision is 0.
func appendFraction(b []byte, nanosecond, precision int) []byte {
	//: nothing to write.
	if precision <= 0 && nanosecond <= 0 {
		//: no fraction.
		return b
	}
	var digits [nanoDigits]byte
	// A negative nanosecond is no time of day; it is written as zero rather
	// than as the characters its negative digits would make.
	n := max(nanosecond, 0)
	//: the nine digits, least significant last.
	for i := nanoDigits - 1; i >= 0; i-- {
		digits[i] = '0' + byte(n%int(decimalBase))
		n /= int(decimalBase)
	}
	width := min(precision, nanoDigits)
	//: no precision: the trailing zeros are dropped.
	if width <= 0 {
		width = nanoDigits
		//: down to the last significant digit.
		for width > 1 && digits[width-1] == '0' {
			width--
		}
	}
	b = append(b, '.')
	//: the digits.
	return append(b, digits[:width]...)
}

// parseExactly parses text as exactly one value of kind want into dt.
func parseExactly(text []byte, dt *datetime, want kind) error {
	p := parser{data: text}
	got, err := p.dateTime(dt)
	//: malformed, or another kind, or something after it.
	if err == nil && (got != want || p.pos != len(text)) {
		err = p.fail(p.pos, problemNotThisKind)
	}
	//: nil, or the refusal.
	return err
}

// localDateOf returns the date part of dt.
func localDateOf(dt *datetime) LocalDate {
	//: the three date fields.
	return LocalDate{Year: dt.year, Month: dt.month, Day: dt.day}
}

// localTimeOf returns the time part of dt.
func localTimeOf(dt *datetime) LocalTime {
	//: the time fields and the precision written.
	return LocalTime{Hour: dt.hour, Minute: dt.minute, Second: dt.second, Nanosecond: dt.nanosecond, Precision: dt.precision}
}
