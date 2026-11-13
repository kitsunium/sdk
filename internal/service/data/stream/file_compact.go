package stream

import (
	"context"
	"math"
	"sort"
	"time"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// lastOfKey is what a compaction keeps of a key while it plans: the offset
// of its last compactable record.
type lastOfKey = map[string]corestream.Offset

// Compact removes from partition every compactable record a later one of its
// key supersedes, and every tombstone past its second window: each segment
// holding one is rewritten through seglog, the dropped record leaving its
// instant alone at its offset, so the indexes, the batches and Bounds stay
// as they were.
func (m *fileStream) Compact(_ context.Context, partition int) (corestream.CompactValue, error) {
	//: a partition of this stream.
	if refused := m.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return corestream.CompactValue{}, refused
	}
	//: a closed stream compacts nothing.
	if closed := m.usable(); closed != nil {
		//: the closed refusal.
		return corestream.CompactValue{}, closed
	}
	//: zero never compacts.
	if m.policy.CompactAfter == 0 {
		//: nothing looked at.
		return corestream.CompactValue{}, nil
	}
	part := m.parts[partition]
	part.wmu.Lock()
	value, freed, err := m.compactPart(part)
	part.wmu.Unlock()
	m.release(freed, false, 0)
	//: what was looked at, kept and dropped, or the storage's failure.
	return value, err
}

// compactPart is Compact on part, whose wmu the caller holds.
func (m *fileStream) compactPart(part *filePart) (corestream.CompactValue, int64, error) {
	//: a partition a failure stopped is not written to again.
	if part.broken != nil {
		//: the failure that stopped it.
		return corestream.CompactValue{}, 0, part.broken
	}
	now := m.clk.Now()
	lines := compactLinesAt(now, m.policy.CompactAfter)
	part.mu.Lock()
	freed := part.claim(part.first(now, m.policy.RetainFor))
	lo, line := part.kept, part.youngFrom(lines)
	part.mu.Unlock()
	//: nothing retained is old enough.
	if line <= lo {
		return corestream.CompactValue{}, freed, nil
	}
	last, scanned, err := m.planCompaction(part, lo, line)
	//: the log could not be read.
	if err != nil {
		//: StreamBackendFailed.
		return corestream.CompactValue{}, freed, err
	}
	var dropped []corestream.Offset
	edit := func(index uint64, kind uint8, payload []byte) (uint8, []byte, bool) {
		stamp, record, gone, ok := decodeStored(payload)
		offset := corestream.Offset(index)
		//: below the retained range, a hole already, or bytes the plan refused.
		if offset < lo || gone || !ok {
			//: left as it is.
			return kind, nil, false
		}
		at := timeOf(stamp)
		held, keyed := last[string(record.Key)]
		//: kept.
		if !lines.drops(record, at, keyed && held == offset) {
			return kind, nil, false
		}
		dropped = append(dropped, offset)
		//: the instant alone, at the same offset, of the same kind.
		return kind, encodeGone(nil, stamp), true
	}
	//: rewritten segment by segment, each published by a rename then a directory sync.
	if _, rewriteErr := part.log.Rewrite(uint64(line), edit); rewriteErr != nil {
		part.broken = backendFailed("compact", "", rewriteErr)
		//: StreamBackendFailed: what was rewritten before the failure stays rewritten.
		return corestream.CompactValue{}, freed, part.broken
	}
	part.mu.Lock()
	freed += part.forget(dropped)
	part.mu.Unlock()
	//: what was looked at, kept and dropped.
	return compactResult(scanned, int64(len(dropped))), freed, nil
}

// planCompaction reads part's records from lo up to line excluded, a chunk
// at a time, and returns the offset of every key's last one and how many
// live records it read.
func (m *fileStream) planCompaction(
	part *filePart, lo, line corestream.Offset,
) (last lastOfKey, scanned int64, err error) {
	last = lastOfKey{}
	var dst [][]byte
	//: a chunk at a time, so the payloads of one chunk are all it holds.
	for from := lo; from < line; from += rebuildChunk {
		hi := min(from+rebuildChunk, line)
		dst, err = part.log.Range(uint64(from), uint64(hi), math.MaxInt, dst[:0])
		//: a log that cannot be read cannot be compacted.
		if err != nil {
			//: the log's own refusal, under this engine's code.
			return nil, 0, backendFailed("compact", "", err)
		}
		//: every record of the chunk.
		for i, payload := range dst {
			_, record, gone, ok := decodeStored(payload)
			//: bytes this engine cannot have written.
			if !ok {
				//: refused rather than guessed at.
				return nil, 0, backendFailed("decode", "", nil)
			}
			//: a hole is no record.
			if gone {
				continue
			}
			scanned++
			noteLast(last, record.Key, from+corestream.Offset(i))
		}
	}
	//: the plan.
	return last, scanned, nil
}

// youngFrom returns the first offset of the oldest batch younger than
// CompactAfter, next when every batch is old enough: the records below it
// are compactable. The caller holds mu.
func (p *filePart) youngFrom(lines compactLines) corestream.Offset {
	young := sort.Search(len(p.batches), func(i int) bool {
		//: younger than the line.
		return !lines.compactable(time.Unix(0, p.batches[i].stamp))
	})
	//: every batch is old enough.
	if young == len(p.batches) {
		//: the whole partition.
		return p.next()
	}
	//: the young batch's first offset, never below kept.
	return max(p.batches[young].first, p.kept)
}
