// A node's code level, C4's fourth: its functions, their steps under the
// blocks that guard them, and their effects.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
)

// The kinds of [CodeBlock].
const (
	BlockIf     string = core.BlockIf     // an if's first branch
	BlockElseIf string = core.BlockElseIf // an if's else-if branch
	BlockElse   string = core.BlockElse   // an if's last branch
	BlockCase   string = core.BlockCase   // a switch's or a select's case
	BlockLoop   string = core.BlockLoop   // a for or range loop's body
	BlockGo     string = core.BlockGo     // a go statement: runs beside the caller
	BlockDefer  string = core.BlockDefer  // a defer statement: runs when the function returns
	BlockFunc   string = core.BlockFunc   // a function literal: runs when it is called
	// BlockTransaction is the function a kit.Transact runs: its steps run in
	// one transaction, and the effects they make leave at its commit.
	BlockTransaction string = core.BlockTransaction
)

type (
	// CodeInfo is the code a node runs, as the static analysis read it: the
	// fourth and innermost level of the C4 model. The entry is the function the
	// node runs — an endpoint's handler, a subscription's, a loop's — and Funcs
	// are the functions of the module it reaches, each with what it calls and
	// what it does to other nodes.
	CodeInfo = core.CodeResult
)

type (
	// CodeFunc is one function of the module a node's code reaches.
	// Its steps are listed in the order they run, under the blocks that guard
	// them.
	CodeFunc = core.CodeFuncMessage
)

type (
	// CodeStep is one thing a function does: a call to another function of the
	// module, a call on a building block, or a call into the SDK — under the
	// innermost block that guards it.
	CodeStep = core.CodeStepMessage
)

type (
	// CodeBlock is a block of a function that decides whether, or how many
	// times, the steps under it run.
	CodeBlock = core.CodeBlockMessage
)

type (
	// CodeEffect is one call on a building block, inside a function.
	// It names the node it reaches and the edge kind that call draws.
	CodeEffect = core.CodeEffectMessage
)

type (
	// CodeUse is one SDK function a node's code calls.
	// Sites are where the node's code calls it, so a reader sees what it leans
	// on.
	CodeUse = core.CodeUseMessage
)
