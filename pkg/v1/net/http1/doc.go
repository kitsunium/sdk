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
// ten digits or that repeats, a second Authorization or Host, CONNECT, a
// target two readers could resolve apart — a dot-segment, an empty segment, a
// backslash, an encoded slash, dot, backslash or NUL, an absolute-form not for
// its Host — or a head, target or body past its Limits. HTTP/2, Upgrade and
// TLS are out of the profile: a program that needs them mounts net/http with Group.HandleHTTP.
// Framing depends on neither the method nor the route: a request takes its
// head plus its Content-Length.
//
// # Serving
//
//	type api struct{ lanes []perLane } // one per lane, indexed by Request.Lane
//
//	func (a *api) ServeHTTP1(req *http1.Request, res *http1.Response) {
//		l := &a.lanes[req.Lane]
//		switch req.Route { // set by the router link
//		case routeItem:
//			res.Defer = true // answered at the end of the batch
//			l.pending = append(l.pending, job{res: res, user: req.Auth})
//		default:
//			res.Status = 404
//		}
//	}
//
//	func (a *api) EndBatch(lane int) {
//		l := &a.lanes[lane]
//		// one transaction for every job of l.pending, then for each:
//		// job.res.Status, job.res.Body = ..., ...; job.res.Defer = false
//	}
//
//	routes, _ := http1.Routes(http1.Route{ID: routeItem, Method: "GET", Path: "/items/", Prefix: true},
//		http1.Route{ID: routeHealth, Path: "/health"})
//	bearer, _ := http1.Bearer(verifier, routeHealth) // /health needs no token
//	srv.Group("api", server.Listen("tcp", ":8080"), server.Loop(server.LoopValue{Loops: 2}), server.Plaintext()).
//		HandleLoop(http1.HandleLoop(http1.Chain(a, routes, bearer), http1.Parser{}))
//
// A handler runs on its lane's thread and must not block it. Every callback
// of one lane runs on that thread, one at a time, so state kept per lane needs
// no lock; Request.Lane and the lane argument of EndBatch name the lane. The
// answers of a batch leave at its end, in request order on each connection.
//
// # The chain
//
// A link is a Middleware, composed with Chain, the first listed outermost.
// The codec has already computed the request's canonical Path — no
// dot-segment, no empty segment, no encoded slash, dot, backslash or NUL, an
// absolute-form target reduced to its path once its authority matched Host —
// so every link routes and judges the same bytes. Routes sets
// Request.Route, Bearer verifies the token for every route its exemptions do
// not name and copies the subject and the claims into Request.Auth by value,
// Headers gives every answer its fields, and Log hands an AccessLogger a
// record of the fields it allows, never a credential. No link writes the
// bytes a request takes: the adapter frames with what Parse returned.
//
// # The codec alone
//
// Parser.Parse, AppendResponse and AppendRefusal frame and write without the
// loop, on any []byte: nothing allocates once the caller's buffers have room.
package http1
