package queue

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// suffixOnce ends a reservation's name: the key's digest, then this.
const suffixOnce string = ".key"

// suffixGone ends the name a passed reservation is renamed to before it is
// removed: the key's digest, a random half, then this.
const suffixGone string = ".gone"

// suffixNew ends the name a reservation is written under before it is linked
// to its own: the key's digest, a random half, then this.
const suffixNew string = ".new"

// onceTakeovers bounds how many times one PublishOnce takes a passed
// reservation over before it reads the key as held: each round lost is
// another publication that reserved the key meanwhile.
const onceTakeovers int = 2

// onceSweepEvery is how often one broker sweeps the reservations whose window
// has passed. Each sweep reads the whole directory, so it is not paid on
// every PublishOnce; between two, a passed reservation costs a file and
// nothing else, since every read of one checks its end.
const onceSweepEvery time.Duration = time.Minute

// PublishOnce publishes payload unless key is still reserved, and reserves it
// for window (core/data/queue.OncePublisher).
//
// # A reservation is a file, created exclusively
//
// The key's reservation is once/<sha256 of the key>.key, holding the end of
// its window as nanoWidth digits. It is written under a name of its own,
// created with O_EXCL, and linked to the key's: link(2) refuses an existing
// name as O_EXCL does, so two producers reserving one key — in one process or
// in two — race on that one link and the kernel gives it to one of them, and
// the reservation is never seen without its end. A passed reservation is
// renamed aside before it is removed, so a producer taking it over never
// removes the one another producer has just made.
//
// # The message first, then the reservation
//
// A crash must cost at most a duplicate, never a lost message (the port's
// contract). So the message is written durably FIRST, then the key reserved.
// A crash between the two leaves a message and no reservation: a retry
// publishes it again, which at-least-once already permits. The other order
// would leave a reservation with no message, and every retry inside the
// window would be told the message went out.
//
// The common duplicate — a retry long after the first publication — is read
// off the reservation before anything is written, and costs no write. Only a
// race inside the same instant writes a message and then withdraws it, by
// unlinking its queued name before anybody is woken; a consumer that leased
// it in that instant makes it a publication after all, and the call says so.
//
// The reservation is not flushed: a power cut may forget it, which is a
// duplicate, and a reservation whose end cannot be read back is read as passed.
func (b *fileBroker) PublishOnce(
	ctx context.Context, key string, payload []byte, window time.Duration,
) (message corequeue.MessageValue, published bool, err error) {
	//: a cancelled producer.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.MessageValue{}, false, ctx.Err()
	}
	//: the bound Publish enforces, before a byte reaches the device.
	if tooLarge := checkSize(len(payload), b.policy.MaxMessageBytes); tooLarge != nil {
		//: MessageTooLarge, carrying the two sizes and never the bytes.
		return corequeue.MessageValue{}, false, tooLarge
	}
	now := b.clk.Now()
	until, invalid := onceUntil(now, key, window)
	//: a key or a window no broker honours.
	if invalid != nil {
		//: QueueMisconfigured, and nothing is written.
		return corequeue.MessageValue{}, false, invalid
	}
	//: the passed reservations of every key, at most once a minute.
	if sweepErr := b.sweepOnce(now); sweepErr != nil {
		//: QueueBackendFailed — nothing was written.
		return corequeue.MessageValue{}, false, sweepErr
	}
	digest := onceDigest(key)
	held, readErr := b.reservedUntil(digest)
	//: the directory could not answer.
	if readErr != nil {
		//: QueueBackendFailed — nothing was written.
		return corequeue.MessageValue{}, false, readErr
	}
	//: an earlier publication's window still holds the key: nothing to write.
	if held > now.UnixNano() {
		//: a duplicate, and that is the answer.
		return corequeue.MessageValue{}, false, nil
	}
	name, writeErr := b.write(payload, now, now)
	//: nothing was published.
	if writeErr != nil {
		//: QueueBackendFailed.
		return corequeue.MessageValue{}, false, writeErr
	}
	message = corequeue.MessageValue{ID: name.ID(), Payload: payload, EnqueuedAt: now}
	reserved, reserveErr := b.reserve(digest, until.UnixNano(), now.UnixNano())
	//: reserved: the message stands, and wakes the queue.
	if reserveErr == nil && reserved {
		b.wake.fire()
		//: published, and the key held until the window ends.
		return message, true, nil
	}
	//: another publication reserved the key meanwhile, or the reservation
	//: could not be made: the message is withdrawn before anybody is woken.
	return b.withdraw(message, name, reserveErr)
}

