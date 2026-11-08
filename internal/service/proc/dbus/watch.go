package dbus

import (
	"context"

	coredbus "github.com/kitsunium/sdk/internal/core/proc/dbus"
)

const (
	// maxBacklog is how many owners a watch keeps for a reader that falls
	// behind; past it the newest are coalesced into the last, never the
	// oldest.
	maxBacklog int = 64
)

// watch is one watch of a name: the owners not yet delivered, oldest first,
// and whether its lookup was read — an announcement read before it is older
// than it. Its queue and ready are guarded by the connection's mu.
type watch struct {
	name  string
	queue []string
	wake  chan struct{}
	ready bool
}

// WatchName returns a channel holding name's owner when the watch began —
// its unique name, empty for none —, then the owner after each change the
// bus announces, in order. It is closed when ctx ends, the match removed,
// or when the connection is. A name that is not well-known is NAME_INVALID.
//
// Goroutine lifecycle: one per watch, delivering its owners; ended by ctx
// or by the connection's end, and joined by Close.
func (c *Conn) WatchName(ctx context.Context, name string) (<-chan string, error) {
	//: a well-known name.
	if err := checkName(name); err != nil {
		//: NAME_INVALID.
		return nil, err
	}
	w := &watch{name: name, wake: make(chan struct{}, 1)}
	//: registered before the bus is asked: no announcement is missed.
	if !c.register(w) {
		//: CONN_CLOSED.
		return nil, coredbus.ConnClosed
	}
	rule := matchRule(name)
	//: the bus announces the name's changes from now on.
	if _, err := c.call(ctx, &callValue{member: "AddMatch", sig: "s", strs: []string{rule}}); err != nil {
		c.unwatch(w)
		//: refused, cancelled, or the connection is gone.
		return nil, err
	}
	m, err := c.roundTrip(ctx, &callValue{member: "GetNameOwner", sig: "s", strs: []string{name}}, func(m *message) { c.looked(w, m) })
	//: the lookup answered.
	if err == nil {
		_, err = ownerOf(m)
	}
	//: no owner looked up.
	if err != nil {
		c.unwatch(w)
		c.removeMatch(rule)
		//: refused, cancelled, or the connection is gone.
		return nil, err
	}
	out := make(chan string)
	//: started unless the connection ended meanwhile.
	if !c.track() {
		close(out)
		//: closed with the connection.
		return out, nil
	}
	//: Goroutine lifecycle: see above.
	go c.deliver(ctx, w, out, rule)
	//: the owner first.
	return out, nil
}

// register adds w to the name's watches, unless the connection is closed.
func (c *Conn) register(w *watch) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: closed.
	if c.closed {
		//: refused.
		return false
	}
	c.watches[w.name] = append(c.watches[w.name], w)
	//: registered.
	return true
}

// track counts one more goroutine for Close to join, unless the connection is
// closed — under mu, so it happens before Close waits.
func (c *Conn) track() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: closed.
	if c.closed {
		//: not started.
		return false
	}
	c.wg.Add(1)
	//: counted.
	return true
}

// unwatch removes w from the name's watches.
func (c *Conn) unwatch(w *watch) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := c.watches[w.name][:0]
	//: every other watch of the name.
	for _, o := range c.watches[w.name] {
		//: another.
		if o != w {
			kept = append(kept, o)
		}
	}
	//: the connection ended: nothing to remove from.
	if c.watches == nil {
		return
	}
	//: none left.
	if len(kept) == 0 {
		delete(c.watches, w.name)
		return
	}
	c.watches[w.name] = kept
}

// looked takes the lookup's reply in the reader, before the next message is
// read: the owner first, every announcement read from now on after it.
func (c *Conn) looked(w *watch, m *message) {
	owner, err := ownerOf(m)
	//: no owner looked up: the watch fails.
	if err != nil {
		return
	}
	c.mu.Lock()
	w.ready = true
	w.queue = append(w.queue[:0], owner)
	c.mu.Unlock()
	w.nudge()
}

// deliver hands w's owners to out, in order, until ctx or the connection ends.
func (c *Conn) deliver(ctx context.Context, w *watch, out chan<- string, rule string) {
	defer c.wg.Done()
	defer close(out)
	//: until the watch or the connection ends.
	for {
		next, has := c.head(w)
		var send chan<- string
		//: an owner to hand.
		if has {
			send = out
		}
		//: handed, woken, or ended.
		select {
		//: taken by the reader.
		case send <- next:
			c.pop(w)
		//: an owner announced.
		case _, open := <-w.wake:
			//: wake is never closed: were it, the watch would end.
			if !open {
				c.unwatch(w)
				return
			}
		//: the watcher is done: the bus stops announcing.
		case <-ctx.Done():
			c.unwatch(w)
			c.removeMatch(rule)
			return
		//: the connection ended.
		case <-c.done:
			return
		}
	}
}

// head is w's oldest owner not delivered.
func (c *Conn) head(w *watch) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: none.
	if len(w.queue) == 0 {
		//: nothing to hand.
		return "", false
	}
	//: the oldest.
	return w.queue[0], true
}

// pop drops w's oldest owner, delivered.
func (c *Conn) pop(w *watch) {
	c.mu.Lock()
	w.queue = w.queue[1:]
	c.mu.Unlock()
}

// push queues an owner announced, under the connection's mu: past
// maxBacklog the newest is coalesced into the last, the oldest kept.
func (w *watch) push(owner string) {
	//: a reader far behind.
	if len(w.queue) >= maxBacklog {
		w.queue[len(w.queue)-1] = owner
	} else {
		w.queue = append(w.queue, owner)
	}
	w.nudge()
}

// nudge wakes the watch's goroutine, once.
func (w *watch) nudge() {
	//: woken, or awake already.
	select {
	//: woken.
	case w.wake <- struct{}{}:
	//: a wake-up pending.
	default:
	}
}

// removeMatch asks the bus to stop announcing a watch's name, without
// waiting for its answer.
func (c *Conn) removeMatch(rule string) {
	cv := &callValue{member: "RemoveMatch", sig: "s", strs: []string{rule}, flags: flagNoReplyExpected}
	gone(c.send(context.Background(), c.nextSerial(), cv))
}

// matchRule is the rule that has the bus announce name's changes of owner.
func matchRule(name string) string {
	//: the bus driver's NameOwnerChanged for this name.
	return "type='signal',sender='" + busDriver + "',path='" + busPath + "',interface='" + busInterface +
		"',member='NameOwnerChanged',arg0='" + name + "'"
}
