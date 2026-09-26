// Package profiling — hosts the values a decoded profile is made of: the
// profile, its samples, their stacks' frames and the sample types.
package profiling

import "time"

// ProfileValue is a decoded pprof profile: what was measured, and every
// sample with its stack, its values and its labels. It is plain data — the
// profile.proto message with its tables resolved — and safe to share once
// built.
type ProfileValue struct {
	// Time is when the collection started; zero when the profile does not
	// say.
	Time time.Time
	// SampleTypes names what each of a sample's values measures, in order:
	// {cpu nanoseconds}, {inuse_space bytes}.
	SampleTypes []SampleTypeValue
	// Samples are the measured stacks.
	Samples []SampleValue
	// Comments are the profile's free-text comments.
	Comments []string
	// PeriodType and Period say how often a sample is taken: every Period
	// PeriodType — 10 000 000 nanoseconds of CPU, 524 288 bytes allocated.
	PeriodType SampleTypeValue
	// DefaultSampleType is the sample type a viewer shows first; empty when
	// the profile does not say, and the last sample type is the convention.
	DefaultSampleType string
	// Duration is how long the collection lasted; zero when not recorded.
	Duration time.Duration
	// Period is the sampling period, in PeriodType's unit.
	Period int64
}

// SampleTypeValue names what a value measures and its unit — {cpu
// nanoseconds}, {inuse_space bytes}.
type SampleTypeValue struct {
	// Type is what is measured: "cpu", "samples", "inuse_space".
	Type string
	// Unit is its unit: "nanoseconds", "count", "bytes".
	Unit string
}

// SampleValue is one sample: a stack, a value per sample type, and the labels
// the goroutine carried when it was taken.
type SampleValue struct {
	// Labels are the pprof labels of the goroutine, by key: set with
	// pprof.Do or pprof.SetGoroutineLabels. A CPU profile carries them; a
	// heap profile does not.
	Labels map[string][]string
	// NumLabels are the numeric labels, by key — a heap sample's "bytes",
	// the size of each object it counts.
	NumLabels map[string][]int64
	// Stack is the sample's call stack, innermost frame first. A call the
	// compiler inlined is a frame of its own, before the frame it was
	// inlined into.
	Stack []FrameValue
	// Values holds one value per ProfileValue.SampleTypes entry.
	Values []int64
}

// FrameValue is one frame of a stack: a function, where it is defined, and
// the line executing in it.
type FrameValue struct {
	// Function is the function's name as the runtime spells it —
	// "pkg.(*T).Method", "pkg.F.func1", "pkg.G[...]" — or empty for a frame
	// no symbol was found for.
	Function string
	// File is the function's source file, as recorded when it was compiled.
	File string
	// Line is the line executing in this frame; zero when unknown.
	Line int
	// StartLine is the line the function starts at; zero when unknown, and
	// always zero in a goroutine dump, which does not print it.
	StartLine int
	// Address is the program counter of an unsymbolized frame; zero
	// otherwise.
	Address uint64
}

// sampleType returns the index of the sample type named typ, or -1.
func (p *ProfileValue) sampleType(typ string) int {
	//: the first sample type by that name.
	for i, st := range p.SampleTypes {
		//: an exact name match.
		if st.Type == typ {
			//: its index in every sample's Values.
			return i
		}
	}
	//: the profile does not measure it.
	return -1
}

// defaultType returns the name of the sample type a fold uses when it is not
// told: the profile's own default, else the last sample type — the pprof
// tool's convention, which picks cpu for a CPU profile and inuse_space for a
// heap profile.
func (p *ProfileValue) defaultType() string {
	//: the profile says.
	if p.DefaultSampleType != "" {
		//: honoured.
		return p.DefaultSampleType
	}
	//: a profile with no sample type has nothing to fold.
	if len(p.SampleTypes) == 0 {
		//: the fold refuses it.
		return ""
	}
	//: the convention.
	return p.SampleTypes[len(p.SampleTypes)-1].Type
}