// withdraw unlinks the queued message PublishOnce wrote but could not reserve
// a key for, and reports the call's outcome: a duplicate when the key was
// taken, the reservation's failure when it failed — and a publication after
// all when a consumer leased the message before it could be withdrawn.
func (b *fileBroker) withdraw(
	message corequeue.MessageValue, name nameValue, reserveErr error,
) (corequeue.MessageValue, bool, error) {
	removeErr := b.root.Remove(path.Join(dirReady, name.readyName()))
	//: a consumer leased it in the instant between: it went out.
	if os.IsNotExist(removeErr) {
		b.wake.fire()
		//: a publication, the reservation another's — a duplicate the window
		//: could not stop, which at-least-once permits.
		return message, true, nil
	}
	//: the medium refused the unlink: the message stays queued, and the call
	//: fails as a Publish whose storage failed — a retry may duplicate it,
	//: which at-least-once permits, and nothing is lost.
	if removeErr != nil {
		b.wake.fire()
		//: QueueBackendFailed.
		return corequeue.MessageValue{}, false, backendFailed("remove", dirReady, removeErr)
	}
	//: the reservation itself failed, and nothing was published.
	if reserveErr != nil {
		//: QueueBackendFailed.
		return corequeue.MessageValue{}, false, reserveErr
	}
	//: a duplicate: another publication holds the key.
	return corequeue.MessageValue{}, false, nil
}

// reserve creates the key's reservation, ending at until, and reports whether
// this call made it. An existing reservation whose window has passed by now
// is taken over; one still running means another publication holds the key.
func (b *fileBroker) reserve(digest string, until, now int64) (bool, error) {
	//: a bounded number of rounds: each one lost is somebody else's win.
	for range onceTakeovers {
		created, createErr := b.createReservation(digest, until)
		//: the medium refused the create.
		if createErr != nil {
			//: QueueBackendFailed.
			return false, createErr
		}
		//: the key is this call's.
		if created {
			//: reserved.
			return true, nil
		}
		held, readErr := b.reservedUntil(digest)
		//: the medium refused the read.
		if readErr != nil {
			//: QueueBackendFailed.
			return false, readErr
		}
		//: still running: another publication holds the key.
		if held > now {
			//: a duplicate.
			return false, nil
		}
		//: passed, or gone meanwhile: set aside, and the create tried again.
		if dropErr := b.dropPassed(digest, now); dropErr != nil {
			//: QueueBackendFailed.
			return false, dropErr
		}
	}
	//: every round lost to another producer of the same key.
	return false, nil
}

// createReservation creates once/<digest>.key holding the end of its window,
// and reports false, and no failure, when the name exists.
//
// The end is written first, into a name of its own created with O_EXCL, and
// that file is then linked to the reservation's name — link(2) refuses an
// existing name as O_EXCL does, and the name appears with its content. A
// reservation created and written in two steps would be read empty between
// them, as passed, by a producer racing this one, which would then take it
// over and publish too.
func (b *fileBroker) createReservation(digest string, until int64) (bool, error) {
	entropy, entropyErr := randomHex(leaseEntropyBytes)
	//: the written file's name needs a random half no other producer picks.
	if entropyErr != nil {
		//: QueueBackendFailed.
		return false, backendFailed("rand", "", entropyErr)
	}
	written := path.Join(dirOnce, digest+"."+entropy+suffixNew)
	file, openErr := b.root.OpenFile(written, os.O_WRONLY|os.O_CREATE|os.O_EXCL, messageMode)
	//: the medium refused.
	if openErr != nil {
		//: QueueBackendFailed.
		return false, backendFailed("create", dirOnce, openErr)
	}
	_, writeErr := file.WriteString(pad(until))
	//: written and closed, or neither: the file is this call's either way.
	if closeErr := file.Close(); writeErr != nil || closeErr != nil {
		//: QueueBackendFailed, the write's failure first, the file removed.
		return false, errors.Join(backendFailed("write", dirOnce, errors.Join(writeErr, closeErr)),
			ignoreMissing("remove", dirOnce, b.root.Remove(written)))
	}
	linkErr := b.root.Link(written, path.Join(dirOnce, digest+suffixOnce))
	removeErr := ignoreMissing("remove", dirOnce, b.root.Remove(written))
	//: somebody holds the name, whatever its window.
	if errors.Is(linkErr, fs.ErrExist) {
		//: not this call's; the written file is gone either way.
		return false, removeErr
	}
	//: the medium refused the link.
	if linkErr != nil {
		//: QueueBackendFailed.
		return false, errors.Join(backendFailed("link", dirOnce, linkErr), removeErr)
	}
	//: reserved, whatever became of the written name.
	return true, nil
}

// reservedUntil reads the end of the key's reservation, in Unix nanoseconds:
// 0 when there is none, and when the file holds no end it can read — a
// reservation a crash cut short, which is read as passed.
func (b *fileBroker) reservedUntil(digest string) (int64, error) {
	content, readErr := b.root.ReadFile(path.Join(dirOnce, digest+suffixOnce))
	//: no reservation.
	if errors.Is(readErr, fs.ErrNotExist) {
		//: nothing holds the key.
		return 0, nil
	}
	//: the medium refused.
	if readErr != nil {
		//: QueueBackendFailed.
		return 0, backendFailed("read", dirOnce, readErr)
	}
	until, ok := parseNano(string(content))
	//: half-written: passed.
	if !ok {
		//: nothing holds the key.
		return 0, nil
	}
	//: the window's end.
	return until, nil
}

