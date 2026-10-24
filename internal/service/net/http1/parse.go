package http1

import (
	"bytes"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/swar"
)

// version is the one version the profile speaks.
const version string = "HTTP/1.1"

const (
	// byteValues is how many values a byte takes: the size of a byte table.
	byteValues int = 256
	// tokenStride is how many name bytes tokenEnd tests per turn.
	tokenStride int = 4
	// crlfLen is the length of a line's end.
	crlfLen int = 2
	// decimalBase is the base of a Content-Length.
	decimalBase int64 = 10
	// lastDigit is the largest value of one decimal digit.
	lastDigit byte = 9
	// asciiCase is the bit that tells a lowercase ASCII letter from its capital.
	asciiCase byte = 0x20
	// asciiDEL is the one control byte above the space.
	asciiDEL byte = 0x7f
	// asciiSpace is the first byte that is not a C0 control.
	asciiSpace byte = 0x20
	// knownMaxLen is one past the longest name the profile reads,
	// transfer-encoding.
	knownMaxLen int = len("transfer-encoding") + 1
)

// fieldKind names the fields the profile reads; kindOther is every other one.
const (
	// kindOther is a field the profile carries without reading.
	kindOther fieldKind = iota
	// kindHost is Host.
	kindHost
	// kindExpect is Expect.
	kindExpect
	// kindConnection is Connection.
	kindConnection
	// kindAuthorization is Authorization.
	kindAuthorization
	// kindContentLength is Content-Length.
	kindContentLength
	// kindTransferEncoding is Transfer-Encoding.
	kindTransferEncoding
)

var (
	// tchar holds the RFC 9110 §5.6.2 token characters.
	tchar = tokenTable()
	// targetStop is every byte that ends or breaks a request-target: a space
	// or any byte below it, DEL, and the bytes from 0x80 up.
	targetStop = swar.NewSet(asciiDEL, asciiDEL, asciiSpace+1, true)
	// valueStop is every byte a field value scan must look at: the C0
	// controls, CR and LF included, and DEL. The tab is among them and is the
	// one allowed.
	valueStop = swar.NewSet(asciiDEL, asciiDEL, asciiSpace, false)
	// knownNames holds, at its length, the lowercase name of each field the
	// profile reads: no two share a length, so one comparison identifies one.
	knownNames = [knownMaxLen]string{
		len("host"):              "host",
		len("expect"):            "expect",
		len("connection"):        "connection",
		len("authorization"):     "authorization",
		len("content-length"):    "content-length",
		len("transfer-encoding"): "transfer-encoding",
	}
	// knownKinds is the kind of the name knownNames holds at the same length.
	knownKinds = [knownMaxLen]fieldKind{
		len("host"):              kindHost,
		len("expect"):            kindExpect,
		len("connection"):        kindConnection,
		len("authorization"):     kindAuthorization,
		len("content-length"):    kindContentLength,
		len("transfer-encoding"): kindTransferEncoding,
	}
	// methodConnect is the one method the profile refuses by name.
	methodConnect = []byte("CONNECT")
	// versionBytes is version, for the comparisons with a slice of the input.
	versionBytes = []byte(version)
)

// fieldKind is which of the profile's known fields a name is.
type fieldKind uint8

// Parser frames requests of the restricted HTTP/1.1 profile under its limits.
// It holds three integers and no state between calls, so one Parser serves
// any number of connections and goroutines. The zero Parser uses the
// profile's default limits.
type Parser struct {
	// head bounds the head in bytes; zero selects corenet.HTTP1DefaultHead.
	head int
	// target bounds the request-target in bytes; zero selects corenet.HTTP1DefaultTarget.
	target int
	// body bounds the Content-Length; zero selects corenet.HTTP1DefaultBody.
	body int64
}

// tokenTable builds the tchar table: the alphanumerics and fifteen symbols.
func tokenTable() [byteValues]bool {
	var t [byteValues]bool
	//: the digits.
	for c := '0'; c <= '9'; c++ {
		//: one digit.
		t[c] = true
	}
	//: both cases of the letters.
	for c := 'a'; c <= 'z'; c++ {
		//: a letter and its capital.
		t[c], t[c-'a'+'A'] = true, true
	}
	//: the symbols tchar admits.
	for _, c := range []byte("!#$%&'*+-.^_`|~") {
		//: one symbol.
		t[c] = true
	}
	//: the table.
	return t
}

