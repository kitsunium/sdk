// Package encoder — renders the record timestamp, the one field every encoded
// line carries and the single most expensive thing either encoder does.
//
// A CPU profile of a bare emit attributed 34.6 % of the WHOLE call —
// builder, handler, encoder and sink together — to time.Time.AppendFormat,
// with time.nextStdChunk and time.appendInt the two largest flat entries under
// it. That is the generic formatter re-parsing the layout string, chunk by
// chunk, on every single log record, to reach a result that never varies in
// shape. appendTimestamp writes the same bytes with fixed offsets instead, and
// is measured at 6.0× the stdlib call on a UTC instant and 5.0× on an offset
// zone — `BenchmarkAppendTimestamp{,Zoned}` against their `…Stdlib` controls,
// reported in internal/service/logger/encoder/BENCH.md §5.2.
//
// It is a REPLACEMENT for one exact layout, not a general formatter: anything
// it cannot render identically falls back to AppendFormat rather than
// approximating. TestAppendTimestampMatchesAppendFormat pins the equivalence
// across four zones and a hundred thousand instants each.
package encoder

import "time"

const (
	// timestampLayout is the RFC3339-with-milliseconds format every encoded
	// record timestamp carries — BOTH encoders, and every KindTime attribute.
	// It lives beside appendTimestamp rather than in text.go or json.go
	// because appendTimestamp is what renders it and is the only code that can
	// go wrong about it; it used to be declared twice, once per encoder, with
	// identical values that nothing stopped from drifting apart.
	timestampLayout string = "2006-01-02T15:04:05.000Z07:00"

	// timestampMinYear and timestampMaxYear bound the years the fixed-width
	// four-digit path can render. Outside them the "2006" layout verb emits
	// a different WIDTH — five digits past 9999, a leading minus before year
	// zero — so those instants go to the stdlib formatter instead of being
	// silently truncated into a wrong but well-formed date.
	timestampMinYear int = 0
	// timestampMaxYear is the last year the four-digit form can spell.
	timestampMaxYear int = 9999

	// secondsPerHour and secondsPerMinute split a zone offset, which time.Zone
	// reports in seconds east of UTC.
	secondsPerHour int = 3600
	// secondsPerMinute is the divisor that drops the sub-minute part of an
	// offset, exactly as the "Z07:00" verb does.
	secondsPerMinute int = 60
	// minutesPerHour closes the offset split.
	minutesPerHour int = 60

	// nanosPerMilli converts the nanosecond field to the milliseconds the
	// ".000" verb renders.
	nanosPerMilli int = 1_000_000

	// asciiZero is the byte the digit helpers count up from.
	asciiZero byte = '0'

	// decimalRadix, hundreds and thousands are the place-value divisors the
	// fixed-width digit helpers use. Named rather than inline because every
	// one of them is a POSITION in a format whose widths are fixed by the
	// layout above — not a tunable, and not a number anyone should change
	// without changing timestampLayout in the same edit.
	decimalRadix int = 10
	// hundreds isolates the third decimal place (year centuries, millis).
	hundreds int = 100
	// thousands isolates the fourth decimal place (year millennia).
	thousands int = 1000
)

// appendTimestamp writes t onto dst in the layout both encoders share —
// "2006-01-02T15:04:05.000Z07:00" — producing byte-for-byte what
// t.AppendFormat(dst, timestampLayout) produces.
//
// The equivalence is the contract, not an aspiration: a year the four-digit
// form cannot spell falls back to the stdlib rather than being truncated, and
// the zone offset is split the same way the "Z07:00" verb splits it — to
// whole minutes, dropping any sub-minute part, which historical zones can
// carry.
func appendTimestamp(dst []byte, t time.Time) []byte {
	year, month, day := t.Date()
	//: a year outside the four-digit window would render at a different width;
	//: hand those to the general formatter rather than spell them wrong.
	if year < timestampMinYear || year > timestampMaxYear {
		//: the stdlib owns every instant this fast path cannot reproduce.
		return t.AppendFormat(dst, timestampLayout)
	}
	hour, minute, second := t.Clock()
	dst = appendYear(dst, year)
	dst = append(dst, '-')
	dst = appendPad2(dst, int(month))
	dst = append(dst, '-')
	dst = appendPad2(dst, day)
	dst = append(dst, 'T')
	dst = appendPad2(dst, hour)
	dst = append(dst, ':')
	dst = appendPad2(dst, minute)
	dst = append(dst, ':')
	dst = appendPad2(dst, second)
	dst = append(dst, '.')
	dst = appendMillis(dst, t.Nanosecond()/nanosPerMilli)
	//: the zone is read from the value, never from the process location, so
	//: the rendering follows the instant the caller handed in.
	_, offset := t.Zone()
	//: hand back the buffer with the offset (or 'Z') closing the timestamp.
	return appendZoneOffset(dst, offset)
}

// appendYear writes a year in the fixed four-digit form the "2006" verb uses.
// The caller has already established that year is in [0, 9999].
func appendYear(dst []byte, year int) []byte {
	//: fixed width, most significant digit first — no loop, no division by a
	//: variable, and no branch on magnitude.
	return append(dst,
		asciiZero+byte(year/thousands%decimalRadix),
		asciiZero+byte(year/hundreds%decimalRadix),
		asciiZero+byte(year/decimalRadix%decimalRadix),
		asciiZero+byte(year%decimalRadix),
	)
}

// appendPad2 writes v as exactly two digits, zero-padded, matching every
// two-digit verb in the layout ("01", "02", "15", "04", "05").
func appendPad2(dst []byte, v int) []byte {
	//: every caller passes a value already bounded below 100 by the calendar.
	return append(dst, asciiZero+byte(v/decimalRadix), asciiZero+byte(v%decimalRadix))
}

// appendMillis writes ms as exactly three digits, matching the ".000" verb —
// which pads rather than truncating, so 7 ms renders as "007".
func appendMillis(dst []byte, ms int) []byte {
	//: fixed three digits; the ".000" verb never elides a trailing zero.
	return append(dst,
		asciiZero+byte(ms/hundreds),
		asciiZero+byte(ms/decimalRadix%decimalRadix),
		asciiZero+byte(ms%decimalRadix),
	)
}

// appendZoneOffset writes the "Z07:00" verb's rendering of offset seconds:
// the literal 'Z' at UTC, otherwise a signed ±HH:MM.
//
// The offset is reduced to whole MINUTES before being split, which is what the
// verb does: a historical zone carrying seconds of offset (LMT entries in the
// tz database do) renders without them rather than rounding into the next
// minute.
func appendZoneOffset(dst []byte, offset int) []byte {
	//: UTC is the one offset the verb spells as a letter rather than a number.
	if offset == 0 {
		//: 'Z' is the whole rendering — no sign, no digits, no colon.
		return append(dst, 'Z')
	}
	sign := byte('+')
	//: west of UTC flips the sign and the arithmetic works on the magnitude.
	if offset < 0 {
		//: negate so the digit split below never sees a negative operand.
		sign, offset = '-', -offset
	}
	dst = append(dst, sign)
	dst = appendPad2(dst, offset/secondsPerHour)
	dst = append(dst, ':')
	//: whole minutes only — the sub-minute part of an offset is not rendered.
	return appendPad2(dst, offset/secondsPerMinute%minutesPerHour)
}
