package server

import (
	"net/http"
	"slices"
	"strings"
	"sync/atomic"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
)

// errTypeLostPing is what net/http's CountError reports for a connection
// closed on an unanswered PING.
const errTypeLostPing string = "conn_close_lost_ping"

// netHTTPFrameErrors are the frame layer's error names net/http reports with
// no code, by the code its frame parser answers with: a wrong length is
// FRAME_SIZE_ERROR, a window past 2^31-1 FLOW_CONTROL_ERROR; any other
// frame_ name is a PROTOCOL_ERROR.
var netHTTPFrameErrors = map[string]corehttp2.ErrorType{
	"frame_settings_ack_with_length":     corehttp2.ErrorTypeFrameSize,
	"frame_settings_mod_6":               corehttp2.ErrorTypeFrameSize,
	"frame_ping_length":                  corehttp2.ErrorTypeFrameSize,
	"frame_goaway_short":                 corehttp2.ErrorTypeFrameSize,
	"frame_windowupdate_bad_len":         corehttp2.ErrorTypeFrameSize,
	"frame_priority_bad_length":          corehttp2.ErrorTypeFrameSize,
	"frame_priority_update_bad_length":   corehttp2.ErrorTypeFrameSize,
	"frame_rststream_bad_len":            corehttp2.ErrorTypeFrameSize,
	"frame_data_pad_byte_short":          corehttp2.ErrorTypeFrameSize,
	"frame_headers_pad_short":            corehttp2.ErrorTypeFrameSize,
	"frame_headers_prio_short":           corehttp2.ErrorTypeFrameSize,
	"frame_headers_prio_weight_short":    corehttp2.ErrorTypeFrameSize,
	"frame_pushpromise_pad_short":        corehttp2.ErrorTypeFrameSize,
	"frame_pushpromise_promiseid_short":  corehttp2.ErrorTypeFrameSize,
	"frame_settings_window_size_too_big": corehttp2.ErrorTypeFlowControl,
}

// httpCounts are an HTTP group's counts on net/http's engine (#966): its
// requests per version and the HTTP/2 errors net/http's CountError reports.
type httpCounts struct {
	// h1 and h2 count the requests by version.
	h1, h2 atomic.Uint64
	// errs count the HTTP/2 errors by corehttp2.ErrorType.
	errs [corehttp2.ErrorTypes]atomic.Uint64
}

// countError is http.HTTP2Config.CountError: one error, named by net/http.
func (c *httpCounts) countError(errType string) {
	c.errs[netHTTPErrorType(errType)].Add(1)
}

// value is the counts as State reports them.
func (c *httpCounts) value() corenet.HTTPCountsValue {
	v := corenet.HTTPCountsValue{HTTP1Requests: c.h1.Load(), HTTP2Requests: c.h2.Load()}
	//: a row for each type counted, in the vocabulary's order.
	for i := range c.errs {
		//: a type never counted takes no row.
		if n := c.errs[i].Load(); n > 0 {
			v.HTTP2Errors = append(v.HTTP2Errors, corehttp2.ErrorCountValue{Type: corehttp2.ErrorType(i), Count: n})
		}
	}
	//: the snapshot.
	return v
}

// netHTTPErrorType classifies an error name net/http's CountError reports:
// "conn_" or "stream_" then the RFC code's name then the cause, a frame_
// name of the frame layer, or the lost PING. Anything else is
// corehttp2.ErrorTypeOther, so a name a later Go release adds grows no table.
func netHTTPErrorType(errType string) corehttp2.ErrorType {
	//: the one name that is no code.
	if errType == errTypeLostPing {
		//: the PING sent on silence went unanswered.
		return corehttp2.ErrorTypePingTimeout
	}
	//: the frame layer's, which carry no code.
	if t, ok := netHTTPFrameErrors[errType]; ok {
		//: the length or window it refused.
		return t
	}
	//: any other frame layer name is a protocol violation.
	if strings.HasPrefix(errType, "frame_") {
		//: by elimination.
		return corehttp2.ErrorTypeProtocol
	}
	//: the code's name after the scope.
	return errorTypeByCodeName(errType)
}

