package normalize

import "strings"

// Value removes surrounding single or double quotes from the value if present.
func Value(value string) string {
	if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
		value = strings.Trim(value, "'")
	} else if strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
		value = strings.Trim(value, "\"")
	}
	return strings.TrimSpace(value)
}
