// Package websocket — benchmarks for the two frame paths a caller and an
// attacker both drive.
//
// They are WHITE-BOX on purpose. NewConn takes an unexported config, and more
// importantly the subject is the frame READER, not the loopback socket
// underneath it: benchSocket replays a fixed script of client frames from
// memory, so a row's variance belongs to the parser rather than to the kernel
// scheduler. One loopback family at the end says what that leaves out.
//
// The refusal rows are not decoration. Every RFC 6455 MUST-fail this package
// enforces is a branch an attacker chooses to take, so a refusal that costs
// more than the acceptance it aborts is a DoS lever with a protocol-conformance
// justification. Each refusal is measured against BenchmarkFirstFrame/baseline,
// which is the same harness with no frame at all.
package websocket

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"io"
	stdnet "net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// benchAcceptCode is the "expect no error at all" marker benchFirstFrame's
// assertion takes. Zero is not a dotted quad any package can declare — the
// Major/Layer/Package/Serial layout gives every real code a non-zero Layer —
// so it cannot collide with a refusal a row might name.
const benchAcceptCode errs.Code = 0

// benchLoopbackDeadline bounds every read the loopback peer makes, so a server
// that stops answering fails the benchmark instead of hanging the suite.
const benchLoopbackDeadline time.Duration = 10 * time.Second

var (
	// msgSink, errSink and byteSink keep the compiler from proving a message,
	// an error or a frame unused and eliding the work that produced it.
	msgSink  corenet.WSMessageValue
	errSink  error
	byteSink []byte
	// benchMaskKey is the four-byte key RFC 6455 §5.1 requires on every
	// client-to-server frame, which is why the XOR runs over every inbound byte
	// this server will ever see. It is fixed rather than random because the XOR
	// costs the same either way and a fixed key makes a script reproducible.
	benchMaskKey = [corenet.WSMaskLen]byte{0x37, 0xFA, 0x21, 0x3D}
)

// benchAddr is the address a benchSocket reports. Nothing under test reads it.
type benchAddr struct{}

// Network names the fake socket's network.
func (benchAddr) Network() string {
	//: never inspected; the connection only stores what net/http handed it.
	return "bench"
}

// String renders the fake socket's address.
func (benchAddr) String() string {
	//: never inspected, for the same reason.
	return "bench"
}

// benchSocket is the net.Conn a benchmarked connection reads from and writes
// to. It replays a fixed script of bytes and discards everything written back.
//
// What it deliberately makes free is stated in BENCH.md rather than hidden
// here: Write is a memcpy-free no-op and SetWriteDeadline returns immediately,
// where a real socket pays a syscall for the first and a poller update for the
// second. The receive rows are unaffected — nothing on the inbound path writes
// — and the send rows are therefore a floor, not a wire measurement.
type benchSocket struct {
	// script is the byte stream the connection reads. A steady-state benchmark
	// gives it exactly one whole message, so repeating it yields a seamless
	// infinite frame stream with no partial frame at the seam.
	script []byte
	// offset is how far the connection has read into the script.
	offset int
	// repeat wraps back to the start when the script runs out. A one-shot
	// benchmark leaves it FALSE on purpose: the script then ends in io.EOF, so
	// a refusal that read the payload before checking the ceiling would report
	// WSConnClosed instead of the code its row names, and the assertion after
	// the loop would fail rather than publish a number for the wrong path.
	repeat bool
}

// Read serves the next bytes of the script.
func (s *benchSocket) Read(p []byte) (n int, err error) {
	//: the script is exhausted.
	if s.offset >= len(s.script) {
		//: a one-shot socket ends here; a repeating one wraps.
		if !s.repeat {
			//: the peer stopped sending.
			return 0, io.EOF
		}
		s.offset = 0
	}
	n = copy(p, s.script[s.offset:])
	s.offset += n
	//: whatever fits, up to the wrap.
	return n, nil
}

// Write discards one frame the connection put on the wire.
func (s *benchSocket) Write(p []byte) (n int, err error) {
	//: a real socket costs a syscall here; the type comment says so.
	return len(p), nil
}

// Close ends the fake socket, which owns nothing.
func (s *benchSocket) Close() error {
	//: there is nothing to release.
	return nil
}

// LocalAddr reports the placeholder local address.
func (s *benchSocket) LocalAddr() stdnet.Addr {
	//: never read by the connection.
	return benchAddr{}
}

// RemoteAddr reports the placeholder peer address.
func (s *benchSocket) RemoteAddr() stdnet.Addr {
	//: never read by the connection.
	return benchAddr{}
}

