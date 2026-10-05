package mysql

import driver "github.com/go-sql-driver/mysql"

// withCredentials gives c — the copy of its configuration the driver is
// about to connect with — the user and the password of fresh, the URL as it
// is now: a password rotated where it lives is used by the next connection.
func withCredentials(c, fresh *driver.Config) {
	c.User, c.Passwd = fresh.User, fresh.Passwd
}