// NewParser returns a Parser bounded by l. A zero field selects the profile's
// default; a negative one, a head past corenet.HTTP1MaxHead, a target past the
// head or a body past corenet.HTTP1MaxBody is refused with
// corenet.HTTP1Misconfigured, the field named.
func NewParser(l corenet.HTTP1LimitsValue) (Parser, error) {
	//: each field checked on its own, so the error names the one at fault.
	if l.Head < 0 || l.Head > corenet.HTTP1MaxHead {
		//: a head bound out of range.
		return Parser{}, errs.Wrap(corenet.HTTP1Misconfigured, errs.WrapParams{}, errs.String("option", "head"))
	}
	//: zero is the default, then the target is held under it.
	head := orDefault(l.Head, corenet.HTTP1DefaultHead)
	//: the target is part of the head, so it cannot be larger.
	if l.Target < 0 || l.Target > head {
		//: a target bound out of range.
		return Parser{}, errs.Wrap(corenet.HTTP1Misconfigured, errs.WrapParams{}, errs.String("option", "target"))
	}
	//: the body bound keeps the Content-Length within ten digits.
	if l.Body < 0 || l.Body > corenet.HTTP1MaxBody {
		//: a body bound out of range.
		return Parser{}, errs.Wrap(corenet.HTTP1Misconfigured, errs.WrapParams{}, errs.String("option", "body"))
	}
	//: the defaults resolved once, here.
	return Parser{
		head:   head,
		target: orDefault(l.Target, min(corenet.HTTP1DefaultTarget, head)),
		body:   orDefault(l.Body, corenet.HTTP1DefaultBody),
	}, nil
}

// orDefault returns v, or def when v is zero.
func orDefault[T int | int64](v, def T) T {
	//: zero means the default, never unbounded.
	if v == 0 {
		//: the default.
		return def
	}
	//: the caller's bound.
	return v
}

// Limits returns the bounds the Parser applies, defaults resolved.
func (p Parser) Limits() corenet.HTTP1LimitsValue {
	//: the zero Parser reports the defaults it applies.
	return corenet.HTTP1LimitsValue{
		Head:   orDefault(p.head, corenet.HTTP1DefaultHead),
		Target: orDefault(p.target, corenet.HTTP1DefaultTarget),
		Body:   orDefault(p.body, corenet.HTTP1DefaultBody),
	}
}

// Parse frames the request at the start of buf into req and returns the
// bytes it takes and RefusalNone, once the whole request is there.
//
// It returns 0 and RefusalNone while more bytes are needed. When the head is
// complete and only the body is missing, req.HeadLen and req.Consumed are
// already set: Continue then waits for the body without reading the head
// again, and an early answer knows how much the request still has to send.
//
// It returns 0 and the refusal when the bytes break the profile. A refusal
// never depends on bytes past the one that broke a rule, so more bytes never
// turn it into an acceptance; and an accepted request takes the same bytes
// whatever follows it, the next pipelined request included.
//
// Framing depends on neither the method nor the target: a request takes its
// head plus its Content-Length, a GET's included. Every slice in req aliases
// buf. Parse allocates nothing.
func (p Parser) Parse(buf []byte, req *corenet.HTTP1RequestValue) (int, Refusal) {
	//: a reused value is reset field by field: clearing the 64-entry array would cost more than the parse.
	reset(req)
	head := orDefault(p.head, corenet.HTTP1DefaultHead)
	//: no byte past the head bound is ever read as head.
	limit := min(len(buf), head)
	//: the request line, then the field lines up to the blank line.
	end, r := requestLine(buf[:limit], orDefault(p.target, corenet.HTTP1DefaultTarget), req)
	//: a request line whole and accepted goes on to the fields.
	if r == RefusalNone && end > 0 {
		//: the fields, from where the line ended.
		end, r = fields(buf[:limit], end, orDefault(p.body, corenet.HTTP1DefaultBody), req)
	}
	//: a refusal, or a head not yet whole.
	if r != RefusalNone || end == 0 {
		//: refused, or more bytes needed — unless the bound is already spent.
		return 0, ranOut(r, len(buf), head)
	}
	//: the head is whole: its length and the request's are known.
	req.HeadLen = end
	req.Consumed = end + int(req.ContentLength)
	//: the body is the rest.
	return Continue(buf, req), RefusalNone
}

