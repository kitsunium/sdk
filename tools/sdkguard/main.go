// Package main — sdkguard enforces the SDK's consumer-facing rules on a
// codebase that imports the SDK.
//
// The rules the SDK states in its ADRs — one logging pipeline, typed errors,
// stdout belongs to the protocol — are only real if something checks them. The
// SDK cannot check them from inside: a library sees neither its consumer's
// source nor, at runtime, whether that consumer built a parallel logger. That
// was measured before this tool was written: log/slog is not a module, so it
// never appears in debug.ReadBuildInfo, and a slog.Logger held in a local
// variable never touches slog.Default. Runtime detection is not merely awkward,
// it is impossible for the case that matters.
//
// So the check runs at build time, over source. It is a plain CLI rather than a
// `go vet -vettool`: the vettool protocol lives in golang.org/x/tools, and
// tools/CLAUDE.md requires this tree to stay stdlib-only — a constraint that is
// structural, not stylistic, since a dependency-free module is what lets Bazel
// build tools/* while they sit outside go.work. The CI outcome is identical:
// diagnostics on stderr in the standard file:line:col format, non-zero exit.
//
// Usage:
//
//	sdkguard ./...            # walk the tree, report, exit 1 on findings
//	sdkguard -tests ./...     # include _test.go files
//	sdkguard -rules SDK001 .  # run one rule
//	sdkguard -list            # print the rule table
//
// A finding is suppressed by an inline directive carrying a reason:
//
//	slg := slog.New(h) //sdkguard:allow SDK001 third-party API needs a raw handler
//
// The reason is mandatory. A bare directive does not suppress — an exemption
// nobody had to justify is the kind that outlives its reason.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// exitFindings is the status returned when at least one finding survived.
// Distinct from 2 (tool error) so CI can tell "rules broken" from "tool broke".
const exitFindings int = 1

// exitToolError is the status returned when sdkguard itself failed.
const exitToolError int = 2

var (
	// errUnknownRule reports a -rules entry naming no rule in the table.
	errUnknownRule = errors.New("unknown rule")

	// errUnknownLevel reports a -level value outside the two the table uses.
	errUnknownLevel = errors.New("unknown level")

	// errUnknownMode reports a -version-check value the probe cannot honour.
	errUnknownMode = errors.New("unknown version-check mode")
)

// main parses flags, scans the requested roots and reports what it found.
func main() {
	tests := flag.Bool("tests", false, "include _test.go files")
	rules := flag.String("rules", "", "comma-separated rule IDs to run (default: all)")
	level := flag.String("level", "", "run only rules of this level: invariant | convention")
	versionCheck := flag.String("version-check", versionCheckWarn,
		"SDK freshness probe: warn (default) | off | error")
	list := flag.Bool("list", false, "print the rule table and exit")
	flag.Parse()

	//: -list documents the tool itself; nothing else runs alongside it.
	if *list {
		printRules(os.Stdout)
		//: printing the table IS the whole invocation.
		return
	}

	selected := mustSelect(*rules, *level)
	roots := flag.Args()
	//: no argument means "here and below", the spelling every Go tool takes.
	if len(roots) == 0 {
		roots = []string{"."}
	}

	//: scanning and the freshness probe are one step from here: both report,
	//: and together they decide the exit code.
	if inspect(roots, selected, *tests, *versionCheck) {
		os.Exit(exitFindings)
	}
}

// inspect runs the rules and the freshness probe over roots, reporting whether
// the run should fail.
//
// Split out of main so the entry point states the flags and this states the
// work; together they carried more branches than either needs.
func inspect(roots []string, selected []ruleEntity, withTests bool, mode string) bool {
	findings, err := scanRoots(roots, selected, withTests)
	//: an unreadable tree is a tool failure, distinct from a rule violation.
	if err != nil {
		//: exit 2 so CI can tell "tool broke" from "rules broken".
		fail(err)
	}
	//: print before the freshness warning, so the actionable part comes first.
	if len(findings) > 0 {
		report(os.Stderr, findings)
	}

	stale, err := reportVersions(roots, mode)
	//: a malformed -version-check is a tool error, not a finding.
	if err != nil {
		//: same exit code, same reasoning.
		fail(err)
	}

	//: being behind is a warning, so it must not move the exit code —
	//: otherwise it is not a warning, it is a gate wearing a warning's words.
	//: Only -version-check=error promotes it.
	return len(findings) > 0 || (stale && mode == versionCheckError)
}

