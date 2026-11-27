package consensus

import (
	"math"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
)

// rateWindowTimeouts is the span of the sliding windows a stream's rate and
// the log's write rate are measured over, in maximum election timeouts: 8 s
// at the default 2 s. Long enough that a paced stream's reads, a tenth of a
// second apart, and a burst of writes average out; short enough that a rate
// raised, or writes that stopped, show within it (#1486).
const rateWindowTimeouts int64 = 4

// rateSamples is how many samples a rateWindow keeps, one per eighth of its
// span at most: its memory is bounded whatever the tick, and the rate reads
// over the last seven eighths of the span at least.
const rateSamples = 8

// rateShortClear is the hysteresis of PeerSnapshotRateShort: raised once the
// transfer would end after the log's fill, it falls only once the transfer
// would end within this fraction of the fill, so an estimate that hovers
// around the fill time does not raise it, and count it, at every sample
// (#1486).
const rateShortClear float64 = 0.75

// rateWindow is a sliding window over a growing count — the bytes a stream
// sent, the bytes the log holds — sampled on the loop: the samples at, in
// Unix nanoseconds, and v, a ring of rateSamples whose next slot is head,
// n of them taken.
type rateWindow struct {
	at   [rateSamples]int64
	v    [rateSamples]int64
	head int
	n    int
}

// rateWatch is what a stream's pin measures of its pace: the window over
// the bytes sent, and whether PeerSnapshotRateShort is raised for it. It
// holds on the pin's side by pointer, as pinFollow, so the pin still passes
// by value.
type rateWatch struct {
	sent  rateWindow
	short bool
}

// add samples v at now, once an eighth of span has passed since the newest
// sample: a tick faster than that adds nothing.
func (w *rateWindow) add(now time.Time, v int64, span time.Duration) {
	t := now.UnixNano()
	//: too soon after the newest sample.
	if w.n > 0 && t-w.at[(w.head+rateSamples-1)%rateSamples] < int64(span)/rateSamples {
		return
	}
	w.at[w.head], w.v[w.head] = t, v
	w.head = (w.head + 1) % rateSamples
	w.n = min(w.n+1, rateSamples)
}

// rate is the count's growth a second from the oldest sample to the newest,
// never below 0 — a compaction that shrank the log writes nothing —, and
// false until the ring is full.
func (w *rateWindow) rate() (float64, bool) {
	//: a window that does not span its samples yet.
	if w.n < rateSamples {
		return 0, false
	}
	oldest, newest := w.head, (w.head+rateSamples-1)%rateSamples
	dt := w.at[newest] - w.at[oldest]
	//: samples taken at one instant, which the spacing of add rules out.
	if dt <= 0 {
		return 0, false
	}
	return float64(max(w.v[newest]-w.v[oldest], 0)) / time.Duration(dt).Seconds(), true
}

// tightFrom is the fewest bytes at which the log is tight (logTight): past
// both MaxLogBytes less twice LogReserveBytes and half of MaxLogBytes less
// LogReserveBytes. Every pin stops holding compaction back there.
func (n *Node) tightFrom() int64 {
	m, r := n.cfg.MaxLogBytes, n.cfg.LogReserveBytes
	return max(m-2*r, (m-r+1)/2)
}

// watchRateShort judges, at a tick, whether each snapshot stream in flight
// would end before the writes bring the log to tightFrom, where its pin
// stops holding compaction back (#1486). The stream's rate R is its bytes
// sent over the sliding window, the writes' rate the log's bytes over the
// same window, both rateWindowTimeouts of etMax: it raises
// PeerSnapshotRateShort for the stream once (EncodedSize less the bytes
// sent) over R exceeds (tightFrom less the log's bytes) over the write rate,
// adds one to consensus_snapshot_rate_short_total and logs it at Warn; it
// lowers it once the transfer would end within rateShortClear of the fill.
// A stream that did not move over the window is left as it stands: its pin
// bound judges it (streamDue). Nothing else changes: the pin, the
// compaction and the pacing are left as they are. It runs on the loop,
// which holds n.sending and the windows: no lock.
func (n *Node) watchRateShort(now time.Time, etMax time.Duration) {
	sr, ok := n.cfg.Log.(spaceReporter)
	//: a log that reports no bytes has no fill to judge.
	if !ok {
		return
	}
	span := time.Duration(rateWindowTimeouts) * etMax
	b := sr.logBytes()
	n.logRate.add(now, b, span)
	write, full := n.logRate.rate()
	//: every stream in flight, its window sampled whether judged or not.
	for to, pin := range n.sending {
		//: a streamed pin, or one whose stream has not opened.
		if !pin.until.IsZero() || pin.progress == nil {
			continue
		}
		//: the stream's first tick: its window starts.
		if pin.rate == nil {
			pin.rate = new(rateWatch)
			n.sending[to] = pin
		}
		sent := pin.progress.Load()
		pin.rate.sent.add(now, sent, span)
		r, ok := pin.rate.sent.rate()
		//: either window not full yet, or a stream that did not move.
		if !full || !ok || r <= 0 {
			continue
		}
		n.judgeRateShort(to, pin.rate, float64(max(pin.size-sent, 0))/r, fillSeconds(n.tightFrom()-b, write), r)
	}
}

// fillSeconds is how long writes at rate bytes a second take to add left
// bytes to the log: 0 once there is none left, the log already tight, and
// +Inf with no writes.
func fillSeconds(left int64, rate float64) float64 {
	//: the log is already in the band.
	if left <= 0 {
		return 0
	}
	//: nothing is written: the band is never reached.
	if rate <= 0 {
		return math.Inf(1)
	}
	return float64(left) / rate
}

// judgeRateShort raises or lowers PeerSnapshotRateShort for the stream to
// peer to, whose transfer would end in eta seconds at rate bytes a second
// and whose pin stops holding in fill seconds: raised when eta exceeds
// fill, counted and logged once per rise; lowered once eta is within
// rateShortClear of fill.
func (n *Node) judgeRateShort(to coreconsensus.NodeID, s *rateWatch, eta, fill, rate float64) {
	switch {
	//: the transfer ends after the fill: the rise.
	case !s.short && eta > fill:
		s.short = true
		n.obs.inc(mSnapshotRateShort, coreotel.String("peer", to.String()))
		n.obs.warn("consensus: a snapshot stream cannot end before the writes fill the log budget and lift its pin; raise SnapshotStreamBytesPerSecond, raise MaxLogBytes or lower the write load",
			lattr("peer", uint64(to)), lstr("transfer", secondsText(eta)), lstr("fill", secondsText(fill)),
			lattr("rate", uint64(rate)))
	//: the transfer ends well within the fill: the fall.
	case s.short && eta <= fill*rateShortClear:
		s.short = false
	}
}

// secondsText renders s seconds as a duration, to the millisecond, "never"
// past what a Duration holds.
func secondsText(s float64) string {
	//: an infinite estimate, or one no Duration holds.
	if s >= math.MaxInt64/float64(time.Second) {
		return "never"
	}
	return time.Duration(s * float64(time.Second)).Round(time.Millisecond).String()
}
