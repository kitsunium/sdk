// Package kit — the static analysis a product can be given: the platform's
// analyzer, run in the background in dev (ADR 0143 §1).
package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// AnalyzeFunc reads the product's source — the Go module rooted at dir, and
// the packages that declare the modules it mounts — into a graph the runtime
// merges into its own: the edges found in handler bodies, each function's
// steps. The framework links no analyzer (ADR 0143 §1): the platform's kit
// tool gives one, in dev, through [Analyzer].
type AnalyzeFunc = ikit.AnalyzeFunc

// Analyzer runs fn in the background once the product serves, in dev and when
// KIT_ANALYZE allows it. Without one, the graph is the runtime's alone.
func Analyzer(fn AnalyzeFunc) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Analyzer(fn)
}
