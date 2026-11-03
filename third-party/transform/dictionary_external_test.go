package transform_test

import (
	"bytes"
	"errors"
	"strconv"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	tptransform "github.com/kitsunium/sdk/third-party/transform"
)

// dictionaryIDA and dictionaryIDB are the IDs of the two dictionaries the
// tests train: above 32 767, the range RFC 8878 leaves to private use.
const (
	dictionaryIDA uint32 = 0x0001_0001
	dictionaryIDB uint32 = 0x0001_0002
)

// dictionaryRecords cuts the JSON corpus into its newline-delimited records:
// the small homogeneous payloads a dictionary is for.
func dictionaryRecords(tb testing.TB, n int) [][]byte {
	tb.Helper()
	lines := bytes.SplitAfter(buildJSONCorpus(256<<10), []byte("\n"))
	if len(lines) < n {
		tb.Fatalf("the corpus holds %d records, want %d", len(lines), n)
	}
	return lines[:n]
}

// trainDictionary builds a zstd dictionary under id from records — the
// format "zstd --train" writes, through the library's own builder: the first
// third of the records is its history, the rest the samples its entropy
// tables are fitted on. A history holding every sample would match them whole
// and leave the builder no literal to fit a table on.
func trainDictionary(tb testing.TB, id uint32, records [][]byte) []byte {
	tb.Helper()
	cut := len(records) / 3
	dict, err := zstd.BuildDict(zstd.BuildDictOptions{
		ID:       id,
		Contents: records[cut:],
		History:  bytes.Join(records[:cut], nil),
		Offsets:  [3]int{1, 4, 8},
		Level:    zstd.SpeedFastest,
	})
	if err != nil {
		tb.Fatalf("BuildDict: %v", err)
	}
	return dict
}

// newDictionaryCompressor builds a zstd compressor with dict and closes it
// with the test.
func newDictionaryCompressor(tb testing.TB, dict []byte) *tptransform.ZstdCompressor {
	tb.Helper()
	c, err := tptransform.NewZstdCompressorWithDictionary(tptransform.ZstdFastest, tptransform.DefaultMaxDecompressedBytes, dict)
	if err != nil {
		tb.Fatalf("NewZstdCompressorWithDictionary: %v", err)
	}
	tb.Cleanup(func() { closeChecked(tb, c) })
	return c
}

// TestADictionaryFrameDecodesOnlyWithItsDictionary is #900's second
// acceptance line: a frame compressed with a dictionary decompresses through a
// compressor holding the same dictionary, and through no other — one built
// with another dictionary, one built with none, and the registered singleton
// all refuse it with DictionaryMismatch, dst handed back, never a corrupt
// payload. The fields name both IDs.
func TestADictionaryFrameDecodesOnlyWithItsDictionary(t *testing.T) {
	t.Parallel()
	records := dictionaryRecords(t, 400)
	withA := newDictionaryCompressor(t, trainDictionary(t, dictionaryIDA, records[:200]))
	withB := newDictionaryCompressor(t, trainDictionary(t, dictionaryIDB, records[200:]))
	plain, err := tptransform.NewZstdCompressor(tptransform.ZstdFastest, tptransform.DefaultMaxDecompressedBytes)
	if err != nil {
		t.Fatalf("NewZstdCompressor: %v", err)
	}
	t.Cleanup(func() { closeChecked(t, plain) })

	record := records[300]
	frame, cerr := withA.Compress(nil, record)
	if cerr != nil {
		t.Fatalf("Compress: %v", cerr)
	}
	back, derr := withA.Decompress(nil, frame)
	if derr != nil || !bytes.Equal(back, record) {
		t.Fatalf("the dictionary's own compressor: err=%v, %d bytes, want the %d-byte record", derr, len(back), len(record))
	}
	for _, reader := range []struct {
		name string
		c    interface {
			Decompress(dst, src []byte) ([]byte, error)
		}
		id uint32
	}{
		{"another dictionary", withB, dictionaryIDB},
		{"no dictionary", plain, 0},
		{"the registered singleton", tptransform.Zstd, 0},
	} {
		dst := []byte("prefix")
		out, rerr := reader.c.Decompress(dst, frame)
		if !errs.HasCode(rerr, tptransform.CodeDictionaryMismatch) || !errors.Is(rerr, tptransform.DictionaryMismatch) {
			t.Fatalf("%s: err=%v, want DictionaryMismatch", reader.name, rerr)
		}
		if !bytes.Equal(out, dst) {
			t.Fatalf("%s: a refusal returned %d bytes, want dst back", reader.name, len(out))
		}
		if got, want := fieldOf(rerr, "frame_dictionary_id"), strconv.FormatUint(uint64(dictionaryIDA), 10); got != want {
			t.Fatalf("%s: frame_dictionary_id=%q, want %q", reader.name, got, want)
		}
		if got, want := fieldOf(rerr, "dictionary_id"), strconv.FormatUint(uint64(reader.id), 10); got != want {
			t.Fatalf("%s: dictionary_id=%q, want %q", reader.name, got, want)
		}
	}
}

