//go:build unix

// Package exec — Unix credential resolution: maps Spec.User/Group/Groups (names
// or numeric ids) to a syscall.Credential via os/user, the systemd
// User=/Group=/SupplementaryGroups= analogue.
package exec

import (
	"os/user"
	"strconv"
	"syscall"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// idBase / idBits are the strconv.ParseUint base and bit-size for parsing the
// 32-bit decimal uid/gid strings os/user returns.
const (
	idBase int = 10
	idBits int = 32
)

// wantsIdentity reports whether spec requests any credential change at all.
func wantsIdentity(spec coreproc.Spec) bool {
	//: any non-empty user, group, or supplementary group requires a Credential.
	return spec.User != "" || spec.Group != "" || len(spec.Groups) > 0
}

// resolveCredential builds the syscall.Credential implied by spec, or returns a
// nil cred when no user, group, or supplementary groups are requested (leaving
// the child at the parent's identity). It resolves names and numeric ids alike.
func resolveCredential(spec coreproc.Spec) (cred *syscall.Credential, err error) {
	//: no identity directives at all means inherit the parent's credentials.
	if !wantsIdentity(spec) {
		//: nil credential tells the kernel to keep the calling identity.
		return nil, nil
	}

	out := &syscall.Credential{}
	//: layer the user, explicit group, and supplementary groups onto out.
	if rErr := applyUserGroup(spec, out); rErr != nil {
		//: propagate the already-typed identity error verbatim.
		return nil, rErr
	}
	groups, sErr := resolveGroups(spec.Groups)
	//: any unresolvable supplementary group fails the whole spawn.
	if sErr != nil {
		//: propagate the already-typed resolution error verbatim.
		return nil, sErr
	}
	out.Groups = groups
	//: the assembled credential is ready for SysProcAttr.
	return out, nil
}

// applyUserGroup resolves spec.User and spec.Group into out's Uid/Gid, with an
// explicit Group overriding the login gid derived from the user.
func applyUserGroup(spec coreproc.Spec, out *syscall.Credential) error {
	//: a User directive sets the target uid and a default gid we may override.
	if spec.User != "" {
		uid, gid, uErr := resolveUser(spec.User)
		//: an unresolvable user is a typed UNKNOWN_USER, not a silent fallback.
		if uErr != nil {
			//: propagate the already-typed resolution error verbatim.
			return uErr
		}
		out.Uid = uid
		out.Gid = gid
	}
	//: an explicit Group overrides the login gid derived from the user.
	if spec.Group != "" {
		gid, gErr := resolveGroup(spec.Group)
		//: an unresolvable group is a typed UNKNOWN_GROUP.
		if gErr != nil {
			//: propagate the already-typed resolution error verbatim.
			return gErr
		}
		out.Gid = gid
	}
	//: user and group resolved (or were absent) — out carries the identity.
	return nil
}

// resolveUser maps a user name or numeric uid to its uid and primary gid,
// surfacing UnknownUser when neither lookup succeeds.
func resolveUser(name string) (uid, gid uint32, err error) {
	//: a numeric token bypasses NSS and is taken as a literal uid.
	if n, convErr := strconv.ParseUint(name, idBase, idBits); convErr == nil {
		usr, lookErr := user.LookupId(name)
		//: an unknown numeric uid still resolves to itself with a zero gid.
		if lookErr != nil {
			//: numeric uid with no passwd entry: honour the number, gid stays 0.
			return uint32(n), 0, nil
		}
		//: a numeric uid that does resolve carries a real primary gid.
		return parseUserIDs(usr)
	}

	usr, lookErr := user.Lookup(name)
	//: a name that does not resolve is a hard UNKNOWN_USER.
	if lookErr != nil {
		//: wrap the os/user cause under the central UNKNOWN_USER fields.
		return 0, 0, wrapUnknownUser(lookErr, errs.String("user", name))
	}
	//: the name resolved — extract its numeric uid and primary gid.
	return parseUserIDs(usr)
}

// parseUserIDs extracts the numeric uid and gid from a resolved *user.User.
func parseUserIDs(usr *user.User) (uid, gid uint32, err error) {
	uid64, uErr := strconv.ParseUint(usr.Uid, idBase, idBits)
	//: a non-numeric uid string is a host fault dressed as UNKNOWN_USER.
	if uErr != nil {
		//: wrap the parse cause under UNKNOWN_USER — the identity is unusable.
		return 0, 0, wrapUnknownUser(uErr, errs.String("uid", usr.Uid))
	}
	gid64, gErr := strconv.ParseUint(usr.Gid, idBase, idBits)
	//: a non-numeric primary gid is likewise an unusable identity.
	if gErr != nil {
		//: wrap under UNKNOWN_GROUP — the gid half of the identity failed.
		return 0, 0, wrapUnknownGroup(gErr, errs.String("gid", usr.Gid))
	}
	//: both ids parsed — return the resolved primary identity.
	return uint32(uid64), uint32(gid64), nil
}

// resolveGroup maps a group name or numeric gid to its gid, surfacing
// UnknownGroup when the name does not resolve.
func resolveGroup(name string) (gid uint32, err error) {
	//: a numeric token is taken as a literal gid without an NSS round-trip.
	if n, convErr := strconv.ParseUint(name, idBase, idBits); convErr == nil {
		//: honour the numeric gid directly.
		return uint32(n), nil
	}

	grp, lookErr := user.LookupGroup(name)
	//: a name with no group entry is a hard UNKNOWN_GROUP.
	if lookErr != nil {
		//: wrap the os/user cause under the central UNKNOWN_GROUP fields.
		return 0, wrapUnknownGroup(lookErr, errs.String("group", name))
	}
	gid64, pErr := strconv.ParseUint(grp.Gid, idBase, idBits)
	//: a non-numeric gid string is an unusable group identity.
	if pErr != nil {
		//: wrap the parse cause under UNKNOWN_GROUP.
		return 0, wrapUnknownGroup(pErr, errs.String("gid", grp.Gid))
	}
	//: the group resolved to a usable numeric gid.
	return uint32(gid64), nil
}

// resolveGroups maps each supplementary group name or gid to a numeric gid,
// preserving order and failing the batch on the first unresolvable entry.
func resolveGroups(names []string) (gids []uint32, err error) {
	//: no supplementary groups requested — return a nil slice, not an error.
	if len(names) == 0 {
		//: empty input maps to no supplementary groups.
		return nil, nil
	}
	out := make([]uint32, 0, len(names))
	//: resolve each entry; the first failure aborts with its typed error.
	for _, name := range names {
		gid, gErr := resolveGroup(name)
		//: a single unresolvable supplementary group fails the spawn.
		if gErr != nil {
			//: propagate the already-typed UNKNOWN_GROUP error.
			return nil, gErr
		}
		out = append(out, gid)
	}
	//: every supplementary group resolved to a numeric gid.
	return out, nil
}
