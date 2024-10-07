package parser

import (
	"os"
	"strings"

	"github.com/kistunium/sdk/pkg/kernel/config/normalize"
)

// ENV is a configuration parser for environment variables.
type ENV struct {
}

// Type returns the type of the parser.
func (e *ENV) Type() string {
	return "env"
}

// Load reads environment variables and loads them into a map with normalized keys and values.
// It returns a map where the keys are the normalized environment variable names and the values
// are the normalized environment variable values. If an error occurs during the process, it
// will be returned.
//
// Returns:
//
//   - map[string]string: A map containing the normalized environment variables.
//   - error: An error if one occurs during the loading process.
func (e *ENV) Load() (map[string]string, error) {
	config := make(map[string]string)

	envVars := os.Environ()

	for _, env := range envVars {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key, value := parts[0], parts[1]

		config[normalize.Key(key)] = normalize.Value(value)
	}

	return config, nil
}
