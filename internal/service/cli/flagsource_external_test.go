package cli_test

import (
	"context"
	"flag"
	"testing"
	"time"

	corecli "github.com/kitsunium/sdk/internal/core/cli"
	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	svccli "github.com/kitsunium/sdk/internal/service/cli"
)

// TestFlagSourceYieldsOnlyWhatWasTyped is the decision this adapter exists
// for, and the single word that carries it is Visit rather than VisitAll.
//
// VisitAll yields every DECLARED flag, including the ones nobody passed,
// carrying their Go defaults. Used as the last layer of a config load — which
// is the only position "flags win" can mean — that would make every flag with
// a zero default override the file and the environment on every single run,
// and the configuration file would appear to be ignored for reasons nothing
// reports. This is the most common defect in a flags-plus-config integration
// and it is one identifier wide.
func TestFlagSourceYieldsOnlyWhatWasTyped(t *testing.T) {
	t.Parallel()
	var h harness
	var rec recorder
	root := corecli.CommandValue{
		Name: "tool", Summary: "s",
		Flags: func(flags *flag.FlagSet) {
			flags.Int("port", 8080, "listen `port`")
			flags.String("host", "localhost", "bind `address`")
		},
		Run: rec.action(),
	}
	if err := h.run(t, root, "-port", "9000"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	values, err := svccli.FlagSource(rec.invocation).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, ok := values["port"]; !ok || got != 9000 {
		t.Errorf("port = %v (present=%v), want 9000 as an int", got, ok)
	}
	//: host was declared and NOT typed. Its presence here would silently beat
	//: whatever the file said, on every run, forever.
	if got, ok := values["host"]; ok {
		t.Errorf("host = %v is in the layer; an untyped flag must contribute nothing", got)
	}
	if len(values) != 1 {
		t.Errorf("the layer carries %d keys, want exactly the one that was typed", len(values))
	}
}

// TestFlagSourceKeepsTheStdlibsTypes pins that a value arrives in its Go type
// rather than as the text the shell handed over. Every flag type package flag
// ships implements flag.Getter, so an int stays an int and a Duration stays a
// time.Duration — which matters because the config loader round-trips the
// layer through JSON, and "30s" and 30000000000 decode into different things.
func TestFlagSourceKeepsTheStdlibsTypes(t *testing.T) {
	t.Parallel()
	var h harness
	var rec recorder
	root := corecli.CommandValue{
		Name: "tool", Summary: "s",
		Flags: func(flags *flag.FlagSet) {
			flags.Int("n", 0, "an `n`")
			flags.Bool("verbose", false, "chatty")
			flags.Duration("timeout", 0, "a `duration`")
			flags.Var(&opaqueValue{}, "opaque", "a Value with no Get")
		},
		Run: rec.action(),
	}
	args := []string{"-n", "7", "-verbose", "-timeout", "1500ms", "-opaque", "x"}
	if err := h.run(t, root, args...); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	values, loadErr := svccli.FlagSource(rec.invocation).Load()
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	tests := []struct {
		key  string
		want any
	}{
		{"n", 7},
		{"verbose", true},
		{"timeout", 1500 * time.Millisecond},
		//: a caller's own flag.Value that does NOT implement Getter has only
		//: its String form to offer; falling back to it is stated, not silent.
		{"opaque", "opaque:x"},
	}
	for _, tc := range tests {
		if got := values[tc.key]; got != tc.want {
			t.Errorf("%s = %#v (%T), want %#v (%T)", tc.key, got, got, tc.want, tc.want)
		}
	}
}

// TestALeafFlagOverridesAnAncestorOfTheSameName pins the precedence inside one
// invocation: sets are visited root first, so the nearest declaration wins —
// the same "later wins" direction config.Load already uses between sources,
// rather than a second rule to remember.
func TestALeafFlagOverridesAnAncestorOfTheSameName(t *testing.T) {
	t.Parallel()
	var h harness
	var rec recorder
	root := corecli.CommandValue{
		Name: "tool", Summary: "s",
		Flags: func(flags *flag.FlagSet) { flags.Int("depth", 0, "an `n`") },
		Commands: []corecli.CommandValue{{
			Name: "sub", Summary: "s",
			Flags: func(flags *flag.FlagSet) { flags.Int("depth", 0, "an `n`") },
			Run:   rec.action(),
		}},
	}
	if err := h.run(t, root, "-depth", "1", "sub", "-depth", "2"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	values, loadErr := svccli.FlagSource(rec.invocation).Load()
	if loadErr != nil {
		t.Fatalf("Load: %v", loadErr)
	}
	if values["depth"] != 2 {
		t.Errorf("depth = %v, want 2 — the nearest declaration wins", values["depth"])
	}
}

// TestFlagSourceIsAConfigSourceAndSnapshots pins two things the adapter's
// usefulness rests on: it IS a coreconfig.Source (so config.Load takes it with
// no shim), and Load hands out a fresh map each time.
//
// The second matters because config's own merge writes INTO the destination
// map: a source that handed out its own would be mutated by a later layer, and
// the second Load would return something the command line never carried.
func TestFlagSourceIsAConfigSourceAndSnapshots(t *testing.T) {
	t.Parallel()
	var h harness
	var rec recorder
	root := corecli.CommandValue{
		Name: "tool", Summary: "s",
		Flags: func(flags *flag.FlagSet) { flags.Int("n", 0, "an `n`") },
		Run:   rec.action(),
	}
	if err := h.run(t, root, "-n", "3"); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	source := coreconfig.Source(svccli.FlagSource(rec.invocation))
	first, err := source.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	first["n"] = "clobbered"
	second, secondErr := source.Load()
	if secondErr != nil {
		t.Fatalf("second Load: %v", secondErr)
	}
	if second["n"] != 3 {
		t.Errorf("a caller mutating one Load reached the source: n = %v", second["n"])
	}
}

// TestFlagSourceOnAnEmptyInvocationIsAnEmptyLayer pins the degenerate case: a
// command that declared no flags contributes an empty layer, never nil-map
// panics and never a missing-source error, so wiring cli.FlagSource into a
// config.Load unconditionally is safe.
func TestFlagSourceOnAnEmptyInvocationIsAnEmptyLayer(t *testing.T) {
	t.Parallel()
	values, err := svccli.FlagSource(corecli.InvocationValue{}).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(values) != 0 {
		t.Errorf("layer = %v, want empty", values)
	}
	//: an InvocationValue is a caller-writable struct, so a test double may
	//: legitimately carry a nil set. It must not panic the adapter.
	nilSets := corecli.InvocationValue{Flags: []*flag.FlagSet{nil}}
	if _, err := svccli.FlagSource(nilSets).Load(); err != nil {
		t.Fatalf("Load over a nil set: %v", err)
	}
}

// opaqueValue is a flag.Value that deliberately does NOT implement
// flag.Getter, which is the only case where the adapter has to fall back to a
// string.
type opaqueValue struct{ raw string }

// String renders the stored text.
func (o *opaqueValue) String() string {
	//: the fallback the adapter uses; prefixed so a test can tell it apart.
	if o == nil || o.raw == "" {
		return ""
	}
	return "opaque:" + o.raw
}

// Set stores the raw text.
func (o *opaqueValue) Set(raw string) error {
	o.raw = raw
	return nil
}

// compile-time proof that the double really lacks Get; if flag.Value ever
// gained it, this test's whole point would silently evaporate.
var _ = func() any {
	var value flag.Value = &opaqueValue{}
	if _, ok := value.(flag.Getter); ok {
		panic("opaqueValue must not implement flag.Getter")
	}
	return context.Background()
}()
