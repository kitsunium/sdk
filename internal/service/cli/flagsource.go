// Package cli — the one seam between this domain and internal/core/config.
package cli

import (
	"flag"
	"maps"

	corecli "github.com/kitsunium/sdk/internal/core/cli"
)

// FlagSourceValue is one invocation's flags, shaped as the flat key→value
// layer internal/core/config.Source yields. It is the whole of what this
// domain contributes to configuration, and everything it deliberately is not
// is listed in [FlagSource].
//
// FlagSourceValue is a published concrete shape (pkg/v1/cli.FlagSource returns
// it), so ADR 0040 applies: it may still change while the module is v0, said
// out loud, and not after v1.
type FlagSourceValue struct {
	values map[string]any
}

// FlagSource snapshots the flags an operator ACTUALLY TYPED on one invocation
// and returns them as a configuration layer.
//
// # Only what was set
//
// It walks flag.FlagSet.Visit and never VisitAll. That one word is the whole
// value of this adapter: VisitAll yields every DECLARED flag, including the
// ones nobody passed, carrying their Go defaults — so a flag with a zero
// default would override the file and the environment on every single run, and
// a configuration file would appear to be ignored for reasons nothing reports.
// Visit yields only what the command line actually carried, which is exactly
// what "the flags are the highest-precedence layer" is supposed to mean.
//
// # The flag name IS the config key
//
// No transformation: no camelCase to snake_case, no "-" to "_", no automatic
// prefixing. Every such rule is a second grammar a reader has to learn and a
// generator has to reproduce, and its failure mode is a key nobody typed
// silently not matching a field. A flag that feeds `database.max_conns`
// declares itself as "database.max_conns" — package flag accepts a dot in a
// name and parses -database.max_conns=5 without help. And when the name is
// wrong, the config schema (ADR 0061) REFUSES an unaddressed key rather than
// ignoring it, so the mistake is loud at startup.
//
// # What this is not
//
// It is not a configuration loader. It reads no file, no environment variable
// and no default; it does no layering, no decoding and no validation. Those
// are the config domain, they already exist, and a CLI carrying its own copy
// would be a second one to disagree with. The intended shape is one line:
//
//	err := config.LoadSchema(&cfg, schema,
//		config.FileSource("toml", "/etc/tool.toml"),
//		config.EnvSource("TOOL_"),
//		cli.FlagSource(invocation)) // last wins
//
// # Precedence inside one invocation
//
// The sets are visited root first, so a leaf flag overrides an ancestor flag
// of the same name — the same "later wins" direction config.Load already uses
// between sources, rather than a second rule to remember.
func FlagSource(invocation corecli.InvocationValue) *FlagSourceValue {
	values := make(map[string]any, len(invocation.Flags))
	//: root first, so a leaf flag of the same name overwrites its ancestor's —
	//: the direction config.Load already uses between sources.
	for _, set := range invocation.Flags {
		//: a nil set cannot happen from Execute, but an InvocationValue is a
		//: caller-writable struct and a test double may hand one over.
		if set == nil {
			continue
		}
		//: Visit, never VisitAll — see the doc comment; this is the decision.
		set.Visit(func(f *flag.Flag) { values[f.Name] = flagValue(f) })
	}
	//: snapshotted at construction, so Load is trivially safe for concurrent
	//: use and cannot observe a set somebody re-parsed in the meantime.
	return &FlagSourceValue{values: values}
}

// Load returns the layer. It satisfies internal/core/config.Source.
func (s *FlagSourceValue) Load() (values map[string]any, err error) {
	//: a fresh map per call: config.deepMerge writes into the destination, and
	//: a source that handed out its own map would be mutated by a later merge.
	return maps.Clone(s.values), nil
}

// flagValue extracts a flag's value in its Go type where the stdlib offers
// one.
//
// Every flag type package flag ships implements flag.Getter, so an int stays
// an int and a Duration stays a time.Duration rather than becoming the string
// "30s" that a decode would then have to guess at. A caller's own flag.Value
// that does NOT implement Getter falls back to its String form, which is the
// only thing it offers — stated rather than silently typed as something else.
func flagValue(f *flag.Flag) any {
	//: the stdlib's own types all take this branch.
	if getter, ok := f.Value.(flag.Getter); ok {
		//: the Go type survives into the layer, so a JSON round trip cannot
		//: have to guess between "30s" and a duration.
		return getter.Get()
	}
	//: a third-party flag.Value with no Get: its text is all there is.
	return f.Value.String()
}
