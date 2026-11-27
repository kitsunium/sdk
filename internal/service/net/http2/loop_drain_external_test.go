package http2_test

import (
	"encoding/binary"
	"slices"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	corehttp2 "github.com/kitsunium/sdk/internal/core/net/http2"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/http2"
)

// postIO is a batchIO that keeps what is posted to it, for the test to run
// on the lane's thread at the moment it chooses.
type postIO struct {
	*batchIO
	ops  []corenet.PostFunc
	args []uintptr
}

func (p *postIO) Post(op corenet.PostFunc, arg uintptr) error {
	p.ops, p.args = append(p.ops, op), append(p.args, arg)
	return nil
}

// runPosts runs what was posted, in order, as the loop does inside a batch.
func (p *postIO) runPosts() {
	ops, args := p.ops, p.args
	p.ops, p.args = nil, nil
	for i, op := range ops {
		op(p, args[i])
	}
}

// frameTypes is the type of each frame of out, in order.
func frameTypes(out []byte) []corehttp2.FrameType {
	var types []corehttp2.FrameType
	for len(out) >= 9 {
		n := int(out[0])<<16 | int(out[1])<<8 | int(out[2])
		types = append(types, corehttp2.FrameType(out[3]))
		if len(out) < 9+n {
			break
		}
		out = out[9+n:]
	}
	return types
}

