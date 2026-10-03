//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /framework/telemetry .

// Package telemetry is the port a running product reports what it does on,
// and the exporter that hands it to a tool attached to the product (ADR 0149).
//
// It is not the trace domain. pkg/v1/trace builds spans a backend stores and
// carries its context through context.WithValue; this port carries nothing
// through a context, builds nothing a caller keeps, and costs a producer one
// fixed-size copy into a bounded ring — zero allocations, pinned by a test
// (testing.AllocsPerRun) and measured in BENCH.md:
//
//	emitter.Emit(&telemetry.Event{Kind: telemetry.KindSpan, Op: telemetry.OpRequest,
//		Node: ref, Start: start, Duration: d, Outcome: telemetry.OutcomeOK})
//
// # A closed schema
//
// An [Event] holds numbers only: enumerations ([Kind], [Op], [Outcome]), a
// dotted-quad error code, instants, identifiers a trace already carries, and
// NODE REFERENCES — indexes into the table of node IDs the exporter sends once,
// in its handshake, each one checked against the ID grammar
// (framework/model.ParseID). There is no free text, so there is no field a
// person's name, an address or a request body could travel in: the schema
// itself is what keeps personal data out, in every environment.
//
// # Inert unless configured
//
// [Nop] is the emitter a product holds by default: Emit returns at once. An
// [Exporter] exists only when the product was started with one, and it only
// EMITS — it never reads what a client sends, so nothing reaches the product
// through it. It listens on a private socket (pkg/v1/proc/ipc, ADR 0148): the
// product's account and the UIDs and GIDs the deployment names, under the
// runtime directory; a stale socket is replaced, a live one refused. A client
// connects, reads one handshake line — product, binary, role, instance,
// revision, model version, design digest, the node table, the first sequence
// number — then fixed-size records ([RecordSize] bytes each) for as long as
// it reads. A slow client loses records, counted; the product never waits.
package telemetry

import (
	"context"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/proc/ipc"
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

// Kind says what an event reports.
type Kind uint8

// Op is what the work was: the span operations of framework/model.
type Op uint8

// Outcome says how the work ended.
type Outcome uint8

// NodeRef is a node's index in the handshake's node table, from 1; zero is
// "no node".
type NodeRef uint32

// Event is one report. Its fields are numbers only — see the package comment:
// there is no field a person's data could travel in, in any environment.
type Event struct {
	Kind    Kind
	Op      Op
	Outcome Outcome
	// Node is where the work ran; From, the node it came from, if any.
	Node, From NodeRef
	// Code is the dotted-quad code of the error that ended it, 0 for none.
	Code uint32
	// Start is when it began, in Unix nanoseconds; Duration, how long it took.
	Start, Duration int64
	// TraceID and SpanID are the trace's, when there is one.
	TraceID [16]byte
	SpanID  [8]byte
}

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

// HelloValue is what the handshake says about the process, besides the
// operations and the record size: which product, binary and role run, from
// which revision and design, and the node table events refer to.
type HelloValue struct {
	// Product, Binary and Role name what runs: the app, the binary and the
	// process role it is (framework/model's IDs).
	Product, Binary, Role string
	// Revision is the VCS commit the binary was built from (vcs.revision).
	Revision string
	// Digest is the design's digest the code was generated from, when known.
	Digest string
	// Nodes are the node IDs events refer to: Nodes[i] is NodeRef(i+1).
	Nodes []string
}

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

// ExporterConfig is where the exporter listens, who may attach, and how much
// it holds for a slow reader.
type ExporterConfig struct {
	// Path is the private socket's absolute path (pkg/v1/proc/ipc).
	Path string
	// AllowUIDs and AllowGIDs admit accounts besides the product's own: the
	// on-call group declared at deployment.
	AllowUIDs, AllowGIDs []int
	// Buffer is the ring's size in events, rounded up to a power of two, in
	// [MinBuffer, MaxBuffer]; zero is DefaultBuffer.
	Buffer int
	// Hello is what the handshake says.
	Hello HelloValue
}

// counter is a padded atomic, so the producers' and the drainer's hot
// counters never share a cache line.
type counter struct {
	atomic.Uint64
	_ [56]byte
}

// Exporter is an Emitter that hands events to the clients attached to its
// socket. It never blocks a producer: a full ring drops the event and counts
// it.
type Exporter struct {
	cfg      ExporterConfig
	refs     map[string]NodeRef
	instance string
	ring     ring
	mask     uint64
	tail     counter
	head     uint64
	wake     chan struct{}
	dropped  counter
	sent     counter
	seq      uint64

	mu      sync.Mutex
	ln      *ipc.Listener
	clients []*ipc.Conn
	cancel  context.CancelFunc
	done    sync.WaitGroup
	buf     []byte
}

// Emit reports nothing.
func (nop) Emit(_ *Event) {}

// OpOf is the Op named name, or OpNone.
func OpOf(name string) Op {
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
