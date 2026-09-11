// Package queue — the durable broker's configuration, and the directory
// checks it runs before anything is opened.
package queue

import (
	"io/fs"
	"os"
	"path"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// queueDirMode is the mode a queue directory is CREATED with when it is
// absent: owner-only. A directory this broker created has the mode it asked
// for, so [checkQueueDir] is about directories somebody else created.
const queueDirMode fs.FileMode = 0o700

// messageMode is the mode every message file is published with. Owner-only,
// because the payload is the caller's data and this domain has no way to know
// whether it is a cache warm-up or a password reset.
const messageMode fs.FileMode = 0o600

// worldWritable is the other-write bit.
const worldWritable fs.FileMode = 0o002

// FileConfig configures [NewFile]: which directory holds the queue, which
// clock reads its deadlines, and what delivery discipline it enforces.
//
// Its Policy's zero value is refused at construction — see
// core/queue.PolicyValue — and so is a directory whose entries any account
// could replace.
type FileConfig struct {
	// Clock reads time. It is the narrow half of kernel/clock because this
	// broker never waits: a lapsed lease is noticed by the next Receive, not
	// by a timer.
	//
	// Injecting a ManualClock makes every deadline in a SINGLE-process test
	// instant. It cannot make a cross-process test instant, and that
	// asymmetry is not an oversight: a lease deadline is a wall-clock instant
	// written into a filename precisely so that another process can read it,
	// and another process does not share this one's fake clock. The
	// consumer-death test therefore waits on the real clock, and says so.
	//
	// Nil means clock.System.
	Clock clock.Clock
	// Dir is the queue directory. It is created owner-only when absent, and
	// refused when it exists and any account outside the owner and group
	// could replace its entries.
	//
	// Two brokers over one Dir — in one process or in two — are ONE queue.
	// That is the domain's inter-process claim, and it is the whole reason
	// the directory is the configuration rather than an implementation
	// detail.
	Dir string
	// Policy is the delivery discipline. Its zero value is refused; see
	// core/queue.PolicyValue.
	Policy corequeue.PolicyValue
}

// prepareQueueDir creates the queue directory and its three states, or checks
// them when they already exist.
func prepareQueueDir(dir string) error {
	//: an empty Dir would resolve to the process's working directory, which
	//: is never what anybody meant.
	if dir == "" {
		//: QueueDirectoryUnusable, naming the field.
		return kerrs.Wrap(QueueDirectoryUnusable, kerrs.WrapParams{}, kerrs.String("field", "Dir"))
	}
	//: MkdirAll rather than Mkdir: a queue nested under a fresh state
	//: directory is the ordinary deployment shape.
	if mkErr := os.MkdirAll(dir, queueDirMode); mkErr != nil {
		//: the medium refused.
		return backendFailed("mkdir", dir, mkErr)
	}
	info, statErr := os.Stat(dir)
	//: a directory that cannot be stat'ed cannot be checked.
	if statErr != nil {
		//: the medium could not answer.
		return backendFailed("stat", dir, statErr)
	}
	//: the permission check that has an attack behind it.
	if unsafe := checkQueueDir(dir, info); unsafe != nil {
		//: QueueDirectoryUnusable.
		return unsafe
	}
	//: the three states.
	return makeStates(dir)
}

// checkQueueDir refuses a directory whose entries any account can replace.
//
// Group-writable is ACCEPTED: a queue shared between two service accounts
// through a common group is a deliberate arrangement, and refusing it would
// push callers towards a world-writable directory instead. World-writable
// WITH the sticky bit is accepted for the same reason — that is exactly what
// /tmp is, and the sticky bit is precisely the rule that only an entry's
// owner may unlink it. World-writable WITHOUT it is refused: any account
// could then unlink a queued message, which is a silent, undetectable drain,
// or plant one, which is a silent, undetectable injection.
func checkQueueDir(dir string, info fs.FileInfo) error {
	//: a regular file where a directory belongs is a configuration fault.
	if !info.IsDir() {
		//: QueueDirectoryUnusable, naming the value.
		return kerrs.Wrap(QueueDirectoryUnusable, kerrs.WrapParams{},
			kerrs.String("field", "Dir"), kerrs.String("value", dir),
			kerrs.String("why", "not-a-directory"))
	}
	mode := info.Mode()
	//: two ways to be safe, and they carry the same verdict: either no
	//: account outside the owner and group can replace an entry at all, or
	//: the sticky bit says only an entry's owner may unlink it.
	if mode&worldWritable == 0 || mode&os.ModeSticky != 0 {
		//: acceptable.
		return nil
	}
	//: world-writable without the sticky bit.
	return kerrs.Wrap(QueueDirectoryUnusable, kerrs.WrapParams{},
		kerrs.String("field", "Dir"), kerrs.String("value", dir),
		kerrs.String("why", "world-writable"))
}

// makeStates creates the three state directories.
func makeStates(dir string) error {
	//: ready, inflight, dead — the three the state machine renames between.
	for _, state := range stateDirs {
		//: owner-only, and MkdirAll so an existing one is success.
		if mkErr := os.MkdirAll(path.Join(dir, state), queueDirMode); mkErr != nil {
			//: the medium refused.
			return backendFailed("mkdir", state, mkErr)
		}
	}
	//: ready.
	return nil
}
