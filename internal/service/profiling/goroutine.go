// Package profiling — hosts the goroutine view: the process's goroutines, each
// with its state, how long it has waited, its labels and its stack, parsed
// from the runtime's own dump.
package profiling

import (
	"bufio"
	"bytes"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// dumpLineMax bounds one line of a dump the parser reads: a frame line with a
// very long generic instantiation still fits; anything longer ends the parse.
const dumpLineMax int = 1 << 20

// debugTraceback and debugCounts are the two text forms of runtime/pprof's
// goroutine profile: every goroutine's traceback, and the stacks counted.
const (
	debugTraceback int = 2
	debugCounts    int = 1
)

// stateFlags are the suffixes the runtime appends to a state — a goroutine
// being scanned, one leaked, one durably blocked in a synctest bubble — which
// say something about the moment rather than what the goroutine waits on.
var stateFlags = []string{" (scan)", " (leaked)", " (durable)"}

// GoroutineValue is one goroutine of the process, as the runtime's dump
// describes it.
type GoroutineValue struct {
	// Labels are the goroutine's pprof labels.
	Labels map[string]string
	// CreatedBy is the frame of the go statement that started the goroutine;
	// zero for the main goroutine and for those the runtime starts itself.
	CreatedBy FrameValue
	// State is the runtime's word for what the goroutine does: "running",
	// "runnable", "syscall", or what it waits on — "select", "chan receive",
	// "IO wait", "sleep", "sync.Mutex.Lock".
	State string
	// Stack is the goroutine's stack, innermost frame first.
	Stack []FrameValue
	// Waiting is how long the goroutine has been blocked, to the minute the
	// runtime reports; zero under a minute or when it is not blocked.
	Waiting time.Duration
	// ID is the goroutine's number.
	ID int64
	// Creator is the number of the goroutine that ran the go statement; zero
	// when the dump does not say.
	Creator int64
	// LockedToThread says the goroutine is locked to its OS thread.
	LockedToThread bool
}

// Goroutines returns every goroutine of the process, from the runtime's
// traceback dump. Labels come from the dump's headers (GODEBUG
// tracebacklabels=1, the default since Go 1.27); when the headers carry none,
// they are matched from the labelled goroutine profile by stack — best
// effort, since the two are taken one after the other.
func Goroutines() ([]GoroutineValue, error) {
	dump, err := goroutineProfile(debugTraceback)
	//: the runtime could not write its own dump.
	if err != nil {
		//: already typed.
		return nil, err
	}
	gs := ParseGoroutines(dump)
	//: the headers carried the labels: nothing to match.
	if slices.ContainsFunc(gs, func(g GoroutineValue) bool { return len(g.Labels) > 0 }) {
		//: complete.
		return gs, nil
	}
	counts, err := goroutineProfile(debugCounts)
	//: the labelled profile could not be written: the goroutines stand without labels.
	if err != nil {
		//: already typed.
		return nil, err
	}
	joinLabels(gs, parseCounts(counts))
	//: labels matched where a stack allowed it.
	return gs, nil
}

// goroutineProfile writes the goroutine profile at debug level.
func goroutineProfile(debug int) ([]byte, error) {
	var buf bytes.Buffer
	//: the runtime's own writer.
	if err := pprof.Lookup("goroutine").WriteTo(&buf, debug); err != nil {
		//: the runtime's error goes to the chain.
		return nil, errs.Wrap(err, errs.WrapParams{
			Code: CodeCaptureFailed, Reason: "CAPTURE_FAILED", Public: CaptureFailed.Public(), Private: CaptureFailed.Private(),
		}, errs.String("profile", "goroutine"))
	}
	//: the text.
	return buf.Bytes(), nil
}

// ParseGoroutines reads a goroutine dump — runtime/pprof's goroutine profile
// at debug 2, runtime.Stack of all goroutines, or the dump a crash or SIGQUIT
// prints — into goroutines, in the dump's order:
//
//	goroutine 7 [chan receive, 2 minutes] {kit_node: shop/endpoint/Get}:
//	pkg.f(0x1)
//		/path/file.go:12 +0x1d
//	created by pkg.g in goroutine 1
//		/path/file.go:30 +0x55
//
// It never fails: a block whose header does not parse is skipped, and a line
// it does not recognise is ignored, so a dump from a newer runtime still
// yields what this parser understands.
func ParseGoroutines(dump []byte) []GoroutineValue {
	var p dumpParser
	sc := bufio.NewScanner(bytes.NewReader(dump))
	sc.Buffer(make([]byte, 0, 64<<10), dumpLineMax)
	//: line by line; a line past the bound ends the parse where it is.
	for sc.Scan() {
		p.line(sc.Text())
	}
	//: every goroutine whose header parsed.
	return p.out
}

// dumpParser holds the goroutine being read and where its next location line
// goes.
type dumpParser struct {
	out []GoroutineValue
	// cur is the goroutine being read; nil between blocks.
	cur *GoroutineValue
	// pending is the frame a location line completes: the last function
	// line, or the created-by frame.
	pending *FrameValue
}

// line reads one line of a dump.
func (p *dumpParser) line(line string) {
	//: a new block starts wherever the previous one was.
	if strings.HasPrefix(line, "goroutine ") {
		p.header(line)
		//: the header opened the block.
		return
	}
	//: which kind of line inside a block it is.
	switch {
	//: a location: completes the frame above it.
	case p.cur != nil && strings.HasPrefix(line, "\t"):
		p.location(line)
	//: the go statement that started the goroutine.
	case p.cur != nil && strings.HasPrefix(line, "created by "):
		p.createdBy(line)
	//: a frame's function line — "...additional frames elided..." is not.
	case p.cur != nil && strings.Contains(line, "(") && !strings.HasPrefix(line, "..."):
		p.cur.Stack = append(p.cur.Stack, FrameValue{Function: functionOf(line)})
		p.pending = &p.cur.Stack[len(p.cur.Stack)-1]
	//: outside a block, a separator, or a line this parser does not know.
	default:
		p.pending = nil
	}
}

// header reads "goroutine N [state, extra…] {labels}:" and opens a block.
func (p *dumpParser) header(line string) {
	g, ok := parseHeader(line)
	p.pending = nil
	//: a header this parser does not understand: its block is skipped.
	if !ok {
		p.cur = nil
		//: skipped.
		return
	}
	p.out = append(p.out, g)
	p.cur = &p.out[len(p.out)-1]
}

// location completes the pending frame with its file and line.
func (p *dumpParser) location(line string) {
	//: a location with no frame above it is ignored.
	if p.pending == nil {
		//: nothing to complete.
		return
	}
	p.pending.File, p.pending.Line = fileLine(strings.TrimPrefix(line, "\t"))
	p.pending = nil
}

// createdBy reads "created by pkg.f in goroutine N".
func (p *dumpParser) createdBy(line string) {
	rest := strings.TrimPrefix(line, "created by ")
	fn, creator, found := strings.Cut(rest, " in goroutine ")
	p.cur.CreatedBy = FrameValue{Function: fn}
	//: newer runtimes name the creating goroutine; a number that does not
	//: parse leaves it unknown.
	if id, err := strconv.ParseInt(creator, 10, 64); found && err == nil {
		p.cur.Creator = id
	}
	p.pending = &p.cur.CreatedBy
}

// parseHeader reads a goroutine header.
func parseHeader(line string) (GoroutineValue, bool) {
	rest := strings.TrimPrefix(line, "goroutine ")
	idText, _, _ := strings.Cut(rest, " ")
	id, err := strconv.ParseInt(idText, 10, 64)
	open := strings.Index(rest, " [")
	//: no number, or no state: not a header this parser knows.
	if err != nil || open < 0 {
		//: skipped.
		return GoroutineValue{}, false
	}
	shut := strings.IndexByte(rest[open:], ']')
	//: a state that never closes.
	if shut < 0 {
		//: skipped.
		return GoroutineValue{}, false
	}
	g := GoroutineValue{ID: id}
	g.readState(rest[open+2 : open+shut])
	tail := strings.TrimSuffix(strings.TrimSpace(rest[open+shut+1:]), ":")
	//: labels, when the runtime prints them.
	if strings.HasPrefix(tail, "{") && strings.HasSuffix(tail, "}") {
		g.Labels = parseTracebackLabels(tail[1 : len(tail)-1])
	}
	//: a goroutine with its header read.
	return g, true
}

// readState reads the bracket: the state, then ", N minutes", ", locked to
// thread" and whatever else the runtime adds.
func (g *GoroutineValue) readState(bracket string) {
	parts := strings.Split(bracket, ", ")
	state := parts[0]
	//: the flags the runtime appends to the state itself.
	for _, flag := range stateFlags {
		state = strings.TrimSuffix(state, flag)
	}
	g.State = state
	//: the parts after the state.
	for _, part := range parts[1:] {
		//: how long it has been blocked; a count that does not parse is
		//: left unknown.
		if n, err := strconv.Atoi(strings.TrimSuffix(part, " minutes")); err == nil && strings.HasSuffix(part, " minutes") {
			g.Waiting = time.Duration(n) * time.Minute
		}
		//: pinned to its thread.
		if part == "locked to thread" {
			g.LockedToThread = true
		}
	}
}

// functionOf strips a frame line's arguments: "pkg.(*T).M(0x1, {…})" is
// "pkg.(*T).M".
func functionOf(line string) string {
	//: the arguments are the last parenthesised group.
	if i := strings.LastIndex(line, "("); i > 0 {
		//: the name before them.
		return line[:i]
	}
	//: no arguments printed.
	return line
}

// fileLine reads "/path/file.go:12 +0x1d". The line number follows the LAST
// colon, so a Windows path — "C:/Users/x/file.go:12" — keeps its drive.
func fileLine(loc string) (string, int) {
	loc, _, _ = strings.Cut(loc, " +0x")
	colon := strings.LastIndex(loc, ":")
	//: no line number printed.
	if colon < 0 {
		//: the whole text is the file.
		return loc, 0
	}
	n, err := strconv.Atoi(loc[colon+1:])
	//: what follows the colon is not a line number.
	if err != nil {
		//: the whole text is the file.
		return loc, 0
	}
	//: file and line.
	return loc[:colon], n
}
