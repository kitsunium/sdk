//go:build !windows

// Package entitlement - the read side of the cache guard where the kernel
// already provides what the guard would buy.
package entitlement

// holdCacheForRead runs a cache READ with no exclusion at all.
//
// rename(2) is atomic: a reader either sees the whole old file or the whole
// new one, and a reader already inside the old one keeps reading it after the
// name has moved on. Nothing a reader does can make a concurrent writer's
// install fail, so a guard here would protect nobody.
//
// It would also be expensive for nothing. The file locker fsyncs its fencing
// ledger on every acquisition — 3.8 ms against 0.13 ms for the same read
// unguarded, measured on ext4/NVMe — and Verify reads the cache on its ordinary
// path, so guarding it would put two fsyncs into every licence check to buy a
// property the kernel already provides. The Windows build takes the guard
// because there the kernel does not; see cache_lock_windows.go.
func (s *Service) holdCacheForRead(fn func()) {
	//: Nothing to exclude: the rename is atomic for this reader.
	fn()
}
