package server

import (
	"bytes"
	"encoding/binary"
	"io"
	stdnet "net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/concur/buffer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// defaultProxyTimeout bounds a header when the group sets none. A header is
	// a balancer's first bytes on a connection it opened itself, so the time it
	// is allowed is short.
	defaultProxyTimeout time.Duration = 3 * time.Second
	// proxyV1MaxBytes is the longest v1 header the PROXY protocol allows, CRLF
	// included.
	proxyV1MaxBytes int = 107
	// proxyV2FixedBytes is the v2 header's fixed part: the signature, the
	// version and command, the family and the length.
	proxyV2FixedBytes int = 16
	// proxyV2MaxPayload bounds what follows the fixed part: the addresses and
	// the TLVs a balancer adds (AWS, GCP and a host name fit well inside it).
	// A trusted balancer is not an excuse: the length is read before anything
	// is, and a header past this is refused without reading it.
	proxyV2MaxPayload int = 2048
	// proxyBufferBytes is one header's scratch, taken from a shared pool.
	proxyBufferBytes int = proxyV2FixedBytes + proxyV2MaxPayload
	// proxyV2Version is the high nibble of the version and command byte.
	proxyV2Version byte = 2
	// proxyV2Local is the LOCAL command: the balancer's own traffic, and the
	// socket's address is the real one.
	proxyV2Local byte = 0
	// proxyV2Proxy is the PROXY command.
	proxyV2Proxy byte = 1
	// proxyV2Unspec, TCP4 and TCP6 are the family-and-protocol bytes this
	// listener believes; every other — UDP, Unix — is a refusal.
	proxyV2Unspec byte = 0x00
	// proxyV2TCP4 is AF_INET, STREAM.
	proxyV2TCP4 byte = 0x11
	// proxyV2TCP6 is AF_INET6, STREAM.
	proxyV2TCP6 byte = 0x21
	// proxyV2TCP4Bytes is the addresses and ports of an IPv4 v2 header.
	proxyV2TCP4Bytes int = 12
	// proxyV2TCP6Bytes is the addresses and ports of an IPv6 v2 header.
	proxyV2TCP6Bytes int = 36
)

// proxyV2Signature is the v2 header's first twelve bytes.
var proxyV2Signature = [12]byte{'\r', '\n', '\r', '\n', 0, '\r', '\n', 'Q', 'U', 'I', 'T', '\n'}

// proxyPolicy is a group's ProxyProtocol, parsed at Start.
type proxyPolicy struct {
	// trusted are the balancers' ranges.
	trusted []netip.Prefix
	// timeout bounds a header.
	timeout time.Duration
	// refused is the group's count, shared by its listeners.
	refused atomic.Uint64
}

// parseProxyPolicy reads v, or returns ok false for a value outside its range.
func parseProxyPolicy(v corenet.ProxyProtocolValue) (policy *proxyPolicy, ok bool) {
	//: an empty list would trust nobody and refuse everybody, silently.
	if len(v.Trusted) == 0 || v.Timeout.Duration() < 0 {
		//: refused by name.
		return nil, false
	}
	p := &proxyPolicy{timeout: v.Timeout.Duration()}
	//: every range must parse: a typo must not open the listener or close it.
	for _, cidr := range v.Trusted {
		prefix, err := netip.ParsePrefix(cidr)
		//: refused by name.
		if err != nil {
			//: not a range.
			return nil, false
		}
		p.trusted = append(p.trusted, prefix.Masked())
	}
	//: zero is the default, never no bound (ADR 0031).
	if p.timeout == 0 {
		p.timeout = defaultProxyTimeout
	}
	//: ready for the listeners.
	return p, true
}

// trusts reports whether the socket's real peer is a balancer.
func (p *proxyPolicy) trusts(addr stdnet.Addr) bool {
	tcp, ok := addr.(*stdnet.TCPAddr)
	//: only an IP socket has an address a range can hold.
	if !ok {
		//: a Unix peer has no address to believe.
		return false
	}
	ip, ok := netip.AddrFromSlice(tcp.IP)
	//: a malformed address trusts nobody.
	if !ok {
		//: untrusted.
		return false
	}
	ip = ip.Unmap()
	//: any one of the ranges.
	for _, prefix := range p.trusted {
		//: found.
		if prefix.Contains(ip) {
			//: trusted.
			return true
		}
	}
	//: unknown peer.
	return false
}

