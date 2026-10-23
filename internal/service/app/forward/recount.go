package forward

import "context"

// The counts against the queue (issue #580). The forwarder counts what it
// publishes and what it settles, by identifier; another process changes the
// queue behind it — a dead letter replayed into it, a batch another consumer
// took or another producer queued, a batch the broker dead-lettered on its
// own. Run lists the queue again when asked, on its own goroutine and never
// on Enqueue's, and the counts become what the listing holds: a batch listed
// that the forwarder does not count is counted, a batch counted that is not
// listed is no longer. Three cases are the forwarder's word and not the
// listing's, since a message published, acknowledged or dead-lettered while
// the queue is listed may be listed or not (MessageReader's contract): a
// batch Enqueue kept during the listing stays counted; a batch the consumer
// settled stays uncounted while the broker still lists it leased, since the
// acknowledgement comes after the settle; and one listed again unleased —
// replayed, or handed back by a lapsed lease — is counted again.

// listedBatch is one live message as a count lists it.
type listedBatch struct {
	// size is its payload's length.
	size int64
	// leased reports that a consumer holds it.
	leased bool
}

// list reads every live message of the queue, queued or leased, by
// identifier.
func (f *Forwarder) list(ctx context.Context) (map[string]listedBatch, error) {
	listed := make(map[string]listedBatch)
	after := ""
	//: page by page, in the broker's identifier order.
	for {
		page, err := f.reader.Messages(ctx, after, countPage)
		//: the medium failed.
		if err != nil {
			//: surfaced as the broker said it.
			return nil, err
		}
		//: one message, one batch.
		for _, live := range page {
			listed[live.Message.ID] = listedBatch{size: int64(len(live.Message.Payload)), leased: live.Leased}
			after = live.Message.ID
		}
		//: a short page is the last.
		if len(page) < countPage {
			//: every one listed.
			return listed, nil
		}
	}
}

// recountLoop counts the queue each time it is asked, until ctx ends; a
// forwarder whose broker cannot list its messages is never asked.
func (f *Forwarder) recountLoop(ctx context.Context) {
	//: one count per request.
	for {
		//: a request, or the end.
		select {
		//: Run is over.
		case <-ctx.Done():
			//: nothing left to count for.
			return
		//: asked.
		case <-f.recount:
			f.recountOnce(ctx)
		}
	}
}

// recountOnce lists the queue and makes the counts what it holds. A listing
// that fails leaves the counts as they were, until the next request.
func (f *Forwarder) recountOnce(ctx context.Context) {
	f.mu.Lock()
	f.counted = f.cfg.Clock.Now()
	f.generation++
	generation := f.generation
	f.recorded = make(map[string]struct{})
	f.mu.Unlock()
	listed, err := f.list(ctx)
	f.publish.Lock()
	defer f.publish.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	//: the queue as it stands, but for what moved while it was listed.
	if err == nil {
		f.forgetGone(listed, generation)
		f.countListed(listed)
		f.uncountGone(listed)
	}
	f.recorded = nil
}

// forgetGone forgets, with mu held, the settled batches the broker no longer
// holds as the consumer left them: gone from a listing that began after they
// were settled, or listed unleased — back in the queue.
func (f *Forwarder) forgetGone(listed map[string]listedBatch, generation uint64) {
	//: each settled batch.
	for id, settledIn := range f.leaving {
		batch, present := listed[id]
		//: still leased by the consumer that settled it, or settled during the listing.
		if present && batch.leased || !present && settledIn >= generation {
			continue
		}
		delete(f.leaving, id)
	}
}

// countListed counts, with mu held, every listed batch the forwarder does not
// count and did not settle.
func (f *Forwarder) countListed(listed map[string]listedBatch) {
	//: the batches another process put in.
	for id, batch := range listed {
		_, held := f.held[id]
		_, settled := f.leaving[id]
		//: counted already, or settled and not yet acknowledged.
		if held || settled {
			continue
		}
		f.hold(id, batch.size)
	}
}

// uncountGone stops counting, with mu held, every counted batch the listing
// does not hold but one Enqueue kept while the queue was listed.
func (f *Forwarder) uncountGone(listed map[string]listedBatch) {
	//: the batches another process took out.
	for id := range f.held {
		_, present := listed[id]
		_, recorded := f.recorded[id]
		//: still there, or kept after the listing passed it.
		if present || recorded {
			continue
		}
		f.release(id)
	}
}

// dueRecount asks Run to count the queue again once Config.RecountInterval
// has passed since the last count, with mu held.
func (f *Forwarder) dueRecount() {
	//: due.
	if f.reader != nil && f.cfg.Clock.Now().Sub(f.counted) >= f.cfg.RecountInterval {
		f.askRecount()
	}
}

// askRecount asks Run to count the queue again, without ever blocking; a
// request already pending is the same request.
func (f *Forwarder) askRecount() {
	//: no listing, nothing to count against.
	if f.reader == nil {
		//: the counts are this process's alone.
		return
	}
	//: one request at most waits.
	select {
	//: asked.
	case f.recount <- struct{}{}:
	//: already asked.
	default:
	}
}
