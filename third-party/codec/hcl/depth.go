package hcl

import "bytes"

// maxHCLDepth is the nesting ceiling the pre-scan holds an input to — the
// native YAML reader's (#1099). hclsyntax's recursive-descent parser has no
// guard of its own: past a few hundred thousand levels it overflows the
// goroutine stack, a fatal error no recover() catches.
const maxHCLDepth int = 100

// The frames the pre-scan stacks: code inside brackets, braces, parentheses
// or a template sequence, and the text of a quoted string.
const (
	// frameCode is code opened by '[', '{', '(' — closed by its pair.
	frameCode byte = 'c'
	// frameSequence is code opened by "${" or "%{" — closed by '}'.
	frameSequence byte = 's'
	// frameString is a quoted string, which is a template.
	frameString byte = 'q'
)

// depthScan is the lexical pre-scan: an upper bound on the parser's
// recursion, read from the bytes alone, in one pass that allocates nothing —
// the frame stack is an array the ceiling sizes.
type depthScan struct {
	// src is the input.
	src []byte
	// frames is the open frames, innermost last, up to open.
	frames [maxHCLDepth + 1]byte
	// pending counts, per level, the conditionals and template directives
	// whose branches the parser descends into; pending[0] is the top level's.
	pending [maxHCLDepth + 2]int
	// open is how many frames are open.
	open int
	// pendingSum is the sum of pending.
	pendingSum int
	// unary counts consecutive unary operators.
	unary int
	// pos is the read position.
	pos int
}

// exceedsDepth reports whether src nests past maxHCLDepth.
func exceedsDepth(src []byte) bool {
	s := depthScan{src: src}
	//: one byte, or one construct, per step.
	for s.pos < len(s.src) {
		//: refuse as soon as the bound is crossed.
		if s.step() {
			//: too deep.
			return true
		}
	}
	//: within the ceiling.
	return false
}

// depth is the nesting the parser would recurse through at this point.
func (s *depthScan) depth() int {
	//: frames, pending branches, and a run of unary operators.
	return s.open + s.pendingSum + s.unary
}

// top is the innermost frame, or 0 at the top level.
func (s *depthScan) top() byte {
	//: nothing open.
	if s.open == 0 {
		//: the top level is code.
		return 0
	}
	//: the innermost.
	return s.frames[s.open-1]
}

// push opens a frame and reports whether that crossed the ceiling; a frame
// that would not fit the array is past it.
func (s *depthScan) push(f byte) bool {
	//: the array holds one frame past the ceiling at most.
	if s.open == len(s.frames) {
		//: refused.
		return true
	}
	s.frames[s.open] = f
	s.open++
	s.pending[s.open] = 0
	//: refuse before the stack grows further.
	return s.depth() > maxHCLDepth
}

// pop closes the innermost frame.
func (s *depthScan) pop() {
	//: an unbalanced closer is the parser's to report.
	if s.open == 0 {
		//: nothing to close.
		return
	}
	s.pendingSum -= s.pending[s.open]
	s.open--
}

// addPending counts a conditional or directive branch in the innermost frame.
func (s *depthScan) addPending(n int) bool {
	//: never below zero: an unmatched end is the parser's to report.
	n = max(n, -s.pending[s.open])
	s.pending[s.open] += n
	s.pendingSum += n
	//: refuse once the bound is crossed.
	return s.depth() > maxHCLDepth
}

// step reads one byte or one construct and reports whether the ceiling was
// crossed.
func (s *depthScan) step() bool {
	//: inside a string, the template rules apply.
	if s.top() == frameString {
		//: text, escapes, sequences.
		return s.stringStep()
	}
	//: code.
	return s.codeStep()
}

// codeStep reads code: comments and heredocs are skipped, brackets and
// strings open frames, operators nest the parser's recursion.
func (s *depthScan) codeStep() bool {
	c := s.src[s.pos]
	s.pos++
	//: text that is not code.
	if s.skipNonCode(c) {
		//: nothing nests in it.
		return false
	}
	crossed, handled := s.frameByte(c)
	//: a frame opened or closed.
	if handled {
		//: past the ceiling or not.
		return crossed
	}
	//: an operator, a separator, or anything else.
	return s.operatorByte(c)
}

// skipNonCode skips a comment or a heredoc's text opening at c and reports
// whether it did.
func (s *depthScan) skipNonCode(c byte) bool {
	//: one form per opening byte.
	switch c {
	//: a comment runs to the end of its line.
	case '#':
		s.skipLine()
	//: "//" or "/*", or division.
	case '/':
		s.skipSlashComment()
	//: "<<ID", or a comparison.
	case '<':
		s.skipHeredoc()
	//: code.
	default:
		return false
	}
	//: skipped.
	return true
}

// frameByte opens or closes a frame at c, reporting whether that crossed the
// ceiling and whether c was a frame byte at all.
func (s *depthScan) frameByte(c byte) (crossed, handled bool) {
	//: one class of byte per case.
	switch c {
	//: a string is a template.
	case '"':
		s.unary = 0
		return s.push(frameString), true
	//: a tuple, an object, a parenthesised expression.
	case '[', '{', '(':
		s.unary = 0
		return s.push(frameCode), true
	//: their ends, and the end of a template sequence.
	case ']', ')', '}':
		s.pop()
		return false, true
	//: not a frame byte.
	default:
		return false, false
	}
}