// errorTypeByCodeName reads the code name after a "conn_" or "stream_" scope.
func errorTypeByCodeName(errType string) corehttp2.ErrorType {
	rest, ok := strings.CutPrefix(errType, "conn_")
	//: not a connection's: a stream's, or nothing known.
	if !ok {
		rest, ok = strings.CutPrefix(errType, "stream_")
	}
	//: neither scope.
	if !ok {
		//: unknown.
		return corehttp2.ErrorTypeOther
	}
	//: RFC 9113's codes past NO_ERROR, by name.
	for code := corehttp2.ErrCodeProtocol; code <= corehttp2.ErrCodeHTTP11Required; code++ {
		//: the name, then the cause.
		if strings.HasPrefix(rest, code.String()+"_") {
			//: the code's type.
			return corehttp2.ErrorTypeOf(code)
		}
	}
	//: a code outside the RFC's.
	return corehttp2.ErrorTypeOther
}

// versionCounter counts each request by its version on its way to the
// handler: one atomic add, nothing allocated.
type versionCounter struct {
	// next is the handler.
	next http.Handler
	// counts are the group's.
	counts *httpCounts
}

// ServeHTTP implements http.Handler.
func (h versionCounter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	//: HTTP/2 by its major version; every other request net/http serves is HTTP/1.x.
	if r.ProtoMajor == 2 {
		h.counts.h2.Add(1)
	} else {
		h.counts.h1.Add(1)
	}
	h.next.ServeHTTP(w, r)
}

// findHTTPCounter walks a loop chain for a corenet.LoopHTTPCounter.
func findHTTPCounter(chain corenet.LoopHandler) corenet.LoopHTTPCounter {
	//: outermost first, to the handler.
	for h := chain; h != nil; {
		//: found.
		if c, ok := h.(corenet.LoopHTTPCounter); ok {
			//: the first.
			return c
		}
		l, ok := h.(corenet.LoopLayered)
		//: the end of the chain.
		if !ok {
			//: none.
			return nil
		}
		h = l.Next()
	}
	//: none.
	return nil
}

// groupHTTPCounts are a group's HTTP counts: net/http's engine's, or its loop
// adapter's; false for a group that counts none.
func groupHTTPCounts(g *StreamGroup) (corenet.HTTPCountsValue, bool) {
	//: net/http's engine.
	if g.httpAdapter != nil {
		//: the adapter's.
		return g.httpAdapter.counts.value(), true
	}
	//: a loop adapter that counts.
	if c := findHTTPCounter(g.loopChain); c != nil {
		//: what it reports.
		return c.HTTPCounts(), true
	}
	//: no HTTP.
	return corenet.HTTPCountsValue{}, false
}

// foldHTTPCounts writes each HTTP group's counts and the ALPN list its
// listeners offer (#964) into its rows of listeners, the counts read once a
// group. The caller holds s.mu.
func (s *Server) foldHTTPCounts(listeners []corenet.ListenerStateValue) {
	var (
		last string
		g    *StreamGroup
		v    corenet.HTTPCountsValue
		ok   bool
	)
	//: every row; a group's rows are read from one snapshot.
	for i := range listeners {
		//: a new group: its counts.
		if i == 0 || listeners[i].Group != last {
			last = listeners[i].Group
			g, v, ok = s.byName[last], corenet.HTTPCountsValue{}, false
			//: a datagram group has no StreamGroup.
			if g != nil {
				v, ok = groupHTTPCounts(g)
			}
		}
		//: a group that serves no HTTP keeps zeros.
		if !ok {
			continue
		}
		listeners[i].HTTP1Requests, listeners[i].HTTP2Requests = v.HTTP1Requests, v.HTTP2Requests
		listeners[i].HTTP2Errors = v.HTTP2Errors
		listeners[i].ALPN = slices.Clone(g.httpProtos.alpn)
	}
}