// ranOut turns a scan's outcome into Parse's refusal: a scan that stopped for
// want of bytes is a refusal only once the head bound has been reached.
func ranOut(r Refusal, n, head int) Refusal {
	//: a rule broken is final; past the bound no more bytes can finish the head.
	if r == RefusalNone && n >= head {
		//: the head would be too large.
		return RefusalHeadTooLarge
	}
	//: as found: a refusal, or wait for more.
	return r
}

// Continue returns req.Consumed and sets req.Body once buf, the bytes the
// request was framed from, holds the whole body; until then it returns 0. It
// reads no head: a caller that got 0 from Parse with req.HeadLen set calls
// Continue as bytes arrive, and calls Parse only for the next request.
func Continue(buf []byte, req *corenet.HTTP1RequestValue) int {
	//: no head yet, or a body still on its way.
	if req.HeadLen == 0 || len(buf) < req.Consumed {
		//: not yet.
		return 0
	}
	//: the body is exactly Content-Length bytes after the head.
	req.Body = buf[req.HeadLen:req.Consumed]
	//: the bytes the request takes.
	return req.Consumed
}

// Pending returns how many bytes of req's body have yet to arrive when
// buffered bytes of the request are already held: what an early answer to a
// complete head has to read and discard before the next request, or the
// reason to close instead.
func Pending(req *corenet.HTTP1RequestValue, buffered int) int {
	//: never negative: a pipelined tail is the next request's, not this body's.
	return max(0, req.Consumed-buffered)
}

// reset clears what Parse sets, leaving the field array's stale entries past
// NFields, which nothing reads.
func reset(req *corenet.HTTP1RequestValue) {
	//: every scalar and slice header of the value; the array is left.
	req.Method, req.Target, req.Host, req.Authorization, req.Body = nil, nil, nil, nil, nil
	req.ContentLength, req.HeadLen, req.Consumed, req.NFields = 0, 0, 0, 0
	req.HasHost, req.HasAuthorization, req.Close = false, false, false
}

// requestLine reads "method SP target SP HTTP/1.1 CRLF" from the start of
// head and returns the index after it, or 0 when head ends first, or a
// refusal.
func requestLine(head []byte, target int, req *corenet.HTTP1RequestValue) (int, Refusal) {
	//: the method: a token, then one space.
	end, r := method(head, req)
	//: the method decided: a refusal, or not yet whole.
	if r != RefusalNone || end == 0 {
		//: as it left it.
		return 0, r
	}
	//: the target: visible ASCII up to the next space.
	end, r = requestTarget(head, end, target, req)
	//: same two outcomes.
	if r != RefusalNone || end == 0 {
		//: as it left it.
		return 0, r
	}
	//: the version, then CRLF.
	return versionLine(head, end)
}

// method reads the method and its space, and returns the index after them.
func method(head []byte, req *corenet.HTTP1RequestValue) (int, Refusal) {
	end := tokenEnd(head)
	//: the bytes ran out inside the method.
	if end == len(head) {
		//: more bytes needed.
		return 0, RefusalNone
	}
	//: an empty method.
	if end == 0 {
		//: refused.
		return 0, RefusalMethod
	}
	//: one space, or a refusal named after what is there instead.
	if head[end] != ' ' {
		//: a line end where the target should start, or a non-token byte.
		return 0, lineEnd(head[end], RefusalMethod)
	}
	//: CONNECT asks for a tunnel, which the profile never opens.
	if bytes.Equal(head[:end], methodConnect) {
		//: 501.
		return 0, RefusalMethodUnsupported
	}
	req.Method = head[:end]
	//: past the space.
	return end + 1, RefusalNone
}