// mustSelect resolves the rule set from the two selection flags, exiting when
// either names something the table does not hold.
//
// Split out of main so the entry point states the pipeline and this states the
// selection; together they carried more branches than either needs.
func mustSelect(rules, level string) []ruleEntity {
	selected, err := selectRules(rules)
	//: only narrow by level once the IDs themselves resolved.
	if err == nil {
		selected, err = filterLevel(selected, level)
	}
	//: an unknown rule or level is a typo the caller must see, not a silent
	//: empty run that would report "no issues" and mean nothing.
	if err != nil {
		//: exit 2: the tool could not do what was asked.
		fail(err)
	}
	//: the rules this invocation will run.
	return selected
}

// fail prints a tool-level error and exits with the tool-error status.
func fail(err error) {
	fmt.Fprintln(os.Stderr, "sdkguard:", err)
	os.Exit(exitToolError)
}

// reportVersions probes every root, so a stale requirement in the second
// module of a multi-module invocation is not silently ignored — results must
// not depend on argument order.
func reportVersions(roots []string, mode string) (stale bool, err error) {
	seen := make(map[string]bool, len(roots))
	//: every root gets a turn, so the answer does not depend on their order.
	for _, root := range roots {
		//: one warning per go.mod, not per root: several roots inside one
		//: module would otherwise repeat the same advice.
		path, found := findGoMod(normalizeRoot(root))
		//: a root outside any module still gets probed; workspaceRequirement
		//: may resolve it through go.work.
		if found {
			//: a module already probed has nothing new to say.
			if seen[path] {
				//: skip the duplicate rather than repeat the advice.
				continue
			}
			seen[path] = true
		}
		got, probeErr := reportVersion(root, mode)
		//: a malformed flag is the caller's problem and stops the run.
		if probeErr != nil {
			//: surface it rather than reporting a partial answer.
			return false, probeErr
		}
		//: one stale module is enough to make the whole run stale.
		stale = stale || got
	}
	//: the named error stays nil: every failure above returned early.
	return stale, nil
}

// reportVersion runs the freshness probe for one root and prints its warning,
// reporting whether the SDK was found to be behind.
func reportVersion(root, mode string) (stale bool, err error) {
	//: validate the mode before doing any work, so a typo costs no network.
	switch mode {
	//: off skips the probe entirely — no go.mod read, no request.
	case versionCheckOff:
		//: nothing to report, and nothing that could fail.
		return false, nil
	//: the two modes that probe; they differ only in what the caller does.
	case versionCheckWarn, versionCheckError:
	//: anything else is a typo the caller must see.
	default:
		//: wrap the sentinel so a caller can match on it.
		return false, fmt.Errorf("%w %q (want %s, %s or %s)",
			errUnknownMode, mode, versionCheckWarn, versionCheckOff, versionCheckError)
	}

	notice, stale := checkVersion(normalizeRoot(root), probe{})
	//: every degraded path returns stale=false, so silence is the norm here.
	if !stale {
		//: nothing to say about this module.
		return false, nil
	}
	//: stderr, and a leading blank line so the notice stands apart from findings.
	fmt.Fprintln(os.Stderr, "\nsdkguard: "+notice)
	//: behind, which only -version-check=error turns into a failure.
	return true, nil
}

// scanRoots walks every root and collects the findings of every selected rule.
func scanRoots(roots []string, rules []ruleEntity, withTests bool) (findings []findingEntity, err error) {
	var out []findingEntity
	//: collect across every root before sorting, so the order is global.
	for _, root := range roots {
		found, scanErr := scanDir(normalizeRoot(root), rules, withTests)
		//: an unreadable root must not read as "no violations".
		if scanErr != nil {
			//: surface it rather than reporting a partial answer.
			return nil, scanErr
		}
		out = append(out, found...)
	}
	//: SortFunc is generic where sort.Slice reflects, and the comparison also
	//: settles the column so two findings on one line have a stable order.
	slices.SortFunc(out, compareFindings)
	//: the named error stays nil: the only failure above returned early.
	return out, nil
}

