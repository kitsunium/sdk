// Package queue — the durable broker: one directory, three subdirectories,
// and rename(2) as the only thing that changes a fact.
package queue

import (
	"cmp"
	"context"
	"io/fs"
	"os"
	"path"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	corevfs "github.com/kitsunium/sdk/internal/core/vfs"
	svcvfs "github.com/kitsunium/sdk/internal/service/vfs"
)

// fileBroker is a queue whose entire state is a directory.
//
// # What survives, and what that costs
//
// Nothing about the queue's contents lives in this process. Which messages
// are waiting, which are leased, when each lease lapses and how many times
// each has been delivered are all encoded in directory ENTRIES (see
// file_name.go). A consumer that is SIGKILLed therefore loses nothing but its
// own lease, and any other consumer — in any process on the machine — reads
// the same facts from the same names.
//
// The price is that every call is syscalls rather than a map lookup, and that
// [fileBroker.Publish] pays two device flushes because the guarantee it makes
// is that a crash one instruction later does not lose the message. Both are
// measured in BENCH.md, and the ratio against [NewMemory] is the actionable
// number: it is large, it is supposed to be, and it is the cost of the column
// of ADR 0053's frontier table that this domain lives in.
//
// # No lock, deliberately
//
// Two consumers reaching for the same queued message both attempt the same
// rename; the kernel gives it to one and gives the other ENOENT, which the
// loser reads as "somebody else took it" and moves on. That is the whole
// exclusion mechanism. It needs no lock file, it cannot be held by a dead
// process, and — unlike flock(2), which ADR 0052 MEASURED giving zero
// exclusion between goroutines sharing one open file description — it works
// identically for two goroutines and for two processes.
type fileBroker struct {
	// root confines every name this broker touches to the queue directory.
	// It is the only long-lived handle in the package, and it holds no
	// knowledge of the queue's contents.
	root *os.Root
	// publisher is internal/service/vfs, used for the two writes that must
	// be atomic AND durable. See NewFile's comment for why it is not used
	// for the state transitions.
	publisher corevfs.AtomicWriter
	clk       clock.Clock
	policy    corequeue.PolicyValue
}

// NewFile returns a broker whose messages survive the process that published
// them.
//
// # Why internal/service/vfs, and where it stops
//
// The durable publication of a message is exactly what corevfs.AtomicWriter
// promises: a temporary in the same directory, the payload, an fsync of the
// FILE, a rename, an fsync of the DIRECTORY — five steps whose failure paths
// ADR 0056 tested by injecting a failure at each one and comparing the
// destination's hash. Rewriting that here would be eighty lines of subtle
// code duplicating something the SDK merged, measured and proved, and the
// first thing to rot would be the failure paths, which are the only part that
// matters.
//
// It stops at corevfs.WritableFS, which has WriteFile, MkdirAll, Remove and
// RemoveAll and NO RENAME. That is not an oversight in vfs and it is not
// worked around here: the port is FROZEN (ADR 0039), so adding a fifth method
// to carry this domain's state machine would break every downstream
// implementation at compile time. The state transitions therefore go through
// os.Root directly, which confines them to the queue directory by the same
// mechanism vfs itself uses, and the boundary between the two is exactly the
// boundary between "publish a whole file" and "move one".
//
// It refuses, at construction rather than at first use: a policy it cannot
// honour ([corequeue.QueueMisconfigured]), a directory it cannot use safely
// ([QueueDirectoryUnusable]), and a platform with no atomic replace or no
// flushable directory handle — core/proc.UnsupportedPlatform, raised by
// internal/service/vfs.NewOS and passed through unmodified (ADR 0018).
func NewFile(cfg FileConfig) (broker corequeue.Broker, err error) {
	//: the SAME guard the memory broker runs, which is what makes that one an
	//: honest double for this one.
	if invalid := cfg.Policy.Validate(); invalid != nil {
		//: QueueMisconfigured, naming the field.
		return nil, invalid
	}
	//: the directory and its three states, checked before anything is opened.
	if dirErr := prepareQueueDir(cfg.Dir); dirErr != nil {
		//: QueueDirectoryUnusable or QueueBackendFailed.
		return nil, dirErr
	}
	root, rootErr := os.OpenRoot(cfg.Dir)
	//: a root that cannot be opened will fail every call it is ever given.
	if rootErr != nil {
		//: QueueBackendFailed, with the cause still in the chain.
		return nil, backendFailed("openroot", cfg.Dir, rootErr)
	}
	publisher, vfsErr := svcvfs.NewOS(cfg.Dir)
	//: ADR 0018 — no atomic replace, no durable directory entry, no broker.
	if vfsErr != nil {
		//: the descriptor is released rather than leaked along with the
		//: failure; the vfs verdict wins, a close failure is only reported
		//: when there was nothing better to report.
		return nil, cmp.Or(vfsErr, root.Close())
	}
	//: usable.
	return &fileBroker{
		root: root, publisher: publisher,
		policy: cfg.Policy.Normalized(), clk: clockOrSystem(cfg.Clock),
	}, nil
}

