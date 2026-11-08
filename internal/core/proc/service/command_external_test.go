package service_test

import (
	"errors"
	"slices"
	"testing"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestParseCommand: each prefix of "@-:+!|", in any order, once; "!!", "+!",
// an empty command, a prefix without a program, a relative path with a
// directory and "@" without its word are COMMAND_INVALID.
func TestParseCommand(t *testing.T) {
	for _, tc := range []struct {
		words []string
		path  string
		args  []string
		flags coresvc.CommandFlags
	}{
		{[]string{"/bin/d", "-x"}, "/bin/d", []string{"/bin/d", "-x"}, 0},
		{[]string{"echo"}, "echo", []string{"echo"}, 0},
		{[]string{"@/bin/d", "daemon", "-x"}, "/bin/d", []string{"daemon", "-x"}, coresvc.FlagArgv0},
		{[]string{"-/bin/false"}, "/bin/false", []string{"/bin/false"}, coresvc.FlagIgnoreFailure},
		{[]string{":/bin/echo", "$X"}, "/bin/echo", []string{"/bin/echo", "$X"}, coresvc.FlagNoEnvExpand},
		{[]string{"+/bin/fix"}, "/bin/fix", []string{"/bin/fix"}, coresvc.FlagFullPrivileges},
		{[]string{"!/bin/fix"}, "/bin/fix", []string{"/bin/fix"}, coresvc.FlagNoCredentials},
		{[]string{"|echo", "hi"}, "echo", []string{"echo", "hi"}, coresvc.FlagShell},
		{[]string{"|"}, "", nil, coresvc.FlagShell},
		{[]string{"@|"}, "", nil, coresvc.FlagShell | coresvc.FlagArgv0},
		{[]string{"+:@true", "$TEST"}, "true", []string{"$TEST"}, coresvc.FlagFullPrivileges | coresvc.FlagNoEnvExpand | coresvc.FlagArgv0},
		{[]string{"-:@|!/bin/x"}, "/bin/x", []string{"/bin/x"}, coresvc.FlagIgnoreFailure | coresvc.FlagNoEnvExpand | coresvc.FlagArgv0 | coresvc.FlagShell | coresvc.FlagNoCredentials},
	} {
		got, err := coresvc.ParseCommand(tc.words)
		if err != nil {
			t.Errorf("%q: %v", tc.words, err)
			continue
		}
		if got.Path != tc.path || !slices.Equal(got.Args, tc.args) || got.Flags != tc.flags {
			t.Errorf("%q: %+v, want %q %q %s", tc.words, got, tc.path, tc.args, tc.flags)
		}
	}
	for _, tc := range []struct {
		words []string
		rule  string
	}{
		{nil, "empty"},
		{[]string{"!!/bin/x"}, "prefix-repeated"},
		{[]string{"--/bin/x"}, "prefix-repeated"},
		{[]string{"+!/bin/x"}, "prefix-conflict"},
		{[]string{"!+/bin/x"}, "prefix-conflict"},
		{[]string{"-"}, "no-program"},
		{[]string{"|", "x"}, "no-program"},
		{[]string{"bin/x"}, "relative-path"},
		{[]string{"/bin/\x01"}, "control-character"},
		{[]string{"@/bin/x"}, "no-argv0"},
	} {
		_, err := coresvc.ParseCommand(tc.words)
		e, ok := errors.AsType[*errs.Error](err)
		if !errs.HasCode(err, coresvc.CodeCommandInvalid) || !ok {
			t.Errorf("%q: %v, want COMMAND_INVALID", tc.words, err)
			continue
		}
		rule := ""
		for _, f := range e.Fields() {
			if f.Key() == "rule" {
				rule = f.StringValue()
			}
		}
		if rule != tc.rule {
			t.Errorf("%q: rule %q, want %q", tc.words, rule, tc.rule)
		}
	}
	if s := (coresvc.FlagShell | coresvc.FlagArgv0 | coresvc.FlagIgnoreFailure).String(); s != "@-|" {
		t.Errorf("String = %q", s)
	}
}