// SetDeadline accepts the deadline NewConn clears at the upgrade.
func (s *benchSocket) SetDeadline(t time.Time) error {
	//: free here, a poller update on a real socket.
	return nil
}

// SetReadDeadline accepts a read deadline the connection never sets.
func (s *benchSocket) SetReadDeadline(t time.Time) error {
	//: free here, a poller update on a real socket.
	return nil
}

// SetWriteDeadline accepts the per-frame write bound writeLocked refreshes.
func (s *benchSocket) SetWriteDeadline(t time.Time) error {
	//: free here, a poller update on a real socket.
	return nil
}

// benchPayload builds an n-byte ASCII payload, outside every timed loop.
func benchPayload(n int) []byte {
	out := make([]byte, 0, n)
	//: appended rather than index-assigned, so the value varies per byte and
	//: the compiler cannot turn the fill into a memset the mask would then
	//: measure against unrealistically uniform input.
	for i := range n {
		out = append(out, byte('a'+i%26))
	}
	//: a payload no pass over it can short-circuit.
	return out
}

// benchFrame assembles one frame with full control over every bit, including
// the ones no conforming client would ever set.
//
// The length is written in the MINIMAL encoding RFC 6455 §5.2 requires; the
// non-minimal rows build their own header bytes, because a builder that could
// emit a non-minimal length would also emit one by accident.
func benchFrame(final bool, rsv byte, op byte, masked bool, payload []byte) []byte {
	first := op | (rsv << 4)
	//: FIN says this frame completes its message.
	if final {
		first |= 0x80
	}
	out := []byte{first}
	//: the shortest form that can carry the length.
	switch size := len(payload); {
	//: the 7-bit field carries it outright.
	case size < 126:
		out = append(out, byte(size))
	//: the 16-bit extension.
	case size <= 0xFFFF:
		out = append(out, 126)
		out = binary.BigEndian.AppendUint16(out, uint16(size))
	//: the 64-bit extension.
	default:
		out = append(out, 127)
		out = binary.BigEndian.AppendUint64(out, uint64(size))
	}
	//: an unmasked frame is exactly what §5.1 forbids a client to send, which
	//: is why the builder must be able to produce one.
	if !masked {
		//: no key, no transform.
		return append(out, payload...)
	}
	out[1] |= 0x80
	out = append(out, benchMaskKey[:]...)
	body := slices.Clone(payload)
	corenet.ApplyWSMask(body, benchMaskKey)
	//: masked, as a client must.
	return append(out, body...)
}

// benchClientFrame assembles one well-formed masked client frame.
//
// It is the only direction this server accepts: corenet.AppendWSFrame writes
// the SERVER's unmasked direction, so a benchmark built on it would feed the
// reader bytes §5.1 requires it to refuse.
func benchClientFrame(op corenet.WSOpCode, final bool, payload []byte) []byte {
	//: RSV clear — no extension is negotiated, and a set bit is a refusal.
	return benchFrame(final, 0, byte(op), true, payload)
}

// benchFragments splits one message of total bytes into parts masked frames.
//
// The mask index restarts at zero for each frame, which is what §5.3 says and
// what makes a fragmented message cost one extra key set-up per fragment
// rather than one per message.
func benchFragments(op corenet.WSOpCode, total, parts int) []byte {
	payload := benchPayload(total)
	chunk := total / parts
	script := make([]byte, 0, total+parts*corenet.WSMaxHeaderLen)
	//: the first fragment carries the opcode; every later one continues it, and
	//: only the last one sets FIN.
	for i := range parts {
		end := (i + 1) * chunk
		//: the last fragment absorbs whatever the division left over.
		if i == parts-1 {
			end = total
		}
		frameOp := corenet.WSContinuation
		//: the opcode is carried by the FIRST frame only.
		if i == 0 {
			frameOp = op
		}
		script = append(script, benchClientFrame(frameOp, i == parts-1, payload[i*chunk:end])...)
	}
	//: one whole message, in parts frames.
	return script
}

// benchConfig resolves the option set every benchmark runs under.
//
// WithoutPing is not a performance trick. The heartbeat is a thirty-second
// ticker, so it contributes nothing to a per-operation number — but it does
// leave one parked goroutine per connection, and a one-shot row creates
// millions of connections.
func benchConfig(b *testing.B, opts ...Option) config {
	b.Helper()
	all := append([]Option{WithoutPing()}, opts...)
	cfg, err := resolve(all)
	//: a refused option set would make every row below measure nothing.
	if err != nil {
		b.Fatalf("resolve: %v", err)
	}
	//: the resolved set, defaults filled in.
	return cfg
}

