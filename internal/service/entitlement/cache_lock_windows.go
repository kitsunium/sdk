// Package entitlement - the read side of the cache guard where the kernel
// does not provide it, and a reader is what breaks a writer.
package entitlement

import "log"

// holdCacheForRead runs a cache READ under the same exclusion a write takes.
//
// On Windows a reader is what breaks a writer: replacing a file another handle
// holds open is refused, os.Open never asks for FILE_SHARE_DELETE, and sharing
// DELETE would not help anyway — measured on windows-latest in
// Test_windowsRenameOverAnOpenDestination. The only way a refresh lands while
// somebody is reading is for nobody to be reading, so readers queue behind the
// writer and the writer behind them.
//
// A read is never SKIPPED, which is where this parts company with
// holdCacheForWrite. Standing down from a write costs nothing, because the
// holder is performing it; standing down from a read would mean answering "no
// cached roster" while a perfectly good one sits on disk, turning a contended
// lock file into a refused licence. Unguarded is what this package did before
// the guard existed, so falling through is no worse than that — it merely
// leaves the concurrent writer's rename able to fail, which it logs.
//
// It costs a lock acquisition per read, and that is not free: the file locker
// fsyncs its fencing ledger on every acquisition, measured at 3.8 ms against
// 0.13 ms for the same read unguarded. Windows pays it because the alternative
// is a refresh that silently does not happen; the !windows build does not,
// because rename(2) over an open file is POSIX-guaranteed and the guard would
// buy a reader there precisely nothing.
func (s *Service) holdCacheForRead(fn func()) {
	ran, why := s.underCacheLock(fn)
	//: Ran — guarded, or unguarded on a machine with no guard to be had.
	if ran {
		//: Read.
		return
	}
	//: The guard refused. Read anyway rather than report an empty cache, and
	//: say what it refused with: contention and a broken backend both land
	//: here and want different answers from whoever reads the line.
	log.Printf("roster cache at %s could not be taken (%v); reading it without exclusion", s.cacheDir, why)
	fn()
}
