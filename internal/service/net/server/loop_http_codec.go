package server

import (
	"context"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// findHTTPCodec walks the chain for a corenet.LoopHTTPCodec, the adapter that
// serves more than one HTTP version on a group (ADR 0220).
func findHTTPCodec(chain corenet.LoopHandler) corenet.LoopHTTPCodec {
	//: outermost first, to the handler.
	for h := chain; h != nil; {
		//: found.
		if c, ok := h.(corenet.LoopHTTPCodec); ok {
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

// codecListRefusal judges a Protocols list given to a multi-version adapter:
// an empty, repeated or unknown identifier is refused as an HTTP group's is.
// The h2 cleartext restriction is the goroutine engine's; the adapter serves
// prior knowledge.
func (g *StreamGroup) codecListRefusal() error {
	codec := findHTTPCodec(g.loopChain)
	//: only an adapter that speaks more than one version reads the list.
	if codec == nil {
		//: nothing to judge.
		return nil
	}
	//: told now, so the spill's check reads the group's own bounds.
	codec.SetHTTP(corenet.LoopHTTPValue{Protocols: g.codecServes(), HTTP2: g.http2Limits})
	//: no list to judge.
	if !g.kinds.has(corenet.OptionProtocols) {
		//: servable.
		return nil
	}
	//: the list itself, the cleartext rule set aside.
	if reason := checkProtocols(g.protos, true); reason != "" {
		//: refused by what is wrong with it.
		return g.protocolsErr(reason)
	}
	//: servable.
	return nil
}

// codecServes is the versions a multi-version adapter serves: on a TLS group
// the ALPN list its listeners offer, so a version the handshake cannot
// negotiate is not served by the preface either (#964); otherwise the
// Protocols list, nil when the group gave none.
func (g *StreamGroup) codecServes() []string {
	//: what TLS negotiates, computed by tlsCodecRefusal before.
	if g.tlsCodec() && g.httpProtos.alpn != nil {
		//: the offered list.
		return g.httpProtos.alpn
	}
	//: the option's.
	return g.protocolList()
}

// protocolList is the group's Protocols list, nil when it gave none.
func (g *StreamGroup) protocolList() []string {
	//: unset.
	if !g.kinds.has(corenet.OptionProtocols) {
		//: nil, the adapter's both.
		return nil
	}
	//: as given.
	return g.protos
}

// applyHTTPCodec tells a multi-version adapter what the group serves and what
// the operator switched off, once, before any callback, and keeps its
// connections through a drain.
func (s *Server) applyHTTPCodec(group *StreamGroup) {
	codec := findHTTPCodec(group.loopChain)
	//: a single-version handler.
	if codec == nil {
		//: nothing to tell it.
		return
	}
	codec.SetHTTP(corenet.LoopHTTPValue{Protocols: group.codecServes(), HTTP2: group.http2Limits, HTTP2Off: s.operator.kernelDisabled(corenet.CapabilityHTTP2Loop)})
	group.loopBusy = codec.InFlight
}

// httpCodecEngine is a loop group whose handler is a multi-version adapter: it
// tells the adapter the drain has begun, and reports the http2-loop row.
type httpCodecEngine struct {
	loopEngine
	// codec is the adapter.
	codec corenet.LoopHTTPCodec
	// row is the capability row State appends.
	row corenet.CapabilityValue
}

// wrapHTTPCodec adds the drain's announcement and State's row to a group whose
// chain is a multi-version adapter.
func wrapHTTPCodec(s *Server, group *StreamGroup, eng loopEngine) loopEngine {
	codec := findHTTPCodec(group.loopChain)
	//: a single-version handler: the engine as it is.
	if codec == nil {
		//: unchanged.
		return eng
	}
	row := corenet.CapabilityValue{Name: corenet.CapabilityHTTP2Loop, Class: classCode, Status: statusOn}
	//: switched off by the operator: the preface is refused.
	if s.operator.kernelDisabled(corenet.CapabilityHTTP2Loop) {
		row.Status = statusDisabled
		_, row.Reason = s.operator.kernelSwitches()
	}
	//: wrapped.
	return &httpCodecEngine{loopEngine: eng, codec: codec, row: row}
}

// Shutdown announces the drain to the adapter, then drains the group.
func (e *httpCodecEngine) Shutdown(ctx context.Context) error {
	e.codec.Draining()
	//: the engine's drain.
	return e.loopEngine.Shutdown(ctx)
}

// State reports the group, the adapter's capability row included.
func (e *httpCodecEngine) State() corenet.LoopStateValue {
	st := e.loopEngine.State()
	st.Capabilities = append(st.Capabilities, e.row)
	//: with the row.
	return st
}
