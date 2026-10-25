package net

// EngineValue names the engine family a group runs on, passed by Engine (ADR
// 0187): the goroutine engine or the loop engine. The zero value, EngineAuto,
// lets the handler's type choose. The backend inside the loop family is
// LoopValue.Backend's.
type EngineValue uint8

// The engine families a group may name with Engine. The zero value is EngineAuto.
const (
	// EngineAuto lets the group's handler type choose its engine (ADR 0187): a loop
	// handler runs on the loop engine, every other handler on the goroutine engine.
	EngineAuto EngineValue = iota
	// EngineGoroutine is the default engine: one goroutine per connection on the
	// runtime's netpoller. It serves Handle, HandleFunc and HandleHTTP.
	EngineGoroutine
	// EngineLoop is the loop engine of ADR 0186. It serves HandleLoop; Loop picks its
	// backend.
	EngineLoop
)

// String renders the engine as an error field spells it.
func (e EngineValue) String() string {
	//: a dense switch, so a constant added without its name falls to default
	//: and the test that walks the block names it.
	switch e {
	//: the handler's type decides.
	case EngineAuto:
		//: the spelling an error field uses.
		return "auto"
	//: one goroutine per connection.
	case EngineGoroutine:
		//: the spelling an error field uses.
		return "goroutine"
	//: the loop engine.
	case EngineLoop:
		//: the spelling an error field uses.
		return "loop"
	//: a value past the block is named, never hidden.
	default:
		//: what EngineUnsupported reports for it.
		return "unknown"
	}
}