// benchSteadyReceive measures Receive over ONE connection reading an endless
// repetition of script, which is what a live connection actually does: the
// reassembly buffer is grown once and reused for the connection's whole life,
// so the steady state is the state that matters.
func benchSteadyReceive(b *testing.B, script []byte, perOp int64, opts ...Option) {
	b.Helper()
	cfg := benchConfig(b, opts...)
	socket := &benchSocket{script: script, repeat: true}
	conn := NewConn(socket, bufio.NewReader(socket), "", &cfg, nil)
	b.SetBytes(perOp)
	b.ReportAllocs()
	b.ResetTimer()
	//: one whole message per iteration, control frames served on the way.
	for b.Loop() {
		msgSink, errSink = conn.Receive()
	}
	b.StopTimer()
	//: a row that stopped reading measured the terminal check, not the reader.
	if errSink != nil {
		b.Fatalf("Receive: %v", errSink)
	}
	//: the connection's goroutines are joined before the next row starts.
	if cerr := conn.Close(); cerr != nil {
		b.Fatalf("Close: %v", cerr)
	}
}

// benchFreshConn builds a connection with fresh reader state that owns NO
// goroutine.
//
// It bypasses NewConn deliberately, and the reason is a measurement the first
// attempt got wrong: with NewConn in the loop, the baseline row —
// which reads no frame at all — came out at 4 800 ns against 3 927 ns for the
// accept row that reads one, an arithmetic impossibility. The goroutine watch()
// starts costs microseconds and varies by more than any refusal it was supposed
// to floor, so the whole family was measuring the Go scheduler. The lifecycle
// is measured once, on its own, by BenchmarkConnLifecycle; everything here is
// the frame reader.
//
// Every field the read path touches is set exactly as NewConn sets it. wbuf is
// handed a warm buffer rather than a new one because every connection's is warm
// after its first frame, and a 512-byte allocation per iteration would be the
// largest term in a row measuring an eight-byte header.
func benchFreshConn(socket *benchSocket, reader *bufio.Reader, cfg *config, warm []byte) *Conn {
	//: no watcher and no heartbeat: neither is on the read path.
	return &Conn{
		raw:  socket,
		br:   reader,
		cfg:  *cfg,
		wbuf: warm[:0],
		done: make(chan struct{}),
	}
}

// benchFirstFrame measures ONE Receive on a connection that has just been
// handed the socket.
//
// Every refusal needs this shape rather than a steady-state loop, because a
// refusal TERMINATES the connection — a second Receive on the same Conn returns
// from the terminal check without parsing a byte, so a looping benchmark would
// report the cost of a closed channel and publish it as the refusal's price.
// The accept rows use the identical harness, so a refusal and the acceptance it
// aborts are directly comparable, and BenchmarkFirstFrame/baseline is the
// per-iteration floor to subtract from both.
func benchFirstFrame(b *testing.B, script []byte, want errs.Code, opts ...Option) {
	b.Helper()
	cfg := benchConfig(b, opts...)
	reader := bufio.NewReader(bytes.NewReader(nil))
	warm := make([]byte, 0, initialWriteCapacity)
	b.ReportAllocs()
	b.ResetTimer()
	//: one connection per iteration, because a refusal ends the one it got.
	for b.Loop() {
		socket := &benchSocket{script: script}
		//: Reset rather than NewReader: net/http hands over a POOLED reader, so
		//: allocating a fresh 4 KiB buffer per iteration would measure the
		//: pool's absence rather than this package's cost.
		reader.Reset(socket)
		conn := benchFreshConn(socket, reader, &cfg, warm)
		msgSink, errSink = conn.Receive()
	}
	b.StopTimer()
	benchAssertOutcome(b, want)
}

// benchAssertOutcome pins what a one-shot row actually measured.
//
// A refusal row whose error is not the refusal it names is measuring some other
// path entirely — most usefully, a ceiling check moved AFTER the read reports
// WSConnClosed here, because the script carries a header and no payload.
func benchAssertOutcome(b *testing.B, want errs.Code) {
	b.Helper()
	//: the accept rows expect a message and no error at all.
	if want == benchAcceptCode {
		//: anything else means the row measured a refusal it did not name.
		if errSink != nil {
			b.Fatalf("Receive: unexpected error %v", errSink)
		}
		//: accepted, as the row claims.
		return
	}
	//: the refusal rows expect exactly the code they are named for.
	if !errs.HasCode(errSink, want) {
		b.Fatalf("Receive: got %v, want code %v", errSink, want)
	}
}

