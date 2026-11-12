package consensus

// rwfDsyncProbeName is the file an open's probe writes and removes; a
// crash leaves a .tmp, which recovery deletes as it deletes every one.
const rwfDsyncProbeName = "rwfdsync-probe.tmp"

var (
	// syncEverywhere opens every log in SyncRWFDsync, whatever its options
	// say. Only a test binary sets it, run with CONSENSUS_SYNC_PATH=rwfdsync,
	// so the whole suite runs on writes flagged RWF_DSYNC (#923); it is read
	// by openFileLog alone.
	syncEverywhere bool

	// probeSyncPath answers why pwritev2(2) with RWF_DSYNC cannot write in
	// dir, empty when it can: rwfDsyncProbe, unless a test replaces it to
	// answer as a kernel without it does. Only an open's own goroutine reads
	// it.
	probeSyncPath = rwfDsyncProbe
)

// resolveSyncPath sets how the log makes a batch durable: the mode asked
// for, or SyncFdatasync when RWF_DSYNC is asked for and the kernel or the
// platform answers it cannot. That is never a refusal: the reason is kept
// for the one line bindWarn logs (#923). It runs before recovery writes
// anything.
func (l *fileLog) resolveSyncPath(mode SyncPath) {
	if syncEverywhere {
		mode = SyncRWFDsync
	}
	l.syncPath = SyncFdatasync
	if mode != SyncRWFDsync {
		return
	}
	if why := probeSyncPath(l.logDir()); why != "" {
		l.syncWhy = why
		return
	}
	l.syncPath = SyncRWFDsync
}
