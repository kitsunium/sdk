package net_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// sseErrSink keeps Validate's verdict reachable so no call can be proven
// unused and elided.
var sseErrSink error

// BenchmarkSSEValidate prices the pre-flight check on its own. The encoder —
// internal/service/net/sse.AppendEvent — runs it before it touches the
// caller's buffer, so it is paid on every frame including the ones that go on
// to be encoded.
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
