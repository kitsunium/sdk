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