// requestTarget reads the target that starts at start, and its space, and
// returns the index after them.
func requestTarget(head []byte, start, bound int, req *corenet.HTTP1RequestValue) (int, Refusal) {
	n := targetStop.Index(head[start:])
	//: the bytes ran out inside the target: past the bound no more bytes can save it.
	if n < 0 {
		//: 414 already, or more bytes needed.
		return 0, tooLong(len(head)-start > bound)
	}
	//: an empty target, or one past its bound.
	if n == 0 || n > bound {
		//: refused.
		return 0, emptyOrLong(n)
	}
	end := start + n
	//: the target ends at a space, or at a byte it may not hold.
	if head[end] != ' ' {
		//: a line end, or a control, DEL or high byte.
		return 0, lineEnd(head[end], RefusalTarget)
	}
	req.Target = head[start:end]
	//: past the space.
	return end + 1, RefusalNone
}

// tooLong returns RefusalTargetTooLong when past is true, RefusalNone else.
func tooLong(past bool) Refusal {
	//: the bound decides.
	if past {
		//: 414.
		return RefusalTargetTooLong
	}
	//: not yet.
	return RefusalNone
}

// emptyOrLong names the refusal of a target of n bytes that is either empty
// or past its bound.
func emptyOrLong(n int) Refusal {
	//: nothing between the two spaces, or a stop byte first.
	if n == 0 {
		//: empty.
		return RefusalTarget
	}
	//: 414.
	return RefusalTargetTooLong
}

// versionLine reads "HTTP/1.1 CRLF" at start and returns the index after it,
// or 0 when head ends first, or a refusal.
func versionLine(head []byte, start int) (int, Refusal) {
	avail := len(head) - start
	//: fewer bytes than the version and its CRLF: a prefix that can no longer be the version is refused now.
	if avail < len(version)+crlfLen {
		//: compare what there is, then judge the byte after the version if it came.
		return 0, versionPrefix(head[start:])
	}
	//: the whole version.
	if !bytes.Equal(head[start:start+len(version)], versionBytes) {
		//: not HTTP/1.1.
		return 0, RefusalVersion
	}
	end := start + len(version)
	//: CR, then LF.
	if r := versionEnd(head[end], head[end+1], true); r != RefusalNone {
		//: anything else ends the line badly.
		return 0, r
	}
	//: the first field line, or the blank line.
	return end + crlfLen, RefusalNone
}

// versionPrefix judges a version line cut short: rest holds fewer bytes than
// the version and its CRLF.
func versionPrefix(rest []byte) Refusal {
	n := min(len(rest), len(version))
	//: a prefix that is not the version's.
	if !bytes.Equal(rest[:n], versionBytes[:n]) {
		//: not HTTP/1.1.
		return RefusalVersion
	}
	//: the byte after the version is already there.
	if len(rest) > len(version) {
		//: judged without its successor.
		return versionEnd(rest[len(version)], 0, false)
	}
	//: more bytes needed.
	return RefusalNone
}

// versionEnd judges the bytes after the version: c, and next when have says
// it has arrived.
func versionEnd(c, next byte, have bool) Refusal {
	//: the cases in the order a line can go wrong.
	switch {
	case c == '\n':
		//: a bare LF.
		return RefusalBareLF
	case c != '\r':
		//: junk after the version.
		return RefusalRequestLine
	case !have:
		//: the CR's LF has not arrived yet.
		return RefusalNone
	case next != '\n':
		//: a bare CR.
		return RefusalBareCR
	default:
		//: CRLF.
		return RefusalNone
	}
}

// lineEnd names the refusal for c found where a request-line element should
// go on: a bare CR or LF when it is one, def otherwise.
func lineEnd(c byte, def Refusal) Refusal {
	//: the two line-end bytes have their own names.
	switch c {
	case '\r':
		//: CR inside the request line.
		return RefusalBareCR
	case '\n':
		//: LF inside the request line.
		return RefusalBareLF
	default:
		//: the element's own refusal.
		return def
	}
}

// fields reads the field lines from start up to and including the blank line,
// and returns the index after it, or 0 when head ends first, or a refusal.
func fields(head []byte, start int, body int64, req *corenet.HTTP1RequestValue) (int, Refusal) {
	pos := start
	//: one field line per turn, until the blank line.
	for {
		end, done, r := lineStart(head, pos, req.NFields)
		//: the blank line, a refusal, or the bytes ran out at the start of a line.
		if done || r != RefusalNone || end == 0 {
			//: as the line's start left it.
			return end, r
		}
		pos, r = field(head, pos, body, req)
		//: a refusal, or a line not yet whole.
		if r != RefusalNone || pos == 0 {
			//: as the line left it.
			return 0, r
		}
	}
}

