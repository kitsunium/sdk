//go:build windows

// Package kit — the current user on Windows: its SID, from os/user, which
// reads the process token there without cgo.
package kit

import "os/user"

// currentUser is the current user's SID.
func currentUser() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Uid, nil
}
