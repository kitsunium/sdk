// Package http1 is the restricted HTTP/1.1 profile the SDK serves on its loop
// engine (ADR 0186 §D6): a codec that frames requests out of bytes and appends
// responses to bytes, and HandleLoop, the adapter that serves a Handler on a
// loop group of package server.
//
// It is a profile, not an HTTP server, and what it refuses defines it. A
// request is answered with its status and Connection: close, and its
// connection closed, before any handler sees it, when it carries any
// Transfer-Encoding (chunked, and so trailers, included), Expect, a bare CR or
// LF, a NUL or another control byte in a value, obs-fold, whitespace before a
// colon, a name outside tchar, a Content-Length that is not one run of at most
// ten digits or that repeats, a second Authorization or Host, CONNECT, or a
// head, target or body past its Limits. HTTP/2, Upgrade and TLS are out of the
// profile: a program that needs them mounts net/http with Group.HandleHTTP.
// Framing depends on neither the method nor the route: a request takes its
// head plus its Content-Length.
//
// # Serving
//
//	type api struct{ loops []perLoop } // one per loop, indexed by Request.Loop
//
//	func (a *api) ServeHTTP1(req *http1.Request, res *http1.Response) {
//		l := &a.loops[req.Loop]
//		// route, authenticate with l's own scratch, ...
//		res.Defer = true // answered at the end of the batch
//		l.pending = append(l.pending, job{res: res})
//	}
//
//	func (a *api) EndBatch(loop int) {
//		l := &a.loops[loop]
//		// one transaction for every job of l.pending, then for each:
//		// job.res.Status, job.res.Body = ..., ...; job.res.Defer = false
//	}
//
//	srv.Group("api", server.Listen("tcp", ":8080"), server.Loop(server.LoopValue{Loops: 2})).
//		HandleLoop(http1.HandleLoop(a, http1.Parser{}))
//
// A handler runs on its loop's thread and must not block it. Every callback
// of one loop runs on that thread, one at a time, so state kept per loop needs
// no lock; Request.Loop and the loop argument of EndBatch name the loop. The
// answers of a batch leave at its end, in request order on each connection.
//
// # The codec alone
//
// Parser.Parse, AppendResponse and AppendRefusal frame and write without the
// loop, on any []byte: nothing allocates once the caller's buffers have room.
package http1