// operatorByte counts what nests without brackets: a run of unary
// operators, a chain of conditionals.
func (s *depthScan) operatorByte(c byte) bool {
	//: one class of byte per case.
	switch c {
	//: a unary operator: the parser recurses once per operator in a run.
	case '!', '-':
		s.unary++
		return s.depth() > maxHCLDepth
	//: a conditional: its false branch nests the next one.
	case '?':
		s.unary = 0
		return s.addPending(1)
	//: an element or an attribute ends: its conditionals with it.
	case ',', '\n':
		s.unary = 0
		s.addPending(-s.pending[s.open])
	//: blanks do not end a run of unary operators.
	case ' ', '\t', '\r':
	//: any other byte ends a run of unary operators.
	default:
		s.unary = 0
	}
	//: within the ceiling.
	return false
}

// stringStep reads one byte of a quoted string: an escape, the closing
// quote, or a template sequence opening.
func (s *depthScan) stringStep() bool {
	c := s.src[s.pos]
	s.pos++
	//: one class of byte per case.
	switch c {
	//: an escape takes the next byte with it.
	case '\\':
		s.pos++
	//: the string ends — or a line does, which a string cannot span: the
	//: parser reports that.
	case '"', '\n':
		s.pop()
	//: "${" or "%{" opens a sequence, "$${" and "%%{" are literal.
	case '$', '%':
		return s.sequence(c)
	}
	//: within the ceiling.
	return false
}

// sequence opens a template sequence at "${" or "%{"; a directive's if or
// for nests until its end.
func (s *depthScan) sequence(c byte) bool {
	//: a doubled marker escapes the sequence.
	if bytes.HasPrefix(s.src[s.pos:], []byte{c, '{'}) {
		s.pos += 2
		//: literal text.
		return false
	}
	//: not a sequence.
	if s.pos >= len(s.src) || s.src[s.pos] != '{' {
		//: literal text.
		return false
	}
	s.pos++
	//: a directive whose keyword nests its branch past the ceiling.
	if c == '%' && s.directiveNests() {
		//: refused.
		return true
	}
	//: the sequence's code.
	return s.push(frameSequence)
}

// directiveNests counts a directive in the enclosing string: if and for open
// a branch the parser descends into until endif or endfor closes it.
func (s *depthScan) directiveNests() bool {
	rest := bytes.TrimLeft(s.src[s.pos:], "~ \t")
	//: one keyword per case.
	switch {
	//: a branch opens.
	case bytes.HasPrefix(rest, []byte("if")), bytes.HasPrefix(rest, []byte("for")):
		return s.addPending(1)
	//: a branch closes.
	case bytes.HasPrefix(rest, []byte("endif")), bytes.HasPrefix(rest, []byte("endfor")):
		s.addPending(-1)
	}
	//: within the ceiling.
	return false
}

// skipLine advances past the end of the current line.
func (s *depthScan) skipLine() {
	end := bytes.IndexByte(s.src[s.pos:], '\n')
	//: the last line.
	if end < 0 {
		s.pos = len(s.src)
		//: done.
		return
	}
	//: the newline itself is code again.
	s.pos += end
}

// skipSlashComment skips "//" to the end of the line or "/*" to "*/"; a lone
// '/' is division.
func (s *depthScan) skipSlashComment() {
	//: nothing follows.
	if s.pos >= len(s.src) {
		//: division at the end of the input.
		return
	}
	//: one comment form per case.
	switch s.src[s.pos] {
	//: a line comment.
	case '/':
		s.skipLine()
	//: a block comment.
	case '*':
		end := bytes.Index(s.src[s.pos+1:], []byte("*/"))
		//: unterminated: the parser reports it.
		if end < 0 {
			s.pos = len(s.src)
			//: done.
			return
		}
		s.pos += 1 + end + 2
	}
	s.unary = 0
}

// skipHeredoc skips a heredoc's text — "<<ID" or "<<-ID" to a line holding ID
// alone — and leaves a lone '<' alone, as the comparison it is.
func (s *depthScan) skipHeredoc() {
	rest := s.src[s.pos:]
	//: not "<<".
	if len(rest) == 0 || rest[0] != '<' {
		//: a comparison.
		return
	}
	rest = bytes.TrimPrefix(rest[1:], []byte("-"))
	nl := bytes.IndexByte(rest, '\n')
	//: no text follows.
	if nl < 0 {
		//: the parser reports it.
		return
	}
	marker := bytes.TrimSpace(rest[:nl])
	//: not an identifier: "<<" alone.
	if len(marker) == 0 {
		//: the parser reports it.
		return
	}
	text := rest[nl+1:]
	consumed := len(s.src) - len(text)
	//: the line that holds the marker alone ends the heredoc.
	for len(text) > 0 {
		line, after, _ := bytes.Cut(text, []byte{'\n'})
		//: found.
		if bytes.Equal(bytes.TrimSpace(line), marker) {
			s.pos = consumed + len(line)
			//: the newline after it is code again.
			return
		}
		consumed += len(line) + 1
		text = after
	}
	//: unterminated: the parser reports it.
	s.pos = len(s.src)
}
