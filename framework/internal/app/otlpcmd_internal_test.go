package app

import (
	"context"
	"io"
	"maps"
	"slices"
	"testing"
)

// OTLPCommand runs Main's `otlp` command on the streams given, for the
// external tests (otlpcmd_test.go); it is what Main runs on the process's
// own.
func (a *App) OTLPCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return a.otlpCommand(ctx, args, stdout, stderr)
}

// MainCommandNames are Main's own commands of the app, sorted.
func (a *App) MainCommandNames() []string {
	return slices.Sorted(maps.Keys(a.mainCommands()))
}

// `otlp`'s arguments: dead-letters and at most a signal; replay, a signal,
// --drop and IDs in any order; anything else is a usage error.
func TestTheOTLPCommandsArgumentsAreRead(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ok   bool
		want otlpArgs
	}{
		{[]string{"dead-letters"}, true, otlpArgs{verb: "dead-letters", signals: []string{OTLPTraces, OTLPMetrics}}},
		{[]string{"dead-letters", "metrics"}, true, otlpArgs{verb: "dead-letters", signals: []string{OTLPMetrics}}},
		{[]string{"replay", "traces", "--drop", "a", "b"}, true, otlpArgs{verb: "replay", signals: []string{OTLPTraces}, drop: true, ids: []string{"a", "b"}}},
		{[]string{"replay", "a", "--drop"}, true, otlpArgs{verb: "replay", signals: []string{OTLPTraces, OTLPMetrics}, drop: true, ids: []string{"a"}}},
		{nil, false, otlpArgs{}},
		{[]string{"frob"}, false, otlpArgs{}},
		{[]string{"dead-letters", "logs"}, false, otlpArgs{}},
		{[]string{"dead-letters", "--drop"}, false, otlpArgs{}},
		{[]string{"dead-letters", "traces", "x"}, false, otlpArgs{}},
		{[]string{"replay", "--force"}, false, otlpArgs{}},
	} {
		got, ok := parseOTLPArgs(tc.args)
		if ok != tc.ok || got.verb != tc.want.verb || got.drop != tc.want.drop ||
			!slices.Equal(got.signals, tc.want.signals) || !slices.Equal(got.ids, tc.want.ids) {
			t.Errorf("parseOTLPArgs(%q) = %+v, %v, want %+v, %v", tc.args, got, ok, tc.want, tc.ok)
		}
	}
}

// Main has an `otlp` command when the app declares an export and the
// program imports framework/app/otlp, and none otherwise.
func TestMainHasAnOTLPCommandOnlyWithAnExport(t *testing.T) {
	if slices.Contains(NewApp("plain").MainCommandNames(), "otlp") {
		t.Error("an app with no export has an otlp command")
	}
	if !slices.Contains(otlpProduct().MainCommandNames(), "otlp") {
		t.Error("an app with an export has no otlp command")
	}
	withoutOTLP(t)
	saved := otlpCode.Load()
	otlpCode.Store(nil)
	t.Cleanup(func() { otlpCode.Store(saved) })
	if slices.Contains(otlpProduct().MainCommandNames(), "otlp") {
		t.Error("a program without framework/app/otlp has an otlp command")
	}
}
