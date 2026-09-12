// Package entitlement — discovering which subject this machine is enrolled as,
// from the layout ssh keys actually have on disk.
//
// This is the half of the Identity port that depends on the ssh key FORMAT and
// the ~/.ssh convention, which is why it lives here rather than with the
// mechanism (kitsunium/sdk ADR 0078).
package entitlement

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// DefaultSSHDir returns the conventional key location for the current user.

// DiscoverSubject finds the enrolled UUID by looking for a key named after
// one. Naming the file after the identifier is what removes the need for any
// local state file: the filesystem holds the binding.
//
// It refuses rather than guesses when several identities are present. Picking
// one would decide which licence gets verified, which one a rotation
// overwrites, and which one `license status` reports — none of which this
// function is entitled to decide on the caller's behalf.

// usableIdentities narrows candidates to those this machine can actually
// answer for — both halves present as real files.
//
// A stray .pub is common: a leftover from a rotation, a colleague's key copied
// in to read it, one simply dropped into ~/.ssh. None of them can sign a
// challenge, so none of them should be able to win a selection.

// publishedHalves returns every subject with a <uuid>.pub in sshDir, sorted.
//
// Sorting makes the diagnostics stable; it is deliberately NOT how an identity
// gets picked. Doing that was the defect: the lexicographically first uuid won,
// so a stray .pub decided which licence got verified.

// regularFile reports whether path is a regular file, following symlinks.
//
// Following them is deliberate and applies to BOTH halves of a pair: a key
// symlinked in from a password manager or a mounted volume is an ordinary
// setup, and refusing it would break installs that work today. What must be
// refused is a path that could never have held a key — a directory, a socket,
// a FIFO — because those stat without error and would otherwise count as an
// identity.

// uuidPattern matches the canonical UUID form used to name a subject's keys.
// The filename IS the identifier, which is what lets the roster hold no index.
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// publishedHalves returns every subject with a <uuid>.pub in sshDir, sorted.
//
// Sorting makes the diagnostics stable; it is deliberately NOT how an identity
// gets picked. Doing that was the defect: the lexicographically first uuid won,
// so a stray .pub decided which licence got verified.
func publishedHalves(sshDir string, entries []os.DirEntry) []string {
	var found []string
	//: Scan for the one naming convention that marks an enrolment.
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".pub")
		//: Only a uuid-named published half counts, and only when it is a
		//: real file: a directory called <uuid>.pub would otherwise become
		//: an identity and manufacture an ambiguity against a real one.
		if name != e.Name() && uuidPattern.MatchString(name) &&
			regularFile(PublicKeyPath(sshDir, name)) {
			found = append(found, name)
		}
	}
	sort.Strings(found)
	//: Return the candidates in a stable order.
	return found
}

// usableIdentities narrows candidates to those this machine can actually
// answer for — both halves present as real files.
//
// A stray .pub is common: a leftover from a rotation, a colleague's key copied
// in to read it, one simply dropped into ~/.ssh. None of them can sign a
// challenge, so none of them should be able to win a selection.
func usableIdentities(sshDir string, candidates []string) []string {
	var complete []string
	//: Keep only the subjects whose private half is present and usable.
	for _, uuid := range candidates {
		//: Without the private half the caller holds only published
		//: material, which the roster hands to everyone anyway.
		if regularFile(PrivateKeyPath(sshDir, uuid)) {
			complete = append(complete, uuid)
		}
	}
	//: Return the identities that could prove possession.
	return complete
}

// DiscoverSubject finds the enrolled UUID by looking for a key named after
// one. Naming the file after the identifier is what removes the need for any
// local state file: the filesystem holds the binding.
//
// It refuses rather than guesses when several identities are present. Picking
// one would decide which licence gets verified, which one a rotation
// overwrites, and which one `license status` reports — none of which this
// function is entitled to decide on the caller's behalf.
func DiscoverSubject(sshDir string) (uuid string, err error) {
	entries, readErr := os.ReadDir(sshDir)
	//: An unreadable ssh directory is indistinguishable from never enrolling.
	if readErr != nil {
		//: Report the absent-licence case.
		return "", fmt.Errorf("%w: %s", coreent.ErrNoLicense, sshDir)
	}

	found := publishedHalves(sshDir, entries)
	//: No UUID-named key means this machine was never enrolled.
	if len(found) == 0 {
		//: Report the absent-licence case.
		return "", fmt.Errorf("%w: no uuid-named key in %s", coreent.ErrNoLicense, sshDir)
	}
	//: One candidate is the ordinary case, and it behaves exactly as before —
	//: including a published half with no private key beside it, which must
	//: keep failing later as "cannot prove possession" rather than here as
	//: "never enrolled".
	if len(found) == 1 {
		//: Return the only enrolled subject.
		return found[0], nil
	}

	complete := usableIdentities(sshDir, found)
	//: Exactly one usable identity is not ambiguous, whatever else is lying
	//: around beside it.
	if len(complete) == 1 {
		//: Return the only subject this machine holds both halves of.
		return complete[0], nil
	}

	candidates := complete
	//: When no candidate holds both halves, name them all rather than an
	//: empty list.
	if len(candidates) == 0 {
		candidates = found
	}
	//: Report the ambiguity with the names, so the fix is obvious.
	return "", fmt.Errorf("%w: %s holds %d licence identities (%s)",
		coreent.ErrAmbiguousLicense, sshDir, len(candidates), strings.Join(candidates, ", "))
}

// DefaultSSHDir returns the conventional key location for the current user.
func DefaultSSHDir() string {
	home, err := os.UserHomeDir()
	//: Without a home directory there is no conventional location; return a
	//: relative path so the caller fails on a missing key rather than here.
	if err != nil {
		//: Degrade to a relative path.
		return ".ssh"
	}
	//: Return the conventional key directory.
	return filepath.Join(home, ".ssh")
}

// regularFile reports whether path is a regular file, following symlinks.
//
// Following them is deliberate and applies to BOTH halves of a pair: a key
// symlinked in from a password manager or a mounted volume is an ordinary
// setup, and refusing it would break installs that work today. What must be
// refused is a path that could never have held a key — a directory, a socket, a
// FIFO — because those stat without error and would otherwise count as an
// identity.
func regularFile(path string) bool {
	info, err := os.Stat(path)
	//: An unreadable path is not an identity.
	if err != nil {
		//: Not a usable key.
		return false
	}

	//: Only a regular file can hold key material.
	return info.Mode().IsRegular()
}