// proxyListener wraps every socket it accepts in a proxyConn, under TLS and the
// close tracker, and closes on the spot any peer the group does not trust: no
// goroutine, no byte read, one close per connection from outside the balancers.
type proxyListener struct {
	stdnet.Listener
	// policy is the group's.
	policy *proxyPolicy
}

// Accept implements net.Listener.
func (l *proxyListener) Accept() (conn stdnet.Conn, err error) {
	//: a refused peer does not end the loop: the next connection is accepted.
	for {
		socket, aerr := l.Listener.Accept()
		//: verbatim, so the accept loop still recognises net.ErrClosed.
		if aerr != nil {
			//: nothing was accepted.
			return nil, aerr
		}
		//: refused before anything of it is read.
		if !l.policy.trusts(socket.RemoteAddr()) {
			l.policy.refused.Add(1)
			swallowErr(socket.Close())
			continue
		}
		//: the header is read on the connection's own goroutine (establish).
		return &proxyConn{Conn: socket, policy: l.policy}, nil
	}
}

// announcedClient is the client a header named, held inline so that reading a
// header allocates nothing per connection.
type announcedClient struct {
	// addr is what RemoteAddr returns; IP points into ip.
	addr stdnet.TCPAddr
	// ip holds the address.
	ip [stdnet.IPv6len]byte
	// set records that a header named a client at all.
	set bool
}

// store records ip and port.
func (a *announcedClient) store(ip []byte, port uint16) {
	copy(a.ip[:], ip)
	a.addr.IP = a.ip[:len(ip)]
	a.addr.Port = int(port)
	a.set = true
}

// proxyConn is a socket from a trusted balancer: Read reaches it unchanged, and
// RemoteAddr answers the client the balancer announced, once establish has read
// the header. It forwards what the layers above assert on, so the close tracker
// and the handler's copies keep the socket's own paths.
type proxyConn struct {
	stdnet.Conn
	// policy is the group's.
	policy *proxyPolicy
	// announced is the client's address, unset when the header kept the socket's.
	announced announcedClient
}

// RemoteAddr implements net.Conn.
func (p *proxyConn) RemoteAddr() stdnet.Addr {
	//: the socket's own address, a LOCAL command or an UNKNOWN family's answer.
	if !p.announced.set {
		//: as accepted.
		return p.Conn.RemoteAddr()
	}
	//: the client the balancer announced.
	return &p.announced.addr
}

// CloseWrite forwards the half-close net/http and closeConn assert on.
func (p *proxyConn) CloseWrite() error {
	half, ok := p.Conn.(interface{ CloseWrite() error })
	//: a socket with no separate write half.
	if !ok {
		//: nothing to shut.
		return nil
	}
	//: the socket's own.
	return half.CloseWrite()
}

// ReadFrom forwards the socket's own copy, as trackedConn does.
func (p *proxyConn) ReadFrom(r io.Reader) (n int64, err error) {
	//: a TCP socket's ReadFrom is the zero-copy path.
	if readerFrom, ok := p.Conn.(io.ReaderFrom); ok {
		//: the socket's own copy.
		return readerFrom.ReadFrom(r)
	}
	//: the plain copy; the socket has no ReadFrom.
	return io.Copy(p.Conn, r)
}

// WriteTo forwards the socket's own copy.
func (p *proxyConn) WriteTo(w io.Writer) (n int64, err error) {
	//: a TCP socket's WriteTo chooses splice where it can.
	if writerTo, ok := p.Conn.(io.WriterTo); ok {
		//: the socket's own copy.
		return writerTo.WriteTo(w)
	}
	//: the plain copy; the socket has no WriteTo.
	return io.Copy(w, p.Conn)
}

// Unwrap implements corenet.ConnUnwrapper: the socket beneath the header's layer.
func (p *proxyConn) Unwrap() stdnet.Conn {
	//: what the listener accepted.
	return p.Conn
}

// establish reads the header from the socket, every read exact: nothing past the
// header is consumed, so a handler or a TLS handshake reads the first byte of
// what the client sent. It is bounded by the policy's timeout on clk, and the
// bound's deadline is cleared before it returns.
func (p *proxyConn) establish(clk clock.Timed) error {
	stop := p.armDeadline(clk, p.policy.timeout)
	scratch := buffer.GetSize(proxyBufferBytes)
	defer buffer.Put(scratch)
	buf := (*scratch)[:proxyBufferBytes]
	err := readProxyHeader(p.Conn, buf, &p.announced)
	//: the deadline's goroutine has ended before the deadline is cleared.
	stop()
	swallowErr(p.SetReadDeadline(time.Time{}))
	//: a missing, malformed or late header; nil when the client is known.
	return err
}

