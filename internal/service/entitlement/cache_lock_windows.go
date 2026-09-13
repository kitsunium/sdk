package entitlement

// holdCacheForRead runs a cache READ under the same exclusion a write takes.
//
// On Windows it is the write guard, because here a reader is what breaks a
// writer: replacing a file another handle holds open is refused, os.Open never
// asks for FILE_SHARE_DELETE, and sharing DELETE would not help anyway —
// measured on windows-latest in Test_windowsRenameOverAnOpenDestination. The
// only way a refresh lands while somebody is reading is for nobody to be
// reading, so readers queue behind the writer and the writer behind them.
//
// It costs a lock acquisition per read, and that is not free: the file locker
// fsyncs its fencing ledger on every acquisition, measured at 3.8 ms against
// 0.13 ms for the same read unguarded. Windows pays it because the alternative
// is a refresh that silently does not happen; the !windows build does not,
// because rename(2) over an open file is POSIX-guaranteed and the guard would
// buy a reader there precisely nothing.
func (s *Service) holdCacheForRead(fn func()) {
	//: The same exclusion the writer takes, so the two cannot overlap.
	s.holdCache(fn)
}
