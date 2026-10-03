package codec_test

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// Package-level constants first (KTN-CONST-ORDER: const → var → type → func).
// The iota blocks forward-reference the mtxShape / mtxTier types declared
// below — legal for package-level declarations.

// fixtureSeed is the pinned PRNG seed for the deterministic 5x11 fixture
// matrix ("CODEC42"). math/rand's algorithm is documented version-stable, so
// a fixed seed yields the same matrix across runs, machines, and Go releases.
const fixtureSeed int64 = 0xC0DEC42

const (
	// mtxScalar is a single signed integer.
	mtxScalar mtxShape = iota
	// mtxFlatStruct is a flat record of scalar fields.
	mtxFlatStruct
	// mtxNestedStruct wraps a flat record one level deep.
	mtxNestedStruct
	// mtxStructSlice is a homogeneous slice of flat records.
	mtxStructSlice
	// mtxStringMap is a map[string]int.
	mtxStringMap
	// mtxBytesBlob is a []byte payload.
	mtxBytesBlob
	// mtxDeeplyNested is a fixed-depth nested container.
	mtxDeeplyNested
	// mtxWideFlat is a wide single-level map[string]string record.
	mtxWideFlat
	// mtxUnicodeString is a multibyte string.
	mtxUnicodeString
	// mtxNumericArray is a []float64.
	mtxNumericArray
	// mtxMixed is a heterogeneous map[string]any.
	mtxMixed
	// mtxShapeCount is the shape-enum cardinality (sentinel, not a shape).
	mtxShapeCount
)

const (
	// mtxEmpty yields zero-length collections.
	mtxEmpty mtxTier = iota
	// mtxTiny is a 4-element tier.
	mtxTiny
	// mtxSmall is a 16-element tier.
	mtxSmall
	// mtxMedium is a 128-element tier.
	mtxMedium
	// mtxLarge is a 1024-element tier.
	mtxLarge
	// mtxTierCount is the tier-enum cardinality (sentinel, not a tier).
	mtxTierCount
)

// mtxShape enumerates the 11 payload shapes the matrix spans — the
// structural dimensions a codec's encoder/decoder pays differently for.
type mtxShape int

// mtxTier enumerates the 5 size tiers applied to collection shapes.
type mtxTier int

// mtxFlat is the flat record used by the struct shapes.
type mtxFlat struct {
	A int64   `json:"a"`
	B string  `json:"b"`
	C bool    `json:"c"`
	D float64 `json:"d"`
}

// mtxNested wraps mtxFlat one level deep.
type mtxNested struct {
	Name  string  `json:"name"`
	Inner mtxFlat `json:"inner"`
}

// mtxBuilder draws deterministic fixture values from a single seeded PRNG.
// The rng is a field (not a parameter) so the value-drawing helpers are
// methods — no concrete-type parameter to abstract, no helper interfaces.
type mtxBuilder struct {
	rng *rand.Rand
}

// String names the shape for subtest / sub-bench identifiers.
func (s mtxShape) String() string {
	//: parallel name table; index is the iota value.
	names := [mtxShapeCount]string{
		"scalar", "flat-struct", "nested-struct", "struct-slice", "string-map",
		"bytes-blob", "deeply-nested", "wide-flat", "unicode-string",
		"numeric-array", "mixed",
	}
	//: guard against an unnamed future shape.
	if s < 0 || s >= mtxShapeCount {
		return "unknown"
	}
	return names[s]
}

// String names the tier for subtest / sub-bench identifiers.
func (t mtxTier) String() string {
	//: parallel name table, same pattern as mtxShape.String.
	names := [mtxTierCount]string{"empty", "tiny", "small", "medium", "large"}
	//: guard against an unnamed future tier.
	if t < 0 || t >= mtxTierCount {
		return "unknown"
	}
	return names[t]
}

// mtxScale maps a tier to the element / length factor for collection shapes.
// Scalars and fixed-shape records ignore it; mtxEmpty yields zero-length
// collections (distinct from a nil/zero value).
func mtxScale(t mtxTier) int {
	//: geometric tiers keep encoded payloads well under the codec caps.
	switch t {
	case mtxEmpty:
		return 0
	case mtxTiny:
		return 4
	case mtxSmall:
		return 16
	case mtxMedium:
		return 128
	case mtxLarge:
		return 1024
	default:
		//: mtxTierCount / any future sentinel — no scale.
		return 0
	}
}

// newMtxBuilder seeds a builder deterministically from (shape, tier) so every
// matrix cell is independent yet reproducible.
func newMtxBuilder(shape mtxShape, tier mtxTier) mtxBuilder {
	//: mix shape + tier into the base seed so cells don't share a stream.
	return mtxBuilder{rng: rand.New(rand.NewSource(fixtureSeed + int64(shape)*131 + int64(tier)))}
}

// str draws a deterministic n-byte ASCII string.
func (b mtxBuilder) str(n int) string {
	const alpha = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	//: pre-size the builder; one alphabet pick per byte keeps it deterministic.
	var sb strings.Builder
	sb.Grow(n)
	for range n {
		sb.WriteByte(alpha[b.rng.Intn(len(alpha))])
	}
	return sb.String()
}

