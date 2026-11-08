package service

import (
	"strings"
	"unicode"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// CommandFlags are the prefixes of one Exec*= command line
// (systemd.service(5), Table 2), as bits.
type CommandFlags uint8

const (
	// FlagArgv0 is "@": the second word is argv[0]; with FlagShell, the shell
	// is a login shell.
	FlagArgv0 CommandFlags = 1 << iota
	// FlagIgnoreFailure is "-": a failure is recorded and read as a success.
	FlagIgnoreFailure
	// FlagNoEnvExpand is ":": $VAR and ${VAR} are not expanded.
	FlagNoEnvExpand
	// FlagFullPrivileges is "+": the command runs without the unit's
	// credentials and confinement, on Config.AllowPrivilegedControl only.
	FlagFullPrivileges
	// FlagNoCredentials is "!": the command runs without User=, Group= and
	// SupplementaryGroups=, on Config.AllowPrivilegedControl only.
	FlagNoCredentials
	// FlagShell is "|": the command runs through the shell's -c, or is the
	// shell alone.
	FlagShell
)

// prefixes are the prefix characters and their flags, in the order String
// writes them.
var prefixes = [...]struct {
	char byte
	flag CommandFlags
}{
	{'@', FlagArgv0},
	{'-', FlagIgnoreFailure},
	{':', FlagNoEnvExpand},
	{'+', FlagFullPrivileges},
	{'!', FlagNoCredentials},
	{'|', FlagShell},
}

// String returns the prefixes the flags stand for, in the order "@-:+!|".
func (f CommandFlags) String() string {
	var b strings.Builder
	//: one character per flag set, in a fixed order.
	for _, p := range prefixes {
		//: the flag of this prefix is set.
		if f&p.flag != 0 {
			b.WriteByte(p.char)
		}
	}
	//: the prefixes, possibly none.
	return b.String()
}

// prefixFlag is the flag a prefix character stands for, and whether it is one.
func prefixFlag(c byte) (CommandFlags, bool) {
	//: each prefix.
	for _, p := range prefixes {
		//: this one.
		if p.char == c {
			//: its flag.
			return p.flag, true
		}
	}
	//: no prefix.
	return 0, false
}

// Privileged reports whether the command asks for "+" or "!".
func (f CommandFlags) Privileged() bool {
	//: either elevation.
	return f&(FlagFullPrivileges|FlagNoCredentials) != 0
}

// CommandValue is one Exec*= command, its prefixes parsed: the program, the
// argument vector it receives (argv[0] first) and the flags.
type CommandValue struct {
	// Path is the program: an absolute path or a bare file name the engine
	// looks up. It is empty only for a FlagShell command that is the shell
	// alone.
	Path string
	// Args is the program's argument vector, argv[0] first: Path, or the word
	// "@" named. $VAR and ${VAR} in it are expanded at spawn unless
	// FlagNoEnvExpand.
	Args []string
	// Flags are the command's prefixes.
	Flags CommandFlags
}

// ParseCommand reads one Exec*= command from its words — the line already
// split by systemd.syntax(7)'s quoting, its % specifiers already expanded,
// which is the caller's. The first word may open with the prefixes
// "@-:+!|", each at most once and in any order; "+" with "!", a repeated
// prefix (so "!!"), a missing program, a program that is neither absolute
// nor a bare file name, and "@" without the argv[0] word it names are
// COMMAND_INVALID, the rule field saying which.
func ParseCommand(words []string) (CommandValue, error) {
	//: a command is at least its program.
	if len(words) == 0 {
		//: nothing to run.
		return CommandValue{}, commandInvalid("empty", "")
	}
	flags, path, err := parsePrefixes(words[0])
	//: a prefix the grammar refuses.
	if err != nil {
		//: COMMAND_INVALID naming the rule.
		return CommandValue{}, err
	}
	//: the shell alone, or a program.
	if path == "" {
		//: the shell alone takes no further word.
		if flags&FlagShell != 0 && len(words) == 1 {
			//: the shell, argv[0] left to the engine.
			return CommandValue{Flags: flags}, nil
		}
		//: a prefix with no program after it.
		return CommandValue{}, commandInvalid("no-program", words[0])
	}
	//: a program the fixed search cannot reach ambiguously.
	if bad := checkProgram(path); bad != "" {
		//: COMMAND_INVALID naming the rule.
		return CommandValue{}, commandInvalid(bad, path)
	}
	//: "@" names argv[0] by the second word, except under "|".
	if flags&FlagArgv0 != 0 && flags&FlagShell == 0 {
		//: the word "@" promises.
		if len(words) < 2 {
			//: "@" with nothing to name.
			return CommandValue{}, commandInvalid("no-argv0", path)
		}
		//: argv[0] is the second word, the rest follow.
		return CommandValue{Path: path, Args: append([]string{words[1]}, words[2:]...), Flags: flags}, nil
	}
	//: argv[0] is the program as written.
	return CommandValue{Path: path, Args: append([]string{path}, words[1:]...), Flags: flags}, nil
}

// parsePrefixes splits the first word into its prefix flags and the program.
func parsePrefixes(word string) (CommandFlags, string, error) {
	var flags CommandFlags
	i := 0
	//: every leading prefix character, until the program begins.
	for ; i < len(word); i++ {
		flag, ok := prefixFlag(word[i])
		//: not a prefix: the program starts here.
		if !ok {
			break
		}
		//: each prefix once: "!!" is not a prefix systemd knows.
		if flags&flag != 0 {
			//: a repeated prefix.
			return 0, "", commandInvalid("prefix-repeated", word)
		}
		flags |= flag
	}
	//: "+" and "!" exclude each other.
	if flags&FlagFullPrivileges != 0 && flags&FlagNoCredentials != 0 {
		//: both elevations at once.
		return 0, "", commandInvalid("prefix-conflict", word)
	}
	//: the flags and what follows them.
	return flags, word[i:], nil
}

// checkProgram returns the rule a program breaks, "" when it breaks none: it
// is an absolute path or a file name without a slash, and holds no control
// character.
func checkProgram(path string) string {
	//: a control character in a program is refused.
	if strings.ContainsFunc(path, unicode.IsControl) {
		//: the rule.
		return "control-character"
	}
	//: a relative path with a directory is neither form.
	if !strings.HasPrefix(path, "/") && strings.Contains(path, "/") {
		//: the rule.
		return "relative-path"
	}
	//: an acceptable program.
	return ""
}

// commandInvalid is COMMAND_INVALID with the rule and the word.
func commandInvalid(rule, word string) error {
	//: the core sentinel, the rule and the word as fields.
	return errs.Wrap(CommandInvalid, errs.WrapParams{}, errs.String("rule", rule), errs.String("word", word))
}
