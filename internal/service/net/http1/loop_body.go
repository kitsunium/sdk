package http1

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// minBodyBound is the Content-Length bound a spill with no room beside the head
// still leaves: a parser's bound of zero means the default, so the smallest
// bound it can be given is this one, which only a misconfigured SpillCap reaches.
const minBodyBound int64 = 1

// CheckSpill implements corenet.LoopSpillChecker: Start fails with
// HTTP1Misconfigured naming body for a Limits.Body the spill cannot hold beside
// the head, and a default bound is capped at what it can, so a Content-Length
// past it is refused 413 on the head. A handler that reads bodies as they
// arrive holds only heads in the spill, and is not bound by it.
func (a *LoopHandler) CheckSpill(spillCap int) error {
	//: heads only in the spill: the body is the handler's.
	if a.reader != nil {
		return nil
	}
	room := int64(spillCap) - int64(orDefault(a.p.head, corenet.HTTP1DefaultHead))
	//: an explicit bound the spill cannot hold.
	if a.p.body != 0 && a.p.body > room {
		return errs.Wrap(corenet.HTTP1Misconfigured, errs.WrapParams{},
			errs.String("option", "body"), errs.Int64("spill_room", room), errs.Int64("body", a.p.body))
	}
	//: the default bound, as far as the spill holds it.
	if a.p.body == 0 && room < corenet.HTTP1DefaultBody {
		a.p.body = max(room, minBodyBound)
	}
	return nil
}

// openBody moves a request whose body its handler reads as it arrives aside: the
// head is copied out of the buffer, which will not outlive this call, and framed
// again from the copy into the connection's own request value, so req's head
// stays valid across OnData calls. It returns the head's bytes.
func (a *LoopHandler) openBody(c dataCall, buf []byte) int {
	st, f := c.st, c.f
	headLen := st.req.HeadLen
	f.head = append(f.head[:0], buf[:headLen]...)
	//: the connection's request value, built once per slot.
	if f.request == nil {
		f.request = new(corenet.HTTP1RequestValue)
	}
	//: the same bytes, the same bounds: framed as they just were.
	a.p.Parse(f.head, f.request)
	f.request.Lane = c.loop
	f.bodyLeft = f.request.ContentLength
	return headLen
}

// feedBody gives the body's next bytes to the reader, and serves the request
// once they are its last. It reports the bytes taken and what to do next.
func (a *LoopHandler) feedBody(c dataCall, buf []byte) (int, stepKind) {
	f := c.f
	take := int(min(int64(len(buf)), f.bodyLeft))
	f.bodyLeft -= int64(take)
	last := f.bodyLeft == 0
	a.reader.OnBody(f.request, buf[:take], last)
	//: more of this body is on its way.
	if !last {
		return take, stepMore
	}
	//: complete: answered as a request framed whole would be.
	c.st.req = *f.request
	//: the answer closes the connection: nothing past it is read.
	if a.serve(c.io, c.ref, c.st, c.loop) {
		return take, stepClose
	}
	return take, stepMore
}

// abortBody tells a handler that keeps something between chunks that a closing
// connection's body will not arrive whole.
func (a *LoopHandler) abortBody(f *connFlag, ref corenet.LoopConnRef) {
	//: this connection's body, still arriving.
	if f.gen == ref.Gen && f.bodyLeft > 0 {
		f.bodyLeft = 0
		//: only a handler that asked to be told.
		if a.aborter != nil {
			a.aborter.AbortBody(f.request)
		}
	}
}