// BenchmarkReceive is the inbound steady state: one whole message per
// iteration, arriving in one masked frame.
//
// The binary rows are parse + unmask; the text rows add the UTF-8 pass over the
// REASSEMBLED message, so the difference between the two families at one size
// is what §8.1 costs a text-heavy connection.
func BenchmarkReceive(b *testing.B) {
	sizes := []int{64, 4 << 10, 64 << 10, 1 << 20}
	//: the two data opcodes are the whole space a message can occupy.
	for _, size := range sizes {
		b.Run("binary/"+benchSizeName(size), func(b *testing.B) {
			benchSteadyReceive(b, benchClientFrame(corenet.WSBinary, true, benchPayload(size)), int64(size))
		})
	}
	//: the same sizes again, paying for the UTF-8 judgement.
	for _, size := range sizes {
		b.Run("text/"+benchSizeName(size), func(b *testing.B) {
			benchSteadyReceive(b, benchClientFrame(corenet.WSText, true, benchPayload(size)), int64(size))
		})
	}
}

// BenchmarkReceiveFragmented holds the message size fixed at 64 KiB and varies
// only how many frames carry it.
//
// Fragmentation is the sender's private choice of chunk size, so a server does
// not get to refuse it — which makes this the axis a peer can turn against the
// reader for free. The delta across the rows is the per-FRAME cost: a header
// read, a parse, a fragmentation check and a mask key set-up.
func BenchmarkReceiveFragmented(b *testing.B) {
	const total int = 64 << 10
	parts := []int{1, 2, 8, 16, 32, 256}
	//: one message, the same bytes, split more finely each row.
	for _, count := range parts {
		b.Run(benchPartsName(count), func(b *testing.B) {
			benchSteadyReceive(b, benchFragments(corenet.WSBinary, total, count), int64(total))
		})
	}
}

// BenchmarkReceiveControl measures a control frame injected BETWEEN two
// fragments, which is the placement §5.4 exists to permit and the one that
// costs the reader most: the Ping must be answered from a separate buffer
// without disturbing the message being assembled.
//
// Each row carries the identical message in two fragments; only the frame
// between them changes, so the delta is one served control frame plus the Pong
// that answers it.
//
// The message is 256 bytes and not a realistic one ON PURPOSE. The first
// version carried 32 KiB, which put the whole quantity being measured — a
// control frame — at one per cent of the row, under a run-to-run spread of two
// per cent: the ping_empty delta came out at 293 ns with the none row's own
// five runs spanning 533 ns, so the number was noise wearing a decimal point.
// A control frame costs what it costs regardless of the message it interrupts,
// so the message is shrunk until the answer is legible.
func BenchmarkReceiveControl(b *testing.B) {
	const total int = 256
	two := benchFragments(corenet.WSBinary, total, 2)
	split := len(benchClientFrame(corenet.WSBinary, false, benchPayload(total/2)))
	rows := []struct {
		name    string
		control []byte
	}{
		{name: "none", control: nil},
		{name: "ping_empty", control: benchClientFrame(corenet.WSPing, true, nil)},
		{name: "ping_125B", control: benchClientFrame(corenet.WSPing, true, benchPayload(corenet.WSMaxControlPayload))},
		{name: "pong_empty", control: benchClientFrame(corenet.WSPong, true, nil)},
	}
	//: the control frame is spliced at the fragment boundary, never inside a
	//: frame — a control frame interrupts a MESSAGE, not a frame.
	for _, row := range rows {
		benchControlRow(b, row.name, slices.Concat(two[:split], row.control, two[split:]), int64(total))
	}
}

// benchControlRow runs one control-frame row.
//
// The script is a PARAMETER rather than a loop variable the sub-benchmark's
// closure captures: a captured slice escapes to the heap once per row, which is
// what KTN-VAR-ESCAPECLOSURE is about.
func benchControlRow(b *testing.B, name string, script []byte, perOp int64) {
	b.Helper()
	//: the sub-benchmark closes over nothing that outlives this call.
	b.Run(name, func(b *testing.B) {
		benchSteadyReceive(b, script, perOp)
	})
}

