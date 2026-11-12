package stream

import (
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/fs/seglog"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// rebuildChunk is how many records a reopen reads with one Range.
const rebuildChunk = 4096

// compactAfter is how many offsets below the first retained one the index
// keeps before it lets them go: a copy of the index every so often, rather
// than one per trim.
const compactAfter = 4096

// batchMark is where one batch starts and the instant it was stamped with.
type batchMark struct {
	first corestream.Offset
	stamp int64
}

// filePart is one partition of a file stream: its segmented log, and the
// index of what the log holds that retention, OffsetAt and the byte bounds
// read without touching the disk.
type filePart struct {
	log *seglog.Log
	// broken is the write failure that stopped this partition. A batch a
	// failure cut short may still be pending in the log, so nothing is ever
	// appended after it. Guarded by wmu.
	broken error
	// scratch is the encoding buffer of the batch being written. Guarded by
	// wmu.
	scratch []byte
	// prefix[i] is the bytes, keys and payloads, of the records from base up
	// to base+i excluded; len(prefix)-1 records are indexed.
	prefix []int64
	// batches holds every indexed batch from the one holding base on, in
	// offset order, which is also the order of their instants.
	batches []batchMark
	// base is the offset prefix[0] stands for.
	base corestream.Offset
	// kept is the first offset retention, Trim or the byte bound have
	// claimed for good; the age bound may claim more at read time.
	kept corestream.Offset
	// mu guards the index. Readers hold it shared and never across a disk
	// write: an Append updates the index after its sync.
	mu sync.RWMutex
	// wmu serialises the partition's writers, Append and Trim, across their
	// write and sync.
	wmu sync.Mutex
}

// next returns the offset the partition's next record takes. The caller
// holds mu, or wmu.
func (p *filePart) next() corestream.Offset {
	//: one prefix entry per indexed record, plus the zero.
	return p.base + corestream.Offset(len(p.prefix)-1)
}

// lastBatch returns the first offset of the newest batch, next when there
// is none. The caller holds mu, or wmu.
func (p *filePart) lastBatch() corestream.Offset {
	//: an empty index has no batch to spare.
	if len(p.batches) == 0 {
		//: nothing below next to keep.
		return p.next()
	}
	//: the newest batch.
	return p.batches[len(p.batches)-1].first
}

// between returns the bytes of the records from lo up to hi excluded, both
// within [base, next]. The caller holds mu, or wmu.
func (p *filePart) between(lo, hi corestream.Offset) int64 {
	//: a difference of two prefix sums.
	return p.prefix[hi-p.base] - p.prefix[lo-p.base]
}

// first returns the first offset still readable at now: that of the oldest
// batch still younger than retainFor, next when every one is older, never
// below kept. Zero retainFor keeps every record. The caller holds mu, or wmu.
func (p *filePart) first(now time.Time, retainFor time.Duration) corestream.Offset {
	//: zero keeps every record for good.
	if retainFor == 0 {
		//: what Trim and the byte bound left.
		return p.kept
	}
	young := sort.Search(len(p.batches), func(i int) bool {
		//: younger than the bound: kept.
		return now.Sub(time.Unix(0, p.batches[i].stamp)) < retainFor
	})
	//: every batch has aged out.
	if young == len(p.batches) {
		//: nothing is left to read.
		return p.next()
	}
	//: the young batch's first offset, or kept inside it.
	return max(p.batches[young].first, p.kept)
}

// bounded returns the first offset the byte bound keeps when size more bytes
// follow next: the lowest offset from lo on whose suffix fits retainBytes,
// never past limit — the newest batch, which its own Append never trims.
// Zero retainBytes is unbounded. The caller holds mu, or wmu.
func (p *filePart) bounded(lo, limit corestream.Offset, size, retainBytes int64) corestream.Offset {
	//: zero is unbounded.
	if retainBytes == 0 || limit <= lo {
		//: the lower bound, unchanged.
		return lo
	}
	head := p.next()
	cut := sort.Search(int(limit-lo), func(i int) bool {
		//: the suffix from lo+i, and the size to come, fits.
		return p.between(lo+corestream.Offset(i), head)+size <= retainBytes
	})
	//: limit when nothing below it can go and still fit.
	return lo + corestream.Offset(cut)
}

// index records a batch of sizes, stamped at stamp, as the records from next
// on, and claims everything below claim. It returns the bytes the claim
// released. The caller holds mu exclusively, and wmu.
func (p *filePart) index(stamp int64, sizes []int64, claim corestream.Offset) (freed int64) {
	p.batches = append(p.batches, batchMark{first: p.next(), stamp: stamp})
	total := p.prefix[len(p.prefix)-1]
	//: one prefix entry per record.
	for _, size := range sizes {
		total += size
		p.prefix = append(p.prefix, total)
	}
	//: the retention the batch's Append decided.
	return p.claim(claim)
}

// claim makes to the first offset retained for good and returns the bytes it
// released; at or below kept it changes nothing. The caller holds mu
// exclusively.
func (p *filePart) claim(to corestream.Offset) (freed int64) {
	//: claims only move forward.
	if to <= p.kept {
		//: nothing released.
		return 0
	}
	freed = p.between(p.kept, to)
	p.kept = to
	p.compact()
	//: what the claim released.
	return freed
}

// compact lets the index forget what lies far enough below kept.
func (p *filePart) compact() {
	drop := int(p.kept - p.base)
	//: a copy every compactAfter offsets, never one per claim.
	if drop < compactAfter {
		return
	}
	p.prefix = slices.Clone(p.prefix[drop:])
	p.base = p.kept
	keep := sort.Search(len(p.batches), func(i int) bool {
		//: the first batch starting past the new base.
		return p.batches[i].first > p.base
	})
	//: the batch holding base stays, so its instant is still known.
	p.batches = slices.Clone(p.batches[max(keep-1, 0):])
}

// offsetAt returns the first offset at or past from whose batch was stamped
// at or after t, next when none was. The caller holds mu.
func (p *filePart) offsetAt(from corestream.Offset, t time.Time) corestream.Offset {
	found := sort.Search(len(p.batches), func(i int) bool {
		//: at or after t.
		return !time.Unix(0, p.batches[i].stamp).Before(t)
	})
	//: t follows every batch.
	if found == len(p.batches) {
		//: the next offset.
		return p.next()
	}
	//: the batch's first offset, or the first retained inside it.
	return max(p.batches[found].first, from)
}

// oldestStamp returns the instant of the batch holding offset from, and
// false when from is next. The caller holds mu.
func (p *filePart) oldestStamp(from corestream.Offset) (int64, bool) {
	//: nothing retained, nothing to age.
	if from >= p.next() {
		//: no instant.
		return 0, false
	}
	holding := sort.Search(len(p.batches), func(i int) bool {
		//: the first batch starting past from.
		return p.batches[i].first > from
	})
	//: the batch before it holds from.
	return p.batches[holding-1].stamp, true
}

// rebuild reads the whole log once and indexes it: every record's size,
// every batch's first offset and instant. A batch whose head a Trim dropped
// starts at the log's first record, a tail. It returns the latest instant
// seen, so the engine's stamps keep never going backwards across a restart.
func (p *filePart) rebuild() (latest int64, err error) {
	first, next := p.log.Bounds()
	p.base, p.kept = corestream.Offset(first), corestream.Offset(first)
	p.prefix = append(make([]int64, 0, next-first+1), 0)
	var dst [][]byte
	//: a chunk at a time, so the payloads of one chunk are all it holds.
	for lo := first; lo < next; lo += rebuildChunk {
		hi := min(lo+rebuildChunk, next)
		dst, err = p.log.Range(lo, hi, math.MaxInt, dst[:0])
		//: a log that cannot be read cannot be served.
		if err != nil {
			//: the log's own refusal, under this engine's code.
			return 0, backendFailed("rebuild", "", err)
		}
		//: every record of the chunk.
		for i, payload := range dst {
			stamp, record, ok := decodeRecord(payload)
			//: bytes this engine cannot have written.
			if !ok {
				//: refused rather than skipped: a skip would renumber nothing and lose a record.
				return 0, backendFailed("decode", "", nil)
			}
			index := lo + uint64(i)
			kind, kindErr := p.log.Kind(index)
			//: the log's own refusal.
			if kindErr != nil {
				//: under this engine's code.
				return 0, backendFailed("rebuild", "", kindErr)
			}
			//: a head opens a batch, and so does the first record left.
			if kind == kindHead || index == first {
				p.batches = append(p.batches, batchMark{first: corestream.Offset(index), stamp: stamp})
				latest = max(latest, stamp)
			}
			p.prefix = append(p.prefix, p.prefix[len(p.prefix)-1]+recordBytes(record))
		}
	}
	//: indexed.
	return latest, nil
}
