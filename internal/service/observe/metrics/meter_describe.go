package metrics

import coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"

// Describe binds description to the instrument called name, satisfying
// core/observe/metrics.Describer.
//
// It is a WIRING-TIME call, not an observation. Nothing on the fetch path reads
// the description, the map it lives in is nil until this method is called for
// the first time, and a meter nobody describes therefore pays exactly one extra
// map-header word in its struct and nothing else. The hot path is untouched by
// design — see BENCH.md.
//
// The name is NOT bound to an instrument kind here, which is the one asymmetry
// with ObservableCounter's registration. A description is non-identifying in the
// OTel data model, so there is no kind it could imply, and binding one would
// force a caller to describe an instrument only AFTER minting it — an ordering
// rule with no reason behind it. Describing a name that never becomes an
// instrument is therefore legal and simply never reaches a snapshot; a
// description with no metric has nowhere to be wrong.
//
// # Two refusals, both panics
//
// Describe has nowhere to put an error — its signature returns nothing, exactly
// as Counter(name) returns a Counter — and both mistakes below are programming
// ones: a description is a literal at a wiring site, constant for the process,
// so this fails on the first boot or never. That is the same argument bindName
// makes for InstrumentKindConflict, and the same class of failure.
//
//   - An EMPTY description panics with InvalidDescription. Accepting it would
//     be a call that does nothing, which is the inert outcome ADR 0031 bans.
//   - A DIFFERENT description for a name that already has one panics with
//     DescriptionConflict. Identical text is idempotent: two packages
//     documenting one metric the same way have not disagreed.
//
// The OpenTelemetry SDK specification resolves this conflict differently — it
// says to keep the first description and log a warning. This SDK refuses
// instead, for two reasons it states rather than inherits: core/observe/metrics has no
// logger to warn through (and acquiring one would invert the layer order), and
// "first wins, quietly" is precisely the outcome where the losing wiring site
// stays wrong forever while its author reads a dashboard that looks fine.
func (m *memMeter) Describe(name, description string) {
	//: a description that documents nothing is not a description.
	if description == "" {
		//: panic so the empty literal is caught where it was written.
		panic(coremetrics.InvalidDescription.Error())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, described := m.descriptions[name]
	//: re-describing with identical text is an idempotent no-op.
	if described {
		//: two wiring sites that agree have not conflicted.
		if existing == description {
			//: nothing to change.
			return
		}
		//: two descriptions for one name means one call site is wrong.
		panic(coremetrics.DescriptionConflict.Error())
	}
	//: the map is created on first use, so an undescribed meter allocates
	//: nothing for a feature it does not use.
	if m.descriptions == nil {
		//: first description of this meter's life.
		m.descriptions = make(map[string]string, 1)
	}
	//: bind the name's documentation for every snapshot from now on.
	m.descriptions[name] = description
}

// annotations are the per-name metadata a snapshot carries beside the points:
// the description (ADR 0067) and the unit (ADR 0208). Both maps may be nil.
type annotations struct {
	// descriptions maps a name to its docstring.
	descriptions map[string]string
	// units maps a name to its UCUM unit.
	units map[string]string
}

// annotations returns the meter's two metadata maps. The caller holds mu.
func (m *memMeter) annotations() annotations {
	//: the maps themselves: Collect reads them under the same lock.
	return annotations{descriptions: m.descriptions, units: m.units}
}

// Unit binds unit to the instrument called name, satisfying
// core/observe/metrics.UnitDescriber (ADR 0208). It is a WIRING-TIME call, as
// Describe is: nothing on the fetch path reads it, and the map is nil until
// the first Unit.
//
// Two refusals, both panics, for the reasons Describe states: an EMPTY unit,
// one past coremetrics.MaxUnitLen bytes or holding a byte outside printable ASCII panics with
// InvalidUnit — UCUM's grammar is printable ASCII and case-sensitive, and a
// unit outside it is a typo at the wiring site; a DIFFERENT unit for a name
// that has one panics with UnitConflict. The same unit again is idempotent.
func (m *memMeter) Unit(name, unit string) {
	//: a unit outside UCUM's alphabet is a wiring mistake, caught where written.
	if !validUnit(unit) {
		panic(coremetrics.InvalidUnit.Error())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, bound := m.units[name]
	//: re-binding the same unit is an idempotent no-op.
	if bound {
		//: two wiring sites that agree have not conflicted.
		if existing == unit {
			return
		}
		//: two units for one name means one call site is wrong.
		panic(coremetrics.UnitConflict.Error())
	}
	//: the map is created on first use.
	if m.units == nil {
		m.units = make(map[string]string, 1)
	}
	m.units[name] = unit
}

// validUnit reports whether unit is 1 to MaxUnitLen bytes of printable ASCII
// (0x21 to 0x7E — UCUM has no space).
func validUnit(unit string) bool {
	//: empty or over-long.
	if unit == "" || len(unit) > coremetrics.MaxUnitLen {
		return false
	}
	//: every byte in UCUM's printable alphabet.
	for i := range len(unit) {
		//: a space, a control byte or non-ASCII.
		if unit[i] <= ' ' || unit[i] > '~' {
			return false
		}
	}
	return true
}