// BenchmarkSend is the outbound half: one whole frame per iteration, written
// under the write lock into the buffer the connection reuses for its life.
//
// The socket write itself is free in this harness (see benchSocket), so these
// rows are the framing cost plus the per-frame deadline refresh — a floor, and
// the report says so.
func BenchmarkSend(b *testing.B) {
	sizes := []int{64, 4 << 10, 64 << 10}
	//: binary first, because it is the row with nothing but framing in it.
	for _, size := range sizes {
		b.Run("binary/"+benchSizeName(size), func(b *testing.B) {
			benchSend(b, corenet.WSMessageValue{Binary: true, Data: benchPayload(size)}, int64(size))
		})
	}
	//: text pays the outbound UTF-8 check §8.1 makes symmetrical.
	for _, size := range sizes {
		b.Run("text/"+benchSizeName(size), func(b *testing.B) {
			benchSend(b, corenet.WSMessageValue{Data: benchPayload(size)}, int64(size))
		})
	}
}

// benchSend measures Send over one connection whose write buffer is warm.
func benchSend(b *testing.B, message corenet.WSMessageValue, perOp int64) {
	b.Helper()
	cfg := benchConfig(b)
	socket := &benchSocket{}
	conn := NewConn(socket, bufio.NewReader(socket), "", &cfg, nil)
	b.SetBytes(perOp)
	b.ReportAllocs()
	b.ResetTimer()
	//: one whole frame per iteration; the SDK never fragments what it sends.
	for b.Loop() {
		errSink = conn.Send(message)
	}
	b.StopTimer()
	//: a failed write terminates the connection, so a row that failed measured
	//: the terminal check for most of its iterations.
	if errSink != nil {
		b.Fatalf("Send: %v", errSink)
	}
	//: the connection's goroutines are joined before the next row starts.
	if cerr := conn.Close(); cerr != nil {
		b.Fatalf("Close: %v", cerr)
	}
}

// BenchmarkFirstFrame is the control family every refusal is read against.
//
// baseline is the harness with NO frame at all — the per-iteration Conn, its
// done channel and the socket, and nothing read. Subtracting it from any row in
// this family or in BenchmarkRefusal leaves the frame reader's own cost, which
// is the only quantity the two families can honestly be compared on.
func BenchmarkFirstFrame(b *testing.B) {
	b.Run("baseline", benchFirstFrameBaseline)
	b.Run("accept_binary/64", func(b *testing.B) {
		benchFirstFrame(b, benchClientFrame(corenet.WSBinary, true, benchPayload(64)), benchAcceptCode)
	})
	b.Run("accept_binary/4096", func(b *testing.B) {
		benchFirstFrame(b, benchClientFrame(corenet.WSBinary, true, benchPayload(4<<10)), benchAcceptCode)
	})
	b.Run("accept_text/4096", func(b *testing.B) {
		benchFirstFrame(b, benchClientFrame(corenet.WSText, true, benchPayload(4<<10)), benchAcceptCode)
	})
	b.Run("accept_bounded/4096", func(b *testing.B) {
		benchFirstFrame(b, benchClientFrame(corenet.WSBinary, true, benchPayload(4<<10)),
			benchAcceptCode, MaxFrameSize(4<<10), MaxMessageSize(6<<10))
	})
}

// benchFirstFrameBaseline measures the harness with no frame read at all: the
// per-iteration Conn, its done channel and the socket. It is the floor under
// every first-frame and refusal row.
func benchFirstFrameBaseline(b *testing.B) {
	cfg := benchConfig(b)
	reader := bufio.NewReader(bytes.NewReader(nil))
	warm := make([]byte, 0, initialWriteCapacity)
	b.ReportAllocs()
	b.ResetTimer()
	//: construct the connection state and read nothing.
	for b.Loop() {
		socket := &benchSocket{}
		reader.Reset(socket)
		msgSink.Data = benchFreshConn(socket, reader, &cfg, warm).msg
	}
}

// BenchmarkConnLifecycle measures what a REAL connection adds on top of the
// reader: NewConn, the watcher goroutine it owns, terminate and the join that
// waits for that goroutine to return.
//
// It is one row rather than a family because it is the same number for every
// script — nothing here depends on what arrives — and because it is the term
// that has to be added back to every BenchmarkFirstFrame row to get the cost of
// a connection that serves exactly one message and ends.
func BenchmarkConnLifecycle(b *testing.B) {
	cfg := benchConfig(b)
	reader := bufio.NewReader(bytes.NewReader(nil))
	b.ReportAllocs()
	b.ResetTimer()
	//: construct and tear down a whole connection, reading nothing.
	for b.Loop() {
		socket := &benchSocket{}
		reader.Reset(socket)
		conn := NewConn(socket, reader, "", &cfg, nil)
		conn.terminate()
		conn.join()
	}
}

