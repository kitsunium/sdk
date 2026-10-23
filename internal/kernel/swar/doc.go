// Package swar finds the first byte of a slice that belongs to a set, a word
// at a time: SIMD within a register (SWAR), on the standard library alone. It
// is a kernel primitive — stdlib-only and domain-neutral: a slice, two bytes,
// a bound, and no header, no escape and no token in a signature.
//
// Three searches, each returning the index of the FIRST match or -1:
//
//	swar.IndexByte2(b, '\r', '\n')   // either of two bytes
//	swar.IndexLess(b, 0x20)           // a byte below a bound: a control byte
//	swar.NewSet('"', '\\', 0x20, true).Index(b)
//	                                  // a quote, a backslash, a control byte,
//	                                  // or a byte from 0x80 up
//
// There is no search for one byte: bytes.IndexByte is an assembly loop over
// thirty-two bytes at a time, and on the machine it was measured on (an Apple
// M1, BENCH.md) it beat the word loop at every length but eight bytes. Use it
// for one byte; [IndexByte2] with the byte passed twice also finds one.
//
// # How a word is searched
//
// The slice is read eight bytes at a time as a little-endian word, and a lane
// test turns the word into one flag per byte. The tests are EXACT: a flag is
// set for a byte that matches and for no other, because the sum behind each
// test cannot carry out of its lane. The classic borrowing form
// (v - 0x01..) &^ v & 0x80.. is not used: it flags a byte above a true match
// too, so only its lowest bit can be trusted, and a caller that walks the
// others reads false hits. Here every flag is a real match, and the lowest is
// the first.
//
// A comparison against a bound is exact for a bound of 128 or less. A larger
// one has no exact lane test, so [IndexLess] and a [Set] with such a bound read
// byte by byte: correct, and no faster than a loop.
//
// # What is read
//
// Nothing past the length. Every load is a bounds-checked slice of eight bytes
// within len(b), the last bytes are read one at a time, and the capacity
// beyond len(b) is never touched — whatever it holds, a "\r\n" left by an
// earlier request included. There is no unsafe in the package.
//
// # The vector form
//
// Built with GOEXPERIMENT=simd on amd64, the package also carries an AVX2 form
// of each search that reads thirty-two bytes at a time, used for a slice of
// thirty-two bytes or more on a CPU that reports AVX2 when the program starts.
// The SWAR form is in the same binary and takes every other call: a slice of
// under thirty-two bytes, any slice on a CPU without AVX2, and IndexLess with
// a bound of zero. Without AVX2 (and in a build without the experiment, which
// has only the SWAR form) [IndexByte2] hands a slice of 64 bytes or more to
// two scans of bytes.IndexByte instead. Both forms answer alike, and the suite
// holds them to the same oracle. The experiment's package, simd/archsimd, is not
// covered by the Go 1 promise: the vector form may stop compiling on a new Go
// version, and a consumer that cannot afford that builds without the
// experiment.
//
// Those scans read faster than the word loop from 64 bytes on.
//
// # Cost
//
// Nothing here allocates. BENCH.md has the figures against the byte loop and
// bytes.IndexByte, and says for which lengths each form wins.
package swar
