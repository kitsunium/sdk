package consensus

import (
	"bytes"
	"slices"
	"sync"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// MemorySnapshots is the in-memory SnapshotStore double.
type MemorySnapshots struct {
	mu     sync.Mutex
	done   map[string]*memSnap
	spools map[string]*memSink
}

type memSnap struct {
	meta coreconsensus.SnapshotMeta
	data []byte
	pins int
}

// NewMemorySnapshots returns an empty store.
func NewMemorySnapshots() *MemorySnapshots {
	return &MemorySnapshots{done: map[string]*memSnap{}, spools: map[string]*memSink{}}
}

type memSink struct {
	s    *MemorySnapshots
	id   string
	meta coreconsensus.SnapshotMeta
	buf  bytes.Buffer
}

// Create implements SnapshotStore: an existing spool resumes from its last
// whole chunk.
func (s *MemorySnapshots) Create(id string, meta coreconsensus.SnapshotMeta) (coreconsensus.SnapshotSink, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sp, ok := s.spools[id]; ok {
		off := alignedOffset(sp.buf.Bytes())
		sp.buf.Truncate(int(off))
		return sp, off, nil
	}
	sp := &memSink{s: s, id: id, meta: meta}
	s.spools[id] = sp
	return sp, 0, nil
}

func (k *memSink) Write(p []byte) (int, error) {
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	return k.buf.Write(p)
}

func (k *memSink) Commit(final coreconsensus.SnapshotMeta) error {
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	if int64(k.buf.Len()) != final.EncodedSize {
		return snapshotCorrupt("encoded size")
	}
	final.ID = k.id
	k.s.done[k.id] = &memSnap{meta: final, data: bytes.Clone(k.buf.Bytes())}
	delete(k.s.spools, k.id)
	return nil
}

func (k *memSink) Abort() error {
	k.s.mu.Lock()
	defer k.s.mu.Unlock()
	delete(k.s.spools, k.id)
	return nil
}

// List implements SnapshotStore.
func (s *MemorySnapshots) List() ([]coreconsensus.SnapshotMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]coreconsensus.SnapshotMeta, 0, len(s.done))
	for _, x := range s.done {
		out = append(out, x.meta)
	}
	slices.SortFunc(out, func(a, b coreconsensus.SnapshotMeta) int {
		switch {
		case a.Position.Index > b.Position.Index:
			return -1
		case a.Position.Index < b.Position.Index:
			return 1
		}
		return 0
	})
	return out, nil
}

type memHandle struct {
	*bytes.Reader
	s    *MemorySnapshots
	snap *memSnap
	once sync.Once
}

func (h *memHandle) Meta() coreconsensus.SnapshotMeta { return h.snap.meta }

func (h *memHandle) Close() error {
	h.once.Do(func() {
		h.s.mu.Lock()
		h.snap.pins--
		h.s.mu.Unlock()
	})
	return nil
}

// Open implements SnapshotStore.
func (s *MemorySnapshots) Open(id string) (coreconsensus.SnapshotHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, ok := s.done[id]
	if !ok {
		return nil, origin(coreconsensus.SnapshotCorrupt, errs.String("id", id), errs.String("why", "unknown"))
	}
	x.pins++
	return &memHandle{Reader: bytes.NewReader(x.data), s: s, snap: x}, nil
}

// Remove implements SnapshotStore; refused while a handle is open.
func (s *MemorySnapshots) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if x, ok := s.done[id]; ok {
		if x.pins > 0 {
			return storageFailed("remove", errPinned)
		}
		delete(s.done, id)
	}
	return nil
}