// BenchmarkRefusal measures every RFC 6455 MUST-fail this package enforces.
//
// A refusal is a branch the ATTACKER chooses, so its price is the one that
// decides whether conformance is also a denial-of-service lever. Two rows carry
// no payload at all on purpose — frame_ceiling and message_ceiling — because
// their whole claim is that the bound is checked against the length the peer
// ANNOUNCED, before a byte is read: a check moved after the read finds io.EOF
// and reports WSConnClosed, and benchAssertOutcome fails the row.
func BenchmarkRefusal(b *testing.B) {
	//: the framing refusals, each cited to the section it enforces.
	for _, row := range benchFramingRefusals() {
		b.Run(row.name, func(b *testing.B) {
			benchFirstFrame(b, row.script, row.want)
		})
	}
	//: the ceiling refusals, which are about a number rather than a shape.
	b.Run("frame_ceiling", func(b *testing.B) {
		benchFirstFrame(b, benchAnnouncedLength(corenet.WSBinary, (2<<20)+1), corenet.CodeWSMessageTooLarge)
	})
	b.Run("message_ceiling", func(b *testing.B) {
		script := slices.Concat(
			benchClientFrame(corenet.WSBinary, false, benchPayload(4<<10)),
			benchAnnouncedLength(corenet.WSContinuation, 4<<10))
		benchFirstFrame(b, script, corenet.CodeWSMessageTooLarge, MaxFrameSize(4<<10), MaxMessageSize(6<<10))
	})
	//: the payload refusal, which is the one that costs a FULL parse first.
	b.Run("invalid_utf8/4096", func(b *testing.B) {
		payload := benchPayload(4 << 10)
		payload[len(payload)-1] = 0xC3
		benchFirstFrame(b, benchClientFrame(corenet.WSText, true, payload), corenet.CodeWSInvalidPayload)
	})
}

// benchRefusalRow is one MUST-fail: the bytes that provoke it and the code the
// reader must answer with.
type benchRefusalRow struct {
	// name is the row's benchmark name.
	name string
	// script is the bytes a hostile or broken peer puts on the wire.
	script []byte
	// want is the dotted-quad code the refusal must carry.
	want errs.Code
}

// benchFramingRefusals builds the shape refusals — the ones ParseWSFrameHeader
// and trackFragment decide without reading a payload.
func benchFramingRefusals() []benchRefusalRow {
	small := benchPayload(8)
	//: every row is bytes assembled by hand, because a script written through
	//: this package's own writer could only produce frames it accepts.
	return []benchRefusalRow{
		{
			name: "unmasked_client_frame", want: corenet.CodeWSProtocolViolation,
			script: benchFrame(true, 0, byte(corenet.WSBinary), false, small),
		},
		{
			name: "reserved_bit", want: corenet.CodeWSProtocolViolation,
			script: benchFrame(true, 0x4, byte(corenet.WSBinary), true, small),
		},
		{
			name: "reserved_opcode", want: corenet.CodeWSProtocolViolation,
			script: benchFrame(true, 0, 0x3, true, small),
		},
		{
			name: "oversized_control_frame", want: corenet.CodeWSProtocolViolation,
			script: benchClientFrame(corenet.WSPing, true, benchPayload(corenet.WSMaxControlPayload+1)),
		},
		{
			name: "fragmented_control_frame", want: corenet.CodeWSProtocolViolation,
			script: benchClientFrame(corenet.WSPing, false, small),
		},
		{
			name: "non_minimal_length_16", want: corenet.CodeWSProtocolViolation,
			script: benchNonMinimal16(small),
		},
		{
			name: "non_minimal_length_64", want: corenet.CodeWSProtocolViolation,
			script: benchNonMinimal64(small),
		},
		{
			name: "orphan_continuation", want: corenet.CodeWSProtocolViolation,
			script: benchClientFrame(corenet.WSContinuation, true, small),
		},
		{
			name: "interrupted_fragmentation", want: corenet.CodeWSProtocolViolation,
			script: slices.Concat(
				benchClientFrame(corenet.WSBinary, false, small),
				benchClientFrame(corenet.WSText, true, small)),
		},
	}
}

// benchAnnouncedLength builds a header that ANNOUNCES length and carries no
// payload at all.
//
// It is the shape of the cheapest attack the protocol allows: a dozen bytes of
// header claiming an allocation the peer never intends to fund. The length is
// written in the MINIMAL width — a 4 KiB length spelled in sixty-four bits is
// refused as a non-minimal encoding before any ceiling is consulted, which is
// how the first draft of the message_ceiling row measured §5.2 and reported it
// as a ceiling check.
func benchAnnouncedLength(op corenet.WSOpCode, length uint64) []byte {
	out := []byte{0x80 | byte(op)}
	//: the shortest form that can carry the announced length.
	switch {
	//: the 7-bit field carries it outright.
	case length < 126:
		out = append(out, 0x80|byte(length))
	//: the 16-bit extension.
	case length <= 0xFFFF:
		out = append(out, 0x80|126)
		out = binary.BigEndian.AppendUint16(out, uint16(length))
	//: the 64-bit extension.
	default:
		out = append(out, 0x80|127)
		out = binary.BigEndian.AppendUint64(out, length)
	}
	//: the mask key still has to be there — an unmasked frame would be refused
	//: by §5.1 first, and the row would measure that instead.
	return append(out, benchMaskKey[:]...)
}

