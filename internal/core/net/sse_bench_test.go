package net_test

import (
	"strconv"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// sseBenchSizes are the payload lengths every per-byte SSE row is measured at.
// 64 B is a tick, 256 B a small JSON object, 4 KiB a page of one — the shapes
// an event stream actually carries.
var sseBenchSizes = []int{64, 256, 1024, 4096, 65536}

// sseFrameSink keeps the encoded frame reachable so no AppendTo can be proven
// unused and elided.
var (
	sseFrameSink []byte
	sseLinesSink []string
	sseErrSink   error
)

// sseJSONPayload builds a single-line JSON payload of n bytes carrying NO line
// terminator at all. That is the overwhelmingly common SSE event, and it is the
// worst case for terminator scanning: the scan cannot stop early, it has to
// prove the absence of both terminators over the whole string.
func sseJSONPayload(n int) string {
	var b strings.Builder
	b.Grow(n)
	b.WriteString(`{"seq":0,"kind":"tick","payload":"`)
	//: fill to exactly n bytes, closing the object at the end.
	for b.Len() < n-2 {
		b.WriteByte(byte('a' + b.Len()%26))
	}
	b.WriteString(`"}`)
	//: the builder may have overshot by a byte on the closing pair; the exact
	//: length matters only for the MB/s column, so trim rather than pad.
	return b.String()[:min(n, b.Len())]
}

// sseMultiLinePayload builds a payload of n bytes split into lines of width
// bytes by the terminator term — the shape a log tail or a stack trace has.
func sseMultiLinePayload(n, width int, term string) string {
	var b strings.Builder
	b.Grow(n + n/width*len(term))
	//: emit whole lines until the byte budget is spent.
	for b.Len() < n {
		//: one line of filler, then the terminator under test.
		for i := 0; i < width && b.Len() < n; i++ {
			b.WriteByte(byte('a' + i%26))
		}
		b.WriteString(term)
	}
	return b.String()
}

// BenchmarkSSEAppendTo is the in-situ number: one whole frame encoded into a
// reused buffer, exactly as service/net/sse.Send does it. The single-line rows
// are the common case and the multi-line rows are what a terminator costs.
func BenchmarkSSEAppendTo(b *testing.B) {
	buf := make([]byte, 0, 128*1024)
	//: the single-line shape first — no terminator anywhere in the payload.
	for _, n := range sseBenchSizes {
		b.Run("single_line/"+sseBenchLabel(n), sseAppendToRow(sseJSONPayload(n), n, buf))
	}
	//: the same byte counts, split into 64-byte lines by an LF.
	for _, n := range sseBenchSizes {
		b.Run("multi_line_lf/"+sseBenchLabel(n), sseAppendToRow(sseMultiLinePayload(n, 64, "\n"), n, buf))
	}
	//: and by a CRLF, which appendSSEData must count as ONE terminator.
	for _, n := range sseBenchSizes {
		b.Run("multi_line_crlf/"+sseBenchLabel(n), sseAppendToRow(sseMultiLinePayload(n, 64, "\r\n"), n, buf))
	}
}

// sseAppendToRow returns one AppendTo row over a data-only frame. The payload
// travels as an ARGUMENT rather than as a captured loop variable, so nothing
// about the sweep's shape forces it onto the heap and the row measures only the
// encode.
func sseAppendToRow(data string, n int, buf []byte) func(*testing.B) {
	event := corenet.SSEEventValue{Data: data}
	//: one closure per row, over values that are already fixed.
	return func(b *testing.B) {
		b.SetBytes(int64(n))
		b.ReportAllocs()
		for b.Loop() {
			sseFrameSink, sseErrSink = event.AppendTo(buf[:0])
		}
	}
}

// BenchmarkSSEAppendToFullFrame is the shape a real producer emits: an id, an
// event name and a single-line JSON payload. It prices Validate's two
// single-line checks alongside the payload scan rather than in isolation.
func BenchmarkSSEAppendToFullFrame(b *testing.B) {
	buf := make([]byte, 0, 8192)
	event := corenet.SSEEventValue{
		ID:   "01J8Z9F0X4T7QK5R2M3N6P8V1B",
		Name: "measurement.recorded",
		Data: sseJSONPayload(256),
	}
	b.ReportAllocs()
	for b.Loop() {
		sseFrameSink, sseErrSink = event.AppendTo(buf[:0])
	}
}

// BenchmarkSSEValidate prices the pre-flight check on its own. AppendTo runs it
// before it touches the caller's buffer, so it is paid on every frame including
// the ones that go on to be encoded.
func BenchmarkSSEValidate(b *testing.B) {
	//: an id-and-name frame pays both single-line checks.
	b.Run("id_and_name", func(b *testing.B) {
		event := corenet.SSEEventValue{
			ID:   "01J8Z9F0X4T7QK5R2M3N6P8V1B",
			Name: "measurement.recorded",
			Data: "{}",
		}
		b.ReportAllocs()
		for b.Loop() {
			sseErrSink = event.Validate()
		}
	})
	//: a data-only frame is the cheap end: both string checks are skipped.
	b.Run("data_only", func(b *testing.B) {
		event := corenet.SSEEventValue{Data: "{}"}
		b.ReportAllocs()
		for b.Loop() {
			sseErrSink = event.Validate()
		}
	})
}

// BenchmarkAppendSSEComment prices the keep-alive frame, which every idle
// stream emits on a timer whether it has anything to say or not.
func BenchmarkAppendSSEComment(b *testing.B) {
	buf := make([]byte, 0, 128)
	b.ReportAllocs()
	for b.Loop() {
		sseFrameSink, sseErrSink = corenet.AppendSSEComment(buf[:0], "keep-alive")
	}
}

// sseSplitFunc is one terminator-scanning strategy: it appends the payload's
// lines to dst in order and returns the extended slice. Every strategy sees the
// WHOLE payload, because that is the unit appendSSEData works on and the unit
// where a per-line strategy's cost stops being linear.
type sseSplitFunc func(dst []string, data string) []string

// sseStrategies are the four terminator scans BENCH.md compares: the shipped
// form and the three it was chosen over. splitIndexAny is also the correctness
// ORACLE — TestSSELineSplitStrategiesAgree judges every other entry against it.
var sseStrategies = []struct {
	name  string
	split sseSplitFunc
}{
	{name: "index_any", split: splitIndexAny},
	{name: "two_index_byte", split: splitTwoIndexByte},
	{name: "bounded_index_byte", split: splitBoundedIndexByte},
	{name: "cursor", split: splitCursor},
}

// sseCorpus are the four payload shapes a terminator scan meets. The last two
// exist because they are where an obvious strategy stops being linear: a
// payload with LFs and no CR, and a payload with CRs and no LF.
var sseCorpus = []struct {
	name string
	make func(int) string
}{
	{name: "no_terminator", make: sseJSONPayload},
	{name: "lf_every_64B", make: func(n int) string { return sseMultiLinePayload(n, 64, "\n") }},
	{name: "crlf_every_64B", make: func(n int) string { return sseMultiLinePayload(n, 64, "\r\n") }},
	{name: "cr_every_64B", make: func(n int) string { return sseMultiLinePayload(n, 64, "\r") }},
}

// BenchmarkSSELineSplit is the isolated per-byte comparison behind BENCH.md's
// terminator-scan section: every strategy over every corpus shape at every
// size, so the table can be read down a column as well as across a row.
func BenchmarkSSELineSplit(b *testing.B) {
	//: reused across every row, so the split itself allocates nothing after the
	//: first row warms it and the ns/op column is scanning, not growth.
	lines := make([]string, 0, 4096)
	for _, s := range sseStrategies {
		for _, c := range sseCorpus {
			for _, n := range sseBenchSizes {
				b.Run(s.name+"/"+c.name+"/"+sseBenchLabel(n), sseSplitRow(s.split, c.make(n), n, lines))
			}
		}
	}
}

// sseSplitRow returns one line-split row. The payload and the strategy travel
// as ARGUMENTS rather than as captured loop variables, so the sweep's shape
// cannot put either on the heap and the row measures only the scan.
func sseSplitRow(split sseSplitFunc, payload string, n int, lines []string) func(*testing.B) {
	//: one closure per row, over values that are already fixed.
	return func(b *testing.B) {
		b.SetBytes(int64(n))
		b.ReportAllocs()
		for b.Loop() {
			lines = split(lines[:0], payload)
		}
		sseLinesSink = lines
	}
}

// splitIndexAny is the terminator scan this package shipped before the
// campaign, transcribed. It is the ORACLE: the equivalence test judges the
// shipped form against it, so it must never be changed to call the
// implementation — that would prove only that the implementation equals itself.
func splitIndexAny(dst []string, data string) []string {
	rest := data
	//: walk the payload one line at a time until the last one is written.
	for {
		idx := strings.IndexAny(rest, "\n\r")
		//: no terminator means this is the final line.
		if idx < 0 {
			//: the whole remainder is one line.
			return append(dst, rest)
		}
		skip := 1
		//: CRLF is one terminator, not two.
		if rest[idx] == '\r' && idx+1 < len(rest) && rest[idx+1] == '\n' {
			skip = 2
		}
		dst = append(dst, rest[:idx])
		rest = rest[idx+skip:]
	}
}

// splitTwoIndexByte is the first candidate and it is REFUSED: two UNBOUNDED
// IndexByte scans per line means the CR scan re-reads the whole tail on every
// line of a payload that has no CR at all, which is quadratic on ordinary
// Unix-terminated text. BENCH.md prints the row.
func splitTwoIndexByte(dst []string, data string) []string {
	rest := data
	//: walk the payload one line at a time until the last one is written.
	for {
		lf := strings.IndexByte(rest, '\n')
		cr := strings.IndexByte(rest, '\r')
		idx := lf
		//: the earlier of the two terminators cuts; -1 means the byte is
		//: absent, so it only wins when the other is absent too.
		if cr >= 0 && (lf < 0 || cr < lf) {
			idx = cr
		}
		//: no terminator means this is the final line.
		if idx < 0 {
			//: the whole remainder is one line.
			return append(dst, rest)
		}
		skip := 1
		//: CRLF is one terminator, not two.
		if rest[idx] == '\r' && idx+1 < len(rest) && rest[idx+1] == '\n' {
			skip = 2
		}
		dst = append(dst, rest[:idx])
		rest = rest[idx+skip:]
	}
}

// splitBoundedIndexByte is the second candidate and it is REFUSED for the
// MIRROR of the first one's reason: bounding the CR scan by the LF fixes the
// LF-only payload and leaves the CR-only payload quadratic, because then it is
// the LF scan that re-reads the tail every line. BENCH.md prints both rows.
func splitBoundedIndexByte(dst []string, data string) []string {
	rest := data
	//: walk the payload one line at a time until the last one is written.
	for {
		idx := strings.IndexByte(rest, '\n')
		head := rest
		//: an LF caps how far a CR could still matter: a CR after it cannot be
		//: the FIRST terminator.
		if idx >= 0 {
			head = rest[:idx]
		}
		//: a CR inside that prefix is the earlier terminator and wins.
		if cr := strings.IndexByte(head, '\r'); cr >= 0 {
			idx = cr
		}
		//: no terminator means this is the final line.
		if idx < 0 {
			//: the whole remainder is one line.
			return append(dst, rest)
		}
		skip := 1
		//: CRLF is one terminator, not two.
		if rest[idx] == '\r' && idx+1 < len(rest) && rest[idx+1] == '\n' {
			skip = 2
		}
		dst = append(dst, rest[:idx])
		rest = rest[idx+skip:]
	}
}

// splitCursor is the shipped form, transcribed: two cursors that only ever move
// FORWARD, so each terminator byte is searched for over each stretch of the
// payload exactly once and the whole walk is linear whatever the payload's
// shape. It is here so BENCH.md can price it against the three above.
func splitCursor(dst []string, data string) []string {
	lf := strings.IndexByte(data, '\n')
	cr := strings.IndexByte(data, '\r')
	at := 0
	//: walk the payload one line at a time until the last one is written.
	for {
		idx := lf
		//: the earlier surviving cursor cuts; -1 means that byte is absent from
		//: the rest of the payload, so it only wins when the other is absent.
		if lf < 0 || (cr >= 0 && cr < lf) {
			idx = cr
		}
		//: no terminator left means this is the final line.
		if idx < 0 {
			//: the whole remainder is one line.
			return append(dst, data[at:])
		}
		skip := 1
		//: CRLF is one terminator, not two.
		if data[idx] == '\r' && idx+1 < len(data) && data[idx+1] == '\n' {
			skip = 2
		}
		dst = append(dst, data[at:idx])
		at = idx + skip
		//: refresh only a cursor the cut consumed or overtook, resuming AT the
		//: new position — never before it, which is the whole linearity claim.
		if lf >= 0 && lf < at {
			lf = sseIndexFrom(data, at, '\n')
		}
		//: the same for the carriage return.
		if cr >= 0 && cr < at {
			cr = sseIndexFrom(data, at, '\r')
		}
	}
}

// sseIndexFrom returns the index of b at or after from in s, in s's own
// coordinates, or -1 when there is none.
func sseIndexFrom(s string, from int, b byte) int {
	i := strings.IndexByte(s[from:], b)
	//: a miss stays a miss.
	if i < 0 {
		//: absent from the remainder.
		return -1
	}
	//: report in the whole string's coordinates.
	return from + i
}

// sseBenchLabel renders a size zero-padded so sub-benchmarks sort numerically
// rather than lexically.
func sseBenchLabel(n int) string {
	label := strconv.Itoa(n)
	//: seven digits covers every size in the sweep.
	for len(label) < 7 {
		label = "0" + label
	}
	return label
}
