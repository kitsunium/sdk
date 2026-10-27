package net

// LoopJobValue is one job a lane sends its worker through LoopOutbound (R3):
// the connection it answers, by its full reference, the slot the sender
// keeps it in with that slot's own generation, and an integer the sender
// gives meaning to. It is a value with no pointer, so the queue that carries
// it holds nothing the garbage collector must scan, and a completion that
// comes back after its connection closed finds a stale reference, never
// another connection.
type LoopJobValue struct {
	// Ref is the connection the job answers.
	Ref LoopConnRef
	// Slot is the sender's index of the job's state.
	Slot uint32
	// Gen is the generation of that slot when the job was sent.
	Gen uint32
	// Arg is the sender's own argument.
	Arg uint64
}

// LoopOutboundValue is what a loop group gives its offloader at Start, once,
// after LoopStarter.OnStart and before any other callback: one outbound per
// lane, indexed by LoopIO.Loop, and the output ceiling of one connection
// (LoopValue.OutputCap, its default applied), which bounds what an offloader
// holds back for a connection while one of its answers is pending.
type LoopOutboundValue struct {
	// Lanes are the lanes' outbounds, by lane.
	Lanes []LoopOutbound
	// OutputCap is LoopValue.OutputCap, its default applied.
	OutputCap int
}
