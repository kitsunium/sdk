// Package profiling — hosts the resolver: string indexes into strings,
// location ids into frames — each location's frames built once and shared by
// every sample that passes through it — and labels into maps.
package profiling

// resolver resolves a decoder's raw tables.
type resolver struct {
	d *decoder
	// frames caches the frames of each location already resolved.
	frames map[uint64][]FrameValue
}

// str resolves a string index.
func (r *resolver) str(i int64) (string, error) {
	//: an index outside the table points nowhere.
	if i < 0 || i >= int64(len(r.d.strings)) {
		//: the index is not repeated: the input is the caller's.
		return "", malformed("string index")
	}
	//: the table entry.
	return r.d.strings[i], nil
}

// valueType resolves a ValueType's two strings.
func (r *resolver) valueType(t rawType) (SampleTypeValue, error) {
	typ, err := r.str(t.typ)
	//: an unresolvable type name.
	if err != nil {
		//: already typed.
		return SampleTypeValue{}, err
	}
	unit, err := r.str(t.unit)
	//: the pair, or the malformation.
	return SampleTypeValue{Type: typ, Unit: unit}, err
}

// header resolves the profile-level strings: the sample types, the period
// type, the default sample type and the comments.
func (r *resolver) header(p *ProfileValue) error {
	//: one value type per value of every sample.
	for _, t := range r.d.types {
		st, err := r.valueType(t)
		//: an unresolvable sample type.
		if err != nil {
			//: already typed.
			return err
		}
		p.SampleTypes = append(p.SampleTypes, st)
	}
	var err error
	//: the period's type, then the default sample type.
	if p.PeriodType, err = r.valueType(r.d.periodType); err != nil {
		//: already typed.
		return err
	}
	//: an index, zero meaning "not said".
	if p.DefaultSampleType, err = r.str(r.d.defaultType); err != nil {
		//: already typed.
		return err
	}
	//: every comment is a string index.
	for _, c := range r.d.comments {
		text, err := r.str(c)
		//: an unresolvable comment.
		if err != nil {
			//: already typed.
			return err
		}
		p.Comments = append(p.Comments, text)
	}
	//: resolved.
	return nil
}

// sample resolves one sample: its values checked against the sample types,
// its stack built from its locations, its labels mapped.
func (r *resolver) sample(raw *rawSample, types int) (SampleValue, error) {
	//: profile.proto: one value per sample type, always.
	if len(raw.values) != types {
		//: a sample a fold could not index.
		return SampleValue{}, malformed("sample values")
	}
	stack := make([]FrameValue, 0, len(raw.locations))
	//: innermost location first, as the profile orders them.
	for _, id := range raw.locations {
		frames, err := r.location(id)
		//: a location id that points nowhere.
		if err != nil {
			//: already typed.
			return SampleValue{}, err
		}
		stack = append(stack, frames...)
	}
	labels, numbers, err := r.labels(raw.labels)
	//: the sample, or the malformation.
	return SampleValue{Labels: labels, NumLabels: numbers, Stack: stack, Values: raw.values}, err
}

// location returns the frames of a location, innermost inlined call first,
// resolving them the first time.
func (r *resolver) location(id uint64) ([]FrameValue, error) {
	//: already resolved for an earlier sample.
	if frames, done := r.frames[id]; done {
		//: shared, read-only.
		return frames, nil
	}
	loc, known := r.d.locations[id]
	//: a sample names a location the profile does not carry.
	if !known {
		//: the id is not repeated.
		return nil, malformed("location id")
	}
	var frames []FrameValue
	//: no symbol was found: the address is all there is.
	if len(loc.lines) == 0 {
		frames = []FrameValue{{Address: loc.address}}
	}
	//: one frame per line; several when calls were inlined.
	for _, line := range loc.lines {
		frame, err := r.frame(line)
		//: a line names a function the profile does not carry.
		if err != nil {
			//: already typed.
			return nil, err
		}
		frames = append(frames, frame)
	}
	r.frames[id] = frames
	//: resolved once.
	return frames, nil
}

// frame resolves one line of a location.
func (r *resolver) frame(line rawLine) (FrameValue, error) {
	fn, known := r.d.functions[line.function]
	//: a function id that points nowhere.
	if !known {
		//: the id is not repeated.
		return FrameValue{}, malformed("function id")
	}
	name, err := r.str(fn.name)
	//: an unresolvable name.
	if err != nil {
		//: already typed.
		return FrameValue{}, err
	}
	file, err := r.str(fn.file)
	//: the frame, or the malformation.
	return FrameValue{Function: name, File: file, Line: int(line.line), StartLine: int(fn.start)}, err
}

// labels resolves a sample's labels: a string value into Labels, a number
// into NumLabels.
func (r *resolver) labels(raw []rawLabel) (map[string][]string, map[string][]int64, error) {
	//: most heap and goroutine samples carry none: no maps at all.
	if len(raw) == 0 {
		//: nil maps read as empty.
		return nil, nil, nil
	}
	labels, numbers := make(map[string][]string, len(raw)), make(map[string][]int64, len(raw))
	//: each label, in the order the profile lists them.
	for _, l := range raw {
		key, err := r.str(l.key)
		//: an unresolvable key.
		if err != nil {
			//: already typed.
			return nil, nil, err
		}
		//: index 0 is the empty string: the label is a number.
		if l.str == 0 {
			numbers[key] = append(numbers[key], l.num)
			//: next label.
			continue
		}
		value, err := r.str(l.str)
		//: an unresolvable value.
		if err != nil {
			//: already typed.
			return nil, nil, err
		}
		labels[key] = append(labels[key], value)
	}
	//: both maps, possibly one of them empty.
	return labels, numbers, nil
}
