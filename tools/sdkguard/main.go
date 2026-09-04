// Command sdkguard enforces the SDK's consumer-facing rules on a codebase that
// imports the SDK.
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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// exitFindings is the status returned when at least one finding survived.
// Distinct from 2 (tool error) so CI can tell "rules broken" from "tool broke".
const exitFindings int = 1

// exitToolError is the status returned when sdkguard itself failed.
const exitToolError int = 2

// main parses flags, scans the requested roots and reports what it found.
func main() {
	tests := flag.Bool("tests", false, "include _test.go files")
	rules := flag.String("rules", "", "comma-separated rule IDs to run (default: all)")
	level := flag.String("level", "", "run only rules of this level: invariant | convention")
	versionCheck := flag.String("version-check", versionCheckWarn,
		"SDK freshness probe: warn (default) | off | error")
	list := flag.Bool("list", false, "print the rule table and exit")
	flag.Parse()

	if *list {
		printRules()
		return
	}

	selected, err := selectRules(*rules)
	if err == nil {
		selected, err = filterLevel(selected, *level)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sdkguard:", err)
		os.Exit(exitToolError)
	}

	roots := flag.Args()
	if len(roots) == 0 {
		roots = []string{"."}
	}

	findings, err := scanRoots(roots, selected, *tests)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sdkguard:", err)
		os.Exit(exitToolError)
	}

	if len(findings) > 0 {
		report(findings)
	}

	stale, err := reportVersion(roots[0], *versionCheck)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sdkguard:", err)
		os.Exit(exitToolError)
	}

	// Being behind is a warning, so it must not move the exit code — otherwise
	// it is not a warning, it is a gate wearing a warning's words. Only
	// -version-check=error promotes it.
	if len(findings) > 0 || (stale && *versionCheck == versionCheckError) {
		os.Exit(exitFindings)
	}
}

// reportVersion runs the freshness probe and prints its warning, reporting
// whether the SDK was found to be behind.
func reportVersion(root, mode string) (bool, error) {
	switch mode {
	case versionCheckOff:
		return false, nil
	case versionCheckWarn, versionCheckError:
	default:
		return false, fmt.Errorf("unknown -version-check %q (want %s, %s or %s)",
			mode, versionCheckWarn, versionCheckOff, versionCheckError)
	}

	notice, stale := checkVersion(normalizeRoot(root), probe{})
	if !stale {
		return false, nil
	}
	fmt.Fprintln(os.Stderr, "\nsdkguard: "+notice)
	return true, nil
}

// scanRoots walks every root and collects the findings of every selected rule.
func scanRoots(roots []string, rules []Rule, withTests bool) ([]Finding, error) {
	var out []Finding
	for _, root := range roots {
		found, err := scanDir(normalizeRoot(root), rules, withTests)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pos.Filename != out[j].Pos.Filename {
			return out[i].Pos.Filename < out[j].Pos.Filename
		}
		if out[i].Pos.Line != out[j].Pos.Line {
			return out[i].Pos.Line < out[j].Pos.Line
		}
		return out[i].Rule < out[j].Rule
	})
	return out, nil
}

// normalizeRoot accepts the `./...` spelling Go developers already type, so the
// tool drops into an existing CI line without a new argument convention.
func normalizeRoot(root string) string {
	dir := strings.TrimSuffix(strings.TrimSuffix(root, "..."), string(filepath.Separator))
	if dir == "" {
		return "."
	}
	return dir
}

// report prints findings in the standard Go diagnostic format so editors and
// CI annotators parse them without extra configuration.
func report(findings []Finding) {
	for _, f := range findings {
		fmt.Fprintf(os.Stderr, "%s: %s: %s\n", f.Pos, f.Rule, f.Message)
	}
	fmt.Fprintf(os.Stderr, "\nsdkguard: %d finding(s). Each is a rule the SDK states in an ADR;\n"+
		"suppress a justified one with //sdkguard:allow <RULE> <reason>.\n", len(findings))
}

// printRules writes the rule table, so `sdkguard -list` documents itself.
func printRules() {
	for _, r := range allRules {
		fmt.Printf("%-8s %-11s %-46s %s\n", r.ID, r.Level, r.Title, r.Source)
	}
}

// filterLevel narrows a rule set to one level. An empty level keeps them all.
func filterLevel(rules []Rule, level string) ([]Rule, error) {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		return rules, nil
	}
	if level != LevelInvariant && level != LevelConvention {
		return nil, fmt.Errorf("unknown level %q (want %s or %s)", level, LevelInvariant, LevelConvention)
	}
	var out []Rule
	for _, r := range rules {
		if r.Level == level {
			out = append(out, r)
		}
	}
	return out, nil
}

// selectRules resolves the -rules flag to a rule set.
func selectRules(spec string) ([]Rule, error) {
	if strings.TrimSpace(spec) == "" {
		return allRules, nil
	}
	byID := make(map[string]Rule, len(allRules))
	for _, r := range allRules {
		byID[r.ID] = r
	}
	var out []Rule
	for _, id := range strings.Split(spec, ",") {
		id = strings.ToUpper(strings.TrimSpace(id))
		r, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("unknown rule %q (see -list)", id)
		}
		out = append(out, r)
	}
	return out, nil
}