// flat draws a deterministic mtxFlat (fixed field order).
func (b mtxBuilder) flat() mtxFlat {
	return mtxFlat{A: b.rng.Int63(), B: b.str(8), C: b.rng.Intn(2) == 1, D: b.rng.Float64()}
}

// matrixFixture returns the deterministic fixture for one (shape, tier) cell.
// Every value is JSON-serialisable so the matrix can drive any universal
// codec; collection shapes scale with mtxScale(tier).
func matrixFixture(shape mtxShape, tier mtxTier) any {
	b := newMtxBuilder(shape, tier)
	n := mtxScale(tier)
	//: dispatch on shape; each arm consumes the rng in a fixed order.
	switch shape {
	case mtxScalar:
		//: a single signed integer.
		return b.rng.Int63()
	case mtxFlatStruct:
		//: one flat record.
		return b.flat()
	case mtxNestedStruct:
		//: a record one level deep.
		return mtxNested{Name: b.str(8), Inner: b.flat()}
	case mtxStructSlice:
		//: n flat records.
		out := make([]mtxFlat, n)
		for i := range out {
			out[i] = b.flat()
		}
		return out
	case mtxStringMap:
		//: n string→int entries (string keys → stable json ordering).
		out := make(map[string]int, n)
		for i := range n {
			out[fmt.Sprintf("k%d", i)] = b.rng.Intn(1 << 20)
		}
		return out
	case mtxBytesBlob:
		//: n deterministic bytes (json encodes []byte as base64).
		out := make([]byte, n)
		for i := range out {
			out[i] = byte(b.rng.Intn(256))
		}
		return out
	case mtxDeeplyNested:
		//: fixed 4-level nesting with a scaled scalar leaf.
		cur := any(b.rng.Int63())
		for range 4 {
			cur = map[string]any{"v": cur, "tag": b.str(4)}
		}
		return cur
	case mtxWideFlat:
		//: n string→string entries — a wide single-level record.
		out := make(map[string]string, n)
		for i := range n {
			out[fmt.Sprintf("f%d", i)] = b.str(6)
		}
		return out
	case mtxUnicodeString:
		//: a multibyte string repeated to scale with the tier.
		return strings.Repeat("héllo-世界-🌸 ", n+1)
	case mtxNumericArray:
		//: n float64s.
		out := make([]float64, n)
		for i := range out {
			out[i] = b.rng.Float64() * 1e6
		}
		return out
	case mtxMixed:
		//: a heterogeneous record mixing several shapes.
		return map[string]any{
			"n":      b.rng.Int63(),
			"s":      b.str(6),
			"arr":    []int{b.rng.Intn(100), b.rng.Intn(100)},
			"nested": b.flat(),
		}
	default:
		//: unreachable — every shape < mtxShapeCount is handled above.
		return nil
	}
}

// matrixCell is one (shape, tier) coordinate of the matrix.
type matrixCell struct {
	Shape mtxShape
	Tier  mtxTier
}

// matrixCells returns every (shape, tier) pair — the iteration order the
// determinism gate and any matrix bench share.
func matrixCells() []matrixCell {
	out := make([]matrixCell, 0, int(mtxShapeCount)*int(mtxTierCount))
	//: row-major shape × tier (range-over-int keeps the enum types).
	for s := range mtxShapeCount {
		for tr := range mtxTierCount {
			out = append(out, matrixCell{Shape: s, Tier: tr})
		}
	}
	return out
}

// TestFixtureDeterminism is the matrix's regression gate: for every one of
// the 5×11 cells, building the fixture twice from the pinned seed must yield
// byte-identical JSON. This proves the seeded construction is deterministic
// (the property a bench matrix relies on for stable allocator pressure);
// cross-run / cross-machine stability follows from math/rand's
// version-stable algorithm. A failure means a fixture acquired
// non-determinism (map iteration into the wire, time.Now, unseeded rand, …).
func TestFixtureDeterminism(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		shape mtxShape
		tier  mtxTier
	}
	//: one case per (shape, tier) cell — 55 in total.
	tests := make([]tc, 0, int(mtxShapeCount)*int(mtxTierCount))
	for _, cell := range matrixCells() {
		tests = append(tests, tc{name: cell.Shape.String() + "/" + cell.Tier.String(), shape: cell.Shape, tier: cell.Tier})
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: serialise the cell built from the pinned seed twice.
		first, err := stdjson.Marshal(matrixFixture(tc.shape, tc.tier))
		if err != nil {
			t.Fatalf("%s: marshal #1: %v", tc.name, err)
		}
		second, err := stdjson.Marshal(matrixFixture(tc.shape, tc.tier))
		if err != nil {
			t.Fatalf("%s: marshal #2: %v", tc.name, err)
		}
		//: identical bytes ⇒ the seeded construction is deterministic.
		if !bytes.Equal(first, second) {
			t.Errorf("%s: non-deterministic fixture\n #1=%s\n #2=%s", tc.name, first, second)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}
