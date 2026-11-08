//go:build !unix && !windows

package exec

// sameVariable compares two environment variable names byte for byte, as the
// remaining targets (plan9, js/wasm) do.
func sameVariable(left, right string) bool {
	//: byte equality.
	return left == right
}

// variableKey is a name's spelling two names sameVariable takes for one share:
// here, the name itself.
func variableKey(name string) string {
	//: byte equality needs no folding.
	return name
}
