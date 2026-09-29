// Package queue — the durable broker's dead-letter decisions: putting a dead
// letter back into the queue, or deleting it.
package queue

import (
	"context"
	"path"
	"strings"
)

// ReplayDeadLetter queues the dead letter id again, visible now, with its
// delivery count reset (core/queue.DeadLetterManager). It keeps its ID, its
// payload and its enqueue instant.
//
// The queued file is published first — atomically and durably, as Publish
// does — and the dead-letter record is removed after, which is the order that
// degrades correctly: a crash between the two leaves the message queued AND
// still dead, so a second replay would queue it twice — a duplicate the
// at-least-once guarantee permits — where the opposite order would lose it.
// Two replays racing each other can queue it twice for the same reason: a
// record cannot be renamed into the queue, because a queued message is its
// payload alone, so there is no rename for the loser to lose.
func (b *fileBroker) ReplayDeadLetter(ctx context.Context, id string) error {
	//: a cancelled caller must not queue a message nobody asked for any more.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return ctx.Err()
	}
	records, listErr := b.deadRecordsOf(id)
	//: the medium refused.
	if listErr != nil {
		//: QueueBackendFailed.
		return listErr
	}
	//: the first readable record is the message; the others are copies a
	//: crash left behind, and they go with it.
	for _, base := range records {
		record, ok, readErr := b.readDead(base)
		//: the medium refused mid-read.
		if readErr != nil {
			//: QueueBackendFailed.
			return readErr
		}
		//: removed meanwhile by a concurrent replay or deletion, unreadable, or
		//: a record whose header names another message than its file name
		//: does — a truncated or planted file is never queued as a message.
		if !ok || record.Message.ID != id {
			continue
		}
		name, _ := parseDead(base)
		queued := nameValue{At: b.clk.Now().UnixNano(), EnqueuedAt: name.EnqueuedAt, Entropy: name.Entropy}
		//: THE durable step, exactly as Publish takes it.
		if writeErr := b.publisher.WriteAtomic(
			path.Join(dirReady, queued.readyName()), record.Message.Payload, messageMode,
		); writeErr != nil {
			//: QueueBackendFailed — vfs guarantees nothing was published.
			return backendFailed("publish", dirReady, writeErr)
		}
		//: queued; an idle consumer over this directory looks again now.
		b.wake.fire()
		//: only now, every record of the message.
		return b.removeDead(records)
	}
	//: never dead, or already replayed or deleted.
	return deadLetterNotFound("file", id)
}

// DeleteDeadLetter removes every record of the dead letter id for good
// (core/queue.DeadLetterManager).
func (b *fileBroker) DeleteDeadLetter(ctx context.Context, id string) error {
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return ctx.Err()
	}
	records, listErr := b.deadRecordsOf(id)
	//: the medium refused.
	if listErr != nil {
		//: QueueBackendFailed.
		return listErr
	}
	//: never dead, or already replayed or deleted.
	if len(records) == 0 {
		//: DeadLetterNotFound.
		return deadLetterNotFound("file", id)
	}
	//: payload, cause and all.
	return b.removeDead(records)
}

// deadRecordsOf lists the names of every dead-letter record of the message
// id, in name order. There is normally one; a crash between a replay's two
// steps, or a message that died twice, can leave more, and they all name one
// message. An identifier that is not one this broker mints names nothing.
func (b *fileBroker) deadRecordsOf(id string) (records []string, err error) {
	enqueued, entropy, minted := parseMessageID(id)
	//: the ID grammar: a zero-padded instant, a dash, the message's entropy.
	if !minted {
		//: nothing to look for.
		return nil, nil
	}
	entries, listErr := b.entriesOf(dirDead)
	//: the medium refused.
	if listErr != nil {
		//: QueueBackendFailed.
		return nil, listErr
	}
	prefix := pad(enqueued) + "." + entropy + "."
	//: every record of that message, and nothing that merely looks like one.
	for _, entry := range entries {
		//: the name's own grammar, not only its prefix.
		if _, named := parseDead(entry.Name()); named && strings.HasPrefix(entry.Name(), prefix) {
			records = append(records, entry.Name())
		}
	}
	//: possibly none.
	return records, nil
}

// removeDead unlinks the given dead-letter records. A record already gone is
// not a failure: somebody else replayed or deleted it first, and the outcome
// is the one asked for.
func (b *fileBroker) removeDead(records []string) error {
	//: every one, stopping only if the medium refuses.
	for _, base := range records {
		//: absent is fine; anything else is the medium.
		if removeErr := ignoreMissing("remove", base, b.root.Remove(path.Join(dirDead, base))); removeErr != nil {
			//: QueueBackendFailed.
			return removeErr
		}
	}
	//: removed.
	return nil
}
