//go:build unix

// Package exec — Unix credential resolution. Every function here answers "who
// should this child run as", and the failure mode they all guard against is the
// same: a name that does not resolve must never fall back to the PARENT's
// identity, because the parent is typically the one with privileges.
package exec

import (
	"os/user"
	"strconv"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// absentName is a user/group name no host will have.
const absentName string = "sdk-nonexistent-principal-9c3f"

// Test_wantsIdentity pins the discriminator that decides whether a Credential is
// built at all. A false negative would silently run the child as the parent —
// which is the privilege escalation this whole file exists to prevent.
func Test_wantsIdentity(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		spec coreproc.Spec
		want bool
	}
	tests := []tc{
		{"nothing requested", coreproc.Spec{}, false},
		{"a user alone", coreproc.Spec{User: "nobody"}, true},
		{"a group alone", coreproc.Spec{Group: "nogroup"}, true},
		{"supplementary groups alone", coreproc.Spec{Groups: []string{"a"}}, true},
		{"an empty supplementary list", coreproc.Spec{Groups: []string{}}, false},
		{"all three at once", coreproc.Spec{User: "u", Group: "g", Groups: []string{"a"}}, true},
		//: an unrelated field must not be mistaken for an identity request.
		{"an unrelated field", coreproc.Spec{Path: "/bin/true", Setpgid: true}, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := wantsIdentity(c.spec); got != c.want {
			t.Errorf("wantsIdentity(%s) = %v, want %v", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_resolveUser pins the two resolution paths and the deliberate asymmetry
// between them.
//
// A NUMERIC uid is honoured even with no passwd entry — a container image often
// has no /etc/passwd at all, and refusing there would make "run as 65534"
// impossible. A NAME that does not resolve is a hard failure, because there is
// nothing sensible to fall back to and falling back to the parent would be the
// privilege escalation.
func Test_resolveUser(t *testing.T) {
	t.Parallel()
	self, err := user.Current()
	if err != nil {
		t.Fatalf("resolving the current user: %v", err)
	}

	type tc struct {
		name    string
		in      string
		wantUID uint32
		wantErr bool
	}
	//: a numeric uid nothing will have a passwd entry for.
	const orphanUID uint32 = 60123
	tests := []tc{
		{name: "the current user by name", in: self.Username, wantUID: parseID(t, self.Uid)},
		{name: "the current user by numeric id", in: self.Uid, wantUID: parseID(t, self.Uid)},
		{name: "root by numeric id", in: "0", wantUID: 0},
		{name: "a numeric uid with no passwd entry", in: strconv.Itoa(int(orphanUID)), wantUID: orphanUID},
		{name: "a name that does not resolve", in: absentName, wantErr: true},
		{name: "an empty name", in: "", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		uid, _, err := resolveUser(c.in)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeUnknownUser) {
				t.Fatalf("resolveUser(%q) = %v, want UNKNOWN_USER", c.in, err)
			}
			//: a refused resolution must yield no identity, or a caller
			//: checking only the error could run as uid 0.
			if uid != 0 {
				t.Errorf("resolveUser(%q) returned uid %d beside the error", c.in, uid)
			}
			return
		}
		if err != nil {
			t.Fatalf("resolveUser(%q) = %v, want nil", c.in, err)
		}
		if uid != c.wantUID {
			t.Errorf("resolveUser(%q) = uid %d, want %d", c.in, uid, c.wantUID)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_parseUserIDs pins the split classification: an unparseable uid is an
// UNKNOWN_USER, an unparseable gid an UNKNOWN_GROUP. Keeping them apart is what
// lets an operator see which half of the passwd entry is broken.
func Test_parseUserIDs(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		usr      *user.User
		wantCode errs.Code
	}
	tests := []tc{
		{name: "a well-formed entry", usr: &user.User{Uid: "1000", Gid: "1000"}},
		{name: "the root entry", usr: &user.User{Uid: "0", Gid: "0"}},
		{
			name:     "a non-numeric uid",
			usr:      &user.User{Uid: "not-a-number", Gid: "1000"},
			wantCode: coreproc.CodeUnknownUser,
		},
		{
			name:     "a non-numeric gid",
			usr:      &user.User{Uid: "1000", Gid: "not-a-number"},
			wantCode: coreproc.CodeUnknownGroup,
		},
		{
			//: a Windows-style SID is what os/user returns off Unix; it must be
			//: reported rather than silently truncated to a number.
			name:     "a security identifier",
			usr:      &user.User{Uid: "S-1-5-21-1", Gid: "S-1-5-21-2"},
			wantCode: coreproc.CodeUnknownUser,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		uid, gid, err := parseUserIDs(c.usr)
		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("parseUserIDs(%s) = %v, want code %v", c.name, err, c.wantCode)
			}
			if uid != 0 || gid != 0 {
				t.Errorf("parseUserIDs(%s) returned (%d, %d) beside the error", c.name, uid, gid)
			}
			return
		}
		if err != nil {
			t.Fatalf("parseUserIDs(%s) = %v, want nil", c.name, err)
		}
		if strconv.FormatUint(uint64(uid), 10) != c.usr.Uid {
			t.Errorf("uid = %d, want %s", uid, c.usr.Uid)
		}
		if strconv.FormatUint(uint64(gid), 10) != c.usr.Gid {
			t.Errorf("gid = %d, want %s", gid, c.usr.Gid)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_resolveGroup pins the group half, which takes the numeric shortcut one
// step further: a numeric gid is honoured WITHOUT any NSS lookup at all, because
// a supplementary group need not exist in the local database to be meaningful to
// the kernel.
func Test_resolveGroup(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      string
		wantGID uint32
		wantErr bool
	}
	tests := []tc{
		{name: "the root group by id", in: "0", wantGID: 0},
		{name: "an arbitrary numeric gid", in: "60123", wantGID: 60123},
		{name: "the largest 32-bit gid", in: "4294967295", wantGID: 4294967295},
		{name: "a name that does not resolve", in: absentName, wantErr: true},
		{name: "an empty name", in: "", wantErr: true},
		//: past the 32-bit space the numeric shortcut declines and the name
		//: lookup fails, which is the honest outcome.
		{name: "a gid past the 32-bit space", in: "4294967296", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		gid, err := resolveGroup(c.in)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeUnknownGroup) {
				t.Fatalf("resolveGroup(%q) = %v, want UNKNOWN_GROUP", c.in, err)
			}
			if gid != 0 {
				t.Errorf("resolveGroup(%q) returned gid %d beside the error", c.in, gid)
			}
			return
		}
		if err != nil {
			t.Fatalf("resolveGroup(%q) = %v, want nil", c.in, err)
		}
		if gid != c.wantGID {
			t.Errorf("resolveGroup(%q) = %d, want %d", c.in, gid, c.wantGID)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_resolveGroups pins that the batch preserves order and fails whole.
//
// Order matters because supplementary groups are handed to setgroups(2) as a
// list, and a partially-resolved list would grant the child SOME of the
// privileges it was configured with — which is worse than none, because nobody
// would notice.
func Test_resolveGroups(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      []string
		want    []uint32
		wantErr bool
	}
	tests := []tc{
		{name: "no groups at all"},
		{name: "an empty list", in: []string{}},
		{name: "a single numeric gid", in: []string{"10"}, want: []uint32{10}},
		{name: "several, in order", in: []string{"10", "20", "30"}, want: []uint32{10, 20, 30}},
		{name: "the order is preserved", in: []string{"30", "10", "20"}, want: []uint32{30, 10, 20}},
		{name: "one unresolvable name", in: []string{absentName}, wantErr: true},
		{
			//: the first failure aborts the batch, whatever resolved before it.
			name:    "an unresolvable name after good ones",
			in:      []string{"10", absentName, "20"},
			wantErr: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, err := resolveGroups(c.in)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeUnknownGroup) {
				t.Fatalf("resolveGroups(%v) = %v, want UNKNOWN_GROUP", c.in, err)
			}
			//: a partial list is worse than none: it would grant SOME of the
			//: configured privileges with nothing to signal the gap.
			if got != nil {
				t.Errorf("resolveGroups(%v) returned %v beside the error", c.in, got)
			}
			return
		}
		if err != nil {
			t.Fatalf("resolveGroups(%v) = %v, want nil", c.in, err)
		}
		if len(got) != len(c.want) {
			t.Fatalf("resolveGroups(%v) = %v, want %v", c.in, got, c.want)
		}
		for i, want := range c.want {
			if got[i] != want {
				t.Errorf("gid %d = %d, want %d", i, got[i], want)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_applyUserGroup pins the override rule: an explicit Group wins over the
// login gid the user resolved to. Without it, a unit naming both would silently
// run under the user's default group, and the configured one would do nothing.
func Test_applyUserGroup(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		spec     coreproc.Spec
		wantUID  uint32
		wantGID  uint32
		wantCode errs.Code
	}
	tests := []tc{
		{name: "neither set leaves the zero credential"},
		{name: "a numeric user", spec: coreproc.Spec{User: "60123"}, wantUID: 60123},
		{name: "a numeric group", spec: coreproc.Spec{Group: "60124"}, wantGID: 60124},
		{
			//: the explicit group overrides the login gid; uid 0's login gid is
			//: 0, so a non-zero result proves the override happened.
			name:    "an explicit group overrides the login gid",
			spec:    coreproc.Spec{User: "0", Group: "60124"},
			wantUID: 0,
			wantGID: 60124,
		},
		{
			name:     "an unresolvable user",
			spec:     coreproc.Spec{User: absentName},
			wantCode: coreproc.CodeUnknownUser,
		},
		{
			name:     "an unresolvable group",
			spec:     coreproc.Spec{Group: absentName},
			wantCode: coreproc.CodeUnknownGroup,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		out := &syscall.Credential{}

		err := applyUserGroup(c.spec, out)

		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("applyUserGroup(%s) = %v, want code %v", c.name, err, c.wantCode)
			}
			return
		}
		if err != nil {
			t.Fatalf("applyUserGroup(%s) = %v, want nil", c.name, err)
		}
		if out.Uid != c.wantUID {
			t.Errorf("Uid = %d, want %d", out.Uid, c.wantUID)
		}
		if out.Gid != c.wantGID {
			t.Errorf("Gid = %d, want %d", out.Gid, c.wantGID)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_resolveCredential pins the whole assembly, and the nil that means "keep
// the parent's identity". Returning an empty non-nil Credential instead would
// tell the kernel to setuid(0) — the exact opposite of leaving things alone.
func Test_resolveCredential(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		spec     coreproc.Spec
		wantNil  bool
		wantUID  uint32
		wantGID  uint32
		wantSupp int
		wantCode errs.Code
	}
	tests := []tc{
		{name: "no identity at all", wantNil: true},
		{name: "an unrelated field only", spec: coreproc.Spec{Path: "/bin/true"}, wantNil: true},
		{name: "a numeric user", spec: coreproc.Spec{User: "60123"}, wantUID: 60123},
		{
			name:     "a user with supplementary groups",
			spec:     coreproc.Spec{User: "60123", Groups: []string{"10", "20"}},
			wantUID:  60123,
			wantSupp: 2,
		},
		{
			name:     "an unresolvable user",
			spec:     coreproc.Spec{User: absentName},
			wantCode: coreproc.CodeUnknownUser,
		},
		{
			name:     "an unresolvable supplementary group",
			spec:     coreproc.Spec{User: "60123", Groups: []string{absentName}},
			wantCode: coreproc.CodeUnknownGroup,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cred, err := resolveCredential(c.spec)
		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) {
				t.Fatalf("resolveCredential(%s) = %v, want code %v", c.name, err, c.wantCode)
			}
			//: a refused resolution must hand back no credential at all.
			if cred != nil {
				t.Errorf("resolveCredential(%s) returned %+v beside the error", c.name, cred)
			}
			return
		}
		if err != nil {
			t.Fatalf("resolveCredential(%s) = %v, want nil", c.name, err)
		}
		if c.wantNil {
			//: nil means "inherit"; an empty Credential would mean setuid(0).
			if cred != nil {
				t.Fatalf("resolveCredential(%s) = %+v, want nil to inherit", c.name, cred)
			}
			return
		}
		if cred == nil {
			t.Fatalf("resolveCredential(%s) = nil, want a credential", c.name)
		}
		if cred.Uid != c.wantUID || cred.Gid != c.wantGID {
			t.Errorf("credential = (uid %d, gid %d), want (%d, %d)", cred.Uid, cred.Gid, c.wantUID, c.wantGID)
		}
		if len(cred.Groups) != c.wantSupp {
			t.Errorf("supplementary groups = %v, want %d", cred.Groups, c.wantSupp)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// parseID converts an os/user id string for a fixture.
func parseID(t *testing.T, s string) uint32 {
	t.Helper()
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		t.Fatalf("parsing the id %q: %v", s, err)
	}
	return uint32(v)
}