// armDeadline ends a blocked read once the budget elapses on clk, by a deadline
// in the past: a manual clock moves the budget, and the socket has no reference
// to it. The returned stop waits for the watcher to end.
//
// Goroutine lifecycle: one per header being read, ending when stop runs; stop
// runs on the serving goroutine before the header's outcome is acted on.
func (p *proxyConn) armDeadline(clk clock.Timed, budget time.Duration) func() {
	timer := clk.NewTimer(budget)
	done := make(chan struct{})
	var ended sync.WaitGroup
	ended.Go(func() {
		//: whichever comes first: the header, or the budget.
		select {
		//: refused: the blocked read returns at once.
		case <-timer.C():
			swallowErr(p.SetReadDeadline(time.Unix(1, 0)))
		//: read in time.
		case <-done:
		}
	})
	//: stops and waits, so nothing can arm a deadline after it is cleared.
	return func() {
		timer.Stop()
		close(done)
		ended.Wait()
	}
}

// proxyRefusal is ProxyHeaderInvalid, a field naming why.
func proxyRefusal(why string) error {
	//: the connection is closed on it; the field is what a log line carries.
	return errs.Wrap(corenet.ProxyHeaderInvalid, errs.WrapParams{}, errs.String("why", why))
}

// readProxyHeader reads a v1 or v2 header off socket into buf, exactly, and
// stores the client it announced in out, unset to keep the socket's own.
func readProxyHeader(socket io.Reader, buf []byte, out *announcedClient) error {
	//: the first byte decides the version, and is the signature's or "P".
	if _, err := io.ReadFull(socket, buf[:1]); err != nil {
		//: the peer closed, or the budget ran out.
		return proxyRefusal("read")
	}
	switch buf[0] {
	//: v2's binary signature starts with CR.
	case proxyV2Signature[0]:
		return readProxyV2(socket, buf, out)
	//: v1's text starts with its "PROXY".
	case 'P':
		return readProxyV1(socket, buf, out)
	//: a trusted peer that sent anything else.
	default:
		//: no header.
		return proxyRefusal("no header")
	}
}

// readProxyV2 reads what is left of a v2 header after its first byte, the
// length checked before the payload is read.
func readProxyV2(socket io.Reader, buf []byte, out *announcedClient) error {
	if _, err := io.ReadFull(socket, buf[1:proxyV2FixedBytes]); err != nil {
		//: cut short: a header that never finishes.
		return proxyRefusal("truncated header")
	}
	//: the signature, the protocol's own defence against reading a stray stream.
	if !bytes.Equal(buf[:len(proxyV2Signature)], proxyV2Signature[:]) {
		//: not a PROXY header.
		return proxyRefusal("bad signature")
	}
	verCmd, family := buf[12], buf[13]
	length := int(binary.BigEndian.Uint16(buf[14:16]))
	//: bounded before the bytes are asked for.
	if length > proxyV2MaxPayload {
		//: refused without reading it.
		return proxyRefusal("header too long")
	}
	payload := buf[proxyV2FixedBytes : proxyV2FixedBytes+length]
	if _, err := io.ReadFull(socket, payload); err != nil {
		//: cut short.
		return proxyRefusal("truncated payload")
	}
	//: the version is v2's, the command LOCAL or PROXY, nothing else.
	return parseProxyV2(verCmd, family, payload, out)
}

// parseProxyV2 reads a complete v2 header's command, family and addresses.
func parseProxyV2(verCmd, family byte, payload []byte, out *announcedClient) error {
	version, command := verCmd>>4, verCmd&0x0f
	//: a version past this protocol's.
	if version != proxyV2Version || (command != proxyV2Local && command != proxyV2Proxy) {
		//: refused.
		return proxyRefusal("unknown version or command")
	}
	//: the balancer's own traffic — a health check — keeps the socket's address.
	if command == proxyV2Local {
		//: no address to believe.
		return nil
	}
	//: the family decides how many bytes the addresses take.
	switch family {
	//: the protocol's "no address": the socket's own.
	case proxyV2Unspec:
		//: no address to believe.
		return nil
	//: IPv4, addresses then ports.
	case proxyV2TCP4:
		//: the client's half of it.
		return announceV2(payload, stdnet.IPv4len, proxyV2TCP4Bytes, out)
	//: IPv6, addresses then ports.
	case proxyV2TCP6:
		//: the client's half of it.
		return announceV2(payload, stdnet.IPv6len, proxyV2TCP6Bytes, out)
	//: UDP or Unix on a stream listener: a protocol this listener does not
	//: serve.
	default:
		//: refused.
		return proxyRefusal("unsupported family")
	}
}

