// Package queue — waking an idle consumer: the broadcast both brokers close on
// a publication, and the table that lets two durable brokers over one
// directory in one process wake each other's consumers.
package queue

import (
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"weak"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// wakeSignal is a broadcast: every consumer waiting on the channel wakes when
// it is closed, and a fresh channel is armed for the next wait. A channel
// closed and replaced, rather than one value sent, is what makes it a
// broadcast — a send wakes one receiver and is lost if nobody is receiving.
//
// For the durable broker it also keeps the earliest instant a message the
// broker last saw becomes receivable on its own, because that broker's state
// is a directory and the only moment it can learn the instant is while a
// Receive is already reading the names.
type wakeSignal struct {
	// ch is the channel handed out until the next fire.
	ch chan struct{}
	// due is the earliest known instant, in Unix nanoseconds, at which a
	// message becomes receivable with no further event; zero when none.
	due int64
	mu  sync.Mutex
}

// newWakeSignal returns a signal with its first channel armed.
func newWakeSignal() *wakeSignal {
	//: never nil, so a consumer can select on it without a nil check.
	return &wakeSignal{ch: make(chan struct{})}
}

// current returns the channel the next fire will close.
func (w *wakeSignal) current() (signal <-chan struct{}, due int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	//: both under one lock, so they describe the same moment.
	return w.ch, w.due
}

// fire wakes every consumer waiting on the current channel and arms the next.
func (w *wakeSignal) fire() {
	w.mu.Lock()
	defer w.mu.Unlock()
	//: the channel every current waiter holds is swapped out under the lock,
	//: so exactly one fire ever closes it.
	fired := w.ch
	w.ch = make(chan struct{})
	//: close is the broadcast; the replacement is what the next wait gets.
	close(fired)
}

// record replaces the known due instant with what a scan just read. The
// LATEST scan wins rather than the earliest instant, because a scan reads the
// whole state: an instant it did not see has since been acted on.
func (w *wakeSignal) record(due int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.due = due
}

// wakeValue builds what [corequeue.Waker.Wake] returns from a channel and a
// due instant read on the broker's clock at now.
func wakeValue(signal <-chan struct{}, due, now time.Time) corequeue.WakeValue {
	//: nothing is scheduled: the consumer waits for an event or its poll.
	if due.IsZero() {
		//: an event, or the poll, and nothing else.
		return corequeue.WakeValue{Signal: signal}
	}
	//: relative to the broker's own reading, so a consumer on another clock
	//: still waits the right length of time.
	return corequeue.WakeValue{Signal: signal, In: due.Sub(now), Scheduled: true}
}

// earliest returns the smallest non-zero instant among its arguments, in Unix
// nanoseconds, or zero when every one is zero.
func earliest(instants ...int64) int64 {
	var first int64
	//: zero means "none" and never wins.
	for _, instant := range instants {
		//: a real instant earlier than the best so far.
		if instant != 0 && (first == 0 || instant < first) {
			first = instant
		}
	}
	//: zero when nothing was scheduled.
	return first
}

// directoryWakes holds, per queue directory, the signal every durable broker
// over that directory in this process shares — weakly, so a directory whose
// brokers have all been collected does not keep an entry forever.
//
// Two brokers over one directory are ONE queue (FileConfig.Dir says so), so a
// publication through either must wake the consumers of both. Keying the
// signal by broker instance would have made that true only when the producer
// and the consumer happened to hold the same value.
type directoryWakes struct {
	byDir map[string]weak.Pointer[wakeSignal]
	mu    sync.Mutex
}

// fileWakes is the process's table. It is process-wide by necessity: the
// point is to connect brokers that know nothing of each other.
var fileWakes = &directoryWakes{byDir: make(map[string]weak.Pointer[wakeSignal])}

// forDir returns the signal shared by every broker over dir, creating it.
//
// The key is the directory resolved through its symbolic links, so /tmp/q and
// /private/tmp/q — one directory on macOS — share a signal. A spelling this
// cannot reconcile only costs a wake: the consumer finds the message at its
// next poll, exactly as a consumer in another process does.
func (d *directoryWakes) forDir(dir string) *wakeSignal {
	key := canonicalDir(dir)
	d.mu.Lock()
	defer d.mu.Unlock()
	//: a live signal is shared, which is the whole point.
	if held, found := d.byDir[key]; found {
		//: still referenced by some broker in this process.
		if signal := held.Value(); signal != nil {
			//: the same queue, the same wake.
			return signal
		}
	}
	signal := newWakeSignal()
	d.byDir[key] = weak.Make(signal)
	//: the entry goes when the last broker holding the signal is collected —
	//: unless a newer signal has taken the key meanwhile.
	runtime.AddCleanup(signal, d.drop, key)
	//: the first broker over this directory in this process.
	return signal
}

// drop removes key's entry once its signal has been collected.
func (d *directoryWakes) drop(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	//: a key re-armed by a newer broker keeps its live signal.
	if held, found := d.byDir[key]; found && held.Value() == nil {
		delete(d.byDir, key)
	}
}

// canonicalDir resolves dir to one spelling: absolute, cleaned, and through
// its symbolic links where they resolve.
func canonicalDir(dir string) string {
	resolved, absErr := filepath.Abs(dir)
	//: an unresolvable path keys by its own spelling.
	if absErr != nil {
		//: the caller's spelling, which still works for the caller's brokers.
		return filepath.Clean(dir)
	}
	//: NewFile has already created the directory, so this normally succeeds.
	if linked, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
		//: the one spelling every alias reaches.
		return linked
	}
	//: absolute and clean.
	return resolved
}
