//go:build !unix && !windows

// Package static — the lookup failures a platform with no kernel names of its
// own reports for a name.
package static

// platformNameRefused reports nothing beyond the portable refusals: a platform
// that is neither Unix nor Windows reports a bad name through fs.ErrNotExist or
// fs.ErrInvalid, which nameRefused already reads.
func platformNameRefused(error) bool {
	//: the portable refusals are the whole list here.
	return false
}
