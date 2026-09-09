// Package lock — the on-disk fencing ledger: the one piece of state the file
// locker keeps, and the reason its tokens survive a restart.
package lock

import (
	"bytes"
	"errors"
	"io"
	"strconv"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corelock "github.com/kitsunium/sdk/internal/core/lock"
)

// fenceBase is the radix the ledger is written in. Decimal is chosen over a
// fixed-width binary encoding so the file is readable with cat during an
// incident, which is when someone actually looks at it.
const fenceBase int = 10

// fenceBits is the width strconv parses and formats the counter at.
const fenceBits int = 64

// maxFenceBytes caps how much of the lock file is read before deciding it is
// not a counter. A decimal uint64 is at most 20 digits; anything an order of
// magnitude past that is not a ledger and is refused without being read whole.
const maxFenceBytes int = 64

// fenceFile is the narrow view of the lock file the ledger needs: read it,
// shorten it, overwrite it, force it to the medium.
//
// It is deliberately not *os.File. The ledger's whole job is four positional
// operations, and naming them here is what makes it obvious that this code
// never seeks, never appends and never reads the descriptor's offset — which
// matters because the SAME descriptor carries the flock.
type fenceFile interface {
	io.ReaderAt
	io.WriterAt
	// Truncate shortens the file, so a smaller number never leaves the tail of
	// a larger one behind.
	Truncate(size int64) error
	// Sync forces the ledger to the medium before a lease is handed out.
	Sync() error
}

// readFence returns the highest fencing token recorded in file.
//
// An EMPTY file is 0, and that is the only lenient case: a lock file is
// created empty by the first acquisition, so 0 is the honest reading of "no
// acquisition has happened yet". Any other unparseable content is REFUSED,
// never reset — see [LockFenceCorrupt] for why a restarted counter is worse
// than no counter.
func readFence(file fenceFile, path string) (fence uint64, err error) {
	var buf [maxFenceBytes]byte
	read, readErr := file.ReadAt(buf[:], 0)
	//: a short read is the normal case (the ledger is a handful of bytes), so
	//: only a read that returned nothing AND failed is a real failure.
	if read == 0 && readErr != nil && !errors.Is(readErr, io.EOF) {
		//: the medium could not answer.
		return 0, backendFailed("readFence", path, readErr)
	}
	raw := bytes.TrimSpace(buf[:read])
	//: a fresh lock file: no acquisition recorded yet.
	if len(raw) == 0 {
		//: the next token will be 1.
		return 0, nil
	}
	parsed, parseErr := strconv.ParseUint(string(raw), fenceBase, fenceBits)
	//: not a counter — refuse rather than start over. Starting over reissues
	//: numbers the protected resource has already accepted, which converts the
	//: one mechanism that survives a stalled holder into one that endorses it.
	if parseErr != nil {
		//: LOCK_FENCE_CORRUPT, naming the path and the length only — the bytes
		//: themselves are not echoed, because a lock name is caller data.
		return 0, kerrs.Wrap(LockFenceCorrupt, kerrs.WrapParams{},
			kerrs.String("path", path),
			kerrs.Int("bytes", len(raw)))
	}
	//: the recorded high-water mark.
	return parsed, nil
}

// writeFence records fence in file and forces it to the medium.
//
// The Sync is not optional. A fencing token that is issued to a caller and
// then lost to a crash is reissued to the next caller, which is precisely the
// duplicate the token exists to make impossible. The cost — one fsync per
// acquisition — is paid on a path that already blocks on a lock.
func writeFence(file fenceFile, path string, fence uint64) error {
	raw := strconv.AppendUint(make([]byte, 0, maxFenceBytes), fence, fenceBase)
	raw = append(raw, '\n')
	//: truncate first: a shorter number must not leave the tail of a longer
	//: one behind, which would parse as a different, larger token.
	if truncErr := file.Truncate(0); truncErr != nil {
		//: the medium refused.
		return backendFailed("truncateFence", path, truncErr)
	}
	//: WriteAt rather than Write: the descriptor's offset is shared with the
	//: read above and is not what addresses this write.
	if _, writeErr := file.WriteAt(raw, 0); writeErr != nil {
		//: the medium refused.
		return backendFailed("writeFence", path, writeErr)
	}
	//: durable before the lease is handed out, never after.
	if syncErr := file.Sync(); syncErr != nil {
		//: the medium could not make it durable.
		return backendFailed("syncFence", path, syncErr)
	}
	//: recorded.
	return nil
}

// backendFailed builds the LOCK_BACKEND_FAILED outcome for a medium error.
func backendFailed(op, path string, cause error) error {
	//: restate the core sentinel's fields; the code is never re-Defined here.
	return kerrs.Wrap(cause, kerrs.WrapParams{
		Code:    corelock.CodeLockBackendFailed,
		Reason:  "LOCK_BACKEND_FAILED",
		Public:  "The lock backend could not serve the operation",
		Private: "service/lock: a filesystem operation behind the file locker failed; the fields name the operation and the path",
	}, kerrs.String("operation", op), kerrs.String("path", path))
}