// TestLoopHandler_drainAnnouncedMidBatchKeepsTheBatchsAnswers pins #1385 on
// the adapter: the drain's GOAWAY, run on the lane's thread in the batch that
// fed a request, after it was served and before OnBatchEnd wrote its answer,
// leaves the connection to the batch's end, which writes the answer, and its
// close to the engine's drain (#1609). The announcement closed it at once —
// its stream, answered in the protocol's state, no longer in flight — and
// the answer was refused.
func TestLoopHandler_drainAnnouncedMidBatchKeepsTheBatchsAnswers(t *testing.T) {
	type tc struct {
		name string
		// fedThisBatch feeds the request in the announcing batch; otherwise
		// the request is answered a batch before, and the announcement closes
		// an idle connection at once.
		fedThisBatch bool
	}
	runCase := func(t *testing.T, c tc) {
		hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
		a := http2.HandleLoop(hello, http1.Parser{})
		a.OnStart(1)
		io := &postIO{batchIO: newBatchIO()}
		ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
		a.OnOpen(io, ref)
		req := h2Request()
		if !c.fedThisBatch {
			if n := a.OnData(io, ref, req); n != len(req) {
				t.Fatalf("OnData consumed %d of %d", n, len(req))
			}
			a.OnBatchEnd(io)
		}
		a.Draining()
		// The batch: the request fed, then the posted announcement, then its end.
		if c.fedThisBatch {
			if n := a.OnData(io, ref, req); n != len(req) {
				t.Fatalf("OnData consumed %d of %d", n, len(req))
			}
		}
		io.runPosts()
		if c.fedThisBatch && io.closed[ref] {
			t.Fatal("the drain's announcement closed the connection before the batch wrote its answer")
		}
		a.OnBatchEnd(io)
		if !hasFrame(io.out[ref], corehttp2.FrameHeaders, 1) || !hasFrame(io.out[ref], corehttp2.FrameData, 1) {
			t.Fatalf("the answer on stream 1 was not written: % x", io.out[ref])
		}
		if !hasFrame(io.out[ref], corehttp2.FrameGoAway, 0) {
			t.Fatalf("no GOAWAY was written: % x", io.out[ref])
		}
		// The server's SETTINGS is the connection's first frame (RFC 9113
		// §3.4), and the GOAWAY follows the batch's answers.
		order := frameTypes(io.out[ref])
		if len(order) == 0 || order[0] != corehttp2.FrameSettings {
			t.Fatalf("the connection's frames %v, want SETTINGS first", order)
		}
		if slices.Index(order, corehttp2.FrameGoAway) < slices.Index(order, corehttp2.FrameData) {
			t.Fatalf("the connection's frames %v, want the GOAWAY behind the answer", order)
		}
		// Its close is the engine's drain's (#1609): the adapter leaves it
		// open, no longer in flight, for the drain to close with a linger.
		if io.closed[ref] {
			t.Fatal("the adapter closed the drained connection: its close is the engine's drain's")
		}
		if a.InFlight(0, ref) {
			t.Fatal("the drained connection is still in flight")
		}
	}
	for _, c := range []tc{{name: "fed in the announcing batch", fedThisBatch: true}, {name: "idle", fedThisBatch: false}} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// workerH answers every request pending, its answer filled by the lane's
// worker.
type workerH struct{}

func (workerH) ServeHTTP1(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	res.Pending, res.Job = true, 7
}

func (workerH) WorkHTTP1(_ int, _ uint64, res *corenet.HTTP1ResponseValue) {
	res.Body = []byte("ok")
}

// sentJobs is a lane's outbound that keeps the jobs sent to it.
type sentJobs struct{ jobs []corenet.LoopJobValue }

func (o *sentJobs) Send(_ corenet.LoopIO, job corenet.LoopJobValue) error {
	o.jobs = append(o.jobs, job)
	return nil
}

// TestLoopHandler_aWorkersAnswerInTheDrainingBatchFollowsItsOutput pins
// #1385 on a worker's answer: in the batch that fed a request and queued the
// drain's GOAWAY behind the connection's SETTINGS, the worker's answer comes
// back. Written and closed at once, it went out ahead of the SETTINGS and the
// GOAWAY, and its close refused them at the batch's end; it now joins the
// batch's output, and the batch's end writes it in order — SETTINGS first —;
// the close is the engine's drain's (#1609).
func TestLoopHandler_aWorkersAnswerInTheDrainingBatchFollowsItsOutput(t *testing.T) {
	type tc struct {
		name string
		// draining announces the drain in the batch.
		draining bool
	}
	runCase := func(t *testing.T, c tc) {
		a := http2.HandleLoop(workerH{}, http1.Parser{})
		a.OnStart(1)
		out := &sentJobs{}
		a.OnOutbound(corenet.LoopOutboundValue{Lanes: []corenet.LoopOutbound{out}, OutputCap: 1 << 20})
		io := &postIO{batchIO: newBatchIO()}
		ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
		a.OnOpen(io, ref)
		req := h2Request()
		if c.draining {
			a.Draining()
		}
		// The batch: the request fed and offloaded, the drain's announcement,
		// the worker's answer back, then the batch's end.
		if n := a.OnData(io, ref, req); n != len(req) {
			t.Fatalf("OnData consumed %d of %d", n, len(req))
		}
		io.runPosts()
		if len(out.jobs) != 1 {
			t.Fatalf("%d jobs sent to the worker, want 1", len(out.jobs))
		}
		a.Worker().Work(0, out.jobs[0])
		a.OnDone(io, out.jobs[0])
		a.OnBatchEnd(io)
		order := frameTypes(io.out[ref])
		if len(order) == 0 || order[0] != corehttp2.FrameSettings {
			t.Fatalf("the connection's frames %v, want SETTINGS first", order)
		}
		if !hasFrame(io.out[ref], corehttp2.FrameHeaders, 1) || !hasFrame(io.out[ref], corehttp2.FrameData, 1) {
			t.Fatalf("the worker's answer on stream 1 was not written: %v", order)
		}
		if !c.draining {
			return
		}
		// The GOAWAY, announced before the answer came back, names stream 1
		// as taken: the answer may follow it.
		if !hasFrame(io.out[ref], corehttp2.FrameGoAway, 0) {
			t.Fatalf("the connection's frames %v, want a GOAWAY", order)
		}
		// Its close is the engine's drain's (#1609), once no longer in flight.
		if io.closed[ref] {
			t.Fatal("the adapter closed the drained connection: its close is the engine's drain's")
		}
		if a.InFlight(0, ref) {
			t.Fatal("the drained connection, its stream answered, is still in flight")
		}
	}
	for _, c := range []tc{{name: "draining", draining: true}, {name: "serving"}} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestLoopHandler_aConnectionKnownAsHTTP2AfterTheAnnouncementGetsItsGoAway
// pins #1609 on the adapter: the drain's announcement walks the lane's HTTP/2
// connections once, and a connection whose preface had not been read by then
// — still sniffing, or not yet open — is not one of them. Read later, its
// preface made it HTTP/2 with no GOAWAY to come, nothing in flight, and the
// drain closed it silent: the client saw EOF where #961's fourth criterion
// promises GOAWAY(NO_ERROR) before a clean close. Its GOAWAY now follows the
// batch's SETTINGS and any answer the batch gave it, and the engine's drain
// closes it, nothing in flight.
func TestLoopHandler_aConnectionKnownAsHTTP2AfterTheAnnouncementGetsItsGoAway(t *testing.T) {
	type tc struct {
		name string
		// openFirst opens the connection before the announcement; otherwise
		// it opens after it.
		openFirst bool
		// before is what the connection sent before the announcement, the
		// rest of data coming after it.
		before int
		// data is all the connection sends.
		data []byte
		// stream is the stream the batch answers, zero for none.
		stream uint32
	}
	preface := h2Request()[:len("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")+corehttp2.FrameHeaderLen]
	runCase := func(t *testing.T, c tc) {
		hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
		a := http2.HandleLoop(hello, http1.Parser{})
		a.OnStart(1)
		io := &postIO{batchIO: newBatchIO()}
		ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
		//: open, with the part of the data sent before the announcement.
		if c.openFirst {
			a.OnOpen(io, ref)
			//: a preface still arriving: the sniff keeps it in the spill.
			if c.before > 0 {
				if n := a.OnData(io, ref, c.data[:c.before]); n != 0 {
					t.Fatalf("a partial preface: OnData consumed %d, want 0", n)
				}
				a.OnBatchEnd(io)
			}
		}
		a.Draining()
		io.runPosts()
		//: opened only now, the announcement run already.
		if !c.openFirst {
			a.OnOpen(io, ref)
		}
		if n := a.OnData(io, ref, c.data); n != len(c.data) {
			t.Fatalf("OnData consumed %d of %d", n, len(c.data))
		}
		a.OnBatchEnd(io)
		order := frameTypes(io.out[ref])
		//: RFC 9113 §3.4: the server's SETTINGS opens the connection.
		if len(order) == 0 || order[0] != corehttp2.FrameSettings {
			t.Fatalf("the connection's frames %v, want SETTINGS first", order)
		}
		//: the drain's word, on a connection it had not seen as HTTP/2.
		if !hasFrame(io.out[ref], corehttp2.FrameGoAway, 0) {
			t.Fatalf("the connection's frames %v, want a GOAWAY", order)
		}
		//: the stream the batch took is answered, the GOAWAY behind it.
		if c.stream != 0 {
			if !hasFrame(io.out[ref], corehttp2.FrameData, c.stream) {
				t.Fatalf("the connection's frames %v, want the answer on stream %d", order, c.stream)
			}
			if slices.Index(order, corehttp2.FrameGoAway) < slices.Index(order, corehttp2.FrameData) {
				t.Fatalf("the connection's frames %v, want the GOAWAY behind the answer", order)
			}
		}
		//: nothing in flight: left to the engine's drain, which closes it
		//: with a linger (#1609).
		if io.closed[ref] {
			t.Fatal("the adapter closed the drained connection: its close is the engine's drain's")
		}
		if a.InFlight(0, ref) {
			t.Fatal("the drained connection, nothing in flight, is still in flight")
		}
	}
	for _, c := range []tc{
		{name: "sniffing at the announcement", openFirst: true, data: preface},
		{name: "preface split across the announcement", openFirst: true, before: 10, data: preface},
		{name: "opened after the announcement", data: preface},
		{name: "opened after the announcement with a request", data: h2Request(), stream: 1},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// goAwayCodes is the code of each GOAWAY of out, a run of whole frames, in
// order.
func goAwayCodes(out []byte) []corehttp2.ErrCode {
	var codes []corehttp2.ErrCode
	for len(out) >= corehttp2.FrameHeaderLen {
		n := int(out[0])<<16 | int(out[1])<<8 | int(out[2])
		if len(out) < corehttp2.FrameHeaderLen+n {
			break
		}
		if corehttp2.FrameType(out[3]) == corehttp2.FrameGoAway && n >= 8 {
			codes = append(codes, corehttp2.ErrCode(binary.BigEndian.Uint32(out[corehttp2.FrameHeaderLen+4:])))
		}
		out = out[corehttp2.FrameHeaderLen+n:]
	}
	return codes
}

// TestLoopHandler_oneGoAwayAConnectionThroughTheDrain pins the GOAWAY the
// drain writes to one a connection (#1609), and a connection error's to the
// only one: an error a feed meets while the drain runs writes its GOAWAY,
// and neither the feed's drain GOAWAY nor the announcement adds a
// GOAWAY(NO_ERROR) after it, which a client could take for the final cause.
// Two feeds of one connection, or a feed after the announcement covered it,
// write one GOAWAY(NO_ERROR) between them.
func TestLoopHandler_oneGoAwayAConnectionThroughTheDrain(t *testing.T) {
	type tc struct {
		name string
		// run drives the adapter through the case and returns the connection's
		// output.
		run  func(t *testing.T) []byte
		want []corehttp2.ErrCode
	}
	preface := h2Request()[:len("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")+corehttp2.FrameHeaderLen]
	// badData is DATA on stream 0, a connection error (RFC 9113 §6.1).
	badData := http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FrameData, StreamID: 0, Length: 1})
	badData = append(badData, 'x')
	ping := http2.AppendFrameHeader(nil, corehttp2.FrameValue{Type: corehttp2.FramePing, Length: 8})
	ping = append(ping, "pingping"...)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	hello := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Body = []byte("ok") })
	feed := func(t *testing.T, a *http2.LoopHandler, io corenet.LoopIO, b []byte) {
		t.Helper()
		if n := a.OnData(io, ref, b); n != len(b) {
			t.Fatalf("OnData consumed %d of %d", n, len(b))
		}
	}
	runCase := func(t *testing.T, c tc) {
		out := c.run(t)
		if got := goAwayCodes(out); !slices.Equal(got, c.want) {
			t.Fatalf("GOAWAY codes %v, want %v; frames %v", got, c.want, frameTypes(out))
		}
	}
	for _, c := range []tc{
		{name: "a connection error in the feed that knows it HTTP/2", want: []corehttp2.ErrCode{corehttp2.ErrCodeProtocol}, run: func(t *testing.T) []byte {
			a := http2.HandleLoop(hello, http1.Parser{})
			a.OnStart(1)
			io := &postIO{batchIO: newBatchIO()}
			a.OnOpen(io, ref)
			a.Draining()
			io.runPosts()
			feed(t, a, io, append(append([]byte(nil), preface...), badData...))
			a.OnBatchEnd(io)
			return io.out[ref]
		}},
		{name: "a connection error in the announcing batch", want: []corehttp2.ErrCode{corehttp2.ErrCodeProtocol}, run: func(t *testing.T) []byte {
			a := http2.HandleLoop(hello, http1.Parser{})
			a.OnStart(1)
			io := &postIO{batchIO: newBatchIO()}
			a.OnOpen(io, ref)
			a.Draining()
			feed(t, a, io, append(append([]byte(nil), preface...), badData...))
			io.runPosts()
			a.OnBatchEnd(io)
			return io.out[ref]
		}},
		{name: "two feeds during the drain", want: []corehttp2.ErrCode{corehttp2.ErrCodeNoError}, run: func(t *testing.T) []byte {
			a := http2.HandleLoop(hello, http1.Parser{})
			a.OnStart(1)
			io := &postIO{batchIO: newBatchIO()}
			a.OnOpen(io, ref)
			a.Draining()
			io.runPosts()
			feed(t, a, io, preface)
			feed(t, a, io, ping)
			a.OnBatchEnd(io)
			return io.out[ref]
		}},
		{name: "a feed after the announcement covered it", want: []corehttp2.ErrCode{corehttp2.ErrCodeNoError}, run: func(t *testing.T) []byte {
			a := http2.HandleLoop(workerH{}, http1.Parser{})
			a.OnStart(1)
			jobs := &sentJobs{}
			a.OnOutbound(corenet.LoopOutboundValue{Lanes: []corenet.LoopOutbound{jobs}, OutputCap: 1 << 20})
			io := &postIO{batchIO: newBatchIO()}
			a.OnOpen(io, ref)
			feed(t, a, io, h2Request())
			a.OnBatchEnd(io)
			a.Draining()
			io.runPosts()
			if io.closed[ref] {
				t.Fatal("the announcement closed a connection with a stream in flight")
			}
			feed(t, a, io, ping)
			a.OnBatchEnd(io)
			if len(jobs.jobs) != 1 {
				t.Fatalf("%d jobs sent to the worker, want 1", len(jobs.jobs))
			}
			a.Worker().Work(0, jobs.jobs[0])
			a.OnDone(io, jobs.jobs[0])
			a.OnBatchEnd(io)
			return io.out[ref]
		}},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
