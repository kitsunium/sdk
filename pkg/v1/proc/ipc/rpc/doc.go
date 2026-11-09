// Package rpc is a call between two processes of one machine over the private
// socket of proc/ipc (ADR 0218): a request and its reply, or a request and the
// items a server pushes, framed, bounded and encoded in a codec format both
// ends agreed on.
//
//	srv, err := rpc.NewServer(rpc.ServerConfig{
//		Formats: []codec.Format{"json"}, MaxFrameBytes: 1 << 20,
//		MaxInFlight: 64, FrameTimeout: 5 * time.Second,
//	}, rpc.Methods{"status": rpc.HandlerFunc(status)})
//	go srv.Serve(ln) // ln from ipc.Listen
//
//	c, err := rpc.NewClient(ctx, dialer, rpc.ClientConfig{
//		Format: "json", MaxFrameBytes: 1 << 20, MaxInFlight: 64,
//		FrameTimeout: 5 * time.Second, CallTimeout: 2 * time.Second,
//	})
//	err = c.Call(ctx, "status", req, &resp)
//
// A format is a codec registered under its name: import the codec's package
// (pkg/v1/data/codec/json, say) for it to be there.
//
// # Bounds, every one the caller's
//
// A frame longer than MaxFrameBytes is refused on its length, before a byte of
// its body is read or allocated ([CodeFrameTooLarge]); one not whole within
// FrameTimeout of its first byte closes the connection. At most MaxInFlight
// calls run per connection, the next answered [CodeOverloaded] at once. A call
// carries its deadline to the server, which ends the handler's context on it;
// a caller that gives up sends a cancel frame ([CodeCallTimeout],
// [CodeCallCancelled]). None of these bounds has a default: zero is refused
// ([CodeMisconfigured]).
//
// # Errors across the socket
//
// A handler's error carrying an SDK code reaches the caller with that code,
// reason and public text, so errs.HasCode works on both sides; any other error
// is [CodeRemoteFailed] there, its text kept on the server.
package rpc
