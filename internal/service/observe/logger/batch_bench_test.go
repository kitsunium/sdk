package logger_test

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/service/observe/logger/sink/file"
)

// benchBatchSize is the batch the async drainer hands a BatchSink with
// Config.BatchMax 16 under a backlog.
const benchBatchSize = 16

// BenchmarkSink_File_Batch16 measures the file sink's WriteBatch of 16 records
// — one write(2) for the batch — and reports the cost per record beside
// Sink_File's one write(2) per record (#807).
func BenchmarkSink_File_Batch16(b *testing.B) {
	dir := benchPrivateDir(b)
	s, err := file.New(filepath.Join(dir, "bench.log"))
	if err != nil {
		b.Fatalf("file.New err = %v", err)
	}
	b.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			b.Errorf("Close err = %v", cerr)
		}
	})
	bs, ok := s.(corelogger.BatchSink)
	if !ok {
		b.Fatal("the file sink is not a BatchSink")
	}
	recs := make([]corelogger.RecordEvent, benchBatchSize)
	payloads := make([][]byte, benchBatchSize)
	for i := range payloads {
		recs[i] = benchRecord
		payloads[i] = benchPayload
	}
	ctx := b.Context()
	_, benchWriteErr = bs.WriteBatch(ctx, recs, payloads)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, benchWriteErr = bs.WriteBatch(ctx, recs, payloads)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*benchBatchSize), "ns/record")
}

// BenchmarkSink_File_WriteDuringFlush measures parallel writers to one file
// sink while a goroutine flushes it continuously (#815): with the fsync under
// the write mutex every writer waited for the disk; now the reported
// max-ns/write is a write's own worst case, not an fsync's.
func BenchmarkSink_File_WriteDuringFlush(b *testing.B) {
	dir := benchPrivateDir(b)
	s, err := file.New(filepath.Join(dir, "bench.log"))
	if err != nil {
		b.Fatalf("file.New err = %v", err)
	}
	b.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			b.Errorf("Close err = %v", cerr)
		}
	})
	stop := make(chan struct{})
	var flusher sync.WaitGroup
	flusher.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = s.Flush(b.Context())
			}
		}
	})
	var worst atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := b.Context()
		local := int64(0)
		for pb.Next() {
			start := time.Now()
			_, _ = s.Write(ctx, benchRecord, benchPayload)
			local = max(local, int64(time.Since(start)))
		}
		for {
			cur := worst.Load()
			if local <= cur || worst.CompareAndSwap(cur, local) {
				break
			}
		}
	})
	b.StopTimer()
	close(stop)
	flusher.Wait()
	b.ReportMetric(float64(worst.Load()), "max-ns/write")
}