// benchNonMinimal16 spells a short payload's length in the 16-bit form, which
// §5.2 forbids: a second spelling of the same frame is exactly the ambiguity a
// length-prefixed protocol cannot afford between two parsers that disagree.
func benchNonMinimal16(payload []byte) []byte {
	out := []byte{0x80 | byte(corenet.WSBinary), 0x80 | 126}
	out = binary.BigEndian.AppendUint16(out, uint16(len(payload)))
	out = append(out, benchMaskKey[:]...)
	body := slices.Clone(payload)
	corenet.ApplyWSMask(body, benchMaskKey)
	//: a frame that is legal in every way except how it wrote its length.
	return append(out, body...)
}

// benchNonMinimal64 does the same one step up: a length that fits sixteen bits
// spelled in sixty-four.
func benchNonMinimal64(payload []byte) []byte {
	out := []byte{0x80 | byte(corenet.WSBinary), 0x80 | 127}
	out = binary.BigEndian.AppendUint64(out, uint64(len(payload)))
	out = append(out, benchMaskKey[:]...)
	body := slices.Clone(payload)
	corenet.ApplyWSMask(body, benchMaskKey)
	//: the same frame, the same refusal, one length field wider.
	return append(out, body...)
}

// benchSizeName renders a payload size as a benchmark name component.
func benchSizeName(size int) string {
	//: the raw byte count, so a reader can divide two rows without first
	//: decoding a unit suffix.
	return strconv.Itoa(size)
}

// benchPartsName renders a fragment count as a benchmark name component.
func benchPartsName(count int) string {
	//: "x" reads as "times", which is what the row varies.
	return strconv.Itoa(count) + "x"
}

// BenchmarkLoopback is the anchor: a full echo round trip over a real TCP
// socket, through the real Upgrade, with the kernel in the path.
//
// It exists so the in-memory rows above can be read honestly. They measure the
// frame reader; this measures what a caller experiences, and the ratio between
// the two says how much of a WebSocket message is this package's work and how
// much is everything else.
func BenchmarkLoopback(b *testing.B) {
	sizes := []int{64, 4 << 10}
	//: the same two sizes the in-memory rows start with.
	for _, size := range sizes {
		b.Run(benchSizeName(size), func(b *testing.B) {
			benchLoopback(b, size)
		})
	}
}

// BenchmarkLoopbackUpgrade measures what it costs an attacker to REACH a
// refusal: a TCP connect, the RFC 6455 §4.2 opening handshake including the
// SHA-1 accept digest, and the close.
//
// It is the denominator every BenchmarkRefusal row belongs over. A refusal that
// is expensive relative to the parse it aborts is still not a lever if getting
// to it costs an order of magnitude more, and that comparison cannot be made
// from the in-memory rows alone.
func BenchmarkLoopbackUpgrade(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(benchEchoHandler))
	b.Cleanup(srv.Close)
	b.ReportAllocs()
	b.ResetTimer()
	//: one whole connection per iteration, carrying no frame at all.
	for b.Loop() {
		peer, _ := benchDial(b, srv)
		swallowErr(peer.Close())
	}
}

// benchLoopback runs one echo round trip per iteration over loopback TCP.
func benchLoopback(b *testing.B, size int) {
	b.Helper()
	srv := httptest.NewServer(http.HandlerFunc(benchEchoHandler))
	b.Cleanup(srv.Close)
	peer, reader := benchDial(b, srv)
	b.Cleanup(func() { swallowErr(peer.Close()) })
	frame := benchClientFrame(corenet.WSBinary, true, benchPayload(size))
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	//: one message out, one message back.
	for b.Loop() {
		//: a write that fails leaves the read below to report it.
		if _, werr := peer.Write(frame); werr != nil {
			b.Fatalf("write: %v", werr)
		}
		byteSink = benchReadServerFrame(b, reader)
	}
	b.StopTimer()
}

