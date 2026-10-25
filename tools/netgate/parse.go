// Package main — the parser: `go test -bench` output read into samples, one
// per result line and metric, keyed by load and engine.
package main

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// One top-level benchmark per load, so a run can interleave them:
// BenchmarkLoad<load>/engine=<label>.
const (
	// benchPrefix starts the benchmark of every load.
	benchPrefix string = "BenchmarkLoad"
	// enginePrefix introduces the engine's label in a load's sub-benchmark
	// name.
	enginePrefix string = "engine="
)

// seriesKey names one engine on one load.
type seriesKey struct {
	// Load is L1, L2 or L3.
	Load string
	// Engine is the label after engine=.
	Engine string
}

// metricSample is one value of one metric on one result line.
type metricSample struct {
	// Unit is the metric's unit, as ReportMetric names it.
	Unit string
	// Value is the line's value.
	Value float64
}

// samplesEntity holds every sample read, by engine and load, then by metric.
type samplesEntity map[seriesKey]map[string][]float64

// parseBench reads `go test -bench` output and keeps every BenchmarkLoad line:
// one sample per line and per metric. Other lines — headers, other
// benchmarks, PASS — are skipped.
func parseBench(r io.Reader) (samplesEntity, error) {
	out := samplesEntity{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	//: one result per line.
	for sc.Scan() {
		key, metrics, ok := parseLine(sc.Text())
		//: not a load's result.
		if !ok {
			//: skipped.
			continue
		}
		//: first sample of this series.
		if out[key] == nil {
			out[key] = map[string][]float64{}
		}
		//: each metric gains one sample.
		for _, m := range metrics {
			out[key][m.Unit] = append(out[key][m.Unit], m.Value)
		}
	}
	//: a read error is the caller's, the samples read so far are not judged.
	return out, sc.Err()
}

// parseLine reads one result line: the name, the iteration count, then value
// and unit pairs.
func parseLine(line string) (seriesKey, []metricSample, bool) {
	f := strings.Fields(line)
	//: a name, a count and at least one pair.
	if len(f) < 4 || !strings.HasPrefix(f[0], benchPrefix) {
		//: not a result of the loads.
		return seriesKey{}, nil, false
	}
	key, ok := parseName(f[0])
	//: a BenchmarkLoad name of another shape.
	if !ok {
		//: not a load and an engine.
		return seriesKey{}, nil, false
	}
	//: the iteration count proves this is a result line, not a log line.
	if _, err := strconv.ParseUint(f[1], 10, 64); err != nil {
		//: a log line that starts with the name.
		return seriesKey{}, nil, false
	}
	var metrics []metricSample
	//: value then unit.
	for i := 2; i+1 < len(f); i += 2 {
		v, err := strconv.ParseFloat(f[i], 64)
		//: a malformed pair ends the line's metrics.
		if err != nil {
			//: what was read stands.
			break
		}
		metrics = append(metrics, metricSample{Unit: f[i+1], Value: v})
	}
	//: a result line.
	return key, metrics, len(metrics) > 0
}

// parseName splits BenchmarkLoad<load>/engine=<label>[-<procs>].
func parseName(name string) (seriesKey, bool) {
	rest := strings.TrimPrefix(name, benchPrefix)
	//: the GOMAXPROCS suffix the testing package appends.
	if i := strings.LastIndexByte(rest, '-'); i > 0 {
		//: only a numeric suffix is the procs count.
		if _, err := strconv.Atoi(rest[i+1:]); err == nil {
			rest = rest[:i]
		}
	}
	load, engine, ok := strings.Cut(rest, "/")
	//: exactly a load, then the engine.
	if !ok || load == "" || !strings.HasPrefix(engine, enginePrefix) || strings.Contains(engine, "/") {
		//: another shape.
		return seriesKey{}, false
	}
	label := strings.TrimPrefix(engine, enginePrefix)
	//: an empty label names nothing.
	return seriesKey{Load: load, Engine: label}, label != ""
}