// Publish writes payload into the queue and flushes it.
//
// When it returns nil the message is on the device: a power cut one
// instruction later does not lose it. That flush is most of what the call
// costs, and BENCH.md says how much.
func (b *fileBroker) Publish(
	ctx context.Context, payload []byte,
) (message corequeue.MessageValue, err error) {
	//: a cancelled producer gets its own error rather than a durable message
	//: nobody asked for any more.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.MessageValue{}, ctx.Err()
	}
	//: the bound is enforced at the producer, where the payload can still be
	//: made smaller — and before a single byte reaches the device.
	if tooLarge := checkSize(len(payload), b.policy.MaxMessageBytes); tooLarge != nil {
		//: MessageTooLarge, carrying the two sizes and never the bytes.
		return corequeue.MessageValue{}, tooLarge
	}
	entropy, entropyErr := randomHex(idEntropyBytes)
	//: two processes publishing in the same nanosecond have no shared counter
	//: to break the tie with, so the tie is broken by randomness.
	if entropyErr != nil {
		//: QueueBackendFailed — nothing was written.
		return corequeue.MessageValue{}, backendFailed("rand", "", entropyErr)
	}
	now := b.clk.Now()
	name := nameValue{At: now.UnixNano(), EnqueuedAt: now.UnixNano(), Entropy: entropy}
	//: THE durable step: temporary beside the target, write, fsync the file,
	//: rename, fsync the directory. Five steps, one call, tested in ADR 0056.
	if writeErr := b.publisher.WriteAtomic(
		path.Join(dirReady, name.readyName()), payload, messageMode,
	); writeErr != nil {
		//: QueueBackendFailed — vfs guarantees nothing was published.
		return corequeue.MessageValue{}, backendFailed("publish", dirReady, writeErr)
	}
	//: accepted, and on the device. The returned Payload is the CALLER'S
	//: slice: they already hold those bytes, and a copy would serve no reader.
	return corequeue.MessageValue{ID: name.ID(), Payload: payload, EnqueuedAt: now}, nil
}

// Ack unlinks the leased message. It is the only call that removes one.
func (b *fileBroker) Ack(ctx context.Context, receipt corequeue.ReceiptValue) error {
	held := string(receipt)
	//: an unknown receipt and a lapsed one are different bugs.
	if _, resolveErr := b.resolve(ctx, held); resolveErr != nil {
		//: UnknownReceipt or LeaseExpired.
		return resolveErr
	}
	//: no fsync. A crash before the unlink reaches the device resurrects the
	//: message, which is a REDELIVERY — something the at-least-once guarantee
	//: already permits and every consumer is already obliged to survive.
	//: Paying a device flush to make a duplicate slightly less likely would
	//: buy nothing the contract does not already give away.
	if removeErr := b.root.Remove(path.Join(dirInflight, held)); removeErr != nil {
		//: a lease that lapsed between resolve and here is the ordinary race,
		//: and it is reported as what it is.
		return b.classifyMissing("remove", held, removeErr)
	}
	//: gone.
	return nil
}