// compareFindings orders findings by file, then line, then column, then rule.
//
// The column is part of the key on purpose: two findings on one line — a
// nested call flagged twice, say — would otherwise compare equal and their
// order would depend on the sort's internals rather than on the source.
func compareFindings(a, b findingEntity) int {
	//: file first, so a report reads in the order a developer opens files.
	if c := strings.Compare(a.File, b.File); c != 0 {
		//: different files need no further comparison.
		return c
	}
	//: then down the file.
	if a.Line != b.Line {
		//: cmp-style result without importing cmp for one call.
		return a.Line - b.Line
	}
	//: then across the line, which is what makes the order total.
	if a.Column != b.Column {
		//: same reasoning as the line comparison.
		return a.Column - b.Column
	}
	//: finally by rule, so two rules on one token stay in a fixed order.
	return strings.Compare(a.Rule, b.Rule)
}

// normalizeRoot accepts the `./...` spelling Go developers already type, so the
// tool drops into an existing CI line without a new argument convention.
func normalizeRoot(root string) string {
	//: an empty argument means the working directory.
	if root == "" {
		//: the same answer the go command gives for no argument.
		return "."
	}
	trimmed := strings.TrimSuffix(root, "...")
	//: "..." on its own means "here and below", not the filesystem root.
	if trimmed == "" {
		//: here, recursively.
		return "."
	}
	//: filepath.Clean drops the trailing separator "./..." leaves behind while
	//: preserving "/" itself — trimming the separator blindly turned the
	//: filesystem root into the working directory.
	return filepath.Clean(trimmed)
}

// report writes findings to dst in the standard Go diagnostic format so editors
// and CI annotators parse them without extra configuration.
//
// The destination is a parameter rather than os.Stderr directly: it is what
// makes the format assertable, and it matches how the SDK asks every writer to
// be named rather than assumed.
func report(dst io.Writer, findings []findingEntity) {
	//: one line per finding, in the order scanRoots settled.
	for _, f := range findings {
		fmt.Fprintf(dst, "%s:%d:%d: %s: %s\n", f.File, f.Line, f.Column, f.Rule, f.Message)
	}
	fmt.Fprintf(dst, "\nsdkguard: %d finding(s). Each is a rule the SDK states in an ADR;\n"+
		"suppress a justified one with //sdkguard:allow <RULE> <reason>.\n", len(findings))
}

// printRules writes the rule table to dst, so `sdkguard -list` documents
// itself. The destination is a parameter for the same reason report's is.
func printRules(dst io.Writer) {
	//: table order is declaration order, which groups the invariants first.
	for _, r := range allRules {
		fmt.Fprintf(dst, "%-8s %-11s %-46s %s\n", r.ID, r.Level, r.Title, r.Source)
	}
}

// filterLevel narrows a rule set to one level. An empty level keeps them all.
func filterLevel(rules []ruleEntity, level string) (selected []ruleEntity, err error) {
	level = strings.ToLower(strings.TrimSpace(level))
	//: no level named means no narrowing; every selected rule runs.
	if level == "" {
		//: hand the set back untouched.
		return rules, nil
	}
	//: a level outside the two the table uses is a typo, not an empty run.
	if level != LevelInvariant && level != LevelConvention {
		//: wrap the sentinel so a caller can match on it.
		return nil, fmt.Errorf("%w %q (want %s or %s)", errUnknownLevel, level, LevelInvariant, LevelConvention)
	}
	var out []ruleEntity
	//: keep declaration order so -list and the report agree.
	for _, r := range rules {
		//: one level at a time; the flag is not a set.
		if r.Level == level {
			out = append(out, r)
		}
	}
	//: the named error stays nil: the only failure above returned early.
	return out, nil
}

// selectRules resolves the -rules flag to a rule set.
func selectRules(spec string) (selected []ruleEntity, err error) {
	//: an empty -rules means every rule, which is the default invocation.
	if strings.TrimSpace(spec) == "" {
		//: the whole table, in declaration order.
		return allRules, nil
	}
	byID := make(map[string]ruleEntity, len(allRules))
	//: index once so a long -rules list stays linear.
	for _, r := range allRules {
		byID[r.ID] = r
	}
	var out []ruleEntity
	//: SplitSeq walks the entries without materialising the whole slice.
	for id := range strings.SplitSeq(spec, ",") {
		//: accept sdk001 as readily as SDK001; the ID is not a password.
		id = strings.ToUpper(strings.TrimSpace(id))
		r, ok := byID[id]
		//: an unknown ID is a typo the caller must see, not a silent empty run
		//: that would report "no issues" and mean nothing.
		if !ok {
			//: wrap the sentinel so a caller can match on it.
			return nil, fmt.Errorf("%w %q (see -list)", errUnknownRule, id)
		}
		out = append(out, r)
	}
	//: the named error stays nil: the only failure above returned early.
	return out, nil
}