// lineStart judges the first byte of a line at pos: the blank line (done, and
// the index after it), a refusal, the bytes running out (0), or a field line
// (pos itself).
func lineStart(head []byte, pos, nfields int) (int, bool, Refusal) {
	//: the bytes ran out at the start of a line.
	if pos == len(head) {
		//: more bytes needed.
		return 0, false, RefusalNone
	}
	//: the line's first byte decides.
	switch head[pos] {
	case '\r':
		//: the blank line, once its LF is there.
		end, r := crlfAt(head, pos)
		return end, true, r
	case ' ', '\t':
		//: a line that continues the previous one.
		return 0, false, RefusalObsFold
	case '\n':
		//: a LF alone.
		return 0, false, RefusalBareLF
	}
	//: one more line than the array holds.
	if nfields == corenet.HTTP1MaxFields {
		//: 431.
		return 0, false, RefusalTooManyFields
	}
	//: a field line.
	return pos, false, RefusalNone
}

// crlfAt judges the CR at pos: the index after its LF, 0 while the LF has not
// arrived, or a bare CR.
func crlfAt(head []byte, pos int) (int, Refusal) {
	//: the CR's LF has not arrived yet.
	if pos+1 == len(head) {
		//: more bytes needed.
		return 0, RefusalNone
	}
	//: CR must be followed by LF.
	if head[pos+1] != '\n' {
		//: a bare CR.
		return 0, RefusalBareCR
	}
	//: past the CRLF.
	return pos + crlfLen, RefusalNone
}

// field reads one field line at start, records it, and returns the index
// after its CRLF, or 0 when head ends first, or a refusal.
func field(head []byte, start int, body int64, req *corenet.HTTP1RequestValue) (int, Refusal) {
	colon := start + tokenEnd(head[start:])
	//: the bytes ran out inside the name.
	if colon == len(head) {
		//: more bytes needed.
		return 0, RefusalNone
	}
	//: the name must be followed by its colon, and by nothing else.
	if colon == start || head[colon] != ':' {
		//: what is there instead names the refusal.
		return 0, nameEnd(head, colon, colon-start)
	}
	name := head[start:colon]
	valueStart, valueEnd, r := fieldValue(head, colon+1)
	//: a refusal, or a value not yet whole.
	if r != RefusalNone || valueEnd == 0 {
		//: as the value left it.
		return 0, r
	}
	value := trimRight(head[valueStart:valueEnd])
	//: the few names the profile reads, recognised by length before any byte is compared.
	if r = known(name, value, body, req); r != RefusalNone {
		//: a rule of the profile broken.
		return 0, r
	}
	req.Fields[req.NFields] = corenet.HTTP1FieldValue{Name: name, Value: value}
	req.NFields++
	//: past the CRLF.
	return valueEnd + crlfLen, RefusalNone
}

// fieldValue scans a field value from start: it skips the leading whitespace
// and returns where the value starts and the index of its CR, or a 0 end when
// head ends first, or a refusal.
func fieldValue(head []byte, start int) (valueStart, valueEnd int, refusal Refusal) {
	valueStart = start
	//: leading whitespace is not part of the value.
	for valueStart < len(head) && (head[valueStart] == ' ' || head[valueStart] == '\t') {
		//: one space or tab.
		valueStart++
	}
	stop := valueStart
	//: up to the first control byte that is not a tab, a word at a time.
	for {
		n := valueStop.Index(head[stop:])
		//: the bytes ran out inside the value.
		if n < 0 {
			//: more bytes needed.
			return 0, 0, RefusalNone
		}
		stop += n
		//: the tab is the one control a value may hold.
		if head[stop] != '\t' {
			//: the stop that ends or breaks the value.
			break
		}
		stop++
	}
	//: a CR whose LF has not arrived yet.
	if head[stop] == '\r' && stop+1 == len(head) {
		//: more bytes needed.
		return 0, 0, RefusalNone
	}
	//: CRLF ends the value; anything else breaks it.
	return valueStart, stop, valueStopAt(head, stop)
}

