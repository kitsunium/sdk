// Package notify — the bounds of sd_notify(3)'s assignments (#730).
package notify

import (
	"maps"
	"slices"
	"strings"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sdNotifyAssignments is #730's list: every assignment sd_notify(3) defines.
var sdNotifyAssignments = []string{
	"READY", "RELOADING", "STOPPING", "STATUS", "MAINPID", "WATCHDOG", "WATCHDOG_USEC",
	"EXTEND_TIMEOUT_USEC", "ERRNO", "BUSERROR", "VARLINKERROR", "EXIT_STATUS", "MONOTONIC_USEC",
	"NOTIFYACCESS", "FDSTORE", "FDSTOREREMOVE", "FDNAME", "FDPOLL", "BARRIER", "MAINPIDFD",
	"MAINPIDFDID", "RESTART_RESET",
}

// assignmentCorpus holds, per assignment, values within its bounds and values
// out of them.
var assignmentCorpus = map[string]struct{ valid, invalid []string }{
	"READY":               {[]string{"1"}, []string{"0", "2", "yes", ""}},
	"RELOADING":           {[]string{"1"}, []string{"true"}},
	"STOPPING":            {[]string{"1"}, []string{"0"}},
	"STATUS":              {[]string{"", "up", "démarré"}, []string{"\xff\xfe"}},
	"MAINPID":             {[]string{"1", "4242"}, []string{"0", "-1", "+5", "abc", "2147483648", ""}},
	"WATCHDOG":            {[]string{"1", "trigger"}, []string{"2", "TRIGGER"}},
	"WATCHDOG_USEC":       {[]string{"0", "30000000"}, []string{"-1", "1.5", "infinity"}},
	"EXTEND_TIMEOUT_USEC": {[]string{"1000"}, []string{"x"}},
	"ERRNO":               {[]string{"0", "13"}, []string{"-1", "EPERM"}},
	"BUSERROR":            {[]string{"org.freedesktop.DBus.Error.Failed"}, []string{"", "a b", strings.Repeat("a", 256)}},
	"VARLINKERROR":        {[]string{"io.systemd.Error"}, []string{"\x01"}},
	"EXIT_STATUS":         {[]string{"3", "SIGTERM"}, []string{"\xc3\x28"}},
	"MONOTONIC_USEC":      {[]string{"123456789"}, []string{"-5"}},
	"NOTIFYACCESS":        {[]string{"none", "main", "exec", "all"}, []string{"some", "ALL"}},
	"FDSTORE":             {[]string{"1"}, []string{"2"}},
	"FDSTOREREMOVE":       {[]string{"1"}, []string{"yes"}},
	"FDNAME":              {[]string{"", "web", strings.Repeat("n", 255)}, []string{"a:b", "tab\there", strings.Repeat("n", 256), "é"}},
	"FDPOLL":              {[]string{"0", "1"}, []string{"no"}},
	"BARRIER":             {[]string{"1"}, []string{"0"}},
	"MAINPIDFD":           {[]string{"1"}, []string{"true"}},
	"MAINPIDFDID":         {[]string{"18446744073709551615"}, []string{"18446744073709551616", "-3"}},
	"RESTART_RESET":       {[]string{"1"}, []string{"2"}},
}

// TestAssignmentRulesCoverSdNotify pins that every assignment of #730's list
// has a rule and a corpus, and no rule names anything else.
func TestAssignmentRulesCoverSdNotify(t *testing.T) {
	t.Parallel()
	want := slices.Sorted(slices.Values(sdNotifyAssignments))
	if got := slices.Sorted(maps.Keys(assignmentRules)); !slices.Equal(got, want) {
		t.Errorf("assignmentRules names %q, want %q", got, want)
	}
	if got := slices.Sorted(maps.Keys(assignmentCorpus)); !slices.Equal(got, want) {
		t.Errorf("the corpus names %q, want %q", got, want)
	}
	for name, c := range assignmentCorpus {
		if len(c.valid) == 0 || len(c.invalid) == 0 {
			t.Errorf("%s: the corpus needs a valid and an invalid value", name)
		}
	}
}

// TestParsePayloadCorpus pins #730's corpus: each assignment within its bounds
// parses, each out of them refuses the whole datagram with
// INVALID_NOTIFICATION naming it; an unknown name is kept as it came, a name
// given twice keeps its last value, an empty name is refused.
func TestParsePayloadCorpus(t *testing.T) {
	t.Parallel()
	for name, c := range assignmentCorpus {
		for _, v := range c.valid {
			if _, err := parsePayload("READY=1\n" + name + "=" + v + "\n"); err != nil {
				t.Errorf("%s=%q: %v, want accepted", name, v, err)
			}
		}
		for _, v := range c.invalid {
			_, err := parsePayload("READY=1\n" + name + "=" + v + "\n")
			if !errs.HasCode(err, coreproc.CodeInvalidNotification) {
				t.Errorf("%s=%q: %v, want INVALID_NOTIFICATION", name, v, err)
			}
		}
	}
	v, err := parsePayload("X_VENDOR=anything at all\nSTATUS=a\nSTATUS=b\n")
	if err != nil {
		t.Fatalf("unknown name and a duplicate: %v", err)
	}
	if v.State["X_VENDOR"] != "anything at all" || v.Status != "b" {
		t.Errorf("state %v status %q, want the unknown kept and the last STATUS", v.State, v.Status)
	}
	if _, err = parsePayload("=x\n"); !errs.HasCode(err, coreproc.CodeInvalidNotification) {
		t.Errorf("an empty name: %v, want INVALID_NOTIFICATION", err)
	}
}
