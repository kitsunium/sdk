package machine

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"maps"
	"slices"
	"sync"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The operations a KV command carries, its first byte.
const (
	kvOpGet byte = iota + 1
	kvOpPut
	kvOpCAS
)

// kvSnapshotMagic and kvSnapshotVersion open every snapshot a KV writes.
const (
	kvSnapshotMagic        = "KKV"
	kvSnapshotVersion byte = 1
)

// maxKVField bounds a key or a value read back from a snapshot.
const maxKVField uint64 = 1 << 30

// KVResult is what a KV command answers: the key's value and revision
// after the command, whether the key exists, and whether a put or a swap
// took effect. Value is the machine's own: the caller does not write to it.
type KVResult struct {
	Value    []byte
	Revision uint64
	Found    bool
	Applied  bool
}

// kvEntry is one key's value and revision; its bytes are never written to
// once stored, so a snapshot shares them.
type kvEntry struct {
	value []byte
	rev   uint64
}

// KV is the reference key-value state machine: get, put and
// compare-and-swap on a per-key revision. A command it does not read is
// answered a VERSION_UNSUPPORTED error, not applied.
type KV struct {
	mu sync.RWMutex
	m  map[string]kvEntry
}

// NewKV returns an empty KV.
func NewKV() *KV { return &KV{m: map[string]kvEntry{}} }

// KVGet returns the command that reads key through the log.
func KVGet(key string) []byte { return kvCommand(kvOpGet, key, nil, 0) }

// KVPut returns the command that stores value under key.
func KVPut(key string, value []byte) []byte { return kvCommand(kvOpPut, key, value, 0) }

// KVCompareAndSwap returns the command that stores value under key only
// while the key's revision is revision; revision 0 asks that the key be
// absent.
func KVCompareAndSwap(key string, value []byte, revision uint64) []byte {
	return kvCommand(kvOpCAS, key, value, revision)
}

// kvCommand encodes op, key, revision and value: the op byte, the key's
// length as a uvarint and its bytes, the revision as a uvarint, the value.
func kvCommand(op byte, key string, value []byte, rev uint64) []byte {
	b := make([]byte, 0, 1+2*binary.MaxVarintLen64+len(key)+len(value))
	b = append(b, op)
	b = binary.AppendUvarint(b, uint64(len(key)))
	b = append(b, key...)
	b = binary.AppendUvarint(b, rev)
	return append(b, value...)
}