// benchEchoHandler upgrades and echoes every message back, which is the
// smallest handler that exercises both directions.
func benchEchoHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := Upgrade(w, r, WithoutPing())
	//: Upgrade has already written the refusal; the handler owes nothing.
	if err != nil {
		//: nothing left to serve.
		return
	}
	//: the socket is the handler's from here, so the handler closes it. The
	//: closure is not decoration: a deferred CALL would evaluate Close at the
	//: defer statement and end the connection before the first frame.
	defer func() { swallowErr(conn.Close()) }()
	//: echo until the peer or the benchmark ends the connection.
	for {
		msg, rerr := conn.Receive()
		//: the terminal error ends the handler.
		if rerr != nil {
			//: the connection is over.
			return
		}
		//: an echo proves the message survived reassembly intact.
		if serr := conn.Send(msg); serr != nil {
			//: the peer went away mid-echo.
			return
		}
	}
}

// benchDial performs a conforming opening handshake and returns the socket with
// the reader every server frame is read through.
func benchDial(b *testing.B, srv *httptest.Server) (peer stdnet.Conn, reader *bufio.Reader) {
	b.Helper()
	host := strings.TrimPrefix(srv.URL, "http://")
	peer, err := stdnet.Dial("tcp", host)
	//: a benchmark that cannot connect has nothing to measure.
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	//: the socket is the CALLER's to close. Registering a cleanup here would
	//: pin one file descriptor per iteration for the whole row, and
	//: BenchmarkLoopbackUpgrade dials once per iteration.
	//: every read is bounded so a silent server fails rather than hangs.
	if derr := peer.SetDeadline(time.Now().Add(benchLoopbackDeadline)); derr != nil {
		b.Fatalf("deadline: %v", derr)
	}
	request := "GET / HTTP/1.1\r\nHost: " + host + "\r\n" +
		"Upgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(make([]byte, 16)) + "\r\n\r\n"
	//: the handshake itself is outside every timed loop.
	if _, werr := peer.Write([]byte(request)); werr != nil {
		b.Fatalf("handshake write: %v", werr)
	}
	reader = bufio.NewReader(peer)
	benchExpectSwitching(b, reader)
	//: upgraded.
	return peer, reader
}

// benchExpectSwitching reads the 101 response and fails the benchmark on
// anything else, so a row can never measure a refused handshake.
func benchExpectSwitching(b *testing.B, reader *bufio.Reader) {
	b.Helper()
	resp, err := http.ReadResponse(reader, nil)
	//: an unreadable response means the server never switched.
	if err != nil {
		b.Fatalf("handshake response: %v", err)
	}
	//: the status is the whole assertion; the body is empty by definition.
	if resp.StatusCode != http.StatusSwitchingProtocols {
		b.Fatalf("handshake status = %d, want 101", resp.StatusCode)
	}
}

// benchReadServerFrame reads one whole frame the server sent.
//
// The server never masks — §5.1 forbids it — so there is no key to strip, and
// the absence of one is itself an assertion: a masked server frame would make
// the length parse below read the payload four bytes late.
func benchReadServerFrame(b *testing.B, reader *bufio.Reader) []byte {
	b.Helper()
	var head [corenet.WSMinHeaderLen]byte
	//: the two fixed bytes announce how many more the header needs.
	if _, err := io.ReadFull(reader, head[:]); err != nil {
		b.Fatalf("read header: %v", err)
	}
	size := benchServerLength(b, reader, head[1])
	payload := make([]byte, size)
	//: a short payload means the server announced more than it sent.
	if _, err := io.ReadFull(reader, payload); err != nil {
		b.Fatalf("read payload: %v", err)
	}
	//: one whole frame.
	return payload
}

// benchServerLength reads whatever extended length the marker calls for.
func benchServerLength(b *testing.B, reader *bufio.Reader, marker byte) int {
	b.Helper()
	var extended [8]byte
	//: the three width forms RFC 6455 §5.2 defines.
	switch marker {
	//: the 16-bit extension.
	case 126:
		//: a short read here would desynchronise every later frame.
		if _, err := io.ReadFull(reader, extended[:2]); err != nil {
			b.Fatalf("read 16-bit length: %v", err)
		}
		//: the announced size.
		return int(binary.BigEndian.Uint16(extended[:2]))
	//: the 64-bit extension.
	case 127:
		//: a short read here would desynchronise every later frame.
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			b.Fatalf("read 64-bit length: %v", err)
		}
		//: the announced size.
		return int(binary.BigEndian.Uint64(extended[:]))
	//: the 7-bit field carries the length outright.
	default:
		//: the announced size.
		return int(marker)
	}
}
