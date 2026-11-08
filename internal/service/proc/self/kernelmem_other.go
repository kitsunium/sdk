//go:build !unix && !windows

package self

// kernelMemory has nothing to read where the SDK binds no kernel accounting.
func kernelMemory(_ *StatsValue) {
	//: every field stays zero and KernelMemory false.
}