// Apply applies one command and returns its KVResult, or a refusal of a
// command this machine does not read.
func (s *KV) Apply(e coreconsensus.EntryValue) any {
	op, key, rev, value, ok := decodeKV(e.Data)
	if !ok {
		return origin(coreconsensus.VersionUnsupported, errs.String("what", "kv command"), errs.String("why", "the command does not decode"))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, found := s.m[key]
	switch op {
	case kvOpGet:
		return KVResult{Value: cur.value, Revision: cur.rev, Found: found}
	case kvOpCAS:
		if cur.rev != rev {
			//: the revision moved since the caller read it: refused, D6's ABA defence.
			return KVResult{Value: cur.value, Revision: cur.rev, Found: found}
		}
	}
	next := kvEntry{value: bytes.Clone(value), rev: cur.rev + 1}
	s.m[key] = next
	return KVResult{Value: next.value, Revision: next.rev, Found: true, Applied: true}
}

// decodeKV reads a command kvCommand wrote.
func decodeKV(b []byte) (op byte, key string, rev uint64, value []byte, ok bool) {
	if len(b) == 0 || b[0] < kvOpGet || b[0] > kvOpCAS {
		return 0, "", 0, nil, false
	}
	op, b = b[0], b[1:]
	n, w := binary.Uvarint(b)
	if w <= 0 || n > uint64(len(b)-w) {
		return 0, "", 0, nil, false
	}
	key, b = string(b[w:w+int(n)]), b[w+int(n):]
	rev, w = binary.Uvarint(b)
	if w <= 0 {
		return 0, "", 0, nil, false
	}
	return op, key, rev, b[w:], true
}

// Get reads key from this node's state: no read through the log, so not
// linearizable.
func (s *KV) Get(key string) (value []byte, revision uint64, found bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, found := s.m[key]
	return e.value, e.rev, found
}

// kvSnap is a copy of the map's entries; their bytes are shared.
type kvSnap struct{ m map[string]kvEntry }

// Snapshot copies the map: the entries' bytes are shared, never copied.
func (s *KV) Snapshot() (coreconsensus.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return kvSnap{m: maps.Clone(s.m)}, nil
}

// WriteTo writes the header, the count, then each key in order with its
// revision and value, every length a uvarint.
func (k kvSnap) WriteTo(w io.Writer) (int64, error) {
	cw := &countingWriter{w: w}
	bw := bufio.NewWriter(cw)
	var tmp [binary.MaxVarintLen64]byte
	// A bufio.Writer keeps the first error it meets and refuses every write
	// after it, so the one Flush reports reaches the caller; werr only keeps
	// the earlier of the two should they ever differ.
	var werr error
	keep := func(_ int, err error) {
		if werr == nil {
			werr = err
		}
	}
	uv := func(x uint64) { keep(bw.Write(binary.AppendUvarint(tmp[:0], x))) }
	keep(bw.WriteString(kvSnapshotMagic))
	keep(0, bw.WriteByte(kvSnapshotVersion))
	uv(uint64(len(k.m)))
	for _, key := range slices.Sorted(maps.Keys(k.m)) {
		e := k.m[key]
		uv(uint64(len(key)))
		keep(bw.WriteString(key))
		uv(e.rev)
		uv(uint64(len(e.value)))
		keep(bw.Write(e.value))
	}
	if err := bw.Flush(); err != nil {
		return cw.n, err
	}
	return cw.n, werr
}

// Release frees nothing: the copy is the garbage collector's.
func (kvSnap) Release() {}

// Restore replaces the state with a snapshot's. A snapshot no KV wrote
// is SNAPSHOT_CORRUPT, one of a newer format VERSION_UNSUPPORTED.
func (s *KV) Restore(r io.Reader, _ coreconsensus.SnapshotMetaValue) error {
	br := bufio.NewReader(r)
	var head [len(kvSnapshotMagic) + 1]byte
	if _, err := io.ReadFull(br, head[:]); err != nil || string(head[:len(kvSnapshotMagic)]) != kvSnapshotMagic {
		return corrupt("the snapshot is not one a KV wrote")
	}
	if v := head[len(kvSnapshotMagic)]; v != kvSnapshotVersion {
		return unsupported("kv snapshot", v)
	}
	n, err := binary.ReadUvarint(br)
	if err != nil {
		return corrupt("the snapshot's count does not read")
	}
	m := map[string]kvEntry{}
	for range n {
		key, e, err := readEntry(br)
		if err != nil {
			return err
		}
		m[key] = e
	}
	s.mu.Lock()
	s.m = m
	s.mu.Unlock()
	return nil
}

// readEntry reads one key with its revision and value.
func readEntry(br *bufio.Reader) (string, kvEntry, error) {
	key, err := readField(br)
	if err != nil {
		return "", kvEntry{}, err
	}
	rev, err := binary.ReadUvarint(br)
	if err != nil {
		return "", kvEntry{}, corrupt("a revision does not read")
	}
	value, err := readField(br)
	if err != nil {
		return "", kvEntry{}, err
	}
	return string(key), kvEntry{value: value, rev: rev}, nil
}

// readField reads a uvarint length and that many bytes.
func readField(br *bufio.Reader) ([]byte, error) {
	n, err := binary.ReadUvarint(br)
	if err != nil || n > maxKVField {
		return nil, corrupt("a field's length does not read")
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(br, b); err != nil {
		return nil, corrupt("a field is cut short")
	}
	return b, nil
}
