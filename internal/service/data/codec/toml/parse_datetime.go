package toml

// Field widths and bounds of RFC 3339.
const (
	// yearDigits is the width of a year.
	yearDigits int = 4
	// pairDigits is the width of every other field.
	pairDigits int = 2
	// monthsPerYear bounds a month.
	monthsPerYear int = 12
	// maxHour bounds an hour, and an offset's hours.
	maxHour int = 23
	// maxMinute bounds a minute, and an offset's minutes.
	maxMinute int = 59
	// maxSecond bounds a second: a leap second is refused, as the previous
	// library refused it, rather than silently moved to the next minute.
	maxSecond int = 59
	// nanoDigits is how many fractional digits a nanosecond holds; more are
	// truncated (§Offset Date-Time: "must be truncated, not rounded").
	nanoDigits int = 9
	// secondsPerMinute converts an offset's minutes.
	secondsPerMinute int = 60
	// secondsPerHour converts an offset's hours.
	secondsPerHour int = 3600
	// datetimeBytes is the size of one datetime, for the retain decision.
	datetimeBytes int = 72
)

// Days in the months whose length does not depend on the year.
const (
	// longMonth has 31 days.
	longMonth int = 31
	// shortMonth has 30 days.
	shortMonth int = 30
	// februaryDays is February outside a leap year.
	februaryDays int = 28
	// leapFebruaryDays is February in a leap year.
	leapFebruaryDays int = 29
	// february is the month whose length depends on the year.
	february int = 2
	// april has 30 days.
	april int = 4
	// june has 30 days.
	june int = 6
	// september has 30 days.
	september int = 9
	// november has 30 days.
	november int = 11
)

// offsetInvalid marks an offset that offset could not read: no real offset
// comes near it, since an offset's hours are at most 23.
const offsetInvalid int = 1 << 30

// The Gregorian leap-year rule.
const (
	// leapCycle is every fourth year.
	leapCycle int = 4
	// centuryCycle is every hundredth, which is not.
	centuryCycle int = 100
	// quadCenturyCycle is every four hundredth, which is again.
	quadCenturyCycle int = 400
)

// nanoScale multiplies a fraction of n digits up to nanoseconds.
var nanoScale = [...]int{0, 1e8, 1e7, 1e6, 1e5, 1e4, 1e3, 1e2, 1e1, 1e0}

// datetime is the parsed value of any of the four date and time kinds.
type datetime struct {
	// year is the calendar year.
	year int
	// month is 1 to 12.
	month int
	// day is 1 to 31.
	day int
	// hour is 0 to 23.
	hour int
	// minute is 0 to 59.
	minute int
	// second is 0 to 59.
	second int
	// nanosecond is the fraction, truncated to nine digits.
	nanosecond int
	// precision is how many fractional digits were written, up to nine.
	precision int
	// offset is the offset date-time's zone, in seconds east of UTC.
	offset int
}

// numberOrDate reads a value that starts like a number: an integer, a float,
// or a date or a time, which start with four digits and a dash or two digits
// and a colon.
func (p *parser) numberOrDate(parent int32, part keyPart) error {
	//: a date, a date-time or a time.
	if p.digitsThen(yearDigits, '-') || p.digitsThen(pairDigits, ':') {
		//: read as one.
		return p.dateTimeValue(parent, part)
	}
	//: an integer or a float.
	return p.numberValue(parent, part)
}

// digitsThen reports whether count digits then the byte mark start at p.pos.
func (p *parser) digitsThen(count int, mark byte) bool {
	//: the run and its mark must both be there.
	if len(p.data)-p.pos <= count || p.data[p.pos+count] != mark {
		//: not this shape.
		return false
	}
	//: every byte of the run is a digit.
	for _, c := range p.data[p.pos : p.pos+count] {
		//: a non-digit.
		if byteClass[c]&classDigit == 0 {
			//: not this shape.
			return false
		}
	}
	//: the shape.
	return true
}

// dateTimeValue reads one of the four date and time kinds.
func (p *parser) dateTimeValue(parent int32, part keyPart) error {
	at := p.pos
	var dt datetime
	k, err := p.dateTime(&dt)
	//: a malformed or impossible date or time.
	if err != nil {
		//: refused.
		return err
	}
	n := leaf(k, part, at)
	n.num = uint64(len(p.times))
	n.text = span{start: int32(at), end: int32(p.pos)}
	p.times = append(p.times, dt)
	p.add(parent, n)
	//: the date or time.
	return nil
}

// dateTime reads a date and time into dt and returns which of the four kinds
// it is.
func (p *parser) dateTime(dt *datetime) (kind, error) {
	//: a time with no date is a local time, and takes no offset.
	if p.digitsThen(pairDigits, ':') {
		//: the time alone.
		return kindLocalTime, p.timeOfDay(dt)
	}
	//: the date.
	if err := p.date(dt); err != nil {
		//: refused.
		return 0, err
	}
	//: no time after the date.
	if !p.timeDelimiter() {
		//: a local date.
		return kindLocalDate, nil
	}
	//: the time.
	if err := p.timeOfDay(dt); err != nil {
		//: refused.
		return 0, err
	}
	//: an offset makes it an instant.
	if p.offset(dt) {
		//: an offset date-time, unless the offset was malformed.
		return kindDateTime, p.offsetError(dt)
	}
	//: a local date-time.
	return kindLocalDateTime, nil
}