// valueStopAt judges the control byte that stopped a value scan at pos, the
// byte after it present: RefusalNone for a CRLF, or a refusal named after the
// byte.
func valueStopAt(head []byte, pos int) Refusal {
	//: what stopped the scan.
	switch head[pos] {
	case '\r':
		//: CRLF, a CR whose LF has not arrived, or a bare CR.
		return crValue(head, pos)
	case '\n':
		//: a LF alone.
		return RefusalBareLF
	case 0:
		//: a NUL.
		return RefusalNUL
	default:
		//: a C0 control or DEL.
		return RefusalControl
	}
}

// crValue judges the CR that ends a value at pos, the byte after it present.
func crValue(head []byte, pos int) Refusal {
	//: CR must be followed by LF.
	if head[pos+1] != '\n' {
		//: a bare CR.
		return RefusalBareCR
	}
	//: CRLF.
	return RefusalNone
}

// trimRight returns v without its trailing spaces and tabs.
func trimRight(v []byte) []byte {
	end := len(v)
	//: from the end, while whitespace.
	for end > 0 && (v[end-1] == ' ' || v[end-1] == '\t') {
		//: one space or tab.
		end--
	}
	//: the value proper.
	return v[:end]
}

// nameEnd names the refusal for a field name of n bytes that stopped at pos
// on something other than its colon. A CR whose LF has not arrived yet gives
// RefusalNone, which the caller reads as "more bytes".
func nameEnd(head []byte, pos, n int) Refusal {
	c := head[pos]
	//: the other stops, each with its own name.
	switch {
	case c == '\r':
		//: a line with no colon, once its LF is there.
		return crName(head, pos)
	case (c == ' ' || c == '\t') && n > 0:
		//: whitespace between the name and its colon.
		return RefusalSpaceBeforeColon
	case c == '\n':
		//: a LF alone.
		return RefusalBareLF
	case c == 0:
		//: a NUL.
		return RefusalNUL
	default:
		//: an empty name, or a byte outside tchar.
		return RefusalName
	}
}

// crName judges the CR that ends a field line with no colon.
func crName(head []byte, pos int) Refusal {
	//: the CR's LF has not arrived yet.
	if pos+1 == len(head) {
		//: more bytes needed.
		return RefusalNone
	}
	//: CRLF: a field line without a colon.
	if head[pos+1] == '\n' {
		//: no colon.
		return RefusalName
	}
	//: a CR alone.
	return RefusalBareCR
}

// known applies the profile's rules to the fields it reads — Host,
// Authorization, Content-Length, Connection, Expect and Transfer-Encoding —
// and lets every other field through.
func known(name, value []byte, body int64, req *corenet.HTTP1RequestValue) Refusal {
	//: one rule per known name.
	switch kindOf(name) {
	case kindOther:
		//: carried as it is.
		return RefusalNone
	case kindHost:
		//: at most once.
		return host(value, req)
	case kindAuthorization:
		//: at most once.
		return authorization(value, req)
	case kindContentLength:
		//: at most once, digits only.
		return contentLength(value, body, req)
	case kindConnection:
		//: only its close token matters here.
		req.Close = req.Close || hasClose(value)
		return RefusalNone
	case kindExpect:
		//: 417.
		return RefusalExpect
	default:
		//: Transfer-Encoding, any value.
		return RefusalTransferEncoding
	}
}

// kindOf returns which known field name is, or kindOther: no two known
// names share a length, so one comparison decides.
func kindOf(name []byte) fieldKind {
	//: no known name has this length, or the one candidate of this length is not it.
	if len(name) >= knownMaxLen || knownNames[len(name)] == "" || !foldIs(name, knownNames[len(name)]) {
		//: another field.
		return kindOther
	}
	//: the candidate's kind.
	return knownKinds[len(name)]
}

// host records the Host field, refusing a second one.
func host(value []byte, req *corenet.HTTP1RequestValue) Refusal {
	//: a second Host.
	if req.HasHost {
		//: refused.
		return RefusalHostDuplicated
	}
	req.HasHost, req.Host = true, value
	//: recorded.
	return RefusalNone
}

// authorization records the Authorization field, refusing a second one.
func authorization(value []byte, req *corenet.HTTP1RequestValue) Refusal {
	//: a second Authorization.
	if req.HasAuthorization {
		//: refused.
		return RefusalAuthorizationDuplicated
	}
	req.HasAuthorization, req.Authorization = true, value
	//: recorded.
	return RefusalNone
}

