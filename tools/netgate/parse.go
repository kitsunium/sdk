// Package main — the parser: `go test -bench` output read into samples, one
// per result line and metric, keyed by load and engine pair.
package main

import (
	"bufio"
	"io"
	"math"
	"strconv"
	"strings"
)

// One top-level benchmark per load, a sub-benchmark per pair of engines
// measured side by side in one child: BenchmarkLoad<load>/pair=<A>:<B>. Each
// line is one round, its metrics a:<metric>, b:<metric> and ratio:<metric>,
// B over A.
const (
	// benchPrefix starts the benchmark of every load.
	benchPrefix string = "BenchmarkLoad"
	// pairPrefix introduces the pair's labels in a load's sub-benchmark name.
	pairPrefix string = "pair="
	// pairSep separates engine A's label from engine B's.
	pairSep string = ":"
	// ratioUnit prefixes a metric's per-round ratio, B over A.
	ratioUnit string = "ratio:"
	// aUnit prefixes engine A's own value of a metric.
	aUnit string = "a:"
	// bUnit prefixes engine B's own value of a metric.
	bUnit string = "b:"
)

// seriesKey names one pair of engines on one load.
type seriesKey struct {
	// Load is L1, L2 or L3.
	Load string
	// A is the reference engine's label.
	A string
	// B is the measured engine's label.
	B string
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
		//: a malformed pair, or a NaN or an Inf ParseFloat accepts, ends the
		//: line's metrics: a non-finite sample would shift the interval.
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			//: what was read stands.
			break
		}
		metrics = append(metrics, metricSample{Unit: f[i+1], Value: v})
	}
	//: a result line.
	return key, metrics, len(metrics) > 0
}

// parseName splits BenchmarkLoad<load>/pair=<A>:<B>[-<procs>].
func parseName(name string) (seriesKey, bool) {
	rest := strings.TrimPrefix(name, benchPrefix)
	//: the GOMAXPROCS suffix the testing package appends.
	if i := strings.LastIndexByte(rest, '-'); i > 0 {
		//: only a numeric suffix is the procs count.
		if _, err := strconv.Atoi(rest[i+1:]); err == nil {
			rest = rest[:i]
		}
	}
	load, pair, ok := strings.Cut(rest, "/")
	//: exactly a load, then the pair.
	if !ok || load == "" || !strings.HasPrefix(pair, pairPrefix) || strings.Contains(pair, "/") {
		//: another shape.
		return seriesKey{}, false
	}
	a, b, ok := splitPair(strings.TrimPrefix(pair, pairPrefix))
	//: the load and both labels.
	return seriesKey{Load: load, A: a, B: b}, ok
}

// splitPair splits "<A>:<B>" into its two labels, neither empty, with one
// separator only.
func splitPair(pair string) (string, string, bool) {
	a, b, ok := strings.Cut(pair, pairSep)
	//: two labels, neither empty, no third.
	return a, b, ok && a != "" && b != "" && !strings.Contains(b, pairSep)
}
