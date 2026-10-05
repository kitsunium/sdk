package telemetry

import (
	"encoding/binary"
	"sync/atomic"
	"time"
)

// The kinds.
const (
	// KindSpan is one unit of work that ended on a node.
	KindSpan Kind = 1
	// KindPhase is the process changing phase: starting, serving, draining,
	// stopped, failed — Op carries it.
	KindPhase Kind = 2
)

// The operations, in the order of their names in [OpNames]; zero is none.
const (
	OpNone Op = iota
	OpRequest
	OpCall
	OpRead
	OpWrite
	OpPublish
	OpDeliver
	OpTransition
	OpRun
	OpAuth
	OpSend
	OpDeliverMail
	OpSecret
	OpDispatch
	OpAsk
	OpHandle
	OpConnect
	OpCLI
	// The phases, for KindPhase.
	OpStarting
	OpServing
	OpDraining
	OpStopped
	OpFailed
	opCount
)

// The outcomes.
const (
	OutcomeOK    Outcome = 1
	OutcomeError Outcome = 2
)

// RecordSize is the size of one record on the wire.
const RecordSize int = 64

// recordVersion is the first byte of every record.
const recordVersion byte = 1

// Protocol names the wire this package speaks, in the handshake.
const Protocol string = "kit-telemetry/v1"

// The ring's bounds and default, in events.
const (
	MinBuffer     int = 64
	MaxBuffer     int = 1 << 20
	DefaultBuffer int = 4096
)

// writeTimeout bounds one write to a client: a client that does not read is
// dropped rather than waited for.
const writeTimeout time.Duration = 250 * time.Millisecond

// drainTick is how often the drainer looks at the ring when no producer woke
// it: the bound on how long an event waits when its wake was coalesced.
const drainTick time.Duration = 50 * time.Millisecond

// batchRecords is how many records one write carries at most.
const batchRecords int = 64

var (
	// OpNames are the operations' names, as framework/model spells them,
	// indexed by Op. The handshake sends them, so a reader never hard-codes
	// the table.
	OpNames = [opCount]string{
		"", "request", "call", "read", "write", "publish", "deliver", "transition", "run", "auth", "send",
		"deliver-mail", "secret", "dispatch", "ask", "handle", "connect", "cli",
		"starting", "serving", "draining", "stopped", "failed",
	}

	// Nop is the emitter that reports nothing: what a product holds unless it
	// was started with an exporter.
	Nop Emitter = nop{}
)

// slot is one cell of the exporter's ring: seq says whose turn it is — the
// producer at position p writes when seq == p, the drainer reads when
// seq == p+1.
type slot struct {
	seq atomic.Uint64
	ev  Event
}

// ring is the exporter's bounded buffer, a power of two of slots.
type ring []slot

// nop is the emitter of Nop.
type nop struct{}

// handshake is the first line a client reads.
type handshake struct {
	Protocol     string   `json:"protocol"`
	Product      string   `json:"product"`
	Binary       string   `json:"binary,omitempty"`
	Role         string   `json:"role,omitempty"`
	Instance     string   `json:"instance"`
	Revision     string   `json:"revision,omitempty"`
	ModelVersion int      `json:"modelVersion"`
	Digest       string   `json:"digest,omitempty"`
	RecordSize   int      `json:"recordSize"`
	Seq          uint64   `json:"seq"`
	Ops          []string `json:"ops"`
	Nodes        []string `json:"nodes"`
}

// counter is a padded atomic, so the producers' and the drainer's hot
// counters never share a cache line.
type counter struct {
	atomic.Uint64
	_ [56]byte
}

// Emit reports nothing.
func (nop) Emit(_ *Event) {}

// opOf is OpOf's body: decl_gen.go writes OpOf, from the
// design, as one call of it.
func opOf(name string) Op {
	for i, n := range OpNames {
		if n == name && i > 0 {
			return Op(i)
		}
	}
	return OpNone
}

// encode writes e, with its sequence number, as one record into b, which is
// RecordSize long. Little-endian, every field at a fixed offset:
//
//	0 version  1 kind  2 op  3 outcome  4 node  8 from  12 code
//	16 sequence  24 start  32 duration  40 trace id  56 span id
func encode(b []byte, seq uint64, e *Event) {
	b[0], b[1], b[2], b[3] = recordVersion, byte(e.Kind), byte(e.Op), byte(e.Outcome)
	binary.LittleEndian.PutUint32(b[4:], uint32(e.Node))
	binary.LittleEndian.PutUint32(b[8:], uint32(e.From))
	binary.LittleEndian.PutUint32(b[12:], e.Code)
	binary.LittleEndian.PutUint64(b[16:], seq)
	binary.LittleEndian.PutUint64(b[24:], uint64(e.Start))
	binary.LittleEndian.PutUint64(b[32:], uint64(e.Duration))
	copy(b[40:56], e.TraceID[:])
	copy(b[56:64], e.SpanID[:])
}

// Decode reads one record, as a client does: the event and its sequence
// number. ok is false for a record of another size or version.
func Decode(b []byte) (e Event, seq uint64, ok bool) {
	if len(b) != RecordSize || b[0] != recordVersion {
		return Event{}, 0, false
	}
	e.Kind, e.Op, e.Outcome = Kind(b[1]), Op(b[2]), Outcome(b[3])
	e.Node = NodeRef(binary.LittleEndian.Uint32(b[4:]))
	e.From = NodeRef(binary.LittleEndian.Uint32(b[8:]))
	e.Code = binary.LittleEndian.Uint32(b[12:])
	seq = binary.LittleEndian.Uint64(b[16:])
	e.Start = int64(binary.LittleEndian.Uint64(b[24:]))
	e.Duration = int64(binary.LittleEndian.Uint64(b[32:]))
	copy(e.TraceID[:], b[40:56])
	copy(e.SpanID[:], b[56:64])
	return e, seq, true
}
