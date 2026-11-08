package service

import (
	"path/filepath"
	"runtime"
	"strings"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

const (
	// fixedPath is the search path systemd gives a command, and the only one
	// a "+" or "!" command gets (ADR 0214 §9).
	fixedPath string = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	// shellPlain are the bytes a word may hold and need no quote in a shell.
	shellPlain string = "_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789./=:,+@%-"
)

// splitter is splitWords' state: the words so far, the one being read, and
// the quote it is inside.
type splitter struct {
	words []string
	cur   strings.Builder
	quote byte
	open  bool
}

// buildSpec makes the spec of one command from the unit's template: the
// command's program and arguments, $VAR and ${VAR} expanded unless ":", the
// engine's variables over the template's, the shell for "|", and for "+" and
// "!" no credentials, the fixed search path and no loader or shell
// start-up variable; the unit's sandbox — none for "+", which systemd runs
// without any namespacing, capability or filter option, the root
// ExecStart='s alone under RootDirectoryStartOnly= —; the unit's group as its
// CgroupPath when the template names none. The template is never written to.
func buildSpec(template *coreproc.Spec, s *settings, ef *effect) coreproc.Spec {
	spec := *template
	cmd := ef.cmd
	spec.Sandbox = s.sandboxFor(ef.list)
	//: "+": full privileges, no confinement (systemd.service(5), Table 2).
	if cmd.Flags&coresvc.FlagFullPrivileges != 0 {
		spec.Sandbox = nil
	}
	env := mergeEnv(template.Env, ef.vars)
	args := cmd.Args
	//: ":" leaves the arguments as written.
	if cmd.Flags&coresvc.FlagNoEnvExpand == 0 {
		args = expandArgs(args, env)
	}
	path := cmd.Path
	//: "|": through the shell.
	if cmd.Flags&coresvc.FlagShell != 0 {
		path, args = shellCommand(s.shell, cmd, args)
	}
	//: "+" and "!" drop the credentials.
	if cmd.Flags.Privileged() {
		spec.User, spec.Group, spec.Groups = "", "", nil
		env = privilegedEnv(env)
	}
	spec.Path, spec.Args, spec.Env = path, args, env
	//: the unit's group, where the template names none (S0g).
	if spec.CgroupPath == "" {
		spec.CgroupPath = s.cgroupPath
	}
	//: the spec.
	return spec
}

// mergeEnv is base with vars set over it: a name of vars replaces every entry
// of base's, a name base repeats keeps its last value at the place of its last
// occurrence — os/exec's reading of append(os.Environ(), "K=v"), and proc/exec's
// —, and PATH is the fixed search path when neither names one.
func mergeEnv(base, vars []string) []string {
	engine := namesOf(vars)
	last := make(map[string]int, len(base))
	//: where each name of the template is last set.
	for i, v := range base {
		last[nameKey(envName(v))] = i
	}
	out := make([]string, 0, len(base)+len(vars)+1)
	//: the template's, but those the engine sets or a later entry repeats.
	for i, v := range base {
		name := nameKey(envName(v))
		//: set by the engine, or by a later entry.
		if engine[name] || last[name] != i {
			continue
		}
		out = append(out, v)
	}
	path := nameKey("PATH")
	//: systemd's PATH when neither the unit nor the engine names one.
	if _, named := last[path]; !named && !engine[path] {
		out = append(out, "PATH="+fixedPath)
	}
	//: the engine's last.
	return append(out, vars...)
}

// namesOf is the set of names vars assigns, as nameKey compares them.
func namesOf(vars []string) map[string]bool {
	set := make(map[string]bool, len(vars))
	//: each assignment.
	for _, v := range vars {
		set[nameKey(envName(v))] = true
	}
	//: the names.
	return set
}

// envName is an assignment's name. A leading "=" belongs to it, as proc/exec
// reads it: Windows keeps each drive's current directory as "=C:=C:\dir".
func envName(assignment string) string {
	//: past a leading "=".
	if rest, drive := strings.CutPrefix(assignment, "="); drive {
		name, _, _ := strings.Cut(rest, "=")
		//: "=" and what follows, before the next "=".
		return "=" + name
	}
	name, _, _ := strings.Cut(assignment, "=")
	//: before the first "=".
	return name
}

// nameKey is the form two names of one variable share on this platform:
// Windows' names ignore case, so a template's "Path=" is the unit's PATH.
func nameKey(name string) string {
	//: case-insensitive names.
	if runtime.GOOS == osWindows {
		//: one spelling for every case.
		return strings.ToUpper(name)
	}
	//: the name itself.
	return name
}

// privilegedEnv drops the variables a privileged command must not inherit —
// LD_*, ENV, BASH_ENV — and fixes PATH.
func privilegedEnv(env []string) []string {
	out := make([]string, 0, len(env))
	//: each variable.
	for _, v := range env {
		name := envName(v)
		//: the loader's, a shell's start-up file, or the search path.
		if strings.HasPrefix(name, "LD_") || name == "ENV" || name == "BASH_ENV" || name == "PATH" {
			continue
		}
		out = append(out, v)
	}
	//: the fixed search path.
	return append(out, "PATH="+fixedPath)
}

// shellCommand is a "|" command: the shell alone, or the shell's -c over the
// command's words; "@" makes it a login shell ("-" before its argv[0]).
func shellCommand(shell string, cmd coresvc.CommandValue, args []string) (string, []string) {
	argv0 := shell
	//: a login shell.
	if cmd.Flags&coresvc.FlagArgv0 != 0 {
		argv0 = "-" + filepath.Base(shell)
	}
	//: the shell alone.
	if cmd.Path == "" {
		//: no script.
		return shell, []string{argv0}
	}
	words := make([]string, len(args))
	//: each word quoted for the shell.
	for i, w := range args {
		words[i] = shellQuote(w)
	}
	//: the shell running the words.
	return shell, []string{argv0, "-c", strings.Join(words, " ")}
}

// shellQuote quotes a word for a POSIX shell, unless it needs none.
func shellQuote(w string) string {
	//: nothing the shell reads.
	if w != "" && strings.Trim(w, shellPlain) == "" {
		//: as is.
		return w
	}
	//: single quotes, a quote inside closed, escaped and reopened.
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

// expandArgs expands the arguments after argv[0] as systemd.service(5)'s
// "Command lines" says: a word that is exactly $NAME becomes the value split at
// whitespace (quotes respected, then removed), ${NAME} anywhere becomes the
// value as one piece, $$ is $, and an unknown variable is empty.
func expandArgs(args, env []string) []string {
	//: argv[0] only.
	if len(args) < 2 {
		//: nothing to expand.
		return args
	}
	values := envMap(env)
	out := make([]string, 0, len(args))
	out = append(out, args[0])
	//: each argument.
	for _, w := range args[1:] {
		//: a whole-word $NAME splits.
		if name, ok := bareVar(w); ok {
			out = append(out, splitWords(values[name])...)
			continue
		}
		out = append(out, expandWord(w, values))
	}
	//: the arguments.
	return out
}

// envMap reads assignments into a map; mergeEnv left one of each name.
func envMap(env []string) map[string]string {
	values := make(map[string]string, len(env))
	//: each assignment.
	for _, v := range env {
		name, value, _ := strings.Cut(v, "=")
		values[name] = value
	}
	//: the values.
	return values
}

// bareVar reports whether a word is exactly $NAME, and the name.
func bareVar(w string) (string, bool) {
	//: "$" then a name, and nothing else.
	if len(w) < 2 || w[0] != '$' || !isName(w[1:]) {
		//: no.
		return "", false
	}
	//: the name.
	return w[1:], true
}

// isName reports whether s is a variable name: a letter or "_" first, then
// letters, digits and "_".
func isName(s string) bool {
	//: empty, or a digit first.
	if s == "" || isDigit(s[0]) {
		//: no.
		return false
	}
	//: every byte a name's.
	for i := range len(s) {
		//: a byte no name holds.
		if !isDigit(s[i]) && !isLetter(s[i]) {
			//: no.
			return false
		}
	}
	//: a name.
	return true
}

// isDigit reports an ASCII digit.
func isDigit(c byte) bool {
	//: 0 to 9.
	return '0' <= c && c <= '9'
}

// isLetter reports an ASCII letter or "_".
func isLetter(c byte) bool {
	//: a to z, A to Z, or "_".
	return c == '_' || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// expandWord replaces ${NAME} by its value and $$ by $, leaving any other $.
func expandWord(w string, values map[string]string) string {
	var b strings.Builder
	rest := w
	//: up to each "$".
	for {
		i := strings.IndexByte(rest, '$')
		//: no "$" left.
		if i < 0 {
			b.WriteString(rest)
			//: the word.
			return b.String()
		}
		b.WriteString(rest[:i])
		repl, n := dollar(rest[i:], values)
		b.WriteString(repl)
		rest = rest[i+n:]
	}
}

// dollar reads what a "$" opens — "$$", "${NAME}", or a "$" alone — and
// returns what it stands for and how many bytes it spans.
func dollar(s string, values map[string]string) (string, int) {
	//: "$$" is one "$".
	if strings.HasPrefix(s, "$$") {
		//: a dollar.
		return "$", len("$$")
	}
	end := strings.IndexByte(s, '}')
	//: "${NAME}".
	if strings.HasPrefix(s, "${") && end > 0 && isName(s[2:end]) {
		//: the value.
		return values[s[2:end]], end + 1
	}
	//: a "$" alone stays.
	return "$", 1
}

// splitWords splits a value at whitespace, quotes respected and removed.
func splitWords(v string) []string {
	sp := &splitter{}
	//: byte by byte.
	for i := range len(v) {
		sp.take(v[i])
	}
	sp.flush()
	//: zero or more words.
	return sp.words
}

// take reads one byte.
func (sp *splitter) take(c byte) {
	//: by what the byte is.
	switch {
	//: inside quotes: the closing quote, or a byte of the word.
	case sp.quote != 0:
		sp.quoted(c)
	//: an opening quote.
	case c == '\'' || c == '"':
		sp.quote, sp.open = c, true
	//: whitespace ends a word.
	case strings.IndexByte(" \t\n", c) >= 0:
		sp.flush()
	//: a byte of a word.
	default:
		sp.cur.WriteByte(c)
		sp.open = true
	}
}

// quoted reads a byte inside quotes.
func (sp *splitter) quoted(c byte) {
	//: the closing quote.
	if c == sp.quote {
		sp.quote = 0
		return
	}
	sp.cur.WriteByte(c)
}

// flush ends the word being read, if any.
func (sp *splitter) flush() {
	//: a word was open.
	if sp.open {
		sp.words = append(sp.words, sp.cur.String())
		sp.cur.Reset()
		sp.open = false
	}
}