// contentLength reads a Content-Length value: one to ten decimal digits, no
// sign, no space, no list, at most body, and the first of its name.
func contentLength(value []byte, body int64, req *corenet.HTTP1RequestValue) Refusal {
	//: a second Content-Length, even an equal one: the first may have been zero.
	if req.ContentLength != 0 || hasField(req, "content-length") {
		//: refused.
		return RefusalContentLengthDuplicated
	}
	//: the digit count is bounded before any is read, so the sum cannot wrap.
	if len(value) == 0 || len(value) > corenet.HTTP1MaxContentLengthDigits {
		//: empty, or too long.
		return RefusalContentLength
	}
	var n int64
	//: decimal digits only.
	for _, c := range value {
		d := c - '0'
		//: anything but 0–9, the unsigned subtraction folding both sides into one test.
		if d > lastDigit {
			//: a sign, a space, a comma, a letter.
			return RefusalContentLength
		}
		n = n*decimalBase + int64(d)
	}
	//: the body bound.
	if n > body {
		//: 413.
		return RefusalBodyTooLarge
	}
	req.ContentLength = n
	//: accepted.
	return RefusalNone
}

// hasField reports whether req already holds a field named name.
func hasField(req *corenet.HTTP1RequestValue, name string) bool {
	//: the fields recorded so far.
	for k := range req.NFields {
		//: the same name, folded.
		if foldIs(req.Fields[k].Name, name) {
			//: already there.
			return true
		}
	}
	//: absent.
	return false
}

// hasClose reports whether a Connection value, a comma list of tokens, holds
// the close token.
func hasClose(v []byte) bool {
	//: the value nearly every client sends settles without a token walk.
	if foldIs(v, "keep-alive") {
		//: no close.
		return false
	}
	//: one token per turn.
	for pos := 0; pos < len(v); {
		var tok []byte
		tok, pos = nextToken(v, pos)
		//: close, in any case.
		if foldIs(tok, "close") {
			//: found.
			return true
		}
	}
	//: no close token.
	return false
}

// nextToken returns the token of a comma list that starts at or after pos,
// separators skipped, and the index after it.
func nextToken(v []byte, pos int) ([]byte, int) {
	start := pos
	//: the separators before the token.
	for start < len(v) && isListSep(v[start]) {
		//: one separator.
		start++
	}
	end := start
	//: the token, up to the next separator.
	for end < len(v) && !isListSep(v[end]) {
		//: one token byte.
		end++
	}
	//: the token, and where the next search starts.
	return v[start:end], end
}

// isListSep reports whether c separates the tokens of a comma list.
func isListSep(c byte) bool {
	//: a comma, a space or a tab.
	return c == ',' || c == ' ' || c == '\t'
}

// tokenEnd returns the length of the run of tchar bytes b starts with.
func tokenEnd(b []byte) int {
	pos := 0
	//: tokenStride bytes a turn while that many remain: one branch for the common run.
	for ; pos+tokenStride <= len(b); pos += tokenStride {
		//: any of the four outside tchar ends the run inside this group.
		if !tchar[b[pos]] || !tchar[b[pos+1]] || !tchar[b[pos+2]] || !tchar[b[pos+3]] {
			//: the tail loop finds which.
			break
		}
	}
	//: ranging over the rest leaves no bounds check in the loop.
	for k, c := range b[pos:] {
		//: the first byte outside tchar ends the token.
		if !tchar[c] {
			//: its index.
			return pos + k
		}
	}
	//: the whole slice.
	return len(b)
}

// foldIs reports whether b equals lit once A–Z in b are folded to a–z. lit is
// lowercase letters and '-' only, which is what makes OR 0x20 exact: a byte
// ORed with 0x20 equals a lowercase letter only when it is that letter or its
// capital, and equals '-' only when it is '-' or CR, which no caller passes —
// a name is a token, and a Connection value holds no CR.
func foldIs[T ~string | ~[]byte](b T, lit string) bool {
	//: a length mismatch settles it.
	if len(b) != len(lit) {
		//: never equal.
		return false
	}
	//: byte by byte, without a branch per byte on its case.
	for k := range len(lit) {
		//: one differing byte.
		if b[k]|asciiCase != lit[k] {
			//: not equal.
			return false
		}
	}
	//: equal.
	return true
}