// dropPassed removes the key's reservation when its window has passed by now.
//
// It renames the reservation aside first and reads what it renamed, so a
// reservation another producer made since this one read the passed one is put
// back rather than removed: removing it would let a third publication through
// a window that is still running.
func (b *fileBroker) dropPassed(digest string, now int64) error {
	entropy, entropyErr := randomHex(leaseEntropyBytes)
	//: the aside name needs a random half no other producer picks.
	if entropyErr != nil {
		//: QueueBackendFailed.
		return backendFailed("rand", "", entropyErr)
	}
	reservation := path.Join(dirOnce, digest+suffixOnce)
	aside := path.Join(dirOnce, digest+"."+entropy+suffixGone)
	renameErr := b.root.Rename(reservation, aside)
	//: gone already: somebody else dropped it.
	if errors.Is(renameErr, fs.ErrNotExist) {
		//: nothing to drop.
		return nil
	}
	//: the medium refused.
	if renameErr != nil {
		//: QueueBackendFailed.
		return backendFailed("rename", dirOnce, renameErr)
	}
	//: a running reservation, set aside by mistake, goes back where it was —
	//: unless yet another has been made there since, which then holds the key.
	if b.asideStillRuns(aside, now) {
		if linkErr := b.root.Link(aside, reservation); linkErr != nil && !errors.Is(linkErr, fs.ErrExist) {
			//: QueueBackendFailed.
			return backendFailed("link", dirOnce, linkErr)
		}
	}
	//: the aside name is never read again.
	return ignoreMissing("remove", dirOnce, b.root.Remove(aside))
}

// asideStillRuns reports whether the reservation renamed aside to name ends
// after now.
func (b *fileBroker) asideStillRuns(name string, now int64) bool {
	content, readErr := b.root.ReadFile(name)
	//: unreadable is read as passed, as a reservation is.
	if readErr != nil {
		//: passed.
		return false
	}
	until, ok := parseNano(string(content))
	//: still running.
	return ok && until > now
}

// sweepOnce removes every reservation whose window has passed by now, and
// every one set aside and left behind by a crash — at most once per
// onceSweepEvery for this broker, measured on its clock.
func (b *fileBroker) sweepOnce(now time.Time) error {
	last := b.onceSwept.Load()
	recent := last != 0 && now.UnixNano() >= last && now.UnixNano()-last < int64(onceSweepEvery)
	//: swept recently enough — a clock that went back sweeps at once — or
	//: another of this broker's producers is the one sweeping.
	if recent || !b.onceSwept.CompareAndSwap(last, now.UnixNano()) {
		//: nothing to do.
		return nil
	}
	entries, listErr := b.entriesOf(dirOnce)
	//: the directory could not be read.
	if listErr != nil {
		//: QueueBackendFailed.
		return listErr
	}
	//: each reservation, and each one left set aside.
	for _, entry := range entries {
		if sweepErr := b.sweepEntry(entry.Name(), now.UnixNano()); sweepErr != nil {
			//: QueueBackendFailed.
			return sweepErr
		}
	}
	//: swept.
	return nil
}

// sweepEntry removes one entry of the reservations' directory when it no
// longer holds a key: a reservation whose window has passed, or a passed one
// a crash left set aside. Anything else — a running reservation, one another
// producer is putting back, a stranger — is left alone.
func (b *fileBroker) sweepEntry(base string, now int64) error {
	//: a reservation: dropped, through the rename that never removes a
	//: running one.
	if digest, isReservation := strings.CutSuffix(base, suffixOnce); isReservation {
		held, readErr := b.reservedUntil(digest)
		//: the medium refused the read.
		if readErr != nil {
			//: QueueBackendFailed.
			return readErr
		}
		//: still running.
		if held > now {
			//: kept.
			return nil
		}
		//: passed.
		return b.dropPassed(digest, now)
	}
	name := path.Join(dirOnce, base)
	//: set aside and left there, and passed: nothing will put it back. Or
	//: written and never linked — a crash between the two — and passed; one
	//: whose end cannot be read yet may be another producer's, still writing.
	if (strings.HasSuffix(base, suffixGone) && !b.asideStillRuns(name, now)) ||
		(strings.HasSuffix(base, suffixNew) && b.writtenAndPassed(name, now)) {
		//: removed; gone already is as good.
		return ignoreMissing("remove", dirOnce, b.root.Remove(name))
	}
	//: not ours to remove.
	return nil
}

// writtenAndPassed reports whether name holds the end of a window, and that
// end is not after now.
func (b *fileBroker) writtenAndPassed(name string, now int64) bool {
	content, readErr := b.root.ReadFile(name)
	//: unreadable, or gone: not known to be passed.
	if readErr != nil {
		//: left alone.
		return false
	}
	until, ok := parseNano(string(content))
	//: a whole end, behind now.
	return ok && until <= now
}