// resolve reads a receipt, checks the lease is still held, and returns the
// facts its name carries.
//
// The two refusals are distinguished by SHAPE rather than by provenance,
// which is the opposite of the memory broker's rule and for a stated reason:
// this queue is inter-process, so a receipt minted by another process is
// perfectly legitimate here and a broker-instance nonce would refuse the
// domain's whole point. What is refused is a string that is not a name this
// package writes — and since the grammar admits only digits and lower-case
// hex, no receipt can name a file outside the in-flight directory.
func (b *fileBroker) resolve(ctx context.Context, receipt string) (name nameValue, err error) {
	//: a cancelled caller must not be told its work is safely acknowledged.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return nameValue{}, ctx.Err()
	}
	parsed, ok := parseInflight(receipt)
	//: not a receipt this package could have written.
	if !ok {
		//: UnknownReceipt.
		return nameValue{}, kerrs.Wrap(corequeue.UnknownReceipt, kerrs.WrapParams{},
			kerrs.String("broker", "file"))
	}
	//: the DEADLINE is checked BEFORE the file, and that ordering is the
	//: decision: a lapsed lease whose file nobody has reclaimed yet would
	//: otherwise be acknowledged or refused depending on which consumer last
	//: happened to poll, so "is this still mine?" would have two answers for
	//: one state. Checked against the clock it has one.
	if parsed.At <= b.clk.Now().UnixNano() {
		//: LeaseExpired.
		return nameValue{}, kerrs.Wrap(corequeue.LeaseExpired, kerrs.WrapParams{},
			kerrs.String("broker", "file"))
	}
	//: the file is gone: the lease lapsed earlier and somebody reclaimed it.
	if _, statErr := b.root.Stat(path.Join(dirInflight, receipt)); statErr != nil {
		//: LeaseExpired, or a genuine medium failure.
		return nameValue{}, b.classifyMissing("stat", receipt, statErr)
	}
	//: still leased.
	return parsed, nil
}

// classifyMissing turns an absent in-flight file into the domain's verdict and
// anything else into a backend failure.
func (b *fileBroker) classifyMissing(op, name string, cause error) error {
	//: absence is the ordinary outcome of losing a race, not a medium fault.
	if os.IsNotExist(cause) {
		//: LeaseExpired — the message is queued again or dead-lettered, and
		//: another consumer may already hold it.
		return kerrs.Wrap(corequeue.LeaseExpired, kerrs.WrapParams{}, kerrs.String("broker", "file"))
	}
	//: the medium refused for a reason of its own.
	return backendFailed(op, name, cause)
}

// backendFailed wraps an operating-system refusal, keeping the cause in the
// chain so a caller may still ask errors.Is(err, fs.ErrNotExist).
//
// It names the operation and the PATH and never the payload: an error that
// quoted the bytes would put a caller's data into every log that renders it.
func backendFailed(op, name string, cause error) error {
	//: the cause survives; the verdict is added on top.
	return kerrs.Wrap(cause, kerrs.WrapParams{
		Code: CodeQueueBackendFailed, Reason: "QUEUE_BACKEND_FAILED",
		Public:   "The queue's storage refused an operation",
		Private:  "service/queue: a filesystem call failed; the fields name the operation and the path",
		ExitCode: exitIOErr,
	}, kerrs.String("op", op), kerrs.String("path", name))
}

// entriesOf lists one state directory, sorted by name — which, because of the
// zero-padded instant that opens every name, is sorted by TIME.
//
// It reads the whole directory. That is O(entries) per call and it is the
// broker's known scaling limit, measured in BENCH.md rather than asserted:
// this is a durable queue for a backlog of hundreds to thousands, and a
// caller with millions in flight wants a broker over a system built for that,
// behind this same port.
func (b *fileBroker) entriesOf(state string) (entries []fs.DirEntry, err error) {
	read, readErr := fs.ReadDir(b.root.FS(), state)
	//: a state directory that vanished under a live broker is a real fault.
	if readErr != nil {
		//: QueueBackendFailed.
		return nil, backendFailed("readdir", state, readErr)
	}
	//: fs.ReadDir sorts, so this is chronological.
	return read, nil
}
