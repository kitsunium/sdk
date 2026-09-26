// Package profiling — hosts the decoding of a profile's samples, locations and
// functions, and their resolution into frames once the string table is known.
package profiling

import "time"

// Field numbers of the nested messages Parse reads.
const (
	fieldSampleLocation uint64 = 1
	fieldSampleValue    uint64 = 2
	fieldSampleLabel    uint64 = 3
	fieldLabelKey       uint64 = 1
	fieldLabelStr       uint64 = 2
	fieldLabelNum       uint64 = 3
	fieldLocationID     uint64 = 1
	fieldLocationAddr   uint64 = 3
	fieldLocationLine   uint64 = 4
	fieldLineFunction   uint64 = 1
	fieldLineLine       uint64 = 2
	fieldFunctionID     uint64 = 1
	fieldFunctionName   uint64 = 2
	fieldFunctionFile   uint64 = 4
	fieldFunctionStart  uint64 = 5
)

// readSample decodes one Sample: its location ids and values, packed or not,
// and its labels.
func (d *decoder) readSample(w *wire, kind uint64) error {
	buf, err := payload(w, kind)
	//: not a message.
	if err != nil {
		//: already typed.
		return err
	}
	var s rawSample
	//: the three fields of a Sample.
	err = fields(buf, func(w *wire, field, kind uint64) (bool, error) {
		//: one reader per field.
		switch field {
		//: innermost location first.
		case fieldSampleLocation:
			var ferr error
			s.locations, ferr = w.varints(s.locations, kind)
			//: read.
			return true, ferr
		//: one value per sample type.
		case fieldSampleValue:
			values, ferr := w.varints(nil, kind)
			//: two's complement, as every int64.
			for _, v := range values {
				s.values = append(s.values, int64(v))
			}
			//: read.
			return true, ferr
		//: a label.
		case fieldSampleLabel:
			label, ferr := readLabel(w, kind)
			s.labels = append(s.labels, label)
			//: read.
			return true, ferr
		}
		//: anything else is skipped.
		return false, nil
	})
	d.samples = append(d.samples, s)
	//: malformed or not, as the message says.
	return err
}

// readLabel decodes one Label: a key and either a string or a number.
func readLabel(w *wire, kind uint64) (rawLabel, error) {
	buf, err := payload(w, kind)
	//: not a message.
	if err != nil {
		//: already typed.
		return rawLabel{}, err
	}
	var l rawLabel
	//: key, str and num; the unit of a number is not kept.
	err = fields(buf, func(w *wire, field, kind uint64) (bool, error) {
		var target *int64
		//: which of the three the field fills.
		switch field {
		//: the label's key, a string index.
		case fieldLabelKey:
			target = &l.key
		//: a string value, a string index.
		case fieldLabelStr:
			target = &l.str
		//: a numeric value.
		case fieldLabelNum:
			target = &l.num
		//: anything else is skipped.
		default:
			//: not read.
			return false, nil
		}
		var ferr error
		*target, ferr = scalar(w, kind)
		//: read.
		return true, ferr
	})
	//: the label, or the malformation.
	return l, err
}

// readLocation decodes one Location: its id, address and lines.
func (d *decoder) readLocation(w *wire, kind uint64) error {
	buf, err := payload(w, kind)
	//: not a message.
	if err != nil {
		//: already typed.
		return err
	}
	var id uint64
	var loc rawLocation
	//: the fields Parse reads; the mapping is skipped.
	err = fields(buf, func(w *wire, field, kind uint64) (bool, error) {
		//: one reader per field.
		switch field {
		//: the id samples refer to.
		case fieldLocationID:
			v, ferr := scalar(w, kind)
			id = uint64(v)
			//: read.
			return true, ferr
		//: the program counter.
		case fieldLocationAddr:
			v, ferr := scalar(w, kind)
			loc.address = uint64(v)
			//: read.
			return true, ferr
		//: a line, innermost inlined call first.
		case fieldLocationLine:
			line, ferr := readLine(w, kind)
			loc.lines = append(loc.lines, line)
			//: read.
			return true, ferr
		}
		//: anything else is skipped.
		return false, nil
	})
	d.locations[id] = loc
	//: malformed or not, as the message says.
	return err
}

// readLine decodes one Line: a function id and a line number.
func readLine(w *wire, kind uint64) (rawLine, error) {
	buf, err := payload(w, kind)
	//: not a message.
	if err != nil {
		//: already typed.
		return rawLine{}, err
	}
	var l rawLine
	//: the function and the line; the column is skipped.
	err = fields(buf, func(w *wire, field, kind uint64) (bool, error) {
		//: one reader per field.
		switch field {
		//: the function id.
		case fieldLineFunction:
			v, ferr := scalar(w, kind)
			l.function = uint64(v)
			//: read.
			return true, ferr
		//: the line executing.
		case fieldLineLine:
			var ferr error
			l.line, ferr = scalar(w, kind)
			//: read.
			return true, ferr
		}
		//: anything else is skipped.
		return false, nil
	})
	//: the line, or the malformation.
	return l, err
}

// readFunction decodes one Function: its id, name, file and start line.
func (d *decoder) readFunction(w *wire, kind uint64) error {
	buf, err := payload(w, kind)
	//: not a message.
	if err != nil {
		//: already typed.
		return err
	}
	var id int64
	var fn rawFunction
	//: the fields Parse reads; the system name is skipped.
	err = fields(buf, func(w *wire, field, kind uint64) (bool, error) {
		var target *int64
		//: which field the value fills.
		switch field {
		//: the id lines refer to.
		case fieldFunctionID:
			target = &id
		//: the name, a string index.
		case fieldFunctionName:
			target = &fn.name
		//: the file, a string index.
		case fieldFunctionFile:
			target = &fn.file
		//: the first line.
		case fieldFunctionStart:
			target = &fn.start
		//: anything else is skipped.
		default:
			//: not read.
			return false, nil
		}
		var ferr error
		*target, ferr = scalar(w, kind)
		//: read.
		return true, ferr
	})
	d.functions[uint64(id)] = fn
	//: malformed or not, as the message says.
	return err
}

// resolve turns the raw tables into a ProfileValue, checking every index and
// building at most frames frames, the locations' and the stacks' together.
func (d *decoder) resolve(frames int) (*ProfileValue, error) {
	//: profile.proto requires the empty string first.
	if len(d.strings) == 0 || d.strings[0] != "" {
		//: every "absent" string index would resolve to something else.
		return nil, malformed("string table")
	}
	r := resolver{d: d, frames: make(map[uint64][]FrameValue, len(d.locations)), budget: frames, limit: frames}
	p := &ProfileValue{Time: timeOf(d.timeNanos), Duration: time.Duration(d.durationNanos), Period: d.period}
	//: the header first: its strings are few and every sample needs its types.
	if err := r.header(p); err != nil {
		//: already typed.
		return nil, err
	}
	p.Samples = make([]SampleValue, 0, len(d.samples))
	//: every sample, its stack built from the shared location frames.
	for i := range d.samples {
		s, err := r.sample(&d.samples[i], len(p.SampleTypes))
		//: an index that points nowhere.
		if err != nil {
			//: already typed.
			return nil, err
		}
		p.Samples = append(p.Samples, s)
	}
	//: decoded, resolved, checked.
	return p, nil
}
