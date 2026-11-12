package websocket

import (
	"bufio"
	"io"
	"net/http"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// methodConnect is the extended CONNECT's method, RFC 8441 §4.
const methodConnect string = "CONNECT"

// protocolPseudo is the request field net/http's HTTP/2 server carries the
// :protocol pseudo-header in, when extended CONNECT is enabled.
const protocolPseudo string = ":protocol"

// protocolWebSocket is the one :protocol value a stream is accepted for: no
// other tunnel is opened (RFC 8441 §4).
const protocolWebSocket string = "websocket"

// h2ReadBuffer sizes the reader over an HTTP/2 stream's body.
const h2ReadBuffer int = 4096

// isExtendedConnect reports whether r is RFC 8441's WebSocket bootstrap: an
// HTTP/2 CONNECT whose :protocol is exactly "websocket". A CONNECT naming
// another protocol, or none, is not, and is refused as any non-GET is.
func isExtendedConnect(r *http.Request) bool {
	//: the three together; anything else is no WebSocket over HTTP/2.
	return r.ProtoMajor == 2 && r.Method == methodConnect && r.Header.Get(protocolPseudo) == protocolWebSocket
}

// verifyExtendedConnect applies what RFC 8441 §5 keeps of RFC 6455 §4.2.1 to
// an extended CONNECT: the version, then the origin rule. There is no
// Upgrade, Connection, Sec-WebSocket-Key nor Accept on HTTP/2.
func verifyExtendedConnect(w http.ResponseWriter, r *http.Request, cfg *config) error {
	//: §4.4 — answered WITH the version this server speaks.
	if version := r.Header.Get(corenet.WSVersionHeader); version != corenet.WSVersion {
		w.Header().Set(corenet.WSVersionHeader, corenet.WSVersion)
		http.Error(w, "unsupported websocket version", http.StatusUpgradeRequired)
		//: refuse.
		return errs.Wrap(corenet.WSHandshakeFailed, errs.WrapParams{},
			errs.String("header", corenet.WSVersionHeader),
			errs.String("offered", version),
			errs.String("why", "this server speaks only version 13"))
	}
	//: the same policy question as on HTTP/1.1.
	return verifyOrigin(w, r, cfg)
}

// upgradeExtendedConnect is Upgrade for an extended CONNECT: the checks RFC
// 8441 keeps, then the 200 and the Conn.
func upgradeExtendedConnect(w http.ResponseWriter, r *http.Request, cfg *config) (*Conn, error) {
	//: refused with its answer already written.
	if err := verifyExtendedConnect(w, r, cfg); err != nil {
		//: the verifier's.
		return nil, err
	}
	//: accepted.
	return acceptExtendedConnect(w, r, cfg)
}

// acceptExtendedConnect answers an extended CONNECT 200 — never 101 — with
// the agreed subprotocol, switches the stream to full duplex, and returns the
// Conn over it. The stream ends, END_STREAM, when the handler returns: it must
// not return before the Conn is done.
func acceptExtendedConnect(w http.ResponseWriter, r *http.Request, cfg *config) (*Conn, error) {
	rc := http.NewResponseController(w)
	//: the request body is the peer's frames, read while answers are written.
	if err := rc.EnableFullDuplex(); err != nil {
		http.Error(w, "websocket unavailable", http.StatusInternalServerError)
		//: a writer that cannot read and write at once carries no WebSocket.
		return nil, errs.Wrap(corenet.WSUpgradeUnsupported, errs.WrapParams{},
			errs.String("why", "the ResponseWriter cannot run full duplex"), errs.String("cause", err.Error()))
	}
	subprotocol, _ := chooseSubprotocol(cfg.subprotocols, r)
	//: the agreed subprotocol, as on HTTP/1.1.
	if subprotocol != "" {
		w.Header().Set(corenet.WSProtocolHeader, subprotocol)
	}
	w.WriteHeader(http.StatusOK)
	//: the 200 leaves now: the client waits for it before its first frame.
	if err := rc.Flush(); err != nil {
		//: the stream is gone already.
		return nil, errs.Wrap(corenet.WSHandshakeFailed, errs.WrapParams{},
			errs.String("why", "the 200 could not be sent"), errs.String("cause", err.Error()))
	}
	stream := &h2Stream{w: w, rc: rc, body: r.Body}
	//: the stream, framed as a socket is.
	return newConn(stream, bufio.NewReaderSize(r.Body, h2ReadBuffer), subprotocol, cfg, corenet.DrainSignal(r.Context())), nil
}

// h2Stream is an extended CONNECT's stream as a Conn's transport: writes go
// to the ResponseWriter, flushed one by one into DATA frames, deadlines
// through its ResponseController, and Close ends the body, which unblocks a
// read parked on it.
type h2Stream struct {
	// w is the stream's ResponseWriter.
	w http.ResponseWriter
	// rc sets its deadlines and flushes it.
	rc *http.ResponseController
	// body is the request body: the peer's frames.
	body io.ReadCloser
}

// Write writes p and flushes it: a frame is sent whole, not buffered.
func (s *h2Stream) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	//: the stream refused it.
	if err != nil {
		//: as written.
		return n, err
	}
	//: on the wire.
	return n, s.rc.Flush()
}

// SetDeadline bounds reads and writes; the zero time removes both bounds.
func (s *h2Stream) SetDeadline(t time.Time) error {
	//: the read bound first; both are set, the first refusal reported.
	if err := s.rc.SetReadDeadline(t); err != nil {
		//: unsupported or gone.
		return err
	}
	//: then the writes'.
	return s.rc.SetWriteDeadline(t)
}

// SetWriteDeadline bounds the writes.
func (s *h2Stream) SetWriteDeadline(t time.Time) error {
	//: through the controller.
	return s.rc.SetWriteDeadline(t)
}

// Close ends the request body, so a read parked on it returns.
func (s *h2Stream) Close() error {
	//: the stream itself ends when the handler returns.
	return s.body.Close()
}
