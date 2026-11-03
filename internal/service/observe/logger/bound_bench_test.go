package logger_test

import (
	"strconv"
	"testing"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svclogger "github.com/kitsunium/sdk/internal/service/observe/logger"
	"github.com/kitsunium/sdk/internal/service/observe/logger/encoder"
)

// boundAttrs is n bound attributes of the usual service shape: short string
// keys and values (service, version, instance, region, pod…).
func boundAttrs(n int) []corelogger.AttrValue {
	attrs := make([]corelogger.AttrValue, n)
	for i := range attrs {
		attrs[i] = corelogger.AttrValue{Key: "bound_" + strconv.Itoa(i), Value: corelogger.StringValue("value-of-attribute-" + strconv.Itoa(i))}
	}
	return attrs
}

// BenchmarkEmit_Bound prices one static emit through a Logger bound with 0, 8
// and 32 attributes, for the text and the JSON encoder (#1016): the bound set
// is rendered once at With and spliced, so the 8- and 32-bound rows sit near
// the 0-bound one instead of growing ~75 ns per attribute.
func BenchmarkEmit_Bound(b *testing.B) {
	encoders := []struct {
		name string
		enc  corelogger.Encoder
	}{
		{"text", encoder.NewText(clock.System)},
		{"json", encoder.NewJSON(clock.System)},
	}
	for _, e := range encoders {
		for _, n := range []int{0, 8, 32} {
			b.Run(e.name+"/"+strconv.Itoa(n)+"Bound", func(b *testing.B) {
				h, err := svclogger.NewHandler(e.enc, discardSink{}, level.Info)
				if err != nil {
					b.Fatal(err)
				}
				lg, err := svclogger.New(h)
				if err != nil {
					b.Fatal(err)
				}
				lg = lg.With(boundAttrs(n)...)
				ctx := b.Context()
				b.ReportAllocs()
				for b.Loop() {
					lg.Log(ctx, level.Info, "request served")
				}
			})
		}
	}
}
