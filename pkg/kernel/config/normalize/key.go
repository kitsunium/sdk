package normalize

import "strings"

// Key normalizes the key by replacing underscores with dots and converting to lowercase.
func Key(key string) string {
	key = strings.ReplaceAll(key, "_", ".")
	return strings.ToLower(key)
}