// date reads YYYY-MM-DD and checks that the day exists.
func (p *parser) date(dt *datetime) error {
	at := p.pos
	var ok bool
	dt.year, ok = p.fixed(yearDigits, '-')
	//: the year, the month and the day, each with its separator.
	if ok {
		dt.month, ok = p.fixed(pairDigits, '-')
	}
	//: the day has no separator after it.
	if ok {
		dt.day, ok = p.fixed(pairDigits, 0)
	}
	//: a malformed date, or a day the month does not have.
	if !ok || dt.month < 1 || dt.month > monthsPerYear || dt.day < 1 || dt.day > daysIn(dt.month, dt.year) {
		//: refused.
		return p.fail(at, problemBadDate)
	}
	//: a real day.
	return nil
}

// timeDelimiter consumes the T, t or space between a date and a time, and
// reports whether a time follows. A space is a delimiter only before a digit:
// otherwise it is the whitespace after a local date.
func (p *parser) timeDelimiter() bool {
	//: the end of the document after a date.
	if p.pos >= len(p.data) {
		//: a local date.
		return false
	}
	c := p.data[p.pos]
	//: T or t always announces a time.
	if c == 'T' || c == 't' {
		p.pos++
		//: a time must follow.
		return true
	}
	//: a space announces a time only before a digit.
	if c == charSpace && p.pos+1 < len(p.data) && byteClass[p.data[p.pos+1]]&classDigit != 0 {
		p.pos++
		//: a time follows.
		return true
	}
	//: a local date.
	return false
}

// timeOfDay reads HH:MM:SS with an optional fraction. Seconds may be left out
// — TOML v1.1.0, which the previous library accepted — and a fraction then
// cannot follow.
func (p *parser) timeOfDay(dt *datetime) error {
	at := p.pos
	var ok bool
	dt.hour, ok = p.fixed(pairDigits, ':')
	//: the minute.
	if ok {
		dt.minute, ok = p.fixed(pairDigits, 0)
	}
	//: seconds, then a fraction.
	if ok && p.consume(':') {
		dt.second, ok = p.fixed(pairDigits, 0)
		ok = ok && p.secondFraction(dt)
	}
	//: a malformed or impossible time.
	if !ok || dt.hour > maxHour || dt.minute > maxMinute || dt.second > maxSecond {
		//: refused.
		return p.fail(at, problemBadTime)
	}
	//: a real time.
	return nil
}

// secondFraction reads the optional fraction of a second, truncated to the
// nanosecond, and reports whether it was well formed.
func (p *parser) secondFraction(dt *datetime) bool {
	//: no fraction.
	if !p.consume(charDot) {
		//: well formed.
		return true
	}
	start := p.pos
	//: every digit, keeping the first nine.
	for p.pos < len(p.data) && byteClass[p.data[p.pos]]&classDigit != 0 {
		//: digits past the ninth are truncated.
		if p.pos-start < nanoDigits {
			dt.nanosecond = dt.nanosecond*int(decimalBase) + int(p.data[p.pos]-'0')
		}
		p.pos++
	}
	dt.precision = min(p.pos-start, nanoDigits)
	dt.nanosecond *= nanoScale[dt.precision]
	//: a dot needs at least one digit.
	return p.pos > start
}

// offset reads Z, z, or ±HH:MM into dt and reports whether there was one.
// offsetError then says whether it was well formed.
func (p *parser) offset(dt *datetime) bool {
	//: the end of the document after the time.
	if p.pos >= len(p.data) {
		//: no offset.
		return false
	}
	c := p.data[p.pos]
	//: UTC.
	if c == 'Z' || c == 'z' {
		p.pos++
		//: zero offset.
		return true
	}
	//: no offset.
	if c != '+' && c != '-' {
		//: a local date-time.
		return false
	}
	p.pos++
	hours, okH := p.fixed(pairDigits, ':')
	minutes, okM := p.fixed(pairDigits, 0)
	//: a malformed offset is recorded as impossible for offsetError.
	if !okH || !okM || hours > maxHour || minutes > maxMinute {
		dt.offset = offsetInvalid
		//: an offset, malformed.
		return true
	}
	dt.offset = hours*secondsPerHour + minutes*secondsPerMinute
	//: west of UTC is negative.
	if c == '-' {
		dt.offset = -dt.offset
	}
	//: an offset.
	return true
}

// offsetError refuses the offset offset marked as malformed.
func (p *parser) offsetError(dt *datetime) error {
	//: a well-formed offset.
	if dt.offset != offsetInvalid {
		//: nothing to refuse.
		return nil
	}
	//: refused.
	return p.fail(p.pos, problemBadOffset)
}

// fixed reads exactly width digits, then the separator sep unless sep is 0,
// and returns their value.
func (p *parser) fixed(width int, sep byte) (int, bool) {
	//: the digits must all be there.
	if len(p.data)-p.pos < width {
		//: malformed.
		return 0, false
	}
	value := 0
	//: each digit.
	for _, c := range p.data[p.pos : p.pos+width] {
		//: a non-digit.
		if byteClass[c]&classDigit == 0 {
			//: malformed.
			return 0, false
		}
		value = value*int(decimalBase) + int(c-'0')
	}
	p.pos += width
	//: the separator, when one is due.
	if sep != 0 && !p.consume(sep) {
		//: malformed.
		return 0, false
	}
	//: the field.
	return value, true
}

// daysIn returns the number of days of month in year.
func daysIn(month, year int) int {
	switch month {
	//: February depends on the year.
	case february:
		//: 29 days in a leap year.
		if isLeap(year) {
			//: leap.
			return leapFebruaryDays
		}
		//: common.
		return februaryDays
	//: April, June, September, November.
	case april, june, september, november:
		return shortMonth
	//: the others.
	default:
		return longMonth
	}
}

// isLeap reports whether year is a Gregorian leap year.
func isLeap(year int) bool {
	//: every fourth year, except centuries not divisible by four hundred.
	return year%leapCycle == 0 && (year%centuryCycle != 0 || year%quadCenturyCycle == 0)
}
