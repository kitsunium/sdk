// Package queue_test — fuzz targets for the two formats the file broker reads
// without having necessarily written them (issue #1047): the name grammar,
// which validates the receipt a caller hands in and decides which file Ack
// unlinks, and the ktnq/1 dead-letter record, which any file in dead/ may
// claim to be.
package queue_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// fuzzEntropy and fuzzLease are well-formed random halves of a name.
const (
	fuzzEntropy string = "0123456789abcdef"
	fuzzLease   string = "89abcdef"
)

// FuzzNameGrammar holds every grammar to its two rules: a name it accepts is
// exactly the name it would write for what it read, and holds nothing but
// lower-case hex digits and dots before its suffix — so no receipt it
// accepts names a file outside its state directory.
//
// Seen failing before the change: a "+" before the instant parsed, as
// strconv.ParseInt reads a sign, and the name written back lost it —
//
//	ready: "+000000000000000001.…" parsed, but writes back "0000000000000000001.…"
func FuzzNameGrammar(f *testing.F) {
	at, enqueued := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC).UnixNano(), int64(1_700_000_000_000_000_000)
	for _, seed := range []string{
		svcqueue.ReadyNameForTest(at, enqueued, fuzzEntropy, 0),
		svcqueue.ReadyNameForTest(at, enqueued, fuzzEntropy, 12),
		svcqueue.InflightNameForTest(at, enqueued, fuzzEntropy, 1, fuzzLease),
		svcqueue.DeadNameForTest(enqueued, fuzzEntropy, 3),
		"+000000000000000001.0000000000000000001." + fuzzEntropy + ".000.msg",
		".vfs-1234.tmp", "../../etc/passwd.msg", "", ".msg",
	} {
		f.Add(seed)
	}
	grammars := svcqueue.NameGrammarsForTest()
	f.Fuzz(func(t *testing.T, base string) {
		for state, grammar := range grammars {
			rendered, ok := grammar.Parse(base)
			if !ok {
				continue
			}
			if rendered != base {
				t.Fatalf("%s: %q parsed, but writes back %q", state, base, rendered)
			}
			stem := strings.TrimSuffix(base, grammar.Suffix)
			if strings.Trim(stem, "0123456789abcdef.") != "" {
				t.Fatalf("%s: %q parsed with a character outside [0-9a-f.]", state, base)
			}
		}
	})
}

// FuzzDecodeTrail holds the trail reader to its bound and its round trip: at
// most sixteen codes, and what it read is what encodeTrail writes back.
func FuzzDecodeTrail(f *testing.F) {
	for _, seed := range []string{"", "1", "0,4294967295", "33620225,33620226", strings.Repeat("7,", 16) + "7", "1,,2", "-1", "+1"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		codes := svcqueue.DecodeTrailForTest(text)
		if len(codes) > svcqueue.MaxDeadTrailForTest {
			t.Fatalf("%q read as %d codes, more than a trail keeps", text, len(codes))
		}
		if codes == nil {
			return
		}
		if again := svcqueue.DecodeTrailForTest(svcqueue.EncodeTrailForTest(codes)); !slices.Equal(again, codes) {
			t.Fatalf("%q read as %v, written back and read as %v", text, codes, again)
		}
	})
}

// deadSeed is a record as a burial writes it.
func deadSeed(t testing.TB, record corequeue.DeadLetterValue) []byte {
	raw, ok := svcqueue.EncodeDeadForTest(record)
	if !ok {
		t.Fatalf("the seed %+v cannot be encoded", record)
	}
	return raw
}

