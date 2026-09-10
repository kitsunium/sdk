package id_test

import (
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/id"
)

// sinks so no generated identifier can be proven unused and elided.
var (
	strSink string
	errSink error
)

// BenchmarkUUIDv4 through BenchmarkKSUID are the point of this file: seven
// schemes, one workload, one table. A consumer picking a scheme is choosing
// between properties — sortability, embedded time, length, entropy — and the
// cost is the one axis nobody publishes. Now it is published.
func BenchmarkUUIDv4(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		strSink, errSink = id.UUIDv4()
	}
}

func BenchmarkUUIDv7(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		strSink, errSink = id.UUIDv7()
	}
}

func BenchmarkULID(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		strSink, errSink = id.ULID()
	}
}

func BenchmarkSnowflake(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		strSink, errSink = id.Snowflake()
	}
}

func BenchmarkNanoID(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		strSink, errSink = id.NanoID()
	}
}

func BenchmarkKSUID(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		strSink, errSink = id.KSUID()
	}
}

func BenchmarkTypeID(b *testing.B) {
	g, err := id.NewTypeID("user")
	if err != nil {
		b.Fatalf("NewTypeID: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, errSink = g.New()
	}
}

// BenchmarkNewDispatch measures the registry lookup a caller pays when the
// scheme is a VALUE rather than a call — config-driven code takes this path.
// The delta against the direct helper is what the indirection costs.
func BenchmarkNewDispatch(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		strSink, errSink = id.New(id.UUIDv7Scheme)
	}
}

// BenchmarkSnowflakeParallel is the one scheme with shared mutable state — a
// per-node sequence counter that must not repeat within a millisecond — so it
// is the one whose contended cost differs from its serial cost.
func BenchmarkSnowflakeParallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		var local string
		for pb.Next() {
			generated, genErr := id.Snowflake()
			if genErr != nil {
				b.Errorf("Snowflake: %v", genErr)
				return
			}
			local = generated
		}
		strSink = local
	})
}

// BenchmarkParseKSUID and BenchmarkParseTypeID cover the read side: a KSUID
// carries its issue time and a TypeID its prefix, and both are only useful if
// getting them back is cheap.
func BenchmarkParseKSUID(b *testing.B) {
	seed, err := id.KSUID()
	if err != nil {
		b.Fatalf("KSUID: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _, errSink = id.ParseKSUID(seed)
	}
}

func BenchmarkParseTypeID(b *testing.B) {
	g, err := id.NewTypeID("user")
	if err != nil {
		b.Fatalf("NewTypeID: %v", err)
	}
	seed, err := g.New()
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink, _, errSink = id.ParseTypeID(seed)
	}
}
