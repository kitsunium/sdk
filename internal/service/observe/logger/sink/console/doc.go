// Package console implements a Sink that writes formatted records onto an
// io.Writer (typically os.Stdout or os.Stderr) under a local mutex so
// concurrent goroutines emit atomic lines. It is the default Sink shipped
// by pkg/v1/observe/logger.Default.
//
// The console sink intentionally ignores the originating RecordEvent — it
// just streams the encoder's output verbatim. Sinks that need structured
// metadata (CloudWatch, S3, Kafka) read it from r in their own packages.
package console
