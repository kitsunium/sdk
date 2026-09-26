// Package profiling — hosts the reading of goroutine labels in their two
// printed forms, and their matching onto a dump whose headers lack them.
package profiling

import (
	"bufio"
	"bytes"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// joinDepth is how many innermost frames identify a stack when labels are
// matched from the counted profile onto the dump.
const joinDepth int = 12

// countFunctionColumn is the index, among a counted-profile frame line's
// non-empty columns, of the function: after the "#" marker and the address.
const countFunctionColumn int = 2

// countRecord is one record of the counted goroutine profile: goroutines
// sharing a stack and labels.
type countRecord struct {
	labels map[string]string
	stack  []string
	count  int
}

// parseTracebackLabels reads a dump header's labels: `key: value, key:
// "quoted value"`. The runtime quotes a key or a value only when it holds
// something other than letters, digits, '.', '/' and '_', with escapes
// strconv.Unquote reads.
func parseTracebackLabels(s string) map[string]string {
	out := make(map[string]string)
	//: one pair at a time; a pair that does not parse ends the reading.
	for s != "" {
		key, rest, ok := labelToken(s)
		//: a key must be followed by ": ".
		if !ok || !strings.HasPrefix(rest, ": ") {
			//: what was read so far.
			return out
		}
		value, rest, ok := labelToken(rest[2:])
		//: a key with no value.
		if !ok {
			//: what was read so far.
			return out
		}
		out[key] = value
		s = strings.TrimPrefix(rest, ", ")
	}
	//: every pair.
	return out
}

// labelToken reads one key or value off s: a quoted string, or the text up to
// the next ':' or ','.
func labelToken(s string) (tok, rest string, ok bool) {
	//: a quoted token ends at the first unescaped quote.
	if strings.HasPrefix(s, `"`) {
		//: skip escaped characters while looking for the closing quote.
		for i := 1; i < len(s); i++ {
			//: an escape, a close, or an ordinary byte.
			switch s[i] {
			//: the next byte is escaped.
			case '\\':
				i++
			//: the closing quote.
			case '"':
				v, err := strconv.Unquote(s[:i+1])
				//: the unquoted token and what follows it.
				return v, s[i+1:], err == nil
			}
		}
		//: a quote that never closes.
		return "", "", false
	}
	end := strings.IndexAny(s, ":,")
	//: the last token of the list.
	if end < 0 {
		//: all of it.
		return s, "", true
	}
	//: up to the separator.
	return s[:end], s[end:], true
}

// parseCounts reads the counted goroutine profile, debug 1:
//
//	3 @ 0x1 0x2
//	# labels: {"kit_node":"shop/endpoint/Get"}
//	#	0x1	pkg.f+0x10	/path/file.go:12
func parseCounts(text []byte) []countRecord {
	var out []countRecord
	var cur *countRecord
	sc := bufio.NewScanner(bytes.NewReader(text))
	sc.Buffer(make([]byte, 0, 64<<10), dumpLineMax)
	//: record by record.
	for sc.Scan() {
		line := sc.Text()
		//: which kind of line it is.
		switch {
		//: a record's header: its count.
		case strings.Contains(line, " @ "):
			n, err := strconv.Atoi(strings.TrimSpace(line[:strings.Index(line, " @ ")]))
			cur = nil
			//: a count that is not a number opens no record.
			if err == nil {
				out = append(out, countRecord{count: n})
				cur = &out[len(out)-1]
			}
		//: outside a record.
		case cur == nil:
		//: the record's labels, in Go's %q quoting.
		case strings.HasPrefix(line, "# labels: "):
			cur.labels = parseCountLabels(strings.TrimPrefix(line, "# labels: "))
		//: one frame of the record's stack.
		case strings.HasPrefix(line, "#\t"):
			cur.stack = appendCountFrame(cur.stack, line)
		}
	}
	//: every record.
	return out
}

// appendCountFrame appends the function of a counted-profile frame line —
// "#\t0x1\tpkg.f+0x10\t/path/file.go:12" — to stack.
//
// The columns are aligned with tabwriter, so a frame whose address is shorter
// than the widest one carries EMPTY columns between its fields: splitting on
// every tab reads "" for its function. Measured on linux/arm64, where the
// addresses of the runtime and of the program differ in width; darwin's did
// not, which is how the copy this replaces passed its tests there.
func appendCountFrame(stack []string, line string) []string {
	column := 0
	//: the non-empty columns in order: the marker, the address, the function.
	for part := range strings.SplitSeq(line, "\t") {
		//: an alignment column carries nothing.
		if part == "" {
			//: next column.
			continue
		}
		//: the third non-empty column is the function.
		if column == countFunctionColumn {
			fn, _, _ := strings.Cut(part, "+0x")
			//: the function, without its offset.
			return append(stack, fn)
		}
		column++
	}
	//: a line too short to be a frame.
	return stack
}

// parseCountLabels reads `{"key":"value", "key2":"value2"}`.
func parseCountLabels(s string) map[string]string {
	s = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "{"), "}")
	out := make(map[string]string)
	//: one pair at a time; a pair that does not parse ends the reading.
	for s != "" {
		key, rest, ok := quotedToken(s)
		//: a key must be followed by ':'.
		if !ok || !strings.HasPrefix(rest, ":") {
			//: what was read so far.
			return out
		}
		value, rest, ok := quotedToken(rest[1:])
		//: a key with no value.
		if !ok {
			//: what was read so far.
			return out
		}
		out[key] = value
		s = strings.TrimLeft(strings.TrimPrefix(strings.TrimSpace(rest), ","), " ")
	}
	//: every pair.
	return out
}

// quotedToken reads one quoted token off s.
func quotedToken(s string) (tok, rest string, ok bool) {
	s = strings.TrimSpace(s)
	//: this form quotes every key and value.
	if !strings.HasPrefix(s, `"`) {
		//: not a token of this form.
		return "", "", false
	}
	//: the same reading as a header's quoted token.
	return labelToken(s)
}

// stackKey identifies a stack by its innermost frames.
func stackKey(stack []string) string {
	stack = slices.DeleteFunc(slices.Clone(stack), func(fn string) bool { return fn == "runtime.goexit" })
	//: the innermost frames, one per line.
	return strings.Join(stack[:min(joinDepth, len(stack))], "\n")
}

// joinLabels gives each goroutine of a dump the labels the counted profile
// records for its stack, one goroutine per counted record.
func joinLabels(gs []GoroutineValue, records []countRecord) {
	byStack := make(map[string][]*countRecord, len(records))
	//: only labelled records can give anything.
	for i := range records {
		//: a record with labels, indexed by its stack.
		if len(records[i].labels) > 0 {
			k := stackKey(records[i].stack)
			byStack[k] = append(byStack[k], &records[i])
		}
	}
	var names []string
	//: each goroutine takes one count of a record with its stack.
	for i := range gs {
		names = names[:0]
		//: the dump's frames, by function.
		for _, f := range gs[i].Stack {
			names = append(names, f.Function)
		}
		//: the first record with a count left.
		for _, rec := range byStack[stackKey(names)] {
			//: this record still has goroutines to give its labels to.
			if rec.count > 0 {
				rec.count--
				gs[i].Labels = maps.Clone(rec.labels)
				//: labelled.
				break
			}
		}
	}
}