// FuzzDecodeDead holds the dead-letter reader to three rules: it never
// panics; a record a burial wrote reads back and writes back to the same
// bytes; and whatever it accepts reads back the same once written again — so
// a record planted or cut short is either refused or read as one value, and
// never a Reason or a Cause a broker would refuse to write.
//
// Seen failing before the change: a reason line holding a carriage return,
// or longer than 120 runes, was read as it lay, and written again shorter —
//
//	the record reads back as {… Reason:"A\r…"}, then as {… Reason:"A …"}
func FuzzDecodeDead(f *testing.F) {
	enqueued := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	id := "1793620800000000000-" + fuzzEntropy
	base := corequeue.DeadLetterValue{
		Message:  corequeue.MessageValue{ID: id, EnqueuedAt: enqueued, Payload: []byte("payload")},
		FailedAt: enqueued.Add(time.Minute), Deliveries: 3, Reason: "TEST_REASON", Cause: "It failed", Code: 0x00_02_17_06,
	}
	withTrail := base
	withTrail.Trail = []uint32{0x00_02_17_06, 0x00_03_35_04}
	empty := base
	empty.Message.Payload = nil
	long := base
	long.Reason = strings.Repeat("é", 120)
	for _, seed := range []corequeue.DeadLetterValue{base, withTrail, empty, long} {
		f.Add(deadSeed(f, seed))
	}
	f.Add([]byte("ktnq/1\nid " + id + "\ntrail 1,x\nreason R\r\n\npayload"))
	f.Add([]byte("ktnq/1\nreason " + strings.Repeat("r", 200) + "\n\n"))
	f.Add([]byte("ktnq/1\n"))
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, raw []byte) {
		record, ok := svcqueue.DecodeDeadForTest(raw)
		if !ok {
			return
		}
		if len([]rune(record.Reason)) > 120 || len([]rune(record.Cause)) > 120 ||
			strings.ContainsAny(record.Reason+record.Cause, "\r\n") {
			t.Fatalf("the record reads back a reason or a cause no broker writes: %q / %q", record.Reason, record.Cause)
		}
		again, encodable := svcqueue.EncodeDeadForTest(record)
		if !encodable {
			return
		}
		reread, rereadOK := svcqueue.DecodeDeadForTest(again)
		if !rereadOK || !sameDecodedDeadLetter(record, reread) {
			t.Fatalf("the record reads back as %+v, then as %+v", record, reread)
		}
	})
}

// TestADeadLetterRecordAsABurialWritesItIsReadAndWrittenBackByteForByte pins
// the seeds' stronger rule: what encodeDead wrote, decodeDead reads, and
// encodeDead writes again to the very same bytes.
func TestADeadLetterRecordAsABurialWritesItIsReadAndWrittenBackByteForByte(t *testing.T) {
	t.Parallel()
	enqueued := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	record := corequeue.DeadLetterValue{
		Message:  corequeue.MessageValue{ID: "1793620800000000000-" + fuzzEntropy, EnqueuedAt: enqueued, Payload: []byte{0, 1, '\n', '\n', 2}},
		FailedAt: enqueued.Add(time.Second), Deliveries: 1, Reason: "R", Cause: "C", Code: 7, Trail: []uint32{7, 8},
	}
	raw := deadSeed(t, record)
	read, ok := svcqueue.DecodeDeadForTest(raw)
	if !ok || !sameDecodedDeadLetter(read, record) {
		t.Fatalf("the record reads back as %+v, %v, want %+v", read, ok, record)
	}
	if again := deadSeed(t, read); string(again) != string(raw) {
		t.Fatalf("written back as\n%q\nwant\n%q", again, raw)
	}
}

// sameDeadLetter compares two dead letters, their instants as instants.
func sameDecodedDeadLetter(a, b corequeue.DeadLetterValue) bool {
	if !a.FailedAt.Equal(b.FailedAt) || !a.Message.EnqueuedAt.Equal(b.Message.EnqueuedAt) {
		return false
	}
	a.FailedAt, b.FailedAt, a.Message.EnqueuedAt, b.Message.EnqueuedAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	if len(a.Message.Payload) == 0 && len(b.Message.Payload) == 0 {
		a.Message.Payload, b.Message.Payload = nil, nil
	}
	return reflect.DeepEqual(a, b)
}