// announceV2 stores the client of a v2 address block of ipLen addresses, want
// bytes long at least.
func announceV2(payload []byte, ipLen, want int, out *announcedClient) error {
	//: too short for its family.
	if len(payload) < want {
		//: refused.
		return proxyRefusal("short address")
	}
	out.store(payload[:ipLen], binary.BigEndian.Uint16(payload[2*ipLen:2*ipLen+2]))
	//: the client is known.
	return nil
}

// readProxyV1 reads a v1 line, one byte at a time so nothing past its CRLF is
// consumed, then parses it. The first byte is already in buf.
func readProxyV1(socket io.Reader, buf []byte, out *announcedClient) error {
	n := 1
	//: at most 107 bytes, CRLF included, before the line is refused.
	for buf[n-1] != '\n' {
		//: past the longest line the protocol allows.
		if n >= proxyV1MaxBytes {
			//: refused without reading more.
			return proxyRefusal("header too long")
		}
		if _, err := io.ReadFull(socket, buf[n:n+1]); err != nil {
			//: cut short, or past the budget.
			return proxyRefusal("truncated header")
		}
		n++
	}
	//: the line, without its terminator.
	if n < 2 || buf[n-2] != '\r' {
		//: a bare LF is not the protocol.
		return proxyRefusal("bad terminator")
	}
	//: the line's fields.
	return parseProxyV1(string(buf[:n-2]), out)
}

// parseProxyV1 reads "PROXY TCP4 src dst sport dport" or "PROXY UNKNOWN…",
// strictly: one space between fields, addresses of the announced family.
func parseProxyV1(line string, out *announcedClient) error {
	fields := strings.Split(line, " ")
	//: every v1 line starts with its word and the protocol.
	if len(fields) < 2 || fields[0] != "PROXY" {
		//: refused.
		return proxyRefusal("bad line")
	}
	//: UNKNOWN: the balancer's own traffic; addresses, when present, are ignored.
	if fields[1] == "UNKNOWN" {
		//: the socket's own.
		return nil
	}
	//: the six fields of an address line.
	if len(fields) != 6 || (fields[1] != "TCP4" && fields[1] != "TCP6") {
		//: refused.
		return proxyRefusal("bad line")
	}
	ip, err := netip.ParseAddr(fields[2])
	//: an address that is not one, or of the other family.
	if err != nil || ip.Is4() != (fields[1] == "TCP4") {
		//: refused.
		return proxyRefusal("bad address")
	}
	port, perr := strconv.ParseUint(fields[4], 10, 16)
	//: a port that is not one, or written with a leading zero or sign.
	if perr != nil || strconv.FormatUint(port, 10) != fields[4] {
		//: refused.
		return proxyRefusal("bad port")
	}
	raw := ip.AsSlice()
	out.store(raw, uint16(port))
	//: the announced client.
	return nil
}

// proxySocket finds the proxyConn under raw's layers, nil when there is none.
func proxySocket(raw stdnet.Conn) *proxyConn {
	socket := raw
	//: TLS first, then the close tracker: the order layered puts them in.
	for {
		switch layer := socket.(type) {
		//: a TLS group's outermost.
		case interface{ NetConn() stdnet.Conn }:
			socket = layer.NetConn()
		//: a ceilinged group's tracker.
		case *trackedConn:
			socket = layer.Conn
		//: the header's layer.
		case *proxyConn:
			return layer
		//: a group without the option.
		default:
			return nil
		}
	}
}

// admitProxied reads the header of a proxied connection on its own goroutine,
// bounded, then counts it against Admission on the announced client. It returns
// the admission's release, and ok false when the connection must be closed.
func (s *Server) admitProxied(raw stdnet.Conn, group *StreamGroup) (release func(), ok bool) {
	socket := proxySocket(raw)
	//: this group did not ask for the protocol, or the socket is not one of its.
	if socket == nil {
		//: nothing to read.
		return nil, true
	}
	//: a missing, malformed or late header: counted and closed before a byte of
	//: TLS or application data.
	if err := socket.establish(s.clk); err != nil {
		group.proxyPolicy.refused.Add(1)
		//: refused.
		return nil, false
	}
	//: no pre-handshake budget on this group.
	if group.admission == nil {
		//: admitted.
		return nil, true
	}
	//: the budget of the real client, the reason this file exists.
	return group.admission.Admit(socket.RemoteAddr())
}
