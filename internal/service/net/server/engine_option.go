package server

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// reconcileName applies the Name option to the name the group was declared
// with (ADR 0187): an empty declared name takes it, an equal one keeps it,
// and an empty Name or a different one records GroupNameConflict, which Start
// reports. The group keeps its declared name then.
func (g *StreamGroup) reconcileName() {
	//: a group given no Name keeps the name it was declared with.
	if !g.kinds.has(corenet.OptionName) {
		//: nothing to reconcile.
		return
	}
	//: a Name that names something, and contradicts nothing.
	if g.optName != "" && (g.name == "" || g.name == g.defaultName || g.name == g.optName) {
		g.name = g.optName
		//: the group is named.
		return
	}
	//: the first error is the one that explains the others.
	if g.declErr == nil {
		g.declErr = errs.Wrap(corenet.GroupNameConflict, errs.WrapParams{},
			errs.String("group", g.name), errs.String("name", g.optName))
	}
}

// engineRefusal judges, at Start, the options that constrain the group's
// engine against each other and against its handler (ADR 0187): Plaintext
// with TLS is refused with PlaintextWithTLS, and an Engine the handler's type
// or the Loop option contradicts, or a value outside the block, with
// EngineUnsupported. It never picks the other engine instead (R6).
func (g *StreamGroup) engineRefusal() error {
	//: the group cannot both refuse and serve TLS.
	if g.kinds.has(corenet.OptionPlaintext) && g.kinds.has(corenet.OptionTLS) {
		//: refused by name, whatever order the two came in.
		return errs.Wrap(corenet.PlaintextWithTLS, errs.WrapParams{}, errs.String("group", g.name))
	}
	//: no Engine, or EngineAuto: the handler's type chooses.
	if !g.kinds.has(corenet.OptionEngine) || g.engine == corenet.EngineAuto {
		//: nothing to contradict.
		return nil
	}
	loop := g.loopHandler != nil
	//: a goroutine handler with no loop option, or a loop handler: the two
	//: combinations an engine can serve.
	if (g.engine == corenet.EngineGoroutine && !loop && !g.loopSet) || (g.engine == corenet.EngineLoop && loop) {
		//: served on the engine it names.
		return nil
	}
	//: every other combination is refused, never served on the other engine.
	return errs.Wrap(corenet.EngineUnsupported, errs.WrapParams{},
		errs.String("group", g.name), errs.String("engine", g.engine.String()))
}
