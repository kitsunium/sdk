package logger

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

func Test_parseLevel(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		want Level
	}
	tests := []tc{
		{"debug name maps to LevelDebug", "debug", LevelDebug},
		{"warn name maps to LevelWarn", "warn", LevelWarn},
		{"error name maps to LevelError", "error", LevelError},
		{"info name maps to LevelInfo", "info", LevelInfo},
		{"mixed case folds", "DeBuG", LevelDebug},
		{"empty defaults to info", "", LevelInfo},
		{"unrecognised defaults to info", "trace", LevelInfo},
	}
	runCase := func(t *testing.T, in string, want Level) {
		t.Helper()
		//: the parsed level must match the documented mapping / default.
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q)=%v want %v", in, got, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.in, c.want)
		})
	}
}

func TestToLowerASCII(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
		want string
	}
	tests := []tc{
		{"all upper folds", "WARN", "warn"},
		{"mixed folds", "DeBuG", "debug"},
		{"already lower unchanged", "info", "info"},
		{"empty unchanged", "", ""},
		{"digits and dashes pass through", "lvl-3", "lvl-3"},
	}
	runCase := func(t *testing.T, in, want string) {
		t.Helper()
		//: only ASCII A-Z fold; every other byte is copied verbatim.
		if got := toLowerASCII(in); got != want {
			t.Errorf("toLowerASCII(%q)=%q want %q", in, got, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.in, c.want)
		})
	}
}

func TestRedactWriterFailure(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		writer string
		reason string
	}
	tests := []tc{
		{"names writer and reason only", "s3", "writer rejected its config"},
		{"unknown name failure", "bogus", "unknown writer name"},
	}
	runCase := func(t *testing.T, writer, reason string) {
		t.Helper()
		err := redactWriterFailure(writer, reason)
		//: the redacted failure must carry the TopologyInvalid code.
		if !errs.HasCode(err, CodeTopologyInvalid) {
			t.Errorf("redactWriterFailure code mismatch: %v", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.writer, c.reason)
		})
	}
}

func TestDecodeTopology(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		format  Format
		raw     string
		wantErr bool
	}
	tests := []tc{
		{"unregistered format is rejected", Format("c14-no-such-codec"), `{}`, true},
	}
	runCase := func(t *testing.T, format Format, raw string, wantErr bool) {
		t.Helper()
		_, err := decodeTopology(format, []byte(raw))
		//: an unregistered codec must surface the redacted topology sentinel.
		if (err != nil) != wantErr {
			t.Errorf("decodeTopology err=%v wantErr=%v", err, wantErr)
		}
		//: the failure must carry the TopologyInvalid code (redacted).
		if wantErr && !errs.HasCode(err, CodeTopologyInvalid) {
			t.Errorf("decodeTopology err=%v want TopologyInvalid", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.format, c.raw, c.wantErr)
		})
	}
}

func TestResolveSinks(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		entries []WriterEntryConfig
		wantErr bool
	}
	tests := []tc{
		{"unknown writer name aborts", []WriterEntryConfig{{Name: "c14-ghost"}}, true},
	}
	runCase := func(t *testing.T, entries []WriterEntryConfig, wantErr bool) {
		t.Helper()
		_, err := resolveSinks(entries)
		//: an unresolved writer must surface the redacted topology sentinel.
		if (err != nil) != wantErr {
			t.Errorf("resolveSinks err=%v wantErr=%v", err, wantErr)
		}
		//: the failure must carry the TopologyInvalid code (redacted).
		if wantErr && !errs.HasCode(err, CodeTopologyInvalid) {
			t.Errorf("resolveSinks err=%v want TopologyInvalid", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.entries, c.wantErr)
		})
	}
}

func TestOpenEntry(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		entry   WriterEntryConfig
		wantErr bool
	}
	tests := []tc{
		{"unknown writer name is redacted", WriterEntryConfig{Name: "c14-absent"}, true},
	}
	runCase := func(t *testing.T, entry WriterEntryConfig, wantErr bool) {
		t.Helper()
		_, err := openEntry(entry)
		//: an unresolved writer must surface the redacted topology sentinel.
		if (err != nil) != wantErr {
			t.Errorf("openEntry err=%v wantErr=%v", err, wantErr)
		}
		//: the failure must carry the TopologyInvalid code (redacted).
		if wantErr && !errs.HasCode(err, CodeTopologyInvalid) {
			t.Errorf("openEntry err=%v want TopologyInvalid", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c.entry, c.wantErr)
		})
	}
}
