//go:build !(goexperiment.simd && amd64)

package swar

// This build has no vector form: the specs carry none, and every suite holds
// the public function and the SWAR form to the oracle alone.

// vectorByte2 is IndexByte2's vector form: none here.
func vectorByte2(_, _ byte) func([]byte) int { return nil }

// vectorLess is IndexLess's vector form: none here.
func vectorLess(byte) func([]byte) int { return nil }

// vectorSet is Set.Index's vector form: none here.
func vectorSet(Set) func([]byte) int { return nil }

// vectorMin is the shortest slice a vector form takes: no form, so no floor.
const vectorMin = 0

// droppedVectorIndexByte is the dropped one-byte search's vector form: none here.
func droppedVectorIndexByte(byte) func([]byte) int { return nil }
