// Package main — netgate, the network plan's merge gates (plan P8, R7).
//
// The bench of the net-kernel-gate workflow runs engine A against engine B on
// one load, ten times each, interleaved, on one GitHub-hosted runner, and
// writes `go test -bench` output. netgate reads it, judges every gate of its
// table (gates.go) on the bound of benchstat's confidence interval, and writes
// a report tied to the commit measured. An active gate that fails or finds too
// few samples fails the run; an inactive gate is reported with what it found,
// so its harness is seen working before its step turns it on.
//
// Usage:
//
//	netgate -in bench.txt -sha <commit> [-out report.json]
//	netgate -list
//
// Exit 0 when every active gate passes, 1 when one does not, 2 on bad usage
// or an unreadable input.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	// exitGateFailed is the status when an active gate did not pass.
	exitGateFailed int = 1
	// exitUsage is the status on bad usage or an unreadable input.
	exitUsage int = 2
	// reportMode is the report's file mode: world-readable, like any
	// artefact.
	reportMode os.FileMode = 0o644
)

var (
	// errNoSHA reports a run given no commit to tie its report to.
	errNoSHA = errors.New("-sha is required: a report names the commit it measured")
	// errNoInput reports a run given no bench output.
	errNoInput = errors.New("-in is required")
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main with its arguments and streams, returning the exit status.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("netgate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := fs.String("in", "", "go test -bench output to judge")
	sha := fs.String("sha", "", "the commit the samples were measured on")
	out := fs.String("out", "", "where to write the JSON report (optional)")
	list := fs.Bool("list", false, "print the gate table and exit")
	//: a flag error is printed by the set.
	if err := fs.Parse(args); err != nil {
		//: bad usage.
		return exitUsage
	}
	//: the table alone.
	if *list {
		//: nothing judged.
		return printJSON(stdout, stderr, gates)
	}
	rep, err := judgeFile(*in, *sha)
	//: no input, no commit, or an unreadable file.
	if err != nil {
		fmt.Fprintln(stderr, "netgate:", err)
		return exitUsage
	}
	printSummary(stdout, rep)
	//: the report beside the summary.
	if *out != "" {
		//: the artefact the workflow uploads.
		if werr := writeReport(*out, rep); werr != nil {
			fmt.Fprintln(stderr, "netgate:", werr)
			return exitUsage
		}
	}
	//: only an active gate fails the run.
	if !rep.Passed {
		//: the merge is refused.
		return exitGateFailed
	}
	//: every active gate passed.
	return 0
}

// judgeFile reads the bench output at path and judges the table on it.
func judgeFile(path, sha string) (*reportEntity, error) {
	//: the report is tied to a commit or not written.
	if sha == "" {
		//: refused.
		return nil, errNoSHA
	}
	//: nothing to read.
	if path == "" {
		//: refused.
		return nil, errNoInput
	}
	b, err := os.ReadFile(path)
	//: unreadable input.
	if err != nil {
		//: the read's error.
		return nil, err
	}
	s, err := parseBench(bytes.NewReader(b))
	//: a read error is no verdict.
	if err != nil {
		//: the scanner's error.
		return nil, err
	}
	//: judged.
	return evaluate(gates, s, sha), nil
}

// printSummary writes one line per gate and per criterion.
func printSummary(w io.Writer, rep *reportEntity) {
	fmt.Fprintf(w, "netgate: commit %s, %.0f %% interval, n >= %d\n", rep.SHA, rep.Confidence*100, rep.MinSamples)
	//: one block per gate.
	for _, g := range rep.Gates {
		fmt.Fprintf(w, "%-4s %-8s %s: %s vs %s on %s\n", g.Step, g.Verdict, activeWord(g.Active), g.B, g.A, g.Load)
		//: the pending step of an inactive gate.
		if !g.Active && g.Pending != "" {
			fmt.Fprintf(w, "     pending: %s\n", g.Pending)
		}
		//: the allocation budget held outside the bench.
		if g.AllocBudget != "" {
			fmt.Fprintf(w, "     allocs: %s\n", g.AllocBudget)
		}
		//: each criterion's line.
		for _, m := range g.Measured {
			fmt.Fprintf(w, "     %-8s %s\n", m.Verdict, m.Reason)
		}
	}
	fmt.Fprintf(w, "netgate: 0-alloc exclusions: %s\n", strings.Join(rep.AllocExclusions, ", "))
	fmt.Fprintf(w, "netgate: passed=%t\n", rep.Passed)
}

// activeWord names a gate's state.
func activeWord(active bool) string {
	//: required.
	if active {
		//: fails the merge.
		return "required"
	}
	//: reported only.
	return "not required"
}

// writeReport writes rep as indented JSON at path.
func writeReport(path string, rep *reportEntity) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	//: the report types always marshal; kept for the contract.
	if err != nil {
		//: the encoder's error.
		return err
	}
	//: the artefact.
	return os.WriteFile(path, append(b, '\n'), reportMode)
}

// printJSON writes v as indented JSON, returning the exit status.
func printJSON(stdout, stderr io.Writer, v any) int {
	b, err := json.MarshalIndent(v, "", "  ")
	//: the table always marshals; kept for the contract.
	if err != nil {
		fmt.Fprintln(stderr, "netgate:", err)
		return exitUsage
	}
	fmt.Fprintln(stdout, string(b))
	//: printed.
	return 0
}
