package otlp_test

import (
	"bytes"
	"math"
	"reflect"
	"testing"

	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/service/observe/internal/otlp"
)

// arenaSets are attribute sets of every kind, the zeros a oneof must still
// state, an invalid attribute and the empty set among them.
func arenaSets() [][]coreotel.AttrValue {
	return [][]coreotel.AttrValue{
		{coreotel.String("http.request.method", "GET"), coreotel.Int64("http.response.status_code", 200)},
		nil,
		{coreotel.Bool("cache.hit", false), coreotel.Float64("ratio", math.Inf(-1)), coreotel.String("empty", "")},
		{coreotel.Int64("zero", 0), coreotel.Bool("on", true), coreotel.Float64("pi", 3.25), {Key: "invalid"}},
		{coreotel.String("route", "/v1/a<b>&c")},
	}
}

// TestArenaBuildsWhatAttrsBuilds pins #813's arena: every set it hands out is
// the one Attrs builds, value for value and byte for byte once marshalled, at
// any capacity — an arena sized for nothing grows and still hands out the
// right values — and one set is capped at its own end, so an append to it
// never writes into the next.
func TestArenaBuildsWhatAttrsBuilds(t *testing.T) {
	t.Parallel()
	for _, size := range []int{0, 1, 64} {
		arena := otlp.NewArena(size, size)
		var got, want [][]otlp.KeyValue
		for _, set := range arenaSets() {
			got = append(got, arena.Attrs(set))
			want = append(want, otlp.Attrs(set))
		}
		asInt, asDouble := arena.Int64(math.MinInt64), arena.Double(-0.5)
		if !reflect.DeepEqual(got, want) || *asInt != math.MinInt64 || *asDouble != -0.5 {
			t.Fatalf("arena of %d: %+v, want %+v (and the two values)", size, got, want)
		}
		gotDoc, err := otlp.Marshal(got, &appendFailure)
		if err != nil {
			t.Fatal(err)
		}
		wantDoc, err := otlp.Marshal(want, &appendFailure)
		if err != nil || !bytes.Equal(gotDoc, wantDoc) {
			t.Fatalf("arena of %d marshals %s, want %s (%v)", size, gotDoc, wantDoc, err)
		}
		if got[1] != nil {
			t.Fatalf("arena of %d: the empty set is %v, want nil", size, got[1])
		}
		_ = append(got[0], otlp.KeyValue{Key: "appended"})
		if !reflect.DeepEqual(got, want) || cap(got[0]) != len(got[0]) {
			t.Fatalf("arena of %d: a set is not capped at its own end", size)
		}
	}
}
