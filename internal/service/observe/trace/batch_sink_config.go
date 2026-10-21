package trace

import (
	"time"

	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// BatchSinkConfig configures a BatchSink. Exporter is required; every other
// field has a resolved meaning when left unset — the specification's default
// for the four sizes and durations, and never "unbounded" (ADR 0031).
type BatchSinkConfig struct {
	// Exporter receives every batch. Required: NewBatchSink refuses a nil one
	// with BatchExporterMissing. One that implements ContextSpanExporter is
	// bounded by ExportTimeout.
	Exporter coretrace.SpanExporter
	// Resource identifies the producer, stamped on every batch.
	Resource coreotel.ResourceValue
	// Scope identifies the instrumentation, stamped on every batch.
	Scope coreotel.ScopeValue
	// MaxQueueSize bounds how many finished spans wait for export.
	// Non-positive CLAMPS to DefaultBatchMaxQueueSize (2048).
	MaxQueueSize int
	// ScheduledDelay is the longest a queued span waits before an export
	// starts. Non-positive CLAMPS to DefaultBatchScheduledDelay (5 s).
	ScheduledDelay time.Duration
	// ExportTimeout bounds one export, on Clock. Non-positive CLAMPS to
	// DefaultBatchExportTimeout (30 s).
	ExportTimeout time.Duration
	// MaxExportBatchSize bounds how many spans one export carries, and is the
	// fill level that starts an export before ScheduledDelay. Non-positive
	// CLAMPS to DefaultBatchMaxExportBatchSize (512); above MaxQueueSize it
	// clamps to MaxQueueSize, as the specification requires.
	MaxExportBatchSize int
	// Clock drives ScheduledDelay and ExportTimeout. Unset CLAMPS to
	// clock.System; a test injects a ManualClock and waits on nothing.
	Clock clock.Timed
	// OnError receives every export failure, from the exporting goroutine. Nil
	// discards it; Failed counts the spans either way. It must not block: the
	// next export waits for it.
	OnError func(err error)
}

// resolved returns c with every clamp applied once: a non-positive size or
// duration takes the specification's default, a batch larger than the queue
// is the queue, and an unset clock is the system's.
func (c BatchSinkConfig) resolved() BatchSinkConfig {
	//: the queue bound first — the batch clamp reads it.
	if c.MaxQueueSize <= 0 {
		//: maxQueueSize 2048.
		c.MaxQueueSize = DefaultBatchMaxQueueSize
	}
	//: scheduledDelayMillis 5000.
	if c.ScheduledDelay <= 0 {
		c.ScheduledDelay = DefaultBatchScheduledDelay
	}
	//: exportTimeoutMillis 30000.
	if c.ExportTimeout <= 0 {
		c.ExportTimeout = DefaultBatchExportTimeout
	}
	//: maxExportBatchSize 512.
	if c.MaxExportBatchSize <= 0 {
		c.MaxExportBatchSize = DefaultBatchMaxExportBatchSize
	}
	//: the specification: it MUST be less than or equal to maxQueueSize.
	c.MaxExportBatchSize = min(c.MaxExportBatchSize, c.MaxQueueSize)
	//: the real clock is a description, not a guess.
	if c.Clock == nil {
		c.Clock = clock.System
	}
	//: the resolved configuration.
	return c
}
