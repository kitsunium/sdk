package http2

import (
	"bytes"
	"slices"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// Which pseudo-fields a request gave.
const (
	// seenMethod is :method.
	seenMethod pseudoSet = 1 << iota
	// seenTarget is :path.
	seenTarget
	// seenScheme is :scheme.
	seenScheme
	// seenAuthority is :authority.
	seenAuthority
)

// The mapping's constants.
const (
	// hostFieldName is the Host field, as Field looks it up.
	hostFieldName string = "host"
	// maxDecimalDigits is the most digits an int64 surely holds.
	maxDecimalDigits int = 18
	// protoMajorHTTP2 is what mapRequest sets Proto to.
	protoMajorHTTP2 uint8 = 2
	// decimalBase is the radix of a Content-Length.
	decimalBase int64 = 10
)

// The pseudo-fields of a request, RFC 9113 §8.3.1, and the regular fields the
// mapping reads.
var (
	// pseudoMethod is the method.
	pseudoMethod = []byte(":method")
	// pseudoPath is the target.
	pseudoPath = []byte(":path")
	// pseudoScheme is the scheme.
	pseudoScheme = []byte(":scheme")
	// pseudoAuthority is the authority.
	pseudoAuthority = []byte(":authority")
	// fieldAuthorization is the Authorization field.
	fieldAuthorization = []byte("authorization")
	// fieldContentLength is the Content-Length field.
	fieldContentLength = []byte("content-length")
	// fieldCookie is the Cookie field, split over several by §8.2.3.
	fieldCookie = []byte("cookie")
	// fieldExpect is the Expect field, refused by the profile.
	fieldExpect = []byte("expect")
	// fieldTE is the TE field, which may hold "trailers" alone.
	fieldTE = []byte("te")
	// valueTrailers is what a TE field may hold.
	valueTrailers = []byte("trailers")
	// fieldConnection through fieldUpgrade are the connection-specific fields
	// RFC 9113 §8.2.2 calls malformed.
	fieldConnection       = []byte("connection")
	fieldKeepAlive        = []byte("keep-alive")
	fieldProxyConnection  = []byte("proxy-connection")
	fieldTransferEncoding = []byte("transfer-encoding")
	fieldUpgrade          = []byte("upgrade")
	// methodConnect is the method the profile refuses.
	methodConnect = []byte("CONNECT")
	// cookieSep joins a Cookie field's crumbs, §8.2.3.
	cookieSep = []byte("; ")
)

// pseudoSet is which pseudo-fields a request carried.
type pseudoSet uint8

// mapVerdict is what mapping one request found.
type mapVerdict struct {
	// refusal is what to answer on the stream, RefusalNone to serve it.
	refusal http1.Refusal
	// malformed ends the stream with RST_STREAM(PROTOCOL_ERROR), §8.1.1.
	malformed bool
	// wanted is the declared content length, if declared.
	wanted int64
	// declared says a content-length was given.
	declared bool
}

// mapState gathers the fields' verdicts across one request's walk.
type mapState struct {
	// method, target, scheme and authority are the pseudo-fields.
	method, target, scheme, authority []byte
	// seenRegular is set once a regular field went by: a pseudo-field after it
	// is malformed, §8.3.
	seenRegular bool
	// seen is which pseudo-fields the request gave.
	seen pseudoSet
	// hosts counts the Host fields.
	hosts int
	// crumbs counts the Cookie fields.
	crumbs int
	// v is the verdict so far.
	v mapVerdict
}

// mapRequest fills the request value's Method, Target, Host, Authorization and
// ContentLength from its decoded fields, removes the pseudo-fields from
// Fields, joins a split Cookie and computes the canonical target. It reports
// what to do with the stream.
func mapRequest(req *corenet.HTTP1RequestValue, st *laneState, stream uint32) mapVerdict {
	resetMapped(req)
	req.Proto, req.Stream = protoMajorHTTP2, stream
	var m mapState
	write := 0
	//: every field in arrival order; the regular ones kept, the pseudo moved out.
	for i := range req.NFields {
		name := req.Fields[i].Name
		//: a pseudo-field: moved out of Fields.
		if len(name) > 0 && name[0] == ':' {
			m.pseudo(name, req.Fields[i].Value)
			continue
		}
		m.regular(req, i)
		//: a kept field slides over the pseudo-fields removed before it.
		if write != i {
			req.Fields[write] = req.Fields[i]
		}
		write++
	}
	req.NFields = write
	//: a malformed request ends here.
	if m.v.malformed || !m.complete() {
		m.v.malformed = true
		//: its stream.
		return m.v
	}
	req.Method, req.Target, req.Scheme = m.method, m.target, m.scheme
	req.Host, req.HasHost = m.host(req)
	//: :authority and Host disagreeing is two names for one server.
	if m.hostConflict(req) {
		m.v.refusal = http1.RefusalTargetHost
		//: refused.
		return m.v
	}
	//: a split Cookie is one field to every consumer, §8.2.3.
	if m.crumbs > 1 {
		joinCookies(req, st)
	}
	req.ContentLength = m.v.wanted
	m.v.refusal = m.judge(req)
	//: the verdict.
	return m.v
}

// resetMapped clears what mapRequest and a link may have left from the lane's
// previous request.
func resetMapped(req *corenet.HTTP1RequestValue) {
	req.Method, req.Target, req.Host, req.Authorization, req.Body = nil, nil, nil, nil, nil
	req.Path, req.Query, req.Scheme = nil, nil, nil
	req.ContentLength = 0
	req.HeadLen, req.Consumed, req.Route, req.RoutePrefix = 0, 0, 0, 0
	req.HasHost, req.HasAuthorization, req.Close, req.Authenticated = false, false, false, false
	req.Auth.Reset()
	req.Trace = trace.SpanContextValue{}
	req.RequestID.Reset()
}

// pseudo records one pseudo-field; a repeated, unknown or late one is malformed.
func (m *mapState) pseudo(name, value []byte) {
	//: a pseudo-field after a regular one, §8.3.
	if m.seenRegular {
		m.v.malformed = true
		//: malformed.
		return
	}
	//: which one, repeats refused.
	switch {
	case bytes.Equal(name, pseudoMethod):
		m.setOnce(seenMethod, &m.method, value)
	case bytes.Equal(name, pseudoPath):
		m.setOnce(seenTarget, &m.target, value)
	case bytes.Equal(name, pseudoScheme):
		m.setOnce(seenScheme, &m.scheme, value)
	case bytes.Equal(name, pseudoAuthority):
		m.setOnce(seenAuthority, &m.authority, value)
	default:
		//: a pseudo-field the protocol does not define, §8.3.
		m.v.malformed = true
	}
}

// setOnce keeps value in *slot, marking the request malformed on a repeat.
func (m *mapState) setOnce(bit pseudoSet, slot *[]byte, value []byte) {
	//: a pseudo-field appears once.
	if m.seen&bit != 0 {
		m.v.malformed = true
		//: malformed.
		return
	}
	m.seen |= bit
	*slot = value
}

// regular reads the fields of index i the mapping cares about.
func (m *mapState) regular(req *corenet.HTTP1RequestValue, i int) {
	m.seenRegular = true
	f := &req.Fields[i]
	//: by its name; the rest is the handler's.
	switch {
	case bytes.Equal(f.Name, fieldAuthorization):
		m.authorization(req, f.Value)
	case bytes.Equal(f.Name, fieldContentLength):
		m.contentLength(f.Value)
	case bytes.Equal(f.Name, fieldCookie):
		m.crumbs++
	case string(f.Name) == hostFieldName:
		m.host1()
	case bytes.Equal(f.Name, fieldExpect):
		//: the profile sends no interim response, as on HTTP/1.1.
		m.refuse(http1.RefusalExpect)
	case bytes.Equal(f.Name, fieldTE):
		m.te(f.Value)
	case connectionSpecific(f.Name):
		//: connection-specific fields are malformed, §8.2.2.
		m.v.malformed = true
	}
}

// te accepts the one value a TE field may hold, "trailers", §8.2.2.
func (m *mapState) te(value []byte) {
	//: anything else is malformed.
	if !bytes.EqualFold(value, valueTrailers) {
		m.v.malformed = true
	}
}

// connectionSpecific reports a field RFC 9113 §8.2.2 forbids on HTTP/2.
func connectionSpecific(name []byte) bool {
	//: the four the Go client strips before they would reach a request here.
	return bytes.Equal(name, fieldConnection) || bytes.Equal(name, fieldKeepAlive) ||
		bytes.Equal(name, fieldProxyConnection) || bytes.Equal(name, fieldTransferEncoding) ||
		bytes.Equal(name, fieldUpgrade)
}

// host1 refuses a second Host field, as HTTP/1.1 does.
func (m *mapState) host1() {
	m.hosts++
	//: the same field twice.
	if m.hosts > 1 {
		m.refuse(http1.RefusalHostDuplicated)
	}
}

// authorization keeps the one Authorization field a Bearer link reads.
func (m *mapState) authorization(req *corenet.HTTP1RequestValue, value []byte) {
	//: a second Authorization field, refused as on HTTP/1.1.
	if req.HasAuthorization {
		m.refuse(http1.RefusalAuthorizationDuplicated)
		//: refused.
		return
	}
	req.HasAuthorization = true
	req.Authorization = value
}

// contentLength reads the declared length, refused as HTTP/1.1 refuses a
// malformed one; a second one is refused too.
func (m *mapState) contentLength(value []byte) {
	//: a second Content-Length, refused as on HTTP/1.1.
	if m.v.declared {
		m.refuse(http1.RefusalContentLengthDuplicated)
		//: refused.
		return
	}
	n, ok := parseDecimal(value)
	//: empty, signed, not decimal, overflowing.
	if !ok {
		m.refuse(http1.RefusalContentLength)
		//: refused.
		return
	}
	m.v.wanted, m.v.declared = n, true
}

// refuse keeps the first refusal of a request.
func (m *mapState) refuse(r http1.Refusal) {
	//: the first one is reported.
	if m.v.refusal == http1.RefusalNone {
		m.v.refusal = r
	}
}

// parseDecimal reads a non-negative decimal integer that fits an int64.
func parseDecimal(b []byte) (int64, bool) {
	//: a decimal has at least a digit and at most eighteen.
	if len(b) == 0 || len(b) > maxDecimalDigits {
		//: refused.
		return 0, false
	}
	var n int64
	//: each digit; anything else refuses.
	for _, c := range b {
		//: only digits.
		if c < '0' || c > '9' {
			//: refused.
			return 0, false
		}
		n = n*decimalBase + int64(c-'0')
	}
	//: read.
	return n, true
}

// complete reports whether the request has the pseudo-fields §8.3.1 demands:
// :method, and on every method but CONNECT, :scheme and :path. A CONNECT is
// refused by judge, whatever else it names.
func (m *mapState) complete() bool {
	//: every request names its method.
	if m.seen&seenMethod == 0 {
		//: malformed.
		return false
	}
	//: a CONNECT has neither, §8.5, and is refused by the profile.
	if bytes.Equal(m.method, methodConnect) {
		//: judged later.
		return true
	}
	//: an empty :path is malformed for http and https, §8.3.1.
	return m.seen&seenScheme != 0 && len(m.target) > 0
}

// host returns the request's Host, :authority first and a Host field otherwise.
func (m *mapState) host(req *corenet.HTTP1RequestValue) ([]byte, bool) {
	//: :authority names the server when given.
	if m.seen&seenAuthority != 0 {
		//: authority wins.
		return m.authority, true
	}
	v, ok := req.Field(hostFieldName)
	//: the Host field, or none.
	return v, ok
}

// hostConflict reports a request naming two different servers: an :authority
// beside a Host field that differs.
func (m *mapState) hostConflict(req *corenet.HTTP1RequestValue) bool {
	//: no :authority: no two names.
	if m.seen&seenAuthority == 0 {
		//: no conflict.
		return false
	}
	v, ok := req.Field(hostFieldName)
	//: both present and different.
	return ok && !bytes.Equal(v, m.authority)
}

// joinCookies concatenates the Cookie fields into the first, §8.2.3, and
// removes the others.
func joinCookies(req *corenet.HTTP1RequestValue, st *laneState) {
	start := len(st.scratch)
	first := -1
	write := 0
	//: gather every crumb, in order, and close up the holes.
	for i := range req.NFields {
		//: a regular field: kept.
		if !bytes.Equal(req.Fields[i].Name, fieldCookie) {
			req.Fields[write] = req.Fields[i]
			write++
			continue
		}
		//: the first crumb gives the name and the position.
		if first < 0 {
			first = write
			req.Fields[write] = req.Fields[i]
			write++
		} else {
			st.scratch = append(st.scratch, cookieSep...)
		}
		st.scratch = append(st.scratch, req.Fields[i].Value...)
	}
	req.Fields[first].Value = st.scratch[start:len(st.scratch):len(st.scratch)]
	req.NFields = write
}

// judge computes the canonical target and refuses a form the profile does not
// serve: a CONNECT, an absolute-form :path.
func (m *mapState) judge(req *corenet.HTTP1RequestValue) http1.Refusal {
	//: a refusal found while walking the fields.
	if m.v.refusal != http1.RefusalNone {
		//: the first one.
		return m.v.refusal
	}
	//: the profile opens no tunnel.
	if bytes.Equal(m.method, methodConnect) {
		//: 501, as on HTTP/1.1.
		return http1.RefusalMethodUnsupported
	}
	//: an origin-form path or "*": the two forms RFC 9113 §8.3.1 allows here.
	if m.target[0] != '/' && (len(m.target) != 1 || m.target[0] != '*') {
		//: the absolute form is HTTP/1.1's.
		return http1.RefusalTargetForm
	}
	return http1.Canonical(req)
}

// copyRequest makes dst hold src, every slice repointed at bytes appended to
// head: the request outlives the batch's buffers. It returns head grown.
func copyRequest(dst, src *corenet.HTTP1RequestValue, head []byte) []byte {
	size := len(src.Method) + len(src.Target) + len(src.Host) + len(src.Authorization) + len(src.Path) + len(src.Query) + len(src.Scheme)
	//: every field's two strings.
	for i := range src.NFields {
		size += len(src.Fields[i].Name) + len(src.Fields[i].Value)
	}
	head = slices.Grow(head[:0], size)
	*dst = *src
	dst.Method, head = copyBytes(head, src.Method)
	dst.Target, head = copyBytes(head, src.Target)
	dst.Host, head = copyBytes(head, src.Host)
	dst.Authorization, head = copyBytes(head, src.Authorization)
	dst.Path, head = copyBytes(head, src.Path)
	dst.Query, head = copyBytes(head, src.Query)
	dst.Scheme, head = copyBytes(head, src.Scheme)
	dst.Fields = src.Fields
	//: every field's two strings, in place.
	for i := range src.NFields {
		dst.Fields[i].Name, head = copyBytes(head, src.Fields[i].Name)
		dst.Fields[i].Value, head = copyBytes(head, src.Fields[i].Value)
	}
	//: the buffer the slices alias.
	return head
}

// copyBytes appends b to head and returns the slice of head holding it, nil
// for nil.
func copyBytes(head, b []byte) (dst, grown []byte) {
	//: a slice nothing named stays nil.
	if b == nil {
		//: as it was.
		return nil, head
	}
	start := len(head)
	head = append(head, b...)
	//: within the room copyRequest reserved, so no reallocation can happen.
	return head[start:len(head):len(head)], head
}
