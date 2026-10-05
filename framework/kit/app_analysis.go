package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// AnalyzeFunc reads the product's source — the Go module rooted at dir, and
// the packages that declare the modules it mounts — into a graph the runtime
// merges into its own: the edges found in handler bodies, each function's
// steps. The framework links no analyzer (ADR 0147 §1): the platform's kit
// tool gives one, in dev, through [Analyzer].
type AnalyzeFunc = ikit.AnalyzeFunc

// analyzer is Analyzer's body: decl_gen.go writes Analyzer, from the
// design, as one call of it.
func analyzer(fn AnalyzeFunc) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Analyzer(fn)
}
