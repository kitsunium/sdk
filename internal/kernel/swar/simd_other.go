//go:build !(goexperiment.simd && amd64)

package swar

// The SWAR forms are the only forms of this build: the vector forms are
// compiled with GOEXPERIMENT=simd on amd64 (simd_amd64.go), and the
// dispatchers below are the calls those forms replace.

// hasVector reports whether this build carries a vector form that the CPU
// runs: never, here.
const hasVector = false

// indexByte2 is IndexByte2's body: the word loop for a short slice, two
// scans of bytes.IndexByte for a long one.
func indexByte2(b []byte, c1, c2 byte) int {
	//: a long slice takes the standard library's assembly.
	if len(b) >= twoScanMin {
		//: two scans.
		return twoScanIndexByte2(b, c1, c2)
	}
	//: the word loop.
	return swarIndexByte2(b, c1, c2)
}

// indexLess is IndexLess's body.
func indexLess(b []byte, n byte) int {
	return swarIndexLess(b, n)
}

// indexSet is Set.Index's body.
func indexSet(b []byte, s Set) int {
	return swarIndexSet(b, s)
}