// fieldOf returns the value err's chain carries under key, "" when none.
func fieldOf(err error, key string) string {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
}

// TestAPlainFrameDecodesThroughADictionaryCompressor pins the other
// direction: a frame written without a dictionary names none, and a compressor
// holding one reads it as it is.
func TestAPlainFrameDecodesThroughADictionaryCompressor(t *testing.T) {
	t.Parallel()
	records := dictionaryRecords(t, 200)
	withA := newDictionaryCompressor(t, trainDictionary(t, dictionaryIDA, records))
	frame, err := tptransform.Zstd.Compress(nil, records[7])
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	back, derr := withA.Decompress(nil, frame)
	if derr != nil || !bytes.Equal(back, records[7]) {
		t.Fatalf("err=%v, %d bytes, want the %d-byte record", derr, len(back), len(records[7]))
	}
}

// TestADictionaryShrinksASmallRecord is why the knob exists: on a record a few
// hundred bytes long, a dictionary trained on its siblings compresses it
// smaller than the generic encoder does. The ratios are in BENCH.md.
func TestADictionaryShrinksASmallRecord(t *testing.T) {
	t.Parallel()
	records := dictionaryRecords(t, 400)
	withA := newDictionaryCompressor(t, trainDictionary(t, dictionaryIDA, records[:300]))
	record := records[350]
	withDict, derr := withA.Compress(nil, record)
	without, perr := tptransform.Zstd.Compress(nil, record)
	if err := errors.Join(derr, perr); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	t.Logf("a %d-byte record: %d bytes with the dictionary, %d without", len(record), len(withDict), len(without))
	if len(withDict) >= len(without) {
		t.Fatalf("the dictionary did not shrink the record: %d bytes with it, %d without", len(withDict), len(without))
	}
}

// TestTheDictionaryIsRefusedWhenItIsNotOne pins the constructor's refusals:
// an empty dictionary, bytes that are not a zstd dictionary and a dictionary
// whose ID is zero are DictionaryInvalid, and the ceiling is still checked
// first. No compressor comes back with a refusal.
func TestTheDictionaryIsRefusedWhenItIsNotOne(t *testing.T) {
	t.Parallel()
	good := trainDictionary(t, dictionaryIDA, dictionaryRecords(t, 100))
	idZero := bytes.Clone(good)
	copy(idZero[4:8], []byte{0, 0, 0, 0})
	for _, c := range []struct {
		name string
		dict []byte
		max  int64
		want errs.Code
	}{
		{"nil", nil, tptransform.DefaultMaxDecompressedBytes, tptransform.CodeDictionaryInvalid},
		{"empty", []byte{}, tptransform.DefaultMaxDecompressedBytes, tptransform.CodeDictionaryInvalid},
		{"raw content, no magic", []byte(`{"level":"info","msg":"request completed"}`), tptransform.DefaultMaxDecompressedBytes, tptransform.CodeDictionaryInvalid},
		{"truncated", good[:16], tptransform.DefaultMaxDecompressedBytes, tptransform.CodeDictionaryInvalid},
		{"id zero", idZero, tptransform.DefaultMaxDecompressedBytes, tptransform.CodeDictionaryInvalid},
		{"a good dictionary, no ceiling", good, 0, tptransform.CodeLimitMisconfigured},
	} {
		got, err := tptransform.NewZstdCompressorWithDictionary(tptransform.ZstdFastest, c.max, c.dict)
		if !errs.HasCode(err, c.want) || got != nil {
			t.Fatalf("%s: compressor=%v err=%v, want no compressor and code %v", c.name, got, err, c.want)
		}
	}
}

// TestTheDictionaryIsTheCompressorsOwn pins the copy: the library keeps
// slices of the dictionary it loads, so a caller that reuses its buffer after
// construction must not change what the compressor encodes and decodes with.
// DictionaryID reports the ID it was built with, and 0 without one.
func TestTheDictionaryIsTheCompressorsOwn(t *testing.T) {
	t.Parallel()
	records := dictionaryRecords(t, 200)
	dict := trainDictionary(t, dictionaryIDA, records)
	withA := newDictionaryCompressor(t, dict)
	//: the caller's buffer is reused for something else entirely.
	for i := range dict {
		dict[i] = 0xa5
	}
	frame, err := withA.Compress(nil, records[42])
	if err != nil {
		t.Fatalf("Compress: %v", err)
	}
	back, derr := withA.Decompress(nil, frame)
	if derr != nil || !bytes.Equal(back, records[42]) {
		t.Fatalf("after the caller's slice changed: err=%v, %d bytes, want the %d-byte record", derr, len(back), len(records[42]))
	}
	if withA.DictionaryID() != dictionaryIDA {
		t.Fatalf("DictionaryID()=%#x, want %#x", withA.DictionaryID(), dictionaryIDA)
	}
	if tptransform.Zstd.(*tptransform.ZstdCompressor).DictionaryID() != 0 {
		t.Fatal("the registered singleton reports a dictionary")
	}
}
