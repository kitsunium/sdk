// Package postgres — the credentials a new connection is opened with, read
// again from the URL each time.
package postgres

import "github.com/jackc/pgx/v5"

// withCredentials gives c — the copy of its configuration pgx is about to
// connect with — the user and the password of fresh, the URL as it is now:
// a password rotated where it lives is used by the next connection.
func withCredentials(c, fresh *pgx.ConnConfig) {
	c.User, c.Password = fresh.User, fresh.Password
}
