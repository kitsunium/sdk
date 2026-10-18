//go:build windows

package app

import "os/user"

// currentUser is the current user's SID.
func currentUser() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Uid, nil
}
