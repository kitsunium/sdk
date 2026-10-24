// Package http1 is the restricted HTTP/1.1 profile (ADR 0186 §D6): a codec
// that frames requests out of bytes and appends responses to bytes, doing no
// I/O, and HandleLoop, the adapter that serves it on a loop group. Its request
// value and handler ports are internal/core/net's (HTTP1RequestValue,
// HTTP1Handler, HTTP1Batcher); pkg/v1/net/http1 publishes it (ADR 0186 §D9
// step 12).
//
// The profile is defined by what it refuses. A request is answered with its
// Refusal's status and Connection: close, and the connection closed, when it
// carries a bare CR or LF, a NUL or another control byte in a value, obs-fold,
// whitespace before a colon, a name outside tchar, any Transfer-Encoding, a
// Content-Length that is not one run of at most ten digits or that repeats, a
// second Authorization or Host, an Expect field, CONNECT, or a head, target or
// body past its bound. None of those reaches a handler, and a handler cannot
// answer one.
//
// Framing depends on neither the method nor the route: a request takes its
// head plus its Content-Length, which Parse computes, so a body that spells a
// request is never read as one, and an early answer knows what it must drain
// before the next pipelined request.
//
//	var p http1.Parser                    // the profile's default limits
//	var req corenet.HTTP1RequestValue     // reused: Fields is an array
//	n, r := p.Parse(buf, &req)
//	switch {
//	case r != http1.RefusalNone:
//		out = http1.AppendRefusal(out, r) // then close
//	case n == 0:
//		// more bytes; req.Consumed is set once the head is whole
//	default:
//		h.ServeHTTP1(&req, &res)
//		out, err = http1.AppendResponse(out, &req, &res, false)
//		buf = buf[n:]
//	}
//
// Nothing here allocates once the caller's buffers have room: Parse slices the
// bytes it was given, and the writer appends precomputed status lines and an
// integer formatted in place.
package http1
