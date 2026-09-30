// A node's code level, C4's fourth: its functions, their steps under the
// blocks that guard them, and their effects.

package core

// The kinds of [CodeBlockMessage].
const (
	BlockIf     = "if"      // an if's first branch
	BlockElseIf = "else-if" // an if's else-if branch
	BlockElse   = "else"    // an if's last branch
	BlockCase   = "case"    // a switch's or a select's case
	BlockLoop   = "loop"    // a for or range loop's body
	BlockGo     = "go"      // a go statement: runs beside the caller
	BlockDefer  = "defer"   // a defer statement: runs when the function returns
	BlockFunc   = "func"    // a function literal: runs when it is called
	// BlockTransaction is the function a kit.Transact runs: its steps run in
	// one transaction, and the effects they make leave at its commit.
	BlockTransaction = "transaction"
)

// CodeResult is the code a node runs, as the static analysis read it: the
// fourth and innermost level of the C4 model. The entry is the function the
// node runs — an endpoint's handler, a subscription's, a loop's — and Funcs
// are the functions of the module it reaches, each with what it calls and
// what it does to other nodes.
type CodeResult struct {
	// Entry is the qualified name of the function the node runs first.
	Entry string `json:"entry,omitempty"`
	// Funcs are the module's functions reachable from the entry, entry
	// first, then in the order the walk met them.
	Funcs []CodeFuncMessage `json:"funcs,omitempty"`
	// Uses are the SDK functions the code calls: the mechanics it composes
	// rather than implements.
	Uses []CodeUseMessage `json:"uses,omitempty"`
	// Truncated is set when the walk stopped at its depth or size bound.
	Truncated bool `json:"truncated,omitempty"`
}

// CodeFuncMessage is one function of the module a node's code reaches.
// Its steps are listed in the order they run, under the blocks that guard
// them.
type CodeFuncMessage struct {
	// Func is the qualified name: "github.com/kitsunium/todo/tasks.Create".
	Func string `json:"func"`
	// Name is the short name a developer reads: "Create", "(*Store).visible".
	Name string `json:"name"`
	// Source is the function's range.
	Source *SourceMessage `json:"source,omitempty"`
	// Calls are the qualified names of the module functions it calls, in
	// source order, deduplicated.
	Calls []string `json:"calls,omitempty"`
	// Effects are its calls on building blocks, in source order.
	Effects []CodeEffectMessage `json:"effects,omitempty"`
	// Steps are what it does, in the order it does it: its calls to module
	// functions, on building blocks and into the SDK, arguments before the
	// call they are passed to. A sequence diagram is drawn from them.
	Steps []CodeStepMessage `json:"steps,omitempty"`
	// Blocks are its blocks that decide whether, or how many times, the
	// steps under them run: an if's branches, a switch's cases, a loop.
	Blocks []CodeBlockMessage `json:"blocks,omitempty"`
}

// CodeStepMessage is one thing a function does: a call to another function of the
// module, a call on a building block, or a call into the SDK — under the
// innermost block that guards it.
type CodeStepMessage struct {
	// Line is the 1-based line of the call, in the function's file.
	Line int `json:"line"`
	// Call is the qualified name of the module function it calls.
	Call string `json:"call,omitempty"`
	// Effect is its call on a building block: the index in Effects, plus one.
	Effect int `json:"effect,omitempty"`
	// Use is the SDK function it calls, from the SDK's version directory:
	// "crypto/password.Hash".
	Use string `json:"use,omitempty"`
	// Block is the innermost block guarding it: the index in Blocks, plus
	// one; 0 for the function's body.
	Block int `json:"block,omitempty"`
}

// CodeBlockMessage is a block of a function that decides whether, or how many
// times, the steps under it run.
type CodeBlockMessage struct {
	// Kind is one of the Block constants.
	Kind string `json:"kind"`
	// Label is the condition or the clause, as written: "err != nil",
	// `case "done"`, "range ids"; bounded.
	Label string `json:"label,omitempty"`
	// Line is the 1-based line where the block starts.
	Line int `json:"line"`
	// Group is the line of the statement the block is a branch of: an
	// if/else chain, a switch, a select. Blocks with one group are its
	// alternatives.
	Group int `json:"group,omitempty"`
	// Parent is the enclosing block: its index in Blocks, plus one; 0 for
	// the function's body.
	Parent int `json:"parent,omitempty"`
}

// CodeEffectMessage is one call on a building block, inside a function.
// It names the node it reaches and the edge kind that call draws.
type CodeEffectMessage struct {
	// Kind is the edge the call makes: reads, writes, publishes…
	Kind EdgeKind `json:"kind"`
	// Target is the node the call reaches.
	Target string `json:"target"`
	// Label refines Kind like an edge's label: the event of a transition.
	Label string `json:"label,omitempty"`
	// Op is the method called: "Get", "Insert", "Fire", "Send".
	Op string `json:"op"`
	// Line is the 1-based line of the call, in the function's file.
	Line int `json:"line"`
}

// CodeUseMessage is one SDK function a node's code calls.
// Sites are where the node's code calls it, so a reader sees what it leans
// on.
type CodeUseMessage struct {
	// Package is the SDK package: "github.com/kitsunium/sdk/pkg/v1/crypto/password".
	Package string `json:"package"`
	// Func is the function or method: "Hash", "(*Composer).Compose".
	Func string `json:"func"`
	// Sites are the call sites, sorted.
	Sites []SourceMessage `json:"sites,omitempty"`
}
