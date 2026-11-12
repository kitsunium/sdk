package multi

import (
	"io"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// Machine is the state machine of a node whose commands a Packer packs:
// it splits each entry's body into its commands and applies them in order, as
// one batch, through Inner. The result of an entry is a []any holding one
// result per command, in the order the Packer packed them, which the Packer
// hands back to each caller; a body that does not parse is answered
// FRAME_INVALID, the same on every node, and Inner sees none of it.
//
// Every command proposed to a node running a Machine must come through a
// Packer: a body proposed whole does not parse. A sub-entry is a copy of its
// entry whose Data is the command and whose BodySum is the command's own
// CRC-32C; ChainSum is zero. Inner may keep a sub-entry's Data: it is a slice
// of the entry's payload, capped at the command's end. Apply and ApplyBatch
// run on the apply goroutine, one call at a time, as the engine calls them; a
// Machine is not shared between nodes. It forwards no sibling port of
// Inner's: a node running it keeps its state in memory.
type Machine struct {
	// Inner applies the commands.
	Inner coreconsensus.BatchingStateMachine

	// subs is the scratch of split commands, reused from call to call.
	subs []coreconsensus.EntryValue
	// ends holds, per entry of a batch, the end of its commands in subs, or
	// -1 for a body that did not parse.
	ends []int
	// bad holds the FRAME_INVALID of each entry ends marks -1.
	bad []error
}

// NewMachine returns the Machine that applies, through inner, the
// commands a Packer packed.
func NewMachine(inner coreconsensus.BatchingStateMachine) *Machine {
	return &Machine{Inner: inner}
}

// Apply splits e's body and applies its commands through Inner in one
// batch; the result is their results, in order, or FRAME_INVALID.
func (m *Machine) Apply(e coreconsensus.EntryValue) any {
	results := make([]any, 1)
	m.ApplyBatch([]coreconsensus.EntryValue{e}, results)
	return results[0]
}

// ApplyBatch splits every entry of es and applies all their commands through
// ONE call of Inner.ApplyBatch, then writes into results each entry's
// sub-results, a []any of one result per command, or FRAME_INVALID.
func (m *Machine) ApplyBatch(es []coreconsensus.EntryValue, results []any) {
	m.subs, m.ends, m.bad = m.subs[:0], m.ends[:0], m.bad[:0]
	for i := range es {
		var err error
		m.subs, err = splitBody(m.subs, &es[i], wire.BodySum)
		if err != nil {
			m.ends = append(m.ends, -1)
			m.bad = append(m.bad, err)
			continue
		}
		m.ends = append(m.ends, len(m.subs))
	}
	flat := make([]any, len(m.subs))
	if len(m.subs) > 0 {
		m.Inner.ApplyBatch(m.subs, flat)
	}
	start, bad := 0, 0
	for i, end := range m.ends {
		if end < 0 {
			results[i] = m.bad[bad]
			bad++
			continue
		}
		results[i] = flat[start:end:end]
		start = end
	}
	// The scratch keeps its room, not the payloads it pointed into.
	clear(m.subs)
	clear(m.bad)
}

// Snapshot captures Inner's state.
func (m *Machine) Snapshot() (coreconsensus.Snapshot, error) { return m.Inner.Snapshot() }

// Restore replaces Inner's state with a snapshot's.
func (m *Machine) Restore(r io.Reader, meta coreconsensus.SnapshotMetaValue) error {
	return m.Inner.Restore(r, meta)
}
